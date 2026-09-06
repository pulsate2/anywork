package agent

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lightremote/internal/util"
)

// Subscriber 一个 WS 推送订阅者(见 handler.go 的 client)。
type Subscriber interface {
	SendEvent(ev *Event)
}

// liveSession 一个存活会话:driver + 订阅者 + 事件泵。
// 进程退出后 liveSession 从 Manager 移除,DB 记录标 dead,但消息历史都在,
// 可凭 external_id 续聊。
type liveSession struct {
	id     string
	driver Driver

	// model 最近落库的模型(去重用:claude 每个事件都会查一遍)。
	model string

	// wmu 落库写锁:pump(driver 事件)与 Send/Approve(HTTP 请求)会在不同
	// goroutine 里并发 AppendMessage,而 seq 分配是 MAX(seq)+1 —— 不锁的话
	// 插话时两边拿到同一个 seq,前端按 seq 去重会互相吞消息。
	wmu sync.Mutex

	mu          sync.Mutex
	subscribers map[Subscriber]struct{}
}

// Manager 管理存活 agent 会话与 WS 订阅(仿 terminal.Manager 的职责划分,
// 但没有 attach 语义:订阅随时可加可删,历史走 REST)。
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*liveSession
	// watchers 列表观察者:不订具体会话,收所有会话的状态变化
	// (会话列表实时刷新,见 handler.go 的 watch 帧)。
	watchers map[Subscriber]struct{}

	store *Store
	// Root 工作目录边界(会话 cwd 必须在内)。
	Root string
	// ReadOnly 只读模式禁止建会话/发消息/审批。
	ReadOnly bool
	// NotifyPermission / NotifyTurnDone 推送钩子(main 注入;nil = 不推送)。
	NotifyPermission func(sessionTitle, tool string)
	NotifyTurnDone   func(sessionTitle string)
}

func NewManager(store *Store, root string, readonly bool) *Manager {
	// 服务重启后内存态全没了:历史会话全部标死,列表里仍可续聊。
	store.MarkDead()
	return &Manager{
		sessions: map[string]*liveSession{},
		watchers: map[Subscriber]struct{}{},
		store:    store,
		Root:     root,
		ReadOnly: readonly,
	}
}

// CreateOptions 建会话参数。
type CreateOptions struct {
	App            string
	Workspace      string // cwd(绝对路径或相对 root)
	ResumeID       string // 续聊:原会话 id(anywork 的 id,不是 claude session id)
	PermissionMode string
	Title          string
	Model          string
	Effort         string
}

