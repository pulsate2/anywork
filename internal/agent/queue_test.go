package agent

import (
	"encoding/json"
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
	mu   sync.Mutex
	sent []string
}

func (f *fakeDriver) Start(opts StartOpts) error { return nil }
func (f *fakeDriver) Send(text string) error {
	f.mu.Lock()
	f.sent = append(f.sent, text)
	f.mu.Unlock()
	return nil
}
func (f *fakeDriver) Interrupt() error                                { return nil }
func (f *fakeDriver) Resolve(reqID string, allow, session bool) error { return nil }
func (f *fakeDriver) Answer(reqID string, answers map[string][]string) error {
	return nil
}
func (f *fakeDriver) PendingRequests() []PermissionReqPayload { return nil }
func (f *fakeDriver) ExternalID() string                      { return "" }
func (f *fakeDriver) CurrentModel() string                    { return "" }
func (f *fakeDriver) ApplySettings(u SettingsUpdate) error    { return nil }
func (f *fakeDriver) Events() <-chan Event                    { return nil }
func (f *fakeDriver) Done() <-chan struct{}                   { return nil }
func (f *fakeDriver) ExitErr() error                          { return nil }
func (f *fakeDriver) ExitDetail() string                      { return "" }
func (f *fakeDriver) Close() error                            { return nil }

// TestReleaseQueued 回合结束放行:pump 每 idle 放一条,逐条串行;放行后
// 队列里少一条、时间线上多一条 user 消息。
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

	// 第一个 idle:放行队首。
	m.releaseQueued(ls)
	if got := fd.sent; len(got) != 1 || got[0] != "排队A" {
		t.Fatalf("第一次放行应只发「排队A」,发了 %v", got)
	}
	if left, _ := s.Queued(sess.ID); len(left) != 1 || left[0].Text != "排队B" {
		t.Fatalf("放行后队列应剩「排队B」: %v", left)
	}
	// 连续两个 idle 不该挤进同一回合:放行后 state 已被抢占成 running。
	if ls.state != StatusRunning {
		t.Fatalf("放行后 state 应为 running,得到 %s", ls.state)
	}

	// 模拟下一回合结束(pump 置回 idle)再放行第二条。
	ls.mu.Lock()
	ls.state = StatusIdle
	ls.mu.Unlock()
	m.releaseQueued(ls)
	if got := fd.sent; len(got) != 2 || got[1] != "排队B" {
		t.Fatalf("第二次放行应发「排队B」,发了 %v", got)
	}

	// 两条都该作为 user 消息落库(时间线上可见)。
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

	// 队列清空后再 idle:不再发送。
	ls.mu.Lock()
	ls.state = StatusIdle
	ls.mu.Unlock()
	m.releaseQueued(ls)
	if got := fd.sent; len(got) != 2 {
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
