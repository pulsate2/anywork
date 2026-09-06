package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
)

// WS 推送帧:全部是 Event 的 JSON(文本帧),kind 覆盖消息/状态/exit。
// 客户端→服务端只有订阅管理两类帧。

const (
	settingCleanupDays    = "agent.cleanup.days"    // 默认 30
	settingCleanupAuto    = "agent.cleanup.auto"    // 默认关
	settingCleanupLastRun = "agent.cleanup.lastRun" // 最近一次自动清理结果 JSON
	defaultCleanupDays    = 30
)

// Handlers agent 域的 REST + WS 入口。
type Handlers struct {
	mgr   *Manager
	store *Store
}

func NewHandlers(mgr *Manager, store *Store) *Handlers {
	return &Handlers{mgr: mgr, store: store}
} // sessionJSON 会话的对外形状(列表/创建响应)。
type sessionJSON struct {
	ID             string `json:"id"`
	App            string `json:"app"`
	Workspace      string `json:"workspace"`
	Title          string `json:"title"`
	ExternalID     string `json:"externalId,omitempty"`
	PermissionMode string `json:"permissionMode"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	Status         string `json:"status"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

func toSessionJSON(s *Session, running bool) sessionJSON {
	status := s.Status
	if running && status != StatusRunning {
		status = StatusIdle // 存活但当前没有回合
	}
	return sessionJSON{
		ID: s.ID, App: s.App, Workspace: s.Workspace, Title: s.Title,
		ExternalID: s.ExternalID, PermissionMode: s.PermissionMode,
		Model: s.Model, Effort: s.Effort,
		Status: status, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}

// List GET /api/agent/sessions —— 全部会话(含已结束、可续聊),新的在前。
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.store.ListSessions()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	running := h.mgr.RunningIDs()
	out := make([]sessionJSON, 0, len(sessions))
	for i := range sessions {
		out = append(out, toSessionJSON(&sessions[i], running[sessions[i].ID]))
	}
	writeJSON(w, http.StatusOK, out)
}

// Create POST /api/agent/sessions {app, workspace, resume?, permissionMode?}
// CLI 不存在回 409(前端提示先装);workspace 用 workspaces 表里的 path。
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		App            string `json:"app"`
		Workspace      string `json:"workspace"`
		Resume         string `json:"resume"`
		PermissionMode string `json:"permissionMode"`
		Model          string `json:"model"`
		Effort         string `json:"effort"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.App == "" {
		body.App = AppClaude
	}
	sess, err := h.mgr.Create(CreateOptions{
		App:            body.App,
		Workspace:      body.Workspace,
		ResumeID:       body.Resume,
		PermissionMode: body.PermissionMode,
		Model:          body.Model,
		Effort:         body.Effort,
	})
	if err != nil {
		code := http.StatusInternalServerError
		switch {
		case strings.Contains(err.Error(), "可执行文件"):
			code = http.StatusConflict // 409:环境问题,提示装 CLI 而不是报错
		case strings.Contains(err.Error(), "不兼容"),
			strings.Contains(err.Error(), "未登录"):
			code = http.StatusConflict // 协议/登录问题:换版本或 codex login,不是请求错误
		case strings.Contains(err.Error(), "超出根边界"),
			strings.Contains(err.Error(), "不存在"),
			strings.Contains(err.Error(), "只读"),
			strings.Contains(err.Error(), "未知 agent"),
			strings.Contains(err.Error(), "未知权限"),
			strings.Contains(err.Error(), "不支持"):
			code = http.StatusBadRequest
		}
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, http.StatusCreated, toSessionJSON(sess, true))
}

// Messages GET /api/agent/sessions/{id}/messages?afterSeq=&beforeSeq=&limit=
// 三种用法(返回一律升序):afterSeq= 增量补差;beforeSeq= 往前翻"加载更早";
// 都不带 = 最新一页 —— 打开会话先看最近的,更早的由前端再翻。
func (h *Handlers) Messages(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	q := r.URL.Query()
	afterSeq, _ := strconv.ParseInt(q.Get("afterSeq"), 10, 64)
	beforeSeq, _ := strconv.ParseInt(q.Get("beforeSeq"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200 // 默认一页 200 条,与 hapi 一致
	}
	var (
		msgs []Event
		err  error
	)
	switch {
	case beforeSeq > 0:
		msgs, err = h.store.MessagesBefore(id, beforeSeq, limit)
	case afterSeq > 0:
		msgs, err = h.store.Messages(id, afterSeq, limit)
	default:
		msgs, err = h.store.MessagesTail(id, limit)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if msgs == nil {
		msgs = []Event{}
	}
	writeJSON(w, http.StatusOK, msgs)
}

// SendMessage POST /api/agent/sessions/{id}/messages {text}
// 返回落库事件(带 seq):前端本地即时追加,与 WS 推送按 seq 去重。
func (h *Handlers) SendMessage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	ev, err := h.mgr.Send(id, body.Text)
	if ev == nil && err != nil {
		http.Error(w, err.Error(), http.StatusConflict) // 会话不在运行
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

// Interrupt POST /api/agent/sessions/{id}/interrupt
func (h *Handlers) Interrupt(w http.ResponseWriter, r *http.Request) {
	if err := h.mgr.Interrupt(chi.URLParam(r, "id")); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Approve POST /api/agent/sessions/{id}/approve {reqID, allow, session?}
// REST 直达:推送通知点进来不用先建 WS 订阅。
// session=true = 本会话允许(driver 记规则,同类请求自动放行)。
func (h *Handlers) Approve(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		ReqID   string `json:"reqId"`
		Allow   bool   `json:"allow"`
		Session bool   `json:"session"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ReqID == "" {
		http.Error(w, "reqId required", http.StatusBadRequest)
		return
	}
	if err := h.mgr.Approve(id, body.ReqID, body.Allow, body.Session); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Answer POST /api/agent/sessions/{id}/answer {reqId, answers}
// 回答 agent 的提问(AskUserQuestion / request_user_input)。answers 是
// 问题 id → 选中选项 label(或自由文本)的映射;字段缺失或为 null = 取消。
func (h *Handlers) Answer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		ReqID   string               `json:"reqId"`
		Answers *map[string][]string `json:"answers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ReqID == "" {
		http.Error(w, "reqId required", http.StatusBadRequest)
		return
	}
	if err := h.mgr.Answer(id, body.ReqID, mapOrNil(body.Answers)); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// mapOrNil 把 *map 解成 map(nil 保持 nil,区别于"空回答")。
func mapOrNil(m *map[string][]string) map[string][]string {
	if m == nil {
		return nil
	}
	return *m
}

// Kill DELETE /api/agent/sessions/{id} —— 结束进程,记录保留(历史可看、可续聊)。
func (h *Handlers) Kill(w http.ResponseWriter, r *http.Request) {
	if err := h.mgr.Kill(chi.URLParam(r, "id")); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// DeleteSession POST /api/agent/sessions/{id}/delete —— 彻底删除:
// 进程(如在跑)、记录、消息、附件目录一起走,与 Kill 只结束进程不同。
func (h *Handlers) DeleteSession(w http.ResponseWriter, r *http.Request) {
	if err := h.mgr.Delete(chi.URLParam(r, "id")); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Settings POST /api/agent/sessions/{id}/settings —— 运行中调整模型/思考强度/权限模式。
// 响应里的 note 非空 = 设置已保存但不立即生效(claude 要续聊),前端 toast 出来。
func (h *Handlers) Settings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model          string `json:"model"`
		Effort         string `json:"effort"`
		PermissionMode string `json:"permissionMode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	sess, err, note := h.mgr.Settings(chi.URLParam(r, "id"), SettingsUpdate{
		Model:          strings.TrimSpace(body.Model),
		Effort:         body.Effort,
		PermissionMode: body.PermissionMode,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session": toSessionJSON(sess, true),
		"note":    note,
	})
}

