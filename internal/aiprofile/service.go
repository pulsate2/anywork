// Package aiprofile 管理 AI 配置档案(Claude/Codex 切换)。
// 核心:不污染 ~/.claude、~/.codex,每个档案独立目录,通过覆盖
// CLAUDE_CONFIG_DIR / CODEX_HOME 隔离。切换=更新 active 记录;
// 新开终端会话继承当前档案 env,已运行进程不受影响。
package aiprofile

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

// ErrNotFound 档案不存在;ErrExists 档案已存在。
var (
	ErrNotFound = errors.New("profile not found")
	ErrExists   = errors.New("profile already exists")
)

// activeKey 存 active 档案名的 settings 表键。
const activeKey = "active_ai_profile"

// service 档案 CRUD 与 env 构建。
type service struct {
	db  *sql.DB
	dir string // profiles 根目录
}

// New 构造档案服务。
func New(db *sql.DB, dataDir string) *service {
	return &service{db: db, dir: filepath.Join(dataDir, "profiles")}
}

// Profile 一个档案的元信息。
type Profile struct {
	Name      string            `json:"name"`
	Env       map[string]string `json:"env"`
	HasClaude bool              `json:"hasClaude"`
	HasCodex  bool              `json:"hasCodex"`
	CreatedAt string            `json:"createdAt"`
	UpdatedAt string            `json:"updatedAt"`
}

// nameOK 校验档案名(仅安全字符,防目录穿越)。
func nameOK(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func (s *service) profileDir(name string) string {
	return filepath.Join(s.dir, name)
}

func (s *service) envFile(name string) string {
	return filepath.Join(s.profileDir(name), "env.json")
}

// List 返回全部档案(按名称排序)。
func (s *service) List() ([]Profile, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Profile{}, nil
		}
		return nil, err
	}
	profiles := []Profile{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := s.Get(e.Name())
		if err != nil {
			continue
		}
		profiles = append(profiles, p)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	return profiles, nil
}

// Get 读取单个档案的 env 与目录标记。
func (s *service) Get(name string) (Profile, error) {
	if !nameOK(name) {
		return Profile{}, ErrNotFound
	}
	env, err := readEnv(s.envFile(name))
	if err != nil {
		if os.IsNotExist(err) {
			return Profile{}, ErrNotFound
		}
		return Profile{}, err
	}
	dir := s.profileDir(name)
	p := Profile{
		Name:      name,
		Env:       env,
		HasClaude: dirExists(filepath.Join(dir, "claude")),
		HasCodex:  dirExists(filepath.Join(dir, "codex")),
		CreatedAt: mtime(filepath.Join(dir, "env.json")),
		UpdatedAt: mtime(filepath.Join(dir, "env.json")),
	}
	return p, nil
}

// Create 新建档案。preset 可选("direct"/"proxy");cloneFrom 可选(从档案目录复制配置)。
func (s *service) Create(name string, env map[string]string, preset, cloneFrom string) (Profile, error) {
	if !nameOK(name) {
		return Profile{}, errors.New("invalid profile name")
	}
	dir := s.profileDir(name)
	if _, err := os.Stat(dir); err == nil {
		return Profile{}, ErrExists
	}
	if cloneFrom != "" {
		if err := s.cloneConfig(cloneFrom, name); err != nil {
			return Profile{}, err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Profile{}, err
	}
	if len(env) == 0 {
		env = presetEnv(preset)
	}
	if err := writeEnv(s.envFile(name), env); err != nil {
		return Profile{}, err
	}
	_ = os.MkdirAll(filepath.Join(dir, "claude"), 0o700)
	_ = os.MkdirAll(filepath.Join(dir, "codex"), 0o700)
	return s.Get(name)
}

// Update 覆盖档案的 env(全量替换)。
func (s *service) Update(name string, env map[string]string) (Profile, error) {
	if !nameOK(name) {
		return Profile{}, ErrNotFound
	}
	if _, err := os.Stat(s.profileDir(name)); err != nil {
		return Profile{}, ErrNotFound
	}
	if err := writeEnv(s.envFile(name), env); err != nil {
		return Profile{}, err
	}
	return s.Get(name)
}

// Delete 删除档案;若为 active 则同时清空 active 记录。
func (s *service) Delete(name string) error {
	if !nameOK(name) {
		return ErrNotFound
	}
	if _, err := os.Stat(s.profileDir(name)); err != nil {
		return ErrNotFound
	}
	if act, _ := s.Active(); act == name {
		_ = s.SetActive("")
	}
	return os.RemoveAll(s.profileDir(name))
}

// SetActive 设置当前生效档案(空串=无)。
func (s *service) SetActive(name string) error {
	if name != "" && !nameOK(name) {
		return errors.New("invalid profile name")
	}
	if name != "" {
		if _, err := os.Stat(s.profileDir(name)); err != nil {
			return ErrNotFound
		}
	}
	_, err := s.db.Exec(
		`INSERT INTO settings(key,value) VALUES(?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		activeKey, name)
	return err
}

// Active 返回当前生效档案名(空串表示未设置)。
func (s *service) Active() (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, activeKey).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// SessionEnv 返回当前档案在终端会话中应注入的 env(含 CLAUDE_CONFIG_DIR/CODEX_HOME)。
// base 为会话基础 env(os.Environ())。无 active 档案时原样返回 base。
func (s *service) SessionEnv(base []string) []string {
	name, err := s.Active()
	if err != nil || name == "" {
		return base
	}
	env, err := s.Get(name)
	if err != nil {
		return base
	}
	return buildSessionEnv(base, name, s.profileDir(name), env)
}

// Preset list.
func presetEnv(preset string) map[string]string {
	switch preset {
	case "direct":
		return map[string]string{"ANTHROPIC_BASE_URL": "https://api.anthropic.com"}
	case "proxy":
		return map[string]string{
			"ANTHROPIC_BASE_URL": "", // 中转地址,由用户填写
			"HTTPS_PROXY":        "", // 代理,由用户填写
		}
	}
	return map[string]string{}
}