// Create 校验参数、spawn driver、落库、启动事件泵。返回会话记录。
func (m *Manager) Create(opt CreateOptions) (*Session, error) {
	if m.ReadOnly {
		return nil, fmt.Errorf("只读模式")
	}
	if opt.PermissionMode == "" {
		opt.PermissionMode = PermAsk
	}
	if !validPermMode(opt.PermissionMode) {
		return nil, fmt.Errorf("未知权限模式: %s", opt.PermissionMode)
	}
	if _, err := resolveAgentBin(opt.App); err != nil {
		return nil, err
	}
	cwd, err := m.resolve(opt.Workspace)
	if err != nil {
		return nil, err
	}

	// 续聊:取原会话的 external_id 作为 claude --resume 参数,
	// 并沿用原会话的工作目录(用户在原目录里干的活,续聊不该换地方)。
	resumeExternal := ""
	if opt.ResumeID != "" {
		orig, err := m.store.GetSession(opt.ResumeID)
		if err != nil {
			return nil, err
		}
		if orig == nil {
			return nil, fmt.Errorf("要续聊的会话不存在: %s", opt.ResumeID)
		}
		if orig.App != opt.App {
			return nil, fmt.Errorf("续聊的 agent 不一致(%s ≠ %s)", orig.App, opt.App)
		}
		resumeExternal = orig.ExternalID
		if resumeExternal == "" {
			return nil, fmt.Errorf("该会话没有可恢复的凭据(进程未成功启动过)")
		}
		cwd, err = m.resolve(orig.Workspace)
		if err != nil {
			return nil, err
		}
	}

	// 会话 id 先于 Start 生成:driver 要拿它拼附件目录(tool_result 的
	// image 块落 <FilesDir>/<会话id>/)。
	sessID := util.ID()
	driver, err := newDriver(opt.App)
	if err != nil {
		return nil, err
	}
	if err := driver.Start(StartOpts{
		App:            opt.App,
		Cwd:            cwd,
		ResumeID:       resumeExternal,
		PermissionMode: opt.PermissionMode,
		Model:          opt.Model,
		Effort:         opt.Effort,
		FilesDir:       m.sessionFilesDir(sessID),
	}); err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	sess := &Session{
		ID:             sessID,
		App:            opt.App,
		Workspace:      filepath.ToSlash(cwd),
		Title:          opt.Title,
		PermissionMode: opt.PermissionMode,
		Model:          driver.CurrentModel(), // codex 握手即知;claude 稍后由 pump 回填
		Effort:         opt.Effort,
		Status:         StatusIdle,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := m.store.CreateSession(sess); err != nil {
		driver.Close()
		return nil, err
	}

	ls := &liveSession{
		id:          sess.ID,
		driver:      driver,
		subscribers: map[Subscriber]struct{}{},
	}
	m.mu.Lock()
	m.sessions[sess.ID] = ls
	m.mu.Unlock()
	// 列表观察者立刻能看到新会话(别的设备/标签页)。
	m.notifyWatchers(sess.ID, StatusIdle)

	go m.pump(ls)
	return sess, nil
}

// append 会话内串行落库(见 liveSession.wmu)。返回带 seq 的完整事件。
func (m *Manager) append(ls *liveSession, ev *Event) (*Event, error) {
	ls.wmu.Lock()
	defer ls.wmu.Unlock()
	return m.store.AppendMessage(ls.id, ev)
}

// pump 事件泵:driver 事件 → 落库 → 广播。会话所有消息的落库都在这一个
// goroutine 里串行进行,Store 的 MAX(seq)+1 分配因此不需要额外锁。
func (m *Manager) pump(ls *liveSession) {
	for ev := range ls.driver.Events() {
		// system/init 到达后就有 session_id 了:立即落 external_id,
		// 进程哪怕只活了一秒,续聊凭据也不能丢。
		if ls.driver.ExternalID() != "" {
			m.store.SetExternalID(ls.id, ls.driver.ExternalID())
		}
		// 模型同理:claude 在 init 消息里才告知实际模型,握手时还不知道。
		if cur := ls.driver.CurrentModel(); cur != "" && cur != ls.model {
			ls.model = cur
			m.store.SetModel(ls.id, cur)
		}
		stored, err := m.append(ls, &ev)
		if err != nil {
			continue
		}
		m.applyTitle(ls.id, &ev)
		m.broadcast(ls, stored)

		switch ev.Kind {
		case KindPermissionReq, KindAskUser:
			if m.NotifyPermission != nil {
				tool := "提问"
				if pr, ok := ev.Payload.(*PermissionReqPayload); ok {
					tool = pr.Tool
				}
				go m.NotifyPermission(m.titleOf(ls.id), tool)
			}
		case KindStatus:
			if sp, ok := ev.Payload.(*StatusPayload); ok {
				// 列表观察者同步状态(running ⇄ idle)。
				m.notifyWatchers(ls.id, sp.State)
				if m.NotifyTurnDone != nil && sp.State == StatusIdle {
					go m.NotifyTurnDone(m.titleOf(ls.id))
				}
			}
		}
	}

	// 事件流关闭 = 进程退出:标死、广播 exit、移除。
	m.store.SetStatus(ls.id, StatusDead)
	m.notifyWatchers(ls.id, StatusDead)
	m.mu.Lock()
	delete(m.sessions, ls.id)
	subs := make([]Subscriber, 0, len(ls.subscribers))
	for s := range ls.subscribers {
		subs = append(subs, s)
	}
	m.mu.Unlock()
	exitMsg := &Event{Kind: "exit", SessionID: ls.id}
	for _, s := range subs {
		s.SendEvent(exitMsg)
	}
}

// Send 发用户消息:REST 路径先落库广播(界面即时),再写进 driver。
// 会话不在运行(退出/重启过)时自动复活:按 DB 里存的设置重新 spawn(续聊)
// 再发送 —— 前端不区分死活,像 hapi 一样发一句话就接上。
// 返回落库事件(带 seq),REST 响应直接回它,前端本地追加后靠 seq 与 WS 推送去重。
func (m *Manager) Send(sessionID, text string) (*Event, error) {
	if m.ReadOnly {
		return nil, fmt.Errorf("只读模式")
	}
	ls := m.get(sessionID)
	if ls == nil {
		var err error
		ls, err = m.revive(sessionID)
		if err != nil {
			return nil, err
		}
	}
	// 先落库再写 stdin:写 stdin 失败时消息已经在历史里,错误也会以
	// error 事件出现在时间线上,信息不丢。
	ev := &Event{Kind: KindUser, Payload: text}
	stored, err := m.append(ls, ev)
	if err != nil {
		return nil, err
	}
	// 用户消息不走 pump(pump 只消费 driver 事件),标题要在这里补。
	m.applyTitle(sessionID, ev)
	m.broadcast(ls, stored)
	if err := ls.driver.Send(text); err != nil {
		return stored, err
	}
	return stored, nil
}

// revive 复活死会话:用 DB 里存的 external_id + 设置重新 spawn,挂回 sessions。
// 运行中保存的模型/强度/权限(claude 约定"续聊时生效")正是在这里落地。
// spawn 拿着 Manager 锁:并发复活同一会话只会有一个真跑,其它会话的操作
// 最多阻塞一个进程启动的功夫(百毫秒级)。
func (m *Manager) revive(id string) (*liveSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ls, ok := m.sessions[id]; ok { // 双检:等锁的后来者直接复用
		return ls, nil
	}
	sess, err := m.store.GetSession(id)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, fmt.Errorf("会话不存在: %s", id)
	}
	if sess.ExternalID == "" {
		return nil, fmt.Errorf("该会话没有可恢复的凭据(进程未成功启动过)")
	}
	cwd, err := m.resolve(sess.Workspace)
	if err != nil {
		return nil, err
	}
	driver, err := newDriver(sess.App)
	if err != nil {
		return nil, err
	}
	if err := driver.Start(StartOpts{
		App:            sess.App,
		Cwd:            cwd,
		ResumeID:       sess.ExternalID,
		PermissionMode: sess.PermissionMode,
		Model:          sess.Model,
		Effort:         sess.Effort,
		FilesDir:       m.sessionFilesDir(id),
	}); err != nil {
		return nil, err
	}
	ls := &liveSession{
		id:          id,
		driver:      driver,
		subscribers: map[Subscriber]struct{}{},
		model:       driver.CurrentModel(),
	}
	m.sessions[id] = ls
	m.store.SetStatus(id, StatusIdle)
	go m.pump(ls)
	return ls, nil
}

