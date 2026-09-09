// Package agent 实现 Claude Code / Codex 会话远控(设计见 DESIGN-AGENT.md):
// spawn 官方 CLI 子进程 + 进程级 JSON 协议,归一成统一事件模型,
// 消息落库广播,权限请求经审批回传。操作走 REST,推送走 WS。
package agent

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// 事件种类(统一消息模型,DESIGN-AGENT.md 4.3)。
const (
	KindUser             = "user"           // 用户消息(含 REST 发送与插话)
	KindAssistantText    = "assistant_text" // agent 正文
	KindReasoning        = "reasoning"      // 思考过程
	KindToolCall         = "tool_call"      // 工具调用(含结果)
	KindPermissionReq    = "permission_request"
	KindPermissionResult = "permission_result"
	KindStatus           = "status" // 回合状态变化(running/idle 等元信息)
	KindError            = "error"
	// KindSlashCommands CLI 支持的 / 指令列表(claude init 透传;元信息,
	// 前端做输入提示,不渲染成卡片)。
	KindSlashCommands = "slash_commands"
	// KindUsage token 用量(claude 每条 assistant 消息的 usage / codex
	// tokenCount 累计值;元信息,前端取最后一条算上下文占用)。
	KindUsage = "usage"
	// KindCompaction 上下文压缩边界(claude compact_boundary /
	// microcompact_boundary、codex thread/compacted):时间线上插一条
	// 系统提示,之后的 usage 数字会明显回落。
	KindCompaction = "compaction"
	// KindSystemInfo claude 可见的 system 子类型(api_error / turn_duration /
	// away_summary / task_notification):时间线渲染成系统行,不接的话过载
	// 重试像卡死、后台任务完成悄无声息。
	KindSystemInfo = "system_info"
	// KindAssistantDelta / KindReasoningDelta 流式增量(claude
	// --include-partial-messages 的 text_delta / thinking_delta)。瞬态事件:
	// 只广播不落库 —— 完整消息随后照旧到达并持久化,历史回放不依赖增量。
	KindAssistantDelta = "assistant_delta"
	KindReasoningDelta = "reasoning_delta"
	// KindAskUser agent 向用户提问(claude AskUserQuestion / codex
	// request_user_input 走的是控制协议而非普通工具调用):渲染成选项卡,
	// 回答经 Answer 回传。
	KindAskUser = "ask_user"
	// KindAskUserResult 提问的回答(落库,历史回放时选项卡显示已选)。
	KindAskUserResult = "ask_user_result"
	// KindSessionStatus 会话状态变化(running/idle/dead;仅推送给列表
	// 观察者,不落库 —— 列表状态实时刷新用)。
	KindSessionStatus = "session_status"
)

// 会话状态。
const (
	StatusRunning = "running" // 回合进行中
	StatusIdle    = "idle"    // 等待输入,进程存活
	StatusDead    = "dead"    // 进程不存在(退出或重启)
)

// 权限模式(统一 4 档;两家的原生模式在各自 driver 里映射)。
const (
	PermPlan   = "plan"   // 只读规划:claude plan / codex untrusted+read-only
	PermAsk    = "ask"    // 每次询问(默认):claude default / codex on-request
	PermEdits  = "edits"  // 放行编辑:claude acceptEdits / codex on-failure
	PermAccept = "accept" // 全放行:claude bypassPermissions / codex never
)

// validPermMode 校验权限模式取值。
func validPermMode(m string) bool {
	switch m {
	case PermPlan, PermAsk, PermEdits, PermAccept:
		return true
	}
	return false
}

// payload 截断上限:一次超大的 tool result 不该撑爆 DB 与手机渲染。
const payloadLimit = 64 * 1024

