package backup

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
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
		if spec, err := parseCron(c.Schedule); err == nil {
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
	// 启动自动恢复任务。失败必须留痕,否则用户以为数据在而实际不在。
	for _, j := range m.List() {
		if j.AutoRestore && j.Enabled {
			go func(j *Job) {
				if err := m.RestoreLatest(j.ID); err != nil {
					log.Printf("备份: 任务 %s(%s) 启动自动恢复失败: %v", j.Name, j.ID, err)
				}
			}(j)
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
				notRunning := !j.Running
				j.mu.Unlock()
				if due && notRunning {
					j.mu.Lock()
					j.NextRun = j.spec.next(now)
					j.mu.Unlock()
					go m.RunBackup(j.ID, false)
				}
			}
		}
	}
}

// RunBackup 立即执行一次备份。force=false 时内容指纹与上次相同则跳过。
func (m *Manager) RunBackup(id string, force bool) {
	j := m.Get(id)
	if j == nil {
		return
	}
	j.mu.Lock()
	if j.Running {
		j.mu.Unlock()
		return
	}
	j.Running = true
	j.mu.Unlock()

	skipped, err := m.doBackup(j, force)
	j.mu.Lock()
	j.Running = false
	j.LastRun = time.Now()
	j.LastSkipped = skipped
	if err != nil {
		j.LastOK = false
		j.LastErr = err.Error()
		j.Progress = ""
	} else {
		j.LastOK = true
		j.LastErr = ""
		if !skipped {
			j.Progress = ""
		} // 跳过时保留 doBackup 写的「内容未变化,已跳过」
	}
	j.mu.Unlock()

	// 成功且非跳过后轮转(跳过时没有新快照,轮转无意义)。
	if err == nil && !skipped {
		_ = m.Rotate(id)
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