// applyTitle 首条用户消息即标题(只补一次)。
func (m *Manager) applyTitle(sessionID string, ev *Event) {
	if ev.Kind != KindUser {
		return
	}
	if sp, ok := ev.Payload.(string); ok {
		m.store.SetTitle(sessionID, titleFromText(sp))
	}
}

// titleFromText 从用户消息提炼标题:取第一行(多行消息第一行通常就是意图),
// 超过 30 字按 rune 截断加省略号 —— 按字符不按字节,不会切出半个汉字。
func titleFromText(text string) string {
	line := strings.TrimSpace(text)
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	if r := []rune(line); len(r) > 30 {
		return string(r[:30]) + "…"
	}
	return line
}

// Interrupt 打断当前回合。
func (m *Manager) Interrupt(sessionID string) error {
	ls := m.get(sessionID)
	if ls == nil {
		return fmt.Errorf("会话不在运行: %s", sessionID)
	}
	return ls.driver.Interrupt()
}

// Settings 运行中调整设置。总是先落库(codex 下一回合生效;claude 续聊时生效),
// driver 不支持立即生效时返回提示文本,由前端 toast 出来 —— 不算失败。
func (m *Manager) Settings(sessionID string, u SettingsUpdate) (*Session, error, string) {
	if m.ReadOnly {
		return nil, fmt.Errorf("只读模式"), ""
	}
	if u.Model == "" && u.Effort == "" && u.PermissionMode == "" {
		return nil, fmt.Errorf("没有要改的设置"), ""
	}
	ls := m.get(sessionID)
	if ls == nil {
		return nil, fmt.Errorf("会话不在运行: %s", sessionID), ""
	}
	m.store.SetSettings(sessionID, u.Model, u.Effort, u.PermissionMode)
	note := ""
	if err := ls.driver.ApplySettings(u); err != nil {
		note = err.Error()
	}
	// 只在 driver 真正接受了新模型时同步 ls.model;claude 没接受(note 非空)时
	// 保持原值 —— 不然 pump 会拿"仍在跑的旧模型"把用户刚存的选择覆盖回去。
	if u.Model != "" && note == "" {
		ls.model = u.Model
	}
	sess, err := m.store.GetSession(sessionID)
	return sess, err, note
}

