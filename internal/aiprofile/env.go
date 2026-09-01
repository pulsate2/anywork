package aiprofile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// buildSessionEnv 在 base 之上覆盖档案 env,并强制指向档案的配置目录。
func buildSessionEnv(base []string, name, dir string, p Profile) []string {
	merged := make(map[string]string, len(base)+8)
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i > 0 {
			merged[kv[:i]] = kv[i+1:]
		}
	}
	for k, v := range p.Env {
		merged[k] = v
	}
	// 配置目录强制指向档案,实现隔离;不污染 ~/.claude、~/.codex。
	merged["CLAUDE_CONFIG_DIR"] = filepath.Join(dir, "claude")
	merged["CODEX_HOME"] = filepath.Join(dir, "codex")
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		if v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

func readEnv(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func writeEnv(path string, env map[string]string) error {
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func dirExists(p string) bool {
	fi, e := os.Stat(p)
	return e == nil && fi.IsDir()
}

func mtime(p string) string {
	fi, e := os.Stat(p)
	if e != nil {
		return ""
	}
	return fi.ModTime().UTC().Format(time.RFC3339)
}
