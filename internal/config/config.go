// Package config 负责解析启动参数。启动参数只来自 CLI flags 和环境变量,
// 不进数据库 —— 进程启动前就需要它们,而 DB 此时尚未就绪。
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	// Bind 监听地址。默认 127.0.0.1,公网请通过反向代理暴露(见 DESIGN 第 7 节)。
	Bind string
	// Port 监听端口。
	Port int
	// Root 文件管理/终端的根目录,允许 "/"。持密码者即完全控制此目录边界内的机器。
	Root string
	// Password 登录密码(单密码)。优先取 LIGHTREMOTE_PASSWORD,其次 --password。
	Password string
	// DataDir 数据目录:SQLite 文件、会话密钥等。
	DataDir string
	// DatabaseURL 非空时使用远程 PostgreSQL,否则用 SQLite。
	DatabaseURL string
	// ReadOnly 只读模式:禁用终端、禁止写操作。
	ReadOnly bool
	// SessionTTL 会话有效期。
	SessionTTL int // 单位:小时

	// Web Push(里程碑 7)。
	VapidPrivate string // 32 字节 P-256 私钥(base64url/原样);空则用 <dataDir>/vapid.key
	VapidPublic  string // 信息性:公钥由私钥导出,此项仅作文档/调试
	VapidSubject string // VAPID subject(mailto:),默认 mailto:admin@localhost
	PushIdle     int    // 终端安静秒数后发"命令完成"推送;0=禁用,默认 10
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func Load() (*Config, error) {
	var cfg Config
	flag.StringVar(&cfg.Bind, "bind", envOr("LIGHTREMOTE_BIND", "127.0.0.1"), "监听地址 (LIGHTREMOTE_BIND)")
	flag.IntVar(&cfg.Port, "port", 8080, "监听端口 (LIGHTREMOTE_PORT)")
	flag.StringVar(&cfg.Root, "root", envOr("LIGHTREMOTE_ROOT", defaultRoot()), "工作根目录,允许 / (LIGHTREMOTE_ROOT)")
	flag.StringVar(&cfg.Password, "password", envOr("LIGHTREMOTE_PASSWORD", ""), "登录密码 (LIGHTREMOTE_PASSWORD)")
	flag.StringVar(&cfg.DataDir, "data-dir", envOr("LIGHTREMOTE_DATA_DIR", defaultDataDir()), "数据目录 (LIGHTREMOTE_DATA_DIR)")
	flag.StringVar(&cfg.DatabaseURL, "database-url", os.Getenv("DATABASE_URL"), "远程 PostgreSQL 连接串;为空则用 SQLite (DATABASE_URL)")
	flag.BoolVar(&cfg.ReadOnly, "readonly", envBool("LIGHTREMOTE_READONLY"), "只读模式:禁用终端与写操作 (LIGHTREMOTE_READONLY)")
	flag.IntVar(&cfg.SessionTTL, "session-ttl", 24, "会话有效期小时数 (LIGHTREMOTE_SESSION_TTL)")
	flag.StringVar(&cfg.VapidPrivate, "vapid-private", envOr("LIGHTREMOTE_VAPID_PRIVATE", ""), "Web Push VAPID 私钥(base64url/32 字节) (LIGHTREMOTE_VAPID_PRIVATE)")
	flag.StringVar(&cfg.VapidPublic, "vapid-public", envOr("LIGHTREMOTE_VAPID_PUBLIC", ""), "Web Push VAPID 公钥(信息性) (LIGHTREMOTE_VAPID_PUBLIC)")
	flag.StringVar(&cfg.VapidSubject, "vapid-subject", envOr("LIGHTREMOTE_VAPID_SUBJECT", ""), "VAPID subject (LIGHTREMOTE_VAPID_SUBJECT)")
	flag.IntVar(&cfg.PushIdle, "push-idle", envInt("LIGHTREMOTE_PUSH_IDLE", 10), "终端安静秒数后发命令完成推送;0=禁用 (LIGHTREMOTE_PUSH_IDLE)")
	flag.Parse()

	if envPort := os.Getenv("LIGHTREMOTE_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			cfg.Port = p
		}
	}

	if cfg.Password == "" {
		return nil, fmt.Errorf("必须设置密码:--password 或环境变量 LIGHTREMOTE_PASSWORD")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("非法端口: %d", cfg.Port)
	}
	// Root 必须存在;允许 "/"。
	if fi, err := os.Stat(cfg.Root); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("根目录不可用: %s (%v)", cfg.Root, err)
	}
	if cfg.Root != "/" {
		if abs, err := filepath.Abs(cfg.Root); err == nil {
			cfg.Root = abs
		}
		cfg.Root = strings.TrimRight(cfg.Root, "/")
	}
	// Windows 上 "/" 不带盘符,必须补成当前盘根(如 D:\),否则后续 filepath.Clean
	// 会把它变成 "\",所有带盘符的绝对路径都会被判定越界。Unix 下 Abs("/") 仍是 "/"。
	if vol := filepath.VolumeName(cfg.Root); vol == "" && filepath.Separator == '\\' {
		cfg.Root = defaultRoot()
	}
	return &cfg, nil
}

// defaultRoot 返回当前盘符的根目录:Windows 下是 cwd 所在卷(如 D:\),
// 其他平台是 /。默认给盘根而非家目录,否则同一台机器上跨盘的项目根本进不来。
func defaultRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return string(filepath.Separator)
	}
	vol := filepath.VolumeName(cwd)
	return vol + string(filepath.Separator)
}

func defaultDataDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "lightremote")
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/lightremote"
	}
	return filepath.Join(h, ".config", "lightremote")
}

func envBool(key string) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