// Approve 答复权限请求,并落一条 permission_result。
// session=true 表示"本会话允许":driver 记规则/答 acceptForSession,同类
// 请求后续自动放行不再询问。
func (m *Manager) Approve(sessionID, reqID string, allow, session bool) error {
	if m.ReadOnly {
		return fmt.Errorf("只读模式")
	}
	ls := m.get(sessionID)
	if ls == nil {
		return fmt.Errorf("会话不在运行: %s", sessionID)
	}
	if err := ls.driver.Resolve(reqID, allow, session); err != nil {
		return err
	}
	stored, err := m.append(ls, &Event{
		Kind:    KindPermissionResult,
		Payload: &PermissionResultPayload{ReqID: reqID, Allow: allow, Session: session},
	})
	if err == nil {
		m.broadcast(ls, stored)
	}
	return nil
}

// Answer 回答 agent 的提问,并落一条 ask_user_result(历史回放时选项卡
// 显示已选)。answers=nil 表示取消。
func (m *Manager) Answer(sessionID, reqID string, answers map[string][]string) error {
	if m.ReadOnly {
		return fmt.Errorf("只读模式")
	}
	ls := m.get(sessionID)
	if ls == nil {
		return fmt.Errorf("会话不在运行: %s", sessionID)
	}
	if err := ls.driver.Answer(reqID, answers); err != nil {
		return err
	}
	stored, err := m.append(ls, &Event{
		Kind:    KindAskUserResult,
		Payload: &AskUserResultPayload{ReqID: reqID, Answers: answers, Cancelled: answers == nil},
	})
	if err == nil {
		m.broadcast(ls, stored)
	}
	return nil
}

// Kill 终止会话(进程树)。
func (m *Manager) Kill(sessionID string) error {
	ls := m.get(sessionID)
	if ls == nil {
		return fmt.Errorf("会话不在运行: %s", sessionID)
	}
	return ls.driver.Close()
}

// Delete 删除会话:先结束进程(如果在跑),再连记录、消息、附件目录一起删。
// 与 Kill 的区别:Kill 留历史可续聊,Delete 是彻底不要了。
func (m *Manager) Delete(sessionID string) error {
	if m.ReadOnly {
		return fmt.Errorf("只读模式")
	}
	if ls := m.get(sessionID); ls != nil {
		if err := ls.driver.Close(); err != nil {
			return err
		}
	}
	return m.store.DeleteSession(sessionID)
}

// Subscribe / Unsubscribe 订阅某会话的推送。
func (m *Manager) Subscribe(sessionID string, sub Subscriber) error {
	ls := m.get(sessionID)
	if ls == nil {
		return fmt.Errorf("会话不在运行: %s", sessionID)
	}
	ls.mu.Lock()
	ls.subscribers[sub] = struct{}{}
	ls.mu.Unlock()
	return nil
}

