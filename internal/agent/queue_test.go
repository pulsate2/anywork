package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"lightremote/internal/db"
)

// newTestStore 每个用例独立的临时库(跑全套迁移,agent_queue 也在内)。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "data"), "")
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return NewStore(d.DB)
}

func createTestSession(t *testing.T, s *Store) *Session {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	sess := &Session{
		ID: "s-test", App: AppClaude, Workspace: "/w", PermissionMode: PermAsk,
		Status: StatusRunning, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateSession(sess); err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	return sess
}

// TestQueueStore 排队消息的库层操作:入队顺序、放行取最早、撤回幂等语义、
// 空队列放行为 nil、删会话清队列。
func TestQueueStore(t *testing.T) {
	s := newTestStore(t)
	sess := createTestSession(t, s)

	for _, txt := range []string{"第一条", "第二条", "第三条"} {
		if _, err := s.Enqueue(sess.ID, txt); err != nil {
			t.Fatalf("入队 %q: %v", txt, err)
		}
	}
	items, err := s.Queued(sess.ID)
	if err != nil || len(items) != 3 {
		t.Fatalf("Queued = %v, %v;想要 3 条", items, err)
	}
	if items[0].Text != "第一条" || items[2].Text != "第三条" {
		t.Fatalf("入队顺序错了: %v", items)
	}

	// 放行取最早一条,且取走即删。
	first, err := s.PopQueued(sess.ID)
	if err != nil || first == nil || first.Text != "第一条" {
		t.Fatalf("PopQueued = %v, %v;想要第一条", first, err)
	}
	if left, _ := s.Queued(sess.ID); len(left) != 2 {
		t.Fatalf("放行后应剩 2 条,还有 %d", len(left))
	}

	// 撤回成功一次,再撤同一条返回 false(已放行/已撤回)。
	if ok, _ := s.CancelQueued(sess.ID, items[1].ID); !ok {
		t.Fatal("撤回在队的消息应成功")
	}
	if ok, _ := s.CancelQueued(sess.ID, items[1].ID); ok {
		t.Fatal("重复撤回应返回 false")
	}
	if left, _ := s.Queued(sess.ID); len(left) != 1 {
		t.Fatalf("撤回后应剩 1 条,还有 %d", len(left))
	}

	// 空队列放行返回 nil 不报错。
	third, _ := s.PopQueued(sess.ID)
	if third == nil || third.Text != "第三条" {
		t.Fatalf("第三次放行应拿到第三条: %v", third)
	}
	if empty, err := s.PopQueued(sess.ID); err != nil || empty != nil {
		t.Fatalf("空队列放行应返回 nil: %v, %v", empty, err)
	}

	// 删会话连队列一起清。
	if _, err := s.Enqueue(sess.ID, "要被连坐的"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.Queued(sess.ID); len(left) != 0 {
		t.Fatalf("删会话后队列应清空,还有 %d 条", len(left))
	}
}

// fakeDriver 只记录 Send 的最小实现:让 releaseQueued 的全链路
// (出队 → Send → 落库)能在没有真 CLI 的测试里跑通。
type fakeDriver struct {
	mu       sync.Mutex
	sent     []string
	closed   bool // Close 被调过(stale 重启要杀旧进程)
	applyErr bool // 模拟 claude:ApplySettings 报"运行中改不了"
	// resolves Resolve 收到的 reqID(切全部放行时的代答应长这样);
	// pending 是 PendingRequests 的固定快照(模拟挂着的询问)。
	resolves []string
	pending  []PermissionReqPayload
}

func (f *fakeDriver) Start(opts StartOpts) error { return nil }
func (f *fakeDriver) Send(text string) error {
	f.mu.Lock()
	f.sent = append(f.sent, text)
	f.mu.Unlock()
	return nil
}
func (f *fakeDriver) Interrupt() error { return nil }
func (f *fakeDriver) Resolve(reqID string, allow, session bool) error {
	f.mu.Lock()
	f.resolves = append(f.resolves, reqID)
	f.mu.Unlock()
	return nil
}
func (f *fakeDriver) Answer(reqID string, answers map[string][]string) error {
	return nil
}
func (f *fakeDriver) PendingRequests() []PermissionReqPayload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PermissionReqPayload(nil), f.pending...)
}
func (f *fakeDriver) ExternalID() string                      { return "" }
func (f *fakeDriver) CurrentModel() string                    { return "" }
func (f *fakeDriver) ApplySettings(u SettingsUpdate) error {
	if f.applyErr {
		return fmt.Errorf("claude 会话运行中不支持调整")
	}
	return nil
}
func (f *fakeDriver) Events() <-chan Event  { return nil }
func (f *fakeDriver) Done() <-chan struct{} { return nil }
func (f *fakeDriver) ExitErr() error        { return nil }
func (f *fakeDriver) ExitDetail() string    { return "" }
func (f *fakeDriver) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

