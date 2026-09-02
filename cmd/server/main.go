// LightRemote —— 超轻量远程工作台。设计见 DESIGN.md。
package main

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	aiprofile "lightremote/internal/aiprofile"

	"lightremote/internal/auth"
	backupsvc "lightremote/internal/backup"
	"lightremote/internal/config"
	"lightremote/internal/db"
	fsvc "lightremote/internal/fs"
	gitsvc "lightremote/internal/git"
	pushsvc "lightremote/internal/push"
	"lightremote/internal/terminal"
	"lightremote/internal/util"
)

//go:embed all:dist
var webFS embed.FS

func main() {
	// git 需要凭据时,会用 GIT_ASKPASS 拉起本二进制进入"凭据代理"模式:
	// 连回主进程拿用户经弹窗输入的答案,打印给 git。此时不做任何服务启动。
	if gitsvc.InAskPassMode() {
		gitsvc.RunAskPassProxy()
		return
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("配置错误: %v", err)
	}

	// 数据库(含迁移)。
	database, err := db.Open(cfg.DataDir, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("数据库: %v", err)
	}
	defer database.Close()

	// 鉴权:签名密钥持久化到数据目录,重启不丢会话。
	authStore, err := auth.New(filepath.Join(cfg.DataDir, "session.key"))
	if err != nil {
		log.Fatalf("鉴权初始化: %v", err)
	}
	passwordHash, err := auth.HashPassword(cfg.Password)
	if err != nil {
		log.Fatalf("密码哈希: %v", err)
	}

	fsService := fsvc.NewService(cfg.Root, cfg.ReadOnly)
	aiHandlers := aiprofile.NewHandlers(aiprofile.New(database.DB, cfg.DataDir))

	// Web Push:VAPID 密钥(配置/文件/生成)+ 订阅存储 + 投递器。
	vapidKey, err := pushsvc.LoadOrCreate(cfg.DataDir, cfg.VapidPrivate)
	if err != nil {
		log.Fatalf("Web Push VAPID: %v", err)
	}
	pushSubject := cfg.VapidSubject
	if pushSubject == "" {
		pushSubject = "mailto:admin@localhost"
	}
	pushSender := &pushsvc.Sender{HTTP: pushsvc.NewClient(), VAPID: vapidKey, Subject: pushSubject}
	pushHandlers := pushsvc.NewHandlers(pushsvc.NewStore(database.DB), pushSender)
	pushHandlers.IdleSeconds = cfg.PushIdle

	app := &App{
		cfg:          cfg,
		db:           database.DB,
		auth:         authStore,
		passwordHash: passwordHash,
		terminal:     terminal.NewManager(cfg.Root, cfg.ReadOnly),
		fs:           fsvc.NewHandlers(fsService),
		fsSvc:        fsService,
		ai:           aiHandlers,
		backupMgr:    backupsvc.New(database.DB, cfg.Root),
		push:         pushHandlers,
	}
	app.backup = backupsvc.NewHandlers(app.backupMgr)
	// 交互式 git 认证:broker 把"需要凭据"推给浏览器弹窗,answer 回填后放行 push/pull。
	gitService := gitsvc.New(cfg.Root, cfg.ReadOnly, fsService.Resolve)
	credBroker := gitsvc.NewCredentialBroker()
	gitService.SetCredentialBroker(credBroker)
	app.git = gitsvc.NewHandlers(gitService)
	app.authWS = gitsvc.NewAuthWSHandler(gitService)
	// 新终端会话继承当前 AI 档案的 env(已运行进程不受影响)。
	app.terminal.EnvProvider = func() []string {
		return aiHandlers.SessionEnv(os.Environ())
	}
	// 终端空闲 → Web Push "命令完成"通知。
	if cfg.PushIdle > 0 {
		go app.terminal.IdleWatcher(1*time.Second, time.Duration(cfg.PushIdle)*time.Second,
			func(s *terminal.Session) { app.push.NotifyTerminal(s.ID(), s.Dir()) })
	}
	// 启动备份调度与自动恢复。
	app.backupMgr.Start()
	defer app.backupMgr.Stop()

	srv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.Bind, cfg.Port),
		Handler:           app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// 优雅停机:捕获信号,给在途请求留窗口。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("停机: %v", err)
		}
	}()

	root := cfg.Root
	if root == "" {
		root = "/"
	}
	log.Printf("LightRemote 已启动: http://%s:%d (根目录: %s, 只读: %v)", cfg.Bind, cfg.Port, root, cfg.ReadOnly)
	if cfg.DatabaseURL == "" {
		log.Printf("存储: SQLite (%s)", filepath.Join(cfg.DataDir, "lightremote.db"))
	} else {
		log.Printf("存储: PostgreSQL")
	}
	if cfg.Bind == "127.0.0.1" {
		log.Printf("⚠ 监听本地。公网访问请通过 HTTPS 反向代理,并设置强密码。")
	}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("服务器: %v", err)
	}
	log.Println("已退出")
}

