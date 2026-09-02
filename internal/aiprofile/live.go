package aiprofile

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// codexPayload Codex 的配置载荷:auth.json 的内容 + config.toml 的全文。
// Claude 那边不用包一层,记录里存的就是 settings.json 本身。
type codexPayload struct {
	Auth   map[string]any `json:"auth"`
	Config string         `json:"config"`
}

func (s *Service) claudeSettings() string {
	return filepath.Join(s.home, ".claude", "settings.json")
}

func (s *Service) codexConfig() string { return filepath.Join(s.home, ".codex", "config.toml") }

func (s *Service) codexAuth() string { return filepath.Join(s.home, ".codex", "auth.json") }

// validate 只查能确定的部分:Claude 必须是 JSON 对象,Codex 必须是
// {"auth":{…},"config":"…"}。config.toml 的语法不校验(项目里没有 TOML 依赖),
// 写错了 CLI 启动时会报。
func validate(app string, cfg json.RawMessage) error {
	if len(cfg) == 0 {
		return invalid("配置内容不能为空")
	}
	switch app {
	case AppClaude:
		var m map[string]any
		if err := json.Unmarshal(cfg, &m); err != nil {
			return invalid("settings.json 不是合法的 JSON 对象: %v", err)
		}
		return nil
	case AppCodex:
		var p codexPayload
		if err := json.Unmarshal(cfg, &p); err != nil {
			return invalid(`codex 配置要形如 {"auth":{…},"config":"…"}: %v`, err)
		}
		return nil
	}
	return ErrApp
}

// liveExists 主配置文件还在不在。codex 的 auth.json 不算:它主要装 OAuth 登录态,
// 缺了也不代表 config.toml 没了。
func (s *Service) liveExists(app string) bool {
	var path string
	switch app {
	case AppClaude:
		path = s.claudeSettings()
	case AppCodex:
		path = s.codexConfig()
	default:
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// readLive 读回真实配置文件,供回填与首次收编使用。读不到就返回 nil。
func (s *Service) readLive(app string) json.RawMessage {
	switch app {
	case AppClaude:
		raw, err := os.ReadFile(s.claudeSettings())
		if err != nil {
			return nil
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil // 手改坏了的 JSON 不收编,不然存进去以后每次切换都写一份坏配置
		}
		out, err := json.Marshal(m)
		if err != nil {
			return nil
		}
		return out
	case AppCodex:
		conf, confErr := os.ReadFile(s.codexConfig())
		auth, authErr := os.ReadFile(s.codexAuth())
		if confErr != nil && authErr != nil {
			return nil
		}
		p := codexPayload{Auth: map[string]any{}, Config: string(conf)}
		if authErr == nil {
			_ = json.Unmarshal(auth, &p.Auth)
		}
		out, err := json.Marshal(p)
		if err != nil {
			return nil
		}
		return out
	}
	return nil
}

// writeLive 把配置写回真实文件。整份替换 —— 配置以库里这份为准;
// 切走之前的回填保证了手改的内容已经存进旧记录。
func (s *Service) writeLive(app string, cfg json.RawMessage) error {
	switch app {
	case AppClaude:
		var m map[string]any
		if err := json.Unmarshal(cfg, &m); err != nil {
			return invalid("settings.json 不是合法的 JSON 对象: %v", err)
		}
		data, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return err
		}
		return writeAtomic(s.claudeSettings(), append(data, '\n'), 0o600)
	case AppCodex:
		var p codexPayload
		if err := json.Unmarshal(cfg, &p); err != nil {
			return invalid(`codex 配置要形如 {"auth":{…},"config":"…"}: %v`, err)
		}
		if err := writeAtomic(s.codexConfig(), []byte(p.Config), 0o600); err != nil {
			return err
		}
		// auth 为空就不动 auth.json:官方登录态(OAuth token)也存在这个文件里,
		// 删了等于把人踢下线;切走时的回填已经把它收进了旧记录。
		if len(p.Auth) == 0 {
			return nil
		}
		data, err := json.MarshalIndent(p.Auth, "", "  ")
		if err != nil {
			return err
		}
		return writeAtomic(s.codexAuth(), append(data, '\n'), 0o600)
	}
	return ErrApp
}

// writeAtomic 同目录临时文件 + rename:写一半出错也不会留下半份配置。
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".lr-tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