// Event 一条归一化事件:driver 内部把各 flavor 的原生协议转成这个,
// Manager 落库 + 广播,前端只认这一种。
type Event struct {
	Kind    string `json:"kind"`
	Payload any    `json:"payload"`
	// 以下由 Manager 填,driver 不填。
	SessionID string `json:"sessionId,omitempty"`
	Seq       int64  `json:"seq,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// ToolCallPayload KindToolCall 的负载。Result 可为空(还在跑);
// 前端按 ToolUseID 把 tool_use 与 tool_result 合并成同一张卡。
// Images 是 tool_result 里 image 块落盘后的文件名(前端经
// /api/agent/sessions/{id}/files/{name} 取)。
type ToolCallPayload struct {
	Tool      string   `json:"tool"`
	ToolUseID string   `json:"toolUseId,omitempty"`
	// ParentToolUseID 非空 = 子 agent(Task 工具)的内部工具调用:前端把它
	// 挂到父 Task 卡下面当"过程"展示,不进主时间线。
	ParentToolUseID string   `json:"parentToolUseId,omitempty"`
	Args            string   `json:"args"`             // JSON 序列化并截断后的参数
	Result          string   `json:"result,omitempty"` // 文本化并截断后的结果
	Images          []string `json:"images,omitempty"` // image 块的文件名
	State           string   `json:"state"`            // running | ok | error
}

// PermissionReqPayload KindPermissionReq 的负载。
type PermissionReqPayload struct {
	ReqID string `json:"reqId"`
	Tool  string `json:"tool"`
	Args  string `json:"args"`
}

// PermissionResultPayload KindPermissionResult 的负载。
type PermissionResultPayload struct {
	ReqID string `json:"reqId"`
	Allow bool   `json:"allow"`
	// Session=true 表示当时选的是"本会话允许"(仅 Allow=true 有意义),
	// 前端据此把结论显示成"本会话已允许"。
	Session bool `json:"session,omitempty"`
}

// StatusPayload KindStatus 的负载。
type StatusPayload struct {
	State string `json:"state"` // running | idle
}

// UsagePayload KindUsage 的负载。Context 是"当前上下文占用"(claude =
// input+cache_read,cache_creation 不算 —— 缓存写入的 token 不驻留上下文;
// codex 的 input 本就含缓存),前端拿它对模型上下文窗口算百分比。Window 是
// 真实窗口大小(claude result 的 modelUsage 透传;0 = 没给,前端回落启发式)。
type UsagePayload struct {
	Context   int    `json:"context"`
	Output    int    `json:"output,omitempty"`
	CacheRead int    `json:"cacheRead,omitempty"`
	Model     string `json:"model,omitempty"`
	Window    int    `json:"window,omitempty"`
}

// CompactionPayload KindCompaction 的负载。Micro=true 是微压缩(不清历史,
// 只裁缓存);Trigger auto/manual;PreTokens 压缩前占用;TokensSaved 微压缩
// 省下的量(claude 微压缩才有,codex 与整压都不带)。
// Phase="start" 是压缩进行中的瞬态(claude system/status 的 status=
// "compacting"):时间线不渲染,只驱动 StatusBar 的"压缩上下文…"状态;
// Failed 带失败原因(compact_result=failed 的第二条 status)。
type CompactionPayload struct {
	Micro       bool   `json:"micro,omitempty"`
	Trigger     string `json:"trigger,omitempty"`
	PreTokens   int    `json:"preTokens,omitempty"`
	TokensSaved int    `json:"tokensSaved,omitempty"`
	Phase       string `json:"phase,omitempty"`
	Failed      bool   `json:"failed,omitempty"`
	Error       string `json:"error,omitempty"`
}

// SystemInfoPayload KindSystemInfo 的负载。Type 区分四类:api_error(过载/
// 限流重试,Retry/MaxRetry 是进度)、turn_duration(回合结束统计)、
// away_summary(离开期间的 recap,Text 是原文)、task_notification(后台任务
// 完成/Monitor 事件,Text 是摘要,Status 是 completed/failed 等状态词,
// Event 是 Monitor 事件的具体行)。
type SystemInfoPayload struct {
	Type       string  `json:"type"` // api_error | turn_duration | away_summary | task_notification
	Text       string  `json:"text,omitempty"`
	Status     string  `json:"status,omitempty"`
	Event      string  `json:"event,omitempty"`
	Error      string  `json:"error,omitempty"`
	Retry      int     `json:"retry,omitempty"`
	MaxRetry   int     `json:"maxRetry,omitempty"`
	DurationMs float64 `json:"durationMs,omitempty"`
	Turns      int     `json:"turns,omitempty"`
	CostUSD    float64 `json:"costUsd,omitempty"`
}

// AskUserPayload KindAskUser 的负载:agent 向用户提问。两家归一成同一形状:
// claude AskUserQuestion 用下标当问题 id;codex request_user_input 自带 id。
type AskUserPayload struct {
	ReqID     string        `json:"reqId"`
	Questions []AskQuestion `json:"questions"`
}

// AskQuestion 一个问题。Options 空 = 自由文本(codex 的 editor/占位符场景)。
type AskQuestion struct {
	ID          string      `json:"id"`
	Header      string      `json:"header,omitempty"`
	Question    string      `json:"question"`
	Multi       bool        `json:"multi,omitempty"`
	Required    bool        `json:"required,omitempty"`
	Options     []AskOption `json:"options,omitempty"`
	Placeholder string      `json:"placeholder,omitempty"`
}

// AskOption 一个选项。Preview 是单选题的预览内容(markdown,官方客户端
// 选中选项时在旁边展示;多选题官方忽略此字段)。
type AskOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Preview     string `json:"preview,omitempty"`
}

// AskUserResultPayload KindAskUserResult 的负载。Answers 键 = 问题 id,
// 值 = 选中的选项 label(自由文本就是文本本身);Cancelled = 用户取消。
type AskUserResultPayload struct {
	ReqID     string              `json:"reqId"`
	Answers   map[string][]string `json:"answers,omitempty"`
	Cancelled bool                `json:"cancelled,omitempty"`
}

// SessionStatusPayload KindSessionStatus 的负载:会话列表实时刷新用,
// title 顺带捎上(首条消息落标题时,别的设备列表也能同步)。
type SessionStatusPayload struct {
	Status string `json:"status"` // running | idle | dead
	Title  string `json:"title,omitempty"`
}

// ErrorPayload KindError 的负载。
type ErrorPayload struct {
	Message string `json:"message"`
}

// Session 一条 agent_sessions 记录。
type Session struct {
	ID             string
	App            string
	Workspace      string
	Title          string
	ExternalID     string
	PermissionMode string
	Model          string
	Effort         string
	Status         string
	CreatedAt      string
	UpdatedAt      string
}

// Store agent 两张表的仓储。
type Store struct {
	db *sql.DB
	// FilesDir 聊天附件目录(<数据目录>/agent-files):附件按会话 id 分目录存放,
	// 不进工作区;清理会话时连带删掉。空串 = 未启用(测试)。
	FilesDir string
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// CreateSession 插入会话记录。
func (s *Store) CreateSession(sess *Session) error {
	_, err := s.db.Exec(`INSERT INTO agent_sessions
		(id, app, workspace, title, external_id, permission_mode, model, effort, status, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		sess.ID, sess.App, sess.Workspace, sess.Title, sess.ExternalID,
		sess.PermissionMode, sess.Model, sess.Effort, sess.Status, sess.CreatedAt, sess.UpdatedAt)
	return err
}

