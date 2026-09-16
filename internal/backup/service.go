package backup

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// 备份/恢复共用的闸门争用,以及任务不存在。handler 靠 errors.Is 映射成 409/404。
var (
	ErrBusy  = errors.New("任务正忙")
	ErrNoJob = errors.New("任务不存在")
)

// JobConfig 备份任务配置(与 backup_jobs 表对应)。
type JobConfig struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	SourceDir   string   `json:"sourceDir"`
	WebDAVURL   string   `json:"webdavUrl"`
	WebDAVUser  string   `json:"webdavUser"`
	WebDAVPass  string   `json:"webdavPass"`
	Schedule    string   `json:"schedule"`
	Excludes    []string `json:"excludes"`
	Retention   int      `json:"retention"`
	AutoRestore bool     `json:"autoRestore"`
	Enabled     bool     `json:"enabled"`
	CreatedAt   string   `json:"createdAt"`
	// LastFP 上次成功备份的内容指纹(服务端维护,保存接口不可写)。
	LastFP string `json:"lastFp"`
}

// Job 运行时任务:配置 + 状态。
type Job struct {
	JobConfig
	NextRun     time.Time `json:"nextRun"`
	LastRun     time.Time `json:"lastRun"`
	LastOK      bool      `json:"lastOk"`
	LastErr     string    `json:"lastErr,omitempty"`
	LastSkipped bool      `json:"lastSkipped"` // 上次备份因内容未变化被跳过
	Running     bool      `json:"running"`
	Progress    string    `json:"progress,omitempty"`
	// Restoring 正在异步恢复。和 Running 共用一把闸门:两者都在读改写 sourceDir。
	Restoring  bool   `json:"restoring"`
	RestoreErr string `json:"restoreErr,omitempty"`

	mu     sync.Mutex
	spec   *cronSpec
	cancel chan struct{}
}

// Manager 管理备份任务:DB 持久化 + 调度 + 并发闸门。
type Manager struct {
	mu   sync.Mutex
	db   *sql.DB
	jobs map[string]*Job
	root string // 文件根边界
	stop chan struct{}
	wg   sync.WaitGroup
}

// New 构造备份管理器并加载任务。
func New(db *sql.DB, root string) *Manager {
	m := &Manager{
		db:   db,
		root: root,
		jobs: map[string]*Job{},
		stop: make(chan struct{}),
	}
	m.load()
	return m
}

func (m *Manager) load() {
	rows, err := m.db.Query(
		`SELECT id,name,source_dir,webdav_url,webdav_user,webdav_password,
		        schedule_cron,excludes_json,retention,auto_restore,enabled,created_at,last_fp
		 FROM backup_jobs`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var c JobConfig
		var excl string
		var autoRestore, enabled int
		var schedule, lastFP sql.NullString
		if err := rows.Scan(&c.ID, &c.Name, &c.SourceDir, &c.WebDAVURL, &c.WebDAVUser,
			&c.WebDAVPass, &schedule, &excl, &c.Retention, &autoRestore, &enabled, &c.CreatedAt, &lastFP); err != nil {
			continue
		}
		c.Schedule = schedule.String
		json.Unmarshal([]byte(excl), &c.Excludes)
		c.LastFP = lastFP.String
		c.AutoRestore = autoRestore == 1
		c.Enabled = enabled == 1
		j := m.jobFromConfig(c)
		m.jobs[j.ID] = j
	}
}

func (m *Manager) jobFromConfig(c JobConfig) *Job {
	j := &Job{JobConfig: c, cancel: make(chan struct{})}
	if c.Schedule != "" {
		spec, err := parseCron(c.Schedule)
		if err != nil {
			// 不能只当没配:任务会从此再不自动触发,而界面上看不出异样。
			log.Printf("备份: 任务 %s(%s) 的定时 %q 无法解析,该任务不会自动触发: %v", c.Name, c.ID, c.Schedule, err)
		} else {
			j.spec = spec
			j.NextRun = spec.next(time.Now())
		}
	}
	return j
}