// App 汇集各服务的共享依赖。
type App struct {
	cfg          *config.Config
	db           *sql.DB
	auth         *auth.Store
	passwordHash string
	terminal     *terminal.Manager
	fs           *fsvc.Handlers
	fsSvc        *fsvc.Service
	git          *gitsvc.Handlers
	authWS       *gitsvc.AuthWSHandler
	ai           *aiprofile.Handlers
	backup       *backupsvc.Handlers
	backupMgr    *backupsvc.Manager
	push         *pushsvc.Handlers
}

func (a *App) routes() http.Handler {
	r := chi.NewRouter()

	// ---- 公开端点 ----
	r.Get("/api/health", a.handleHealth)
	r.Post("/api/login", a.handleLogin)
	r.Post("/api/logout", a.handleLogout)

	// ---- 受保护 API ----
	r.Group(func(pr chi.Router) {
		pr.Use(a.auth.Middleware)

		pr.Get("/api/me", a.handleMe)

		// 终端长连接(PTY 多窗口 + 服务端滚动缓冲)。
		pr.Get("/api/term", a.terminal.ServeWS)

		// 工作区 CRUD(里程碑 1 落库,供后续使用)。
		pr.Get("/api/workspaces", a.handleListWorkspaces)
		pr.Post("/api/workspaces", a.handleCreateWorkspace)
		pr.Put("/api/workspaces/{id}", a.handleUpdateWorkspace)
		pr.Delete("/api/workspaces/{id}", a.handleDeleteWorkspace)

		// 文件操作(里程碑 3)。
		pr.Get("/api/fs/list", a.fs.List)
		pr.Get("/api/fs/read", a.fs.Read)
		pr.Post("/api/fs/write", a.fs.Write)
		pr.Post("/api/fs/upload", a.fs.Upload)
		pr.Get("/api/fs/download", a.fs.Download)
		pr.Get("/api/fs/search", a.fs.Search)
		pr.Post("/api/fs/replace", a.fs.Replace)
		pr.Post("/api/fs/op", a.fs.Op)
		pr.Get("/api/fs/archive", a.fs.CreateArchive)
		pr.Get("/api/fs/archive/list", a.fs.ListArchive)
		pr.Post("/api/fs/extract", a.fs.ExtractArchive)

		// Git(里程碑 4)。
		pr.Get("/api/git/repo", a.git.RepoInfo)
		pr.Get("/api/git/status", a.git.Status)
		pr.Get("/api/git/diff", a.git.Diff)
		pr.Get("/api/git/show", a.git.Show)
		pr.Get("/api/git/log", a.git.Log)
		pr.Get("/api/git/branches", a.git.Branches)
		pr.Get("/api/git/remotes", a.git.Remotes)
		pr.Post("/api/git/stage", a.git.Stage)
		pr.Post("/api/git/commit", a.git.Commit)
		pr.Post("/api/git/push", a.git.Push)
		pr.Post("/api/git/pull", a.git.Pull)
		pr.Post("/api/git/branch", a.git.Branch)
		pr.Post("/api/git/stash", a.git.Stash)
		pr.Post("/api/git/worktree", a.git.Worktree)
		pr.Post("/api/git/restore", a.git.Restore)
		pr.Post("/api/git/revert", a.git.Revert)

		// 交互式 git 认证(推送/拉取需要账号密码时弹窗输入)。
		pr.Get("/api/git/auth", a.authWS.Begin)
		pr.Post("/api/git/auth/answer", a.authWS.Answer)

		// AI 配置档案(里程碑 5)。
		pr.Get("/api/ai/profiles", a.ai.List)
		pr.Get("/api/ai/profile", a.ai.Get)
		pr.Post("/api/ai/profile", a.ai.Create)
		pr.Put("/api/ai/profile", a.ai.Update)
		pr.Delete("/api/ai/profile", a.ai.Delete)
		pr.Get("/api/ai/active", a.ai.Active)
		pr.Post("/api/ai/active", a.ai.SetActive)
		pr.Get("/api/ai/profile/export", a.ai.Export)
		pr.Post("/api/ai/profile/import", a.ai.Import)

		// 备份(里程碑 6)。
		pr.Get("/api/backup/jobs", a.backup.List)
		pr.Post("/api/backup/job", a.backup.Save)
		pr.Delete("/api/backup/job", a.backup.Delete)
		pr.Post("/api/backup/run", a.backup.Run)
		pr.Get("/api/backup/snapshots", a.backup.Snapshots)
		pr.Post("/api/backup/restore", a.backup.Restore)
		pr.Get("/api/backup/download", a.backup.Download)

		// Web Push(里程碑 7)。
		pr.Get("/api/push/status", a.push.Status)
		pr.Post("/api/push/subscribe", a.push.Subscribe)
		pr.Post("/api/push/unsubscribe", a.push.Unsubscribe)
		pr.Post("/api/push/test", a.push.Test)

		// 系统信息(只读,低成本)。
		pr.Get("/api/sysinfo", a.handleSysInfo)
	})

	// ---- 静态资源(SPA 前端)----
	r.NotFound(a.handleSPA)
	r.MethodNotAllowed(a.handleSPA)
	return r
}