// touchSession 更新时间戳;status 非空时一并更新状态列。
func (s *Store) touchSession(id, status string) {
	if status == "" {
		s.db.Exec(`UPDATE agent_sessions SET updated_at=? WHERE id=?`, nowISO(), id)
		return
	}
	s.db.Exec(`UPDATE agent_sessions SET status=?, updated_at=? WHERE id=?`, status, nowISO(), id)
}

// SetExternalID 记录 claude session_id / codex thread id(续聊凭据)。
func (s *Store) SetExternalID(id, externalID string) {
	s.db.Exec(`UPDATE agent_sessions SET external_id=? WHERE id=?`, externalID, id)
}

// SetModel 当前实际模型(claude init / codex thread 响应回填,运行中换模型时更新)。
func (s *Store) SetModel(id, model string) {
	s.db.Exec(`UPDATE agent_sessions SET model=? WHERE id=?`, model, id)
}

// SetSettings 会话级设置:模型/思考强度/权限模式,空串 = 不改。
// 落库即可:codex 下一回合读取,claude 续聊时读取。
func (s *Store) SetSettings(id, model, effort, perm string) {
	if model != "" {
		s.db.Exec(`UPDATE agent_sessions SET model=? WHERE id=?`, model, id)
	}
	if effort != "" {
		s.db.Exec(`UPDATE agent_sessions SET effort=? WHERE id=?`, effort, id)
	}
	if perm != "" {
		s.db.Exec(`UPDATE agent_sessions SET permission_mode=? WHERE id=?`, perm, id)
	}
}