func (m *Manager) Unsubscribe(sessionID string, sub Subscriber) {
	if ls := m.get(sessionID); ls != nil {
		ls.mu.Lock()
		delete(ls.subscribers, sub)
		ls.mu.Unlock()
	}
}

// Watch / Unwatch 列表观察:不订具体会话,收所有会话的状态变化
// (running/idle/dead + 标题)。会话列表据此实时刷新,不用等 REST 重拉。
func (m *Manager) Watch(sub Subscriber) {
	m.mu.Lock()
	m.watchers[sub] = struct{}{}
	m.mu.Unlock()
}

func (m *Manager) Unwatch(sub Subscriber) {
	m.mu.Lock()
	delete(m.watchers, sub)
	m.mu.Unlock()
}

// notifyWatchers 给列表观察者推一条会话状态。观察者没有就免了;
// title 用 DB 最新值(首条消息落标题时别的设备列表也能同步)。
func (m *Manager) notifyWatchers(sessionID, status string) {
	m.mu.Lock()
	subs := make([]Subscriber, 0, len(m.watchers))
	for s := range m.watchers {
		subs = append(subs, s)
	}
	m.mu.Unlock()
	if len(subs) == 0 {
		return
	}
	if s, _ := m.store.GetSession(sessionID); s != nil {
		ev := &Event{
			Kind:      KindSessionStatus,
			SessionID: sessionID,
			Payload:   &SessionStatusPayload{Status: status, Title: s.Title},
		}
		for _, sub := range subs {
			sub.SendEvent(ev)
		}
	}
}

func (m *Manager) get(id string) *liveSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// RunningIDs 存活会话 id 集合(会话列表合并运行态用)。
func (m *Manager) RunningIDs() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]bool, len(m.sessions))
	for id := range m.sessions {
		out[id] = true
	}
	return out
}

// broadcast 给会话订阅者推送;给 stored 补上内存 seq 去重(前端 afterSeq 补差
// 与推送重合时按 seq 丢弃重复)。
func (m *Manager) broadcast(ls *liveSession, stored *Event) {
	ls.mu.Lock()
	subs := make([]Subscriber, 0, len(ls.subscribers))
	for s := range ls.subscribers {
		subs = append(subs, s)
	}
	ls.mu.Unlock()
	for _, s := range subs {
		s.SendEvent(stored)
	}
}

func (m *Manager) titleOf(id string) string {
	sess, err := m.store.GetSession(id)
	if err != nil || sess == nil {
		return id
	}
	if sess.Title != "" {
		return sess.Title
	}
	return sess.Workspace
}

// sessionFilesDir 会话专属附件目录(聊天上传与 tool_result 图片都落这里);
// FilesDir 未启用(测试)返回空串。
func (m *Manager) sessionFilesDir(id string) string {
	if m.store.FilesDir == "" {
		return ""
	}
	return filepath.Join(m.store.FilesDir, id)
}

// resolve 把 workspace 归一化到 root 内的绝对路径(与 terminal.Manager 同语义)。
func (m *Manager) resolve(dir string) (string, error) {
	root := filepath.Clean(m.Root)
	if dir == "" || dir == "/" || dir == "." {
		return root, nil
	}
	clean := filepath.Clean(dir)
	if filepath.IsAbs(clean) && withinRoot(clean, root) {
		return clean, nil
	}
	abs := filepath.Join(root, clean)
	if withinRoot(abs, root) {
		return abs, nil
	}
	return "", fmt.Errorf("目录超出根边界: %s", dir)
}

// withinRoot:root 可能自带尾分隔符(如 "/" 或 "D:\")。
func withinRoot(abs, root string) bool {
	if abs == root {
		return true
	}
	sep := string(filepath.Separator)
	if !strings.HasSuffix(root, sep) {
		root += sep
	}
	return strings.HasPrefix(abs, root)
}
