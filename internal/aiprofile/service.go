// Package aiprofile 管理 AI CLI 的配置切换。
// 一条记录 = 一份能直接落盘的供应商配置:Claude 存 settings.json 的全文,
// Codex 存 {"auth":{…},"config":"<config.toml>"}。切换就是把它写回真实配置文件,
// 而不是给终端注入环境变量 —— CLI 下次启动自己就读到了,不用重开会话;
// 切走之前会把真实文件回填进旧记录,在机器上手改过的内容不会丢。
package aiprofile

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"lightremote/internal/db"
	"lightremote/internal/util"
)

var (
	ErrNotFound = errors.New("配置不存在")
	ErrExists   = errors.New("同名配置已存在")
	ErrApp      = errors.New("不支持的应用")
)

// 支持切换的 CLI。
const (
	AppClaude = "claude"
	AppCodex  = "codex"
)

// invalidErr 入参不合法(名字为空、配置格式不对),HTTP 层映射成 400。
type invalidErr struct{ msg string }

func (e invalidErr) Error() string { return e.msg }

func invalid(format string, a ...any) error { return invalidErr{fmt.Sprintf(format, a...)} }

// Service 供应商配置的存取与切换。
type Service struct {
	db   *sql.DB
	home string // 用户主目录,真实配置文件的落点
}

func New(database *sql.DB, home string) *Service {
	return &Service{db: database, home: home}
}

// Provider 一份供应商配置。Config 原样保存,由各 app 自己解释。
type Provider struct {
	ID         string          `json:"id"`
	App        string          `json:"app"`
	Name       string          `json:"name"`
	Category   string          `json:"category"`
	WebsiteURL string          `json:"websiteUrl"`
	Config     json.RawMessage `json:"config"`
	IsCurrent  bool            `json:"isCurrent"`
	CreatedAt  string          `json:"createdAt"`
	UpdatedAt  string          `json:"updatedAt"`
}

const selectCols = `SELECT id, app, name, category, website_url, config_json, is_current, created_at, updated_at
	FROM ai_providers`

func appOK(app string) bool { return app == AppClaude || app == AppCodex }

func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }

func cleanName(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 64 {
		s = string(r[:64])
	}
	return s
}