// Upload POST /api/agent/sessions/{id}/attachments (multipart "file")
// 聊天附件收进独立目录(<数据目录>/agent-files/<会话id>/),不进会话工作区 ——
// 工作区是用户的地盘,聊天传的文件不该混进去。返回落盘绝对路径,
// 前端以 @路径 提及,agent 自己去读(只读不受沙箱限制)。
func (h *Handlers) Upload(w http.ResponseWriter, r *http.Request) {
	if h.mgr.ReadOnly {
		http.Error(w, "只读模式", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	if sess, err := h.store.GetSession(id); err != nil || sess == nil {
		http.Error(w, "会话不存在", http.StatusNotFound)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	fh, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file required", http.StatusBadRequest)
		return
	}
	defer fh.Close()

	// 文件名只留 basename(带 ../ 的恶意名挡在工作区外面),同名直接覆盖。
	dir := filepath.Join(h.store.FilesDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := filepath.Base(header.Filename)
	dst, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer dst.Close()
	if _, err := io.Copy(dst, fh); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": filepath.ToSlash(filepath.Join(dir, name))})
}

// File GET /api/agent/sessions/{id}/files/{name} —— 会话附件(tool_result 的
// image 块、聊天上传)读取。<img> 直接引用:Cookie 认证浏览器自动带。
// name 限 basename,防目录穿越;只放行图片类扩展名。
func (h *Handlers) File(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	name := filepath.Base(chi.URLParam(r, "name"))
	var ct string
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		ct = "image/png"
	case ".jpg", ".jpeg":
		ct = "image/jpeg"
	case ".gif":
		ct = "image/gif"
	case ".webp":
		ct = "image/webp"
	default:
		http.Error(w, "只支持图片文件", http.StatusBadRequest)
		return
	}
	if h.store.FilesDir == "" {
		http.Error(w, "附件目录未启用", http.StatusNotFound)
		return
	}
	// 会话必须存在:不存在的会话不给翻它的附件目录。
	if sess, err := h.store.GetSession(id); err != nil || sess == nil {
		http.Error(w, "会话不存在", http.StatusNotFound)
		return
	}
	path := filepath.Join(h.store.FilesDir, id, name)
	if _, err := os.Stat(path); err != nil {
		http.Error(w, "文件不存在", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, path)
}

// ---- 清理(DESIGN-AGENT.md 5.7) ----

type cleanupSettingsJSON struct {
	Days    int            `json:"days"`
	Auto    bool           `json:"auto"`
	LastRun *CleanupResult `json:"lastRun,omitempty"`
	LastAt  string         `json:"lastAt,omitempty"`
}

// CleanupStatus GET /api/agent/cleanup —— 当前配置 + 最近一次自动清理结果。
func (h *Handlers) CleanupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.cleanupStatus())
}

func (h *Handlers) cleanupStatus() cleanupSettingsJSON {
	days, _ := strconv.Atoi(h.store.GetSetting(settingCleanupDays))
	if days <= 0 {
		days = defaultCleanupDays
	}
	out := cleanupSettingsJSON{
		Days: days,
		Auto: h.store.GetSetting(settingCleanupAuto) == "true",
	}
	if raw := h.store.GetSetting(settingCleanupLastRun); raw != "" {
		var wrap struct {
			At     string         `json:"at"`
			Result *CleanupResult `json:"result"`
		}
		if json.Unmarshal([]byte(raw), &wrap) == nil && wrap.Result != nil {
			out.LastAt = wrap.At
			out.LastRun = wrap.Result
		}
	}
	return out
}

// SaveCleanupSettings PUT /api/agent/cleanup {days, auto}
// 开启自动清理是明确动作:这里只存配置,ticker 每日自查。
func (h *Handlers) SaveCleanupSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Days int  `json:"days"`
		Auto bool `json:"auto"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Days < 1 {
		http.Error(w, "days >= 1 required", http.StatusBadRequest)
		return
	}
	if err := h.store.SetSetting(settingCleanupDays, strconv.Itoa(body.Days)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.store.SetSetting(settingCleanupAuto, strconv.FormatBool(body.Auto)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, h.cleanupStatus())
}

// Cleanup POST /api/agent/cleanup {days, dryRun} —— 手动清理;days 一并存为默认。
func (h *Handlers) Cleanup(w http.ResponseWriter, r *http.Request) {
	if h.mgr.ReadOnly {
		http.Error(w, "只读模式", http.StatusForbidden)
		return
	}
	var body struct {
		Days   int  `json:"days"`
		DryRun bool `json:"dryRun"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Days < 1 {
		http.Error(w, "days >= 1 required", http.StatusBadRequest)
		return
	}
	_ = h.store.SetSetting(settingCleanupDays, strconv.Itoa(body.Days))
	res, err := h.store.CleanupOlderThan(time.Now().AddDate(0, 0, -body.Days), body.DryRun)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---- WS 推送 ----

// ServeWS GET /api/agent —— 订阅推送通道(服务端单向,客户端帧仅订阅管理)。
func (h *Handlers) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "bye")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	c := newWSClient(conn)
	go c.writeLoop(ctx)
	go c.pingTicker(ctx, cancel)
	defer c.dropAll(h.mgr)

	// 读循环:subscribe/unsubscribe 按会话订阅,watch/unwatch 观察全部会话状态。
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var in struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(data, &in) != nil {
			continue
		}
		switch in.Type {
		case "watch":
			h.mgr.Watch(c)
			c.setWatch(true)
		case "unwatch":
			c.setWatch(false)
			h.mgr.Unwatch(c)
		case "subscribe":
			if in.SessionID == "" {
				continue
			}
			// 会话可能刚好退出:订阅失败不算错误,前端靠 REST/exit 事件收尾。
			if h.mgr.Subscribe(in.SessionID, c) == nil {
				c.addSub(in.SessionID)
			}
		case "unsubscribe":
			if in.SessionID == "" {
				continue
			}
			c.removeSub(in.SessionID)
			h.mgr.Unsubscribe(in.SessionID, c)
		}
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// ---- 自动清理(5.7,默认关) ----