// SetTitle 会话标题(首条用户消息截断;创建时先落空,首条消息来了再补)。
func (s *Store) SetTitle(id, title string) {
	// 注意占位符顺序:SET 在前 WHERE 在后,参数必须 (title, id) ——
	// 这里传反过一次,静默 no-op,列表全成了"未命名会话"。
	s.db.Exec(`UPDATE agent_sessions SET title=? WHERE id=? AND (title IS NULL OR title='')`, title, id)
}

// BackfillTitles 给标题为空的存量会话补标题(取首条用户消息截断)。
// 一次性修复 SetTitle 参数顺序 bug 期间落库的历史;新会话走正常路径。
func (s *Store) BackfillTitles() {
	rows, err := s.db.Query(`SELECT m.session_id, m.payload FROM agent_messages m
		JOIN agent_sessions s ON s.id = m.session_id
		WHERE (s.title IS NULL OR s.title='') AND m.kind='user'
		ORDER BY m.seq`)
	if err != nil {
		return
	}
	defer rows.Close()
	titles := map[string]string{}
	for rows.Next() {
		var id, payload string
		if rows.Scan(&id, &payload) != nil {
			continue
		}
		if _, ok := titles[id]; ok {
			continue // 每会话只取最早一条
		}
		var text string
		if json.Unmarshal([]byte(payload), &text) != nil {
			text = payload
		}
		titles[id] = titleFromText(text)
	}
	for id, title := range titles {
		s.SetTitle(id, title)
	}
}

// SetStatus 单独更新状态(回合开始/结束)。
func (s *Store) SetStatus(id, status string) {
	s.db.Exec(`UPDATE agent_sessions SET status=?, updated_at=? WHERE id=?`, status, nowISO(), id)
}