// ---- handlers ----

type healthResp struct {
	Status string `json:"status"`
	Now    string `json:"now"`
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResp{Status: "ok", Now: time.Now().UTC().Format(time.RFC3339)})
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ip := clientIP(r)
	token, err := a.auth.CheckLogin(ip, body.Password, a.passwordHash)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, a.auth.NewCookie(token))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, auth.ClearCookie())
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"readonly": a.cfg.ReadOnly,
		"root":     filepath.ToSlash(a.cfg.Root),
	})
}

// ---- workspaces ----

type workspace struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Favorite  bool   `json:"favorite"`
	SortOrder int    `json:"sortOrder"`
	CreatedAt string `json:"createdAt"`
}

func (a *App) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(`SELECT id, name, path, favorite, sort_order, created_at FROM workspaces ORDER BY sort_order, name`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	list := []workspace{}
	for rows.Next() {
		var ws workspace
		var fav int
		if err := rows.Scan(&ws.ID, &ws.Name, &ws.Path, &fav, &ws.SortOrder, &ws.CreatedAt); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ws.Favorite = fav == 1
		ws.Path = filepath.ToSlash(ws.Path)
		list = append(list, ws)
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *App) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string `json:"name"`
		Path      string `json:"path"`
		Favorite  bool   `json:"favorite"`
		SortOrder int    `json:"sortOrder"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.Path) == "" {
		http.Error(w, "name and path required", http.StatusBadRequest)
		return
	}
	// 与 fs/git 用同一套解析:入参可以是相对 root 或 root 内的绝对路径,统一落成绝对路径。
	abs, err := a.fsSvc.Resolve(body.Path)
	if err != nil {
		http.Error(w, "path outside root", http.StatusBadRequest)
		return
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		http.Error(w, "path is not a directory", http.StatusBadRequest)
		return
	}
	// 统一存正斜杠形式的绝对路径(前端/fs/git/terminal 都能直接吃)。
	stored := filepath.ToSlash(abs)
	now := time.Now().UTC().Format(time.RFC3339)
	id := util.ID()
	_, err = a.db.Exec(`INSERT INTO workspaces(id, name, path, favorite, sort_order, created_at) VALUES(?,?,?,?,?,?)`,
		id, body.Name, stored, boolToInt(body.Favorite), body.SortOrder, now)
	if err != nil {
		if db.IsUniqueViolation(err) {
			http.Error(w, "workspace already exists", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id})
}

// handleUpdateWorkspace 只改名字。path 是唯一键,也是文件/终端/Git 的落脚点,
// 换路径等于换一个工作区,重新建一个更清楚。
func (a *App) handleUpdateWorkspace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	res, err := a.db.Exec(`UPDATE workspaces SET name = ? WHERE id = ?`, name, chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	res, err := a.db.Exec(`DELETE FROM workspaces WHERE id = ?`, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- sysinfo ----

func (a *App) handleSysInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.readSysInfo())
}

// ---- SPA ----

// handleSPA 服务 embed 的静态资源,未知路径回退到 index.html(Vue history 路由)。
func (a *App) handleSPA(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	serveEmbed(w, r, path)
}

func serveEmbed(w http.ResponseWriter, r *http.Request, name string) {
	// 仅允许常规静态文件名,禁止目录穿越。
	if strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	content, err := fs.ReadFile(webFS, "dist/"+name)
	if err != nil {
		// 未命中真实文件 → 回退 SPA index.html。
		index, err2 := fs.ReadFile(webFS, "dist/index.html")
		if err2 != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write(index)
		return
	}
	ctype := mimeTypeByExt(name)
	if ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(content))
}

func mimeTypeByExt(name string) string {
	switch ext := strings.ToLower(filepath.Ext(name)); ext {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".ico":
		return "image/x-icon"
	case ".woff2":
		return "font/woff2"
	case ".map":
		return "application/json; charset=utf-8"
	default:
		return ""
	}
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func clientIP(r *http.Request) string {
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		if i := strings.IndexByte(xf, ','); i > 0 {
			return strings.TrimSpace(xf[:i])
		}
		return strings.TrimSpace(xf)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