// TestReleaseQueued 回合结束放行:一次把队列放完(不再一轮只放队首一条),
// 逐条按序 Send、逐条落库成 user 消息;放行后队列空、idle 槽位被占成 running。
func TestReleaseQueued(t *testing.T) {
	s := newTestStore(t)
	sess := createTestSession(t, s)
	m := NewManager(s, "/w", false)
	fd := &fakeDriver{}
	ls := &liveSession{
		id: sess.ID, driver: fd,
		subscribers: map[Subscriber]struct{}{},
		state:       StatusIdle,
	}
	m.mu.Lock()
	m.sessions[sess.ID] = ls
	m.mu.Unlock()

	if _, err := s.Enqueue(sess.ID, "排队A"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(sess.ID, "排队B"); err != nil {
		t.Fatal(err)
	}

	// 一个 idle 把两条一起交出去:顺序不能乱。
	m.releaseQueued(ls)
	if got := fd.sent; len(got) != 2 || got[0] != "排队A" || got[1] != "排队B" {
		t.Fatalf("放行应一次发完「排队A」「排队B」,发了 %v", got)
	}
	if left, _ := s.Queued(sess.ID); len(left) != 0 {
		t.Fatalf("放行后队列该空了: %v", left)
	}
	// 放行后 state 已被抢占成 running:这一轮里其余 idle 事件不再放行,
	// 也就不会挤进同一回合重复发。
	if ls.state != StatusRunning {
		t.Fatalf("放行后 state 应为 running,得到 %s", ls.state)
	}
	m.releaseQueued(ls)
	if got := fd.sent; len(got) != 2 {
		t.Fatalf("回合进行中不该再放行: %v", got)
	}

	// 两条都该作为 user 消息落库(时间线上可见,顺序不变)。
	msgs, err := s.Messages(sess.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var users []string
	for _, ev := range msgs {
		if ev.Kind == KindUser {
			// 库里读回的 payload 是 JSON 原文(字符串消息就是带引号的 JSON 串)。
			var p string
			if raw, ok := ev.Payload.(json.RawMessage); ok && json.Unmarshal(raw, &p) == nil {
				users = append(users, p)
			}
		}
	}
	if len(users) != 2 || users[0] != "排队A" || users[1] != "排队B" {
		t.Fatalf("放行的两条都应落库为 user 事件: %v", users)
	}

	// 新回合里又攒了一条:下一个 idle 照常放行。
	if _, err := s.Enqueue(sess.ID, "排队C"); err != nil {
		t.Fatal(err)
	}
	ls.mu.Lock()
	ls.state = StatusIdle
	ls.mu.Unlock()
	m.releaseQueued(ls)
	if got := fd.sent; len(got) != 3 || got[2] != "排队C" {
		t.Fatalf("下一个 idle 应放行「排队C」,发了 %v", got)
	}

	// 队列清空后再 idle:不再发送,state 归还 idle。
	ls.mu.Lock()
	ls.state = StatusIdle
	ls.mu.Unlock()
	m.releaseQueued(ls)
	if got := fd.sent; len(got) != 3 {
		t.Fatalf("空队列放行不应发送: %v", got)
	}
	if ls.state != StatusIdle {
		t.Fatalf("空放行应归还 idle 槽位,得到 %s", ls.state)
	}
}

// TestEnqueueIdleDirectSend 会话空闲时入队 = 直接发送(前端 running 状态
// 滞后于真实回合的场景):不排队,立即进时间线。
func TestEnqueueIdleDirectSend(t *testing.T) {
	s := newTestStore(t)
	sess := createTestSession(t, s)
	m := NewManager(s, "/w", false)
	fd := &fakeDriver{}
	ls := &liveSession{
		id: sess.ID, driver: fd,
		subscribers: map[Subscriber]struct{}{},
		state:       StatusIdle,
	}
	m.mu.Lock()
	m.sessions[sess.ID] = ls
	m.mu.Unlock()

	queued, item, ev, err := m.Enqueue(sess.ID, "恰好空闲")
	if err != nil {
		t.Fatal(err)
	}
	if queued || item != nil || ev == nil {
		t.Fatalf("空闲时应直接发送: queued=%v item=%v ev=%v", queued, item, ev)
	}
	if len(fd.sent) != 1 || fd.sent[0] != "恰好空闲" {
		t.Fatalf("应立即发给 driver: %v", fd.sent)
	}
	if left, _ := s.Queued(sess.ID); len(left) != 0 {
		t.Fatalf("空闲直发不该入队: %v", left)
	}
}

// recSub 记录收到的事件,断言订阅转移用。
type recSub struct{ got []*Event }

func (r *recSub) SendEvent(ev *Event) { r.got = append(r.got, ev) }

// TestSettingsRestartStaleSession claude 式 driver(ApplySettings 报错)改
// 设置后:标记 stale,下一条消息触发重启 —— 旧进程被杀、订阅者转挂新会话、
// 新进程收到消息;再发一条不再重启(设置没再改)。
func TestSettingsRestartStaleSession(t *testing.T) {
	// 注入假 driver 工厂:记录每个 driver 的生死与发送。
	var mu sync.Mutex
	var made []*fakeDriver
	newDriver = func(app string) (Driver, error) {
		fd := &fakeDriver{applyErr: true} // 模拟 claude:运行中改不了
		mu.Lock()
		made = append(made, fd)
		mu.Unlock()
		return fd, nil
	}
	t.Cleanup(func() {
		newDriver = func(app string) (Driver, error) { // 还原
			switch app {
			case AppClaude:
				return newClaudeDriver(), nil
			case AppCodex:
				return newCodexDriver(), nil
			default:
				return nil, fmt.Errorf("未知 agent: %s", app)
			}
		}
	})

	s := newTestStore(t)
	now := time.Now().UTC().Format(time.RFC3339)
	sess := &Session{
		ID: "s-restart", App: AppClaude, Workspace: "/w", PermissionMode: PermAsk,
		ExternalID: "ext-1", Status: StatusRunning, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateSession(sess); err != nil {
		t.Fatal(err)
	}
	m := NewManager(s, "/w", false)

	// 会话已在跑:第一条消息走旧进程。
	if _, err := m.Send(sess.ID, "第一条"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	mu.Lock()
	first := made[0]
	mu.Unlock()
	if got := first.sent; len(got) != 1 {
		t.Fatalf("第一条应发给旧进程: %v", got)
	}

	// 订阅旧会话,再改设置(claude 式 driver 会报"改不了")。
	sub := &recSub{}
	if err := m.Subscribe(sess.ID, sub); err != nil {
		t.Fatal(err)
	}
	_, _, note := m.Settings(sess.ID, SettingsUpdate{PermissionMode: PermAccept})
	if note == "" {
		t.Fatal("claude 式 driver 改设置应有提示")
	}

	// 第二条消息:stale 触发重启。
	if _, err := m.Send(sess.ID, "第二条"); err != nil {
		t.Fatalf("Send after settings: %v", err)
	}
	mu.Lock()
	if len(made) != 2 {
		mu.Unlock()
		t.Fatalf("应重启出一个新 driver,得到 %d 个", len(made))
	}
	fresh := made[1]
	mu.Unlock()
	if !first.closed {
		t.Fatal("旧进程应被关闭")
	}
	if got := fresh.sent; len(got) != 1 || got[0] != "第二条" {
		t.Fatalf("新进程应收到「第二条」: %v", got)
	}
	// 订阅者已转挂新会话:后续广播直接到达,前端无感。
	cur := m.get(sess.ID)
	cur.mu.Lock()
	_, ok := cur.subscribers[sub]
	nsub := len(cur.subscribers)
	cur.mu.Unlock()
	if cur.driver != fresh || !ok || nsub != 1 {
		t.Fatalf("订阅应转移到新会话(driver/订阅者不对)")
	}

	// 第三条:设置没再改,不再重启,直接发给新进程。
	if _, err := m.Send(sess.ID, "第三条"); err != nil {
		t.Fatalf("Send 3: %v", err)
	}
	mu.Lock()
	nmade := len(made)
	mu.Unlock()
	if nmade != 2 {
		t.Fatalf("设置没改不应再次重启,共 %d 个 driver", nmade)
	}
	if got := fresh.sent; len(got) != 2 {
		t.Fatalf("第三条应发给新进程: %v", got)
	}
}

// TestRestartSessionWithoutExternalID 新建的 claude 会话在发出首条消息前
// external_id 是空的(claude 的 session_id 要等第一条消息的 init 才吐),
// 此时改模型/权限会标 stale,下一条消息触发 restart → reviveLocked。
// 回归:早先 reviveLocked 无条件要 external_id,这条路径报"该会话没有可恢复
// 的凭据(进程未成功启动过)",新建会话从改设置起就废了 —— 用户报的正是
// 「新建 claude → 进入改放行 → 该会话没有可恢复的凭据」。没有可续的东西
// 就该新起一个进程,消息照发。
func TestRestartSessionWithoutExternalID(t *testing.T) {
	var mu sync.Mutex
	var made []*fakeDriver
	newDriver = func(app string) (Driver, error) {
		fd := &fakeDriver{applyErr: true} // claude 式:运行中改不了
		mu.Lock()
		made = append(made, fd)
		mu.Unlock()
		return fd, nil
	}
	t.Cleanup(func() {
		newDriver = func(app string) (Driver, error) {
			switch app {
			case AppClaude:
				return newClaudeDriver(), nil
			case AppCodex:
				return newCodexDriver(), nil
			default:
				return nil, fmt.Errorf("未知 agent: %s", app)
			}
		}
	})

	s := newTestStore(t)
	sess := createTestSession(t, s) // ExternalID 留空:还没发过消息的新会话
	m := NewManager(s, "/w", false)
	// 进程已经在跑(Create 时就 spawn),但 CLI 侧还没建会话。
	running := &fakeDriver{applyErr: true}
	ls := &liveSession{
		id: sess.ID, driver: running,
		subscribers: map[Subscriber]struct{}{},
		state:       StatusIdle,
	}
	m.mu.Lock()
	m.sessions[sess.ID] = ls
	m.mu.Unlock()

	// 改权限为全部放行:落库 + 标 stale。
	if _, _, note := m.Settings(sess.ID, SettingsUpdate{PermissionMode: PermAccept}); note == "" {
		t.Fatal("claude 式 driver 改设置应有提示(下一条消息重启)")
	}

	// 首条消息:stale 触发重启,不能因没有续聊凭据失败。
	if _, err := m.Send(sess.ID, "第一条"); err != nil {
		t.Fatalf("新建会话改设置后首条消息不该失败: %v", err)
	}
	mu.Lock()
	n := len(made)
	var fresh *fakeDriver
	if n > 0 {
		fresh = made[n-1]
	}
	mu.Unlock()
	if n != 1 {
		t.Fatalf("应新起一个进程,得到 %d 个", n)
	}
	if !running.closed {
		t.Fatal("旧进程应被关闭")
	}
	if got := fresh.sent; len(got) != 1 || got[0] != "第一条" {
		t.Fatalf("新进程应收到「第一条」: %v", got)
	}

	// 进程又死了(重进页面/服务重启后标死):再发消息走 revive,同样不能
	// 因为没有 external_id 报错 —— 会话记录在,用户不该被迫删了重建。
	m.mu.Lock()
	delete(m.sessions, sess.ID)
	m.mu.Unlock()
	if _, err := m.Send(sess.ID, "第二条"); err != nil {
		t.Fatalf("无凭据的死会话复活不该失败: %v", err)
	}
	mu.Lock()
	n = len(made)
	fresh = made[n-1]
	mu.Unlock()
	if n != 2 {
		t.Fatalf("应再起一个进程,共 %d 个", n)
	}
	if got := fresh.sent; len(got) != 1 || got[0] != "第二条" {
		t.Fatalf("复活的新进程应收到「第二条」: %v", got)
	}
}

// TestSettingsAcceptAutoApprovePending 运行中切到全部放行立即兑现:落库、
// 标 stale(下一条消息重启)之外,挂着的询问当场代答 —— Resolve 放行 +
// permission_result 落库广播,屏上的审批卡随事件收掉,不用等下一轮重启。
func TestSettingsAcceptAutoApprovePending(t *testing.T) {
	fd := &fakeDriver{
		applyErr: true, // claude 式:spawn 级参数,重启才生效
		pending:  []PermissionReqPayload{{ReqID: "r1"}, {ReqID: "r2"}},
	}
	s := newTestStore(t)
	sess := createTestSession(t, s)
	m := NewManager(s, "/w", false)
	ls := &liveSession{
		id: sess.ID, driver: fd,
		subscribers: map[Subscriber]struct{}{},
		state:       StatusRunning,
	}
	m.mu.Lock()
	m.sessions[sess.ID] = ls
	m.mu.Unlock()

	sub := &recSub{}
	if err := m.Subscribe(sess.ID, sub); err != nil {
		t.Fatal(err)
	}
	updated, err, note := m.Settings(sess.ID, SettingsUpdate{PermissionMode: PermAccept})
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if note == "" {
		t.Fatal("claude 式 driver 改设置应有提示")
	}
	if updated.PermissionMode != PermAccept {
		t.Fatalf("设置应已落库: %s", updated.PermissionMode)
	}
	// 挂着的两个询问都被代答放行。
	fd.mu.Lock()
	resolves := append([]string(nil), fd.resolves...)
	fd.mu.Unlock()
	if len(resolves) != 2 || resolves[0] != "r1" || resolves[1] != "r2" {
		t.Fatalf("挂着的询问应都被代答: %v", resolves)
	}
	// 代答走 Approve:permission_result 落库并广播,前端卡靠它收掉。
	var results []*Event
	for _, ev := range sub.got {
		if ev.Kind == KindPermissionResult {
			results = append(results, ev)
		}
	}
	if len(results) != 2 {
		t.Fatalf("应广播 2 条 permission_result,得到 %d 条", len(results))
	}
	msgs, err := s.Messages(sess.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var stored int
	for _, ev := range msgs {
		if ev.Kind == KindPermissionResult {
			stored++
		}
	}
	if stored != 2 {
		t.Fatalf("permission_result 应落库 2 条,得到 %d 条", stored)
	}
}