// StartAutoCleanup 每日一次:开关开着就按当前阈值跑同一个删除函数。
// 每次自查开关而不是启动时锁死 —— 关掉开关不用重启服务。
func (h *Handlers) StartAutoCleanup(stop <-chan struct{}) {
	go func() {
		// 启动后先等满一个周期再跑:开机立即清理会把"重启前刚好过期"的会话
		// 在用户反应过来之前删掉,先给一天缓冲。
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if h.store.GetSetting(settingCleanupAuto) != "true" {
					continue
				}
				days, _ := strconv.Atoi(h.store.GetSetting(settingCleanupDays))
				if days < 1 {
					days = defaultCleanupDays
				}
				res, err := h.store.CleanupOlderThan(time.Now().AddDate(0, 0, -days), false)
				if err != nil {
					continue
				}
				h.saveCleanupLastRun(res)
			}
		}
	}()
}

func (h *Handlers) saveCleanupLastRun(res CleanupResult) {
	raw, _ := json.Marshal(map[string]any{
		"at":     nowISO(),
		"result": res,
	})
	_ = h.store.SetSetting(settingCleanupLastRun, string(raw))
}

// ---- wsClient ----

// wsClient 一个 WS 推送连接。与 terminal.Client 同款写队列/丢弃策略:
// 消费不过来的连接丢帧保护其它订阅者,ping 探测黑洞连接。
type wsClient struct {
	conn *websocket.Conn
	send chan []byte

	mu       sync.Mutex
	subs     map[string]bool // 已订阅会话(断开时统一退订)
	watching bool            // 是否在观察全部会话状态(断开时统一退订)
}