// GetSession 取单条;不存在返回 nil。
func (s *Store) GetSession(id string) (*Session, error) {
	row := s.db.QueryRow(`SELECT id, app, workspace, title, external_id, permission_mode,
		COALESCE(model,''), COALESCE(effort,''), status, created_at, updated_at
		FROM agent_sessions WHERE id=?`, id)
	var sess Session
	err := row.Scan(&sess.ID, &sess.App, &sess.Workspace, &sess.Title, &sess.ExternalID,
		&sess.PermissionMode, &sess.Model, &sess.Effort, &sess.Status, &sess.CreatedAt, &sess.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// ListSessions 全部会话,新的在前。dead 但未清理的历史会话也在里面(可续聊)。
func (s *Store) ListSessions() ([]Session, error) {
	rows, err := s.db.Query(`SELECT id, app, workspace, title, external_id, permission_mode,
		COALESCE(model,''), COALESCE(effort,''), status, created_at, updated_at
		FROM agent_sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Session{}
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.App, &sess.Workspace, &sess.Title, &sess.ExternalID,
			&sess.PermissionMode, &sess.Model, &sess.Effort, &sess.Status, &sess.CreatedAt, &sess.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, sess)
	}
	return list, rows.Err()
}

// AppendMessage 落一条消息并分配 seq(会话内单调递增),返回带 seq 的完整事件。
// 状态列只在 status 事件时改,普通消息只 touch updated_at —— 后者是清理(5.7)
// 的判龄依据;回合中 assistant 消息不断流,不能每条都把状态打回 idle。
func (s *Store) AppendMessage(sessionID string, ev *Event) (*Event, error) {
	payload := marshalPayload(ev.Payload)
	var seq int64
	// seq 分配用子查询取 MAX:SQLite 单进程一写多读,同一会话的消息只会从
	// Manager 单 goroutine 落库(见 Session.pump),这里不会并发竞争。
	err := s.db.QueryRow(`SELECT COALESCE(MAX(seq),0)+1 FROM agent_messages WHERE session_id=?`, sessionID).Scan(&seq)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(`INSERT INTO agent_messages(session_id, seq, kind, payload, created_at) VALUES(?,?,?,?,?)`,
		sessionID, seq, ev.Kind, payload, nowISO())
	if err != nil {
		return nil, err
	}
	status := ""
	if ev.Kind == KindStatus {
		if sp, ok := ev.Payload.(*StatusPayload); ok {
			status = sp.State
		}
	}
	s.touchSession(sessionID, status)

	out := *ev
	out.SessionID = sessionID
	out.Seq = seq
	out.CreatedAt = nowISO()
	return &out, nil
}

// Messages 分页拉取:seq > afterSeq 的前 limit 条(升序)。afterSeq=0 即全量。
// payload 存的是 JSON 字符串,直接原样回给前端(前端统一 JSON.parse)。
func (s *Store) Messages(sessionID string, afterSeq int64, limit int) ([]Event, error) {
	rows, err := s.db.Query(`SELECT seq, kind, payload, created_at FROM agent_messages
		WHERE session_id=? AND seq>? ORDER BY seq LIMIT ?`, sessionID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows, sessionID)
}

// MessagesTail 最新 limit 条(升序):打开会话先看最近的,更早的往前翻页。
func (s *Store) MessagesTail(sessionID string, limit int) ([]Event, error) {
	rows, err := s.db.Query(`SELECT seq, kind, payload, created_at FROM agent_messages
		WHERE session_id=? ORDER BY seq DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows, sessionID)
}

// MessagesBefore seq < beforeSeq 的最新 limit 条(升序):"加载更早"用。
func (s *Store) MessagesBefore(sessionID string, beforeSeq int64, limit int) ([]Event, error) {
	rows, err := s.db.Query(`SELECT seq, kind, payload, created_at FROM agent_messages
		WHERE session_id=? AND seq<? ORDER BY seq DESC LIMIT ?`, sessionID, beforeSeq, limit)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows, sessionID)
}

// scanEvents 收集行并按 seq 升序返回(Tail/Before 的 DESC 查询在这里倒回来,
// 前端拿到的一律升序,直接拼进时间线)。
func scanEvents(rows *sql.Rows, sessionID string) ([]Event, error) {
	defer rows.Close()
	list := []Event{}
	for rows.Next() {
		var ev Event
		var payload string
		if err := rows.Scan(&ev.Seq, &ev.Kind, &payload, &ev.CreatedAt); err != nil {
			return nil, err
		}
		ev.SessionID = sessionID
		ev.Payload = json.RawMessage(payload)
		list = append(list, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	return list, nil
}

// DeleteSession 删除单条会话:记录、消息、附件目录一起走。
func (s *Store) DeleteSession(id string) error {
	if _, err := s.db.Exec(`DELETE FROM agent_messages WHERE session_id=?`, id); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM agent_sessions WHERE id=?`, id); err != nil {
		return err
	}
	if s.FilesDir != "" {
		_ = os.RemoveAll(filepath.Join(s.FilesDir, id))
	}
	return nil
}

// MarkDead 启动时把遗留的 running/idle 会话标死(Go 进程重启,内存态全没了)。
func (s *Store) MarkDead() {
	s.db.Exec(`UPDATE agent_sessions SET status=? WHERE status IN (?, ?)`, StatusDead, StatusRunning, StatusIdle)
}

// CleanupResult 一次清理的战果。
type CleanupResult struct {
	Sessions int64 `json:"sessions"`
	Messages int64 `json:"messages"`
}

// CleanupOlderThan 删除 status=dead 且 updated_at 早于 cutoff 的会话及其消息(5.7)。
// dryRun 只统计不删。运行中会话无论多旧都不碰。
func (s *Store) CleanupOlderThan(cutoff time.Time, dryRun bool) (CleanupResult, error) {
	var res CleanupResult
	old := cutoff.UTC().Format(time.RFC3339)

	// 先收集要删的 id:SQLite 没数组参数,消息按 id 逐会话删;会话量级是几十,
	// 逐条 DELETE 在这里不是问题。
	rows, err := s.db.Query(`SELECT id FROM agent_sessions WHERE status=? AND updated_at < ?`, StatusDead, old)
	if err != nil {
		return res, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return res, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	if dryRun {
		for _, id := range ids {
			var n int64
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM agent_messages WHERE session_id=?`, id).Scan(&n); err != nil {
				return res, err
			}
			res.Sessions++
			res.Messages += n
		}
		return res, nil
	}

	for _, id := range ids {
		var n int64
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM agent_messages WHERE session_id=?`, id).Scan(&n); err != nil {
			return res, err
		}
		if err := s.DeleteSession(id); err != nil {
			return res, err
		}
		res.Sessions++
		res.Messages += n
	}
	return res, nil
}

func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }

// ---- settings KV(清理配置,DESIGN-AGENT.md 5.7) ----

// GetSetting 读 settings KV;不存在返回空串。
func (s *Store) GetSetting(key string) string {
	var v string
	s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	return v
}

// SetSetting 写 settings KV(upsert)。
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key, value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// marshalPayload 序列化并截断。正常路径直接存 JSON;超限(说明 driver 侧的
// 字符串截断漏网了,比如字段特别多的工具调用)则包成 {"truncated":true,"raw":…}
// 信封存前缀原文——盲切 JSON 会切进字符串中间产生非法 JSON,前端 parse 必炸,
// 而信封保证任何情况落库的都是合法 JSON,截断的内容仍以纯文本可见。
func marshalPayload(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"error":"payload 序列化失败"}`
	}
	if len(b) <= payloadLimit {
		return string(b)
	}
	cut := payloadLimit
	for cut > 0 && b[cut-1]&0xC0 == 0x80 { // 对齐 UTF-8 边界,防乱码
		cut--
	}
	raw, _ := json.Marshal(string(b[:cut]) + "…(已截断)")
	return `{"truncated":true,"raw":` + string(raw) + `}`
}