func scanProviders(rows *sql.Rows) ([]Provider, error) {
	out := []Provider{}
	for rows.Next() {
		var (
			p   Provider
			cfg string
			cur int
		)
		if err := rows.Scan(&p.ID, &p.App, &p.Name, &p.Category, &p.WebsiteURL,
			&cfg, &cur, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.Config = json.RawMessage(cfg)
		p.IsCurrent = cur == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

// List 返回某个 app 的全部配置。
func (s *Service) List(app string) ([]Provider, error) {
	if !appOK(app) {
		return nil, ErrApp
	}
	if err := s.seedFromLive(app); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(selectCols+` WHERE app = ? ORDER BY name`, app)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanProviders(rows)
}

func (s *Service) get(app, id string) (Provider, error) {
	rows, err := s.db.Query(selectCols+` WHERE app = ? AND id = ?`, app, id)
	if err != nil {
		return Provider{}, err
	}
	defer rows.Close()
	list, err := scanProviders(rows)
	if err != nil {
		return Provider{}, err
	}
	if len(list) == 0 {
		return Provider{}, ErrNotFound
	}
	return list[0], nil
}

// current 返回当前生效的那份;一份都没标记时返回零值。
func (s *Service) current(app string) (Provider, error) {
	rows, err := s.db.Query(selectCols+` WHERE app = ? AND is_current = 1`, app)
	if err != nil {
		return Provider{}, err
	}
	defer rows.Close()
	list, err := scanProviders(rows)
	if err != nil || len(list) == 0 {
		return Provider{}, err
	}
	return list[0], nil
}

// Create 新增一份配置。不动真实文件 —— 要生效得再切换过去。
func (s *Service) Create(p Provider) (Provider, error) {
	if !appOK(p.App) {
		return Provider{}, ErrApp
	}
	p.Name = cleanName(p.Name)
	if p.Name == "" {
		return Provider{}, invalid("配置名不能为空")
	}
	if err := validate(p.App, p.Config); err != nil {
		return Provider{}, err
	}
	if p.Category == "" {
		p.Category = "custom"
	}
	now := nowISO()
	p.ID = util.ID()
	_, err := s.db.Exec(`INSERT INTO ai_providers(id, app, name, category, website_url,
		config_json, is_current, created_at, updated_at) VALUES(?,?,?,?,?,?,0,?,?)`,
		p.ID, p.App, p.Name, p.Category, p.WebsiteURL, string(p.Config), now, now)
	if err != nil {
		if db.IsUniqueViolation(err) {
			return Provider{}, ErrExists
		}
		return Provider{}, err
	}
	return s.get(p.App, p.ID)
}

// Update 覆盖一份配置。改的正好是当前生效那份,就顺手写回真实文件,
// 不然用户改完还得再切一次才有效果。
func (s *Service) Update(p Provider) (Provider, error) {
	if !appOK(p.App) {
		return Provider{}, ErrApp
	}
	old, err := s.get(p.App, p.ID)
	if err != nil {
		return Provider{}, err
	}
	p.Name = cleanName(p.Name)
	if p.Name == "" {
		return Provider{}, invalid("配置名不能为空")
	}
	if err := validate(p.App, p.Config); err != nil {
		return Provider{}, err
	}
	if p.Category == "" {
		p.Category = old.Category
	}
	if _, err := s.db.Exec(`UPDATE ai_providers SET name=?, category=?, website_url=?,
		config_json=?, updated_at=? WHERE app=? AND id=?`,
		p.Name, p.Category, p.WebsiteURL, string(p.Config), nowISO(), p.App, p.ID); err != nil {
		if db.IsUniqueViolation(err) {
			return Provider{}, ErrExists
		}
		return Provider{}, err
	}
	if old.IsCurrent {
		if err := s.writeLive(p.App, p.Config); err != nil {
			return Provider{}, err
		}
	}
	return s.get(p.App, p.ID)
}

// Delete 删除一份配置。真实配置文件保持原样 —— 删掉的只是这份记录。
func (s *Service) Delete(app, id string) error {
	if !appOK(app) {
		return ErrApp
	}
	res, err := s.db.Exec(`DELETE FROM ai_providers WHERE app=? AND id=?`, app, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Switch 切到某份配置:先把真实文件回填进旧的那份,再把新的写下去,
// 最后才移动 current 标记 —— 写失败就什么都没变。
func (s *Service) Switch(app, id string) error {
	if !appOK(app) {
		return ErrApp
	}
	target, err := s.get(app, id)
	if err != nil {
		return err
	}
	if err := validate(app, target.Config); err != nil {
		return err
	}
	if cur, err := s.current(app); err == nil && cur.ID != "" && cur.ID != id {
		s.backfill(cur)
	}
	if err := s.writeLive(app, target.Config); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE ai_providers SET is_current=0 WHERE app=?`, app); err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE ai_providers SET is_current=1 WHERE app=? AND id=?`, app, id)
	return err
}

// backfill 把真实配置文件读回旧记录。用户可能直接在机器上改过 settings.json /
// config.toml,不回填的话下次切回来就被库里的老内容覆盖了。读不到就跳过 —— 文件
// 可能压根没建过,不该因此挡住切换。
func (s *Service) backfill(p Provider) {
	live := s.readLive(p.App)
	if len(live) == 0 {
		return
	}
	_, _ = s.db.Exec(`UPDATE ai_providers SET config_json=?, updated_at=? WHERE app=? AND id=?`,
		string(live), nowISO(), p.App, p.ID)
}

// seedFromLive 库里某个 app 一条都没有时,把本机现有的真实配置收成第一条并标为当前。
// 否则用户一进来就切别的,手上唯一那份配置会被无声覆盖掉。
func (s *Service) seedFromLive(app string) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ai_providers WHERE app=?`, app).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	live := s.readLive(app)
	if len(live) == 0 {
		return nil
	}
	now := nowISO()
	_, err := s.db.Exec(`INSERT INTO ai_providers(id, app, name, category, website_url,
		config_json, is_current, created_at, updated_at) VALUES(?,?,'当前配置','custom','',?,1,?,?)`,
		util.ID(), app, string(live), now, now)
	return err
}

// RestoreMissing 启动时按 current 补写缺失的配置文件:容器里 HOME 常是临时的,
// 重启后 settings.json / config.toml 没了,库里却还标着 current —— 页面显示着"当前",
// CLI 其实什么都读不到。文件还在就一律不动,机器上手改的内容不该被开机覆盖。
// 返回补写过的 "app/名字",交给调用方打日志;读库或写盘出错只跳过,不拦启动。
func (s *Service) RestoreMissing() []string {
	done := []string{}
	for _, app := range []string{AppClaude, AppCodex} {
		if s.liveExists(app) {
			continue
		}
		cur, err := s.current(app)
		if err != nil || cur.ID == "" {
			continue
		}
		if err := s.writeLive(app, cur.Config); err != nil {
			continue
		}
		done = append(done, app+"/"+cur.Name)
	}
	return done
}