func newWSClient(conn *websocket.Conn) *wsClient {
	return &wsClient{
		conn: conn,
		send: make(chan []byte, 256),
		subs: map[string]bool{},
	}
}

// SendEvent 实现 Manager.Subscriber:非阻塞投递,满则丢弃(丢的是本连接的帧,
// 消息已在库,前端重连补差就能找回)。
func (c *wsClient) SendEvent(ev *Event) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	select {
	case c.send <- b:
	default:
	}
}

func (c *wsClient) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-c.send:
			if c.conn.Write(ctx, websocket.MessageText, b) != nil {
				return
			}
		}
	}
}

func (c *wsClient) pingTicker(ctx context.Context, cancel context.CancelFunc) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.conn.Ping(pctx)
			pcancel()
			if err != nil {
				cancel()
				return
			}
		}
	}
}

// dropAll 断开时退订全部会话与观察。subs 快照后逐个退,Manager 内部有锁,安全。
func (c *wsClient) dropAll(mgr *Manager) {
	c.mu.Lock()
	ids := make([]string, 0, len(c.subs))
	for id := range c.subs {
		ids = append(ids, id)
	}
	watching := c.watching
	c.mu.Unlock()
	for _, id := range ids {
		mgr.Unsubscribe(id, c)
	}
	if watching {
		mgr.Unwatch(c)
	}
}

func (c *wsClient) setWatch(on bool) {
	c.mu.Lock()
	c.watching = on
	c.mu.Unlock()
}

func (c *wsClient) addSub(id string) {
	c.mu.Lock()
	c.subs[id] = true
	c.mu.Unlock()
}

func (c *wsClient) removeSub(id string) {
	c.mu.Lock()
	delete(c.subs, id)
	c.mu.Unlock()
}