// truncate 字符串截断 helper,driver 构造 payload 时对内容字段用,
// 让大多数截断发生在 marshal 之前(保留结构化形态)。
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && s[cut-1]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…(已截断)"
}

// truncateJSON 工具参数的截断:保持 JSON 合法。盲切字节会切进字符串中间,
// 前端 parse 失败后整段退回纯文本,Edit/Write 的 diff 渲染跟着丢。这里解析后
// 把超长字符串值各自截短、重新序列化 —— 结构与短字段(file_path、old_string
// 边界等)全保留,前端照常取得到;截不进限额时退回信封形态兜底。
func truncateJSON(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return truncateEnvelope(s, limit)
	}
	// 每个字符串值单独限长 limit/2:单值再大也不连累整个结构,剩余额度留给别的字段。
	b, err := json.Marshal(capStringValues(v, limit/2))
	if err != nil || len(b) > limit {
		return truncateEnvelope(s, limit)
	}
	return string(b)
}

// capStringValues 递归截短对象里过长的字符串值(map/array/嵌套都走一遍)。
func capStringValues(v any, cap int) any {
	switch t := v.(type) {
	case string:
		if len(t) > cap {
			return truncate(t, cap)
		}
	case map[string]any:
		for k, val := range t {
			t[k] = capStringValues(val, cap)
		}
	case []any:
		for i, val := range t {
			t[i] = capStringValues(val, cap)
		}
	}
	return v
}

// truncateEnvelope 非法 JSON/截不进限额时的兜底:与 marshalPayload 同款信封,
// 保证落库的始终是合法 JSON,原文前缀以纯文本可见。
func truncateEnvelope(s string, limit int) string {
	raw, _ := json.Marshal(truncate(s, limit))
	return `{"truncated":true,"raw":` + string(raw) + `}`
}