// List 返回所有任务(按创建时间)。
func (m *Manager) List() []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Job, 0, len(m.jobs))
	keys := make([]string, 0, len(m.jobs))
	for k := range m.jobs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, m.jobs[k])
	}
	return out
}

// Get 返回单个任务。
func (m *Manager) Get(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id]
}

// Save 新建或更新任务并落库。last_fp 是服务端指纹,更新时不动它:
// 指纹输入包含 excludes 与文件清单,任一变化算出的指纹必然不同,不会误跳过。
func (m *Manager) Save(c JobConfig) (*Job, error) {
	c.Schedule = strings.TrimSpace(c.Schedule)
	// 存进来就得是能跑的:否则任务被静默存下却永不触发,只有日志里能找到线索。
	if c.Schedule != "" {
		if _, err := parseCron(c.Schedule); err != nil {
			return nil, err
		}
	}
	if c.ID == "" {
		c.ID = fmt.Sprintf("b-%d", time.Now().UnixNano())
		c.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		c.LastFP = "" // 新任务从零指纹开始
	}
	excl, _ := json.Marshal(c.Excludes)
	auto := 0
	if c.AutoRestore {
		auto = 1
	}
	en := 0
	if c.Enabled {
		en = 1
	}
	_, err := m.db.Exec(`
		INSERT INTO backup_jobs(id,name,source_dir,webdav_url,webdav_user,webdav_password,
			schedule_cron,excludes_json,retention,auto_restore,enabled,created_at,last_fp)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, source_dir=excluded.source_dir, webdav_url=excluded.webdav_url,
			webdav_user=excluded.webdav_user, webdav_password=excluded.webdav_password,
			schedule_cron=excluded.schedule_cron, excludes_json=excluded.excludes_json,
			retention=excluded.retention, auto_restore=excluded.auto_restore, enabled=excluded.enabled`,
		c.ID, c.Name, c.SourceDir, c.WebDAVURL, c.WebDAVUser, c.WebDAVPass,
		c.Schedule, string(excl), c.Retention, auto, en, c.CreatedAt, c.LastFP)
	if err != nil {
		return nil, err
	}
	j := m.jobFromConfig(c)
	m.mu.Lock()
	// 更新时从旧任务回填指纹(INSERT 值里只有新建才有意义)。
	if old := m.jobs[c.ID]; old != nil {
		old.mu.Lock()
		j.LastFP = old.LastFP
		old.mu.Unlock()
	}
	m.jobs[j.ID] = j
	m.mu.Unlock()
	return j, nil
}

// Delete 删除任务并停止调度。
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	j := m.jobs[id]
	delete(m.jobs, id)
	m.mu.Unlock()
	if j != nil {
		close(j.cancel)
	}
	_, err := m.db.Exec(`DELETE FROM backup_jobs WHERE id=?`, id)
	return err
}

// Start 启动调度循环(独立 goroutine,每 30s 检查一次)。
func (m *Manager) Start() {
	m.wg.Add(1)
	go m.scheduleLoop()
	// 启动自动恢复。和手动恢复走同一条异步路径(抢闸门 + 进 wg),
	// 这样它不会被 Stop() 甩下留下一半目录,也不会和随后的定时备份撞在一起。
	for _, j := range m.List() {
		if j.AutoRestore && j.Enabled {
			if err := m.StartRestore(j.ID, ""); err != nil {
				log.Printf("备份: 任务 %s(%s) 启动自动恢复未开始: %v", j.Name, j.ID, err)
			}
		}
	}
}

func (m *Manager) Stop() {
	close(m.stop)
	m.wg.Wait()
}

func (m *Manager) scheduleLoop() {
	defer m.wg.Done()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-m.stop:
			return
		case now := <-tick.C:
			for _, j := range m.List() {
				if !j.Enabled || j.spec == nil {
					continue
				}
				j.mu.Lock()
				due := !j.NextRun.IsZero() && !now.Before(j.NextRun)
				j.mu.Unlock()
				if due {
					j.mu.Lock()
					j.NextRun = j.spec.next(now)
					j.mu.Unlock()
					if err := m.RunBackup(j.ID, false); err != nil {
						// 撞上正在跑的备份/恢复就跳过这一轮,不补跑:下一轮到点按定时来。
						log.Printf("备份: 任务 %s(%s) 本次定时未执行: %v", j.Name, j.ID, err)
					}
				}
			}
		}
	}
}

// RunBackup 立即执行一次备份,抢到闸门就返回 nil 并在后台跑完。
// force=false 时内容指纹与上次相同则跳过。
// 闸门同步地抢:抢不到当场返回错误,调用方(HTTP)才能说清为什么没跑,
// 而不是回一句"已触发"然后什么都没发生。
func (m *Manager) RunBackup(id string, force bool) error {
	j := m.Get(id)
	if j == nil {
		return fmt.Errorf("%w: %s", ErrNoJob, id)
	}
	j.mu.Lock()
	switch {
	case j.Restoring:
		j.mu.Unlock()
		return fmt.Errorf("%w: 正在恢复,等恢复结束再备份", ErrBusy)
	case j.Running:
		j.mu.Unlock()
		return fmt.Errorf("%w: 已在备份中", ErrBusy)
	}
	j.Running = true
	j.Progress = "" // 清掉上一轮的文案,本轮由 doBackup 在收尾时写
	j.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.runBackupLocked(j, force)
	}()
	return nil
}

// StartRestore 异步恢复指定快照(snap 为空 = 最近一次)。立刻返回,进度靠 Job.Restoring。
// 与备份共用闸门:恢复中途被备份读到的是半棵新半棵旧的树,两边都得挡。
func (m *Manager) StartRestore(id, snap string) error {
	j := m.Get(id)
	if j == nil {
		return fmt.Errorf("%w: %s", ErrNoJob, id)
	}
	j.mu.Lock()
	switch {
	case j.Restoring:
		j.mu.Unlock()
		return fmt.Errorf("%w: 已在恢复中", ErrBusy)
	case j.Running:
		j.mu.Unlock()
		return fmt.Errorf("%w: 正在备份,等备份结束再恢复", ErrBusy)
	}
	j.Restoring = true
	j.RestoreErr = ""
	j.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		var err error
		if snap != "" {
			err = m.RestoreSnapshot(id, snap)
		} else {
			err = m.RestoreLatest(id)
		}
		j.mu.Lock()
		j.Restoring = false
		if err != nil {
			j.RestoreErr = err.Error()
		}
		j.mu.Unlock()
		if err != nil {
			log.Printf("备份: 任务 %s(%s) 恢复失败: %v", j.Name, j.ID, err)
		}
	}()
	return nil
}

// runBackupLocked 真正跑一次备份。调用前 Running 必须已置位。
func (m *Manager) runBackupLocked(j *Job, force bool) {
	skipped, err := m.doBackup(j, force)
	j.mu.Lock()
	j.Running = false
	j.LastRun = time.Now()
	j.LastSkipped = skipped
	if err != nil {
		j.LastOK = false
		j.LastErr = err.Error()
		j.Progress = "" // 中途失败,进度文案可能停在半路
	} else {
		j.LastOK = true
		j.LastErr = ""
	} // 成功/跳过都保留 doBackup 写的文案(已备份 N 个文件 / 内容未变化,已跳过)
	j.mu.Unlock()

	// 成功且非跳过后轮转(跳过时没有新快照,轮转无意义)。
	if err == nil && !skipped {
		_ = m.Rotate(j.ID)
	}
}

// updateFingerprint 备份成功后落库并更新内存指纹。
func (m *Manager) updateFingerprint(id, fp string) {
	if _, err := m.db.Exec(`UPDATE backup_jobs SET last_fp=? WHERE id=?`, fp, id); err != nil {
		return
	}
	if j := m.Get(id); j != nil {
		j.mu.Lock()
		j.LastFP = fp
		j.mu.Unlock()
	}
}
