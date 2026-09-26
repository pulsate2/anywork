package agent

import (
	"sync"
	"testing"
	"time"
)

// snapSub 记下推给它的每一条事件(订阅者只需实现 SendEvent)。
type snapSub struct {
	mu  sync.Mutex
	evs []*Event
}

func (s *snapSub) SendEvent(ev *Event) {
	s.mu.Lock()
	s.evs = append(s.evs, ev)
	s.mu.Unlock()
}

func (s *snapSub) take() []*Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.evs
	s.evs = nil
	return out
}

func (s *snapSub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.evs)
}

// pumpRig 起一个带 pump 的会话,返回 manager 与喂事件函数 —— 订阅快照要在
// 同一个 Manager 上验(Subscribe 是 Manager 的方法)。
func pumpRig(t *testing.T, s *Store, sessID string) (*Manager, *liveSession, func(evs ...Event)) {
	t.Helper()
	m := NewManager(s, "/w", false)
	pd := &pumpDriver{ch: make(chan Event, 16)}
	ls := &liveSession{
		id: sessID, driver: pd,
		subscribers: map[Subscriber]struct{}{},
		state:       StatusRunning,
	}
	m.mu.Lock()
	m.sessions[sessID] = ls
	m.mu.Unlock()
	go m.pump(ls)
	t.Cleanup(func() { close(pd.ch) })
	return m, ls, func(evs ...Event) {
		for _, ev := range evs {
			pd.ch <- ev
		}
	}
}

// eventually 轮询等条件成立:pump 是异步消费的,不能靠 sleep 猜时长。
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等 %s 超时", what)
}

// TestSubscribeStreamSnapshot 直播快照:流式增量不落库,中途订阅(退出立刻重进、
// 切后台回来)的新订阅者本该只看得见半截正文/思考。订阅时把当前缓冲补给它,
// 并带上这段思考已经走了多久 —— 前端秒表据此续上。
func TestSubscribeStreamSnapshot(t *testing.T) {
	s := newTestStore(t)
	sess := createTestSession(t, s)
	m, ls, feed := pumpRig(t, s, sess.ID)

	feed(Event{Kind: KindReasoningDelta, Payload: "先理一下"})
	time.Sleep(150 * time.Millisecond) // 让这段思考"已经走了"一会儿
	feed(Event{Kind: KindAssistantDelta, Payload: "结论是"})
	eventually(t, "增量累进缓冲", func() bool {
		ls.mu.Lock()
		defer ls.mu.Unlock()
		return ls.partialThink == "先理一下" && ls.partialText == "结论是"
	})

	sub := &snapSub{}
	if err := m.Subscribe(sess.ID, sub); err != nil {
		t.Fatalf("订阅: %v", err)
	}
	got := sub.take()
	if len(got) != 1 || got[0].Kind != KindStreamSnapshot {
		t.Fatalf("订阅该收到一条直播快照,得到 %d 条: %+v", len(got), got)
	}
	p, ok := got[0].Payload.(*StreamSnapshotPayload)
	if !ok {
		t.Fatalf("快照负载类型不对: %T", got[0].Payload)
	}
	// 已经流过的正文/思考一段不能少 —— 半截就从这儿来的。
	if p.Text != "结论是" || p.Think != "先理一下" {
		t.Fatalf("快照内容不全: %+v", p)
	}
	if p.ThinkMs < 150 {
		t.Fatalf("快照该带上这段思考已进行的时长(≥150ms),得到 %dms", p.ThinkMs)
	}

	// 快照之后的增量照常到达(且是原始增量,不是又一份快照)。
	feed(Event{Kind: KindReasoningDelta, Payload: ",并发要收紧"})
	eventually(t, "后续增量投递", func() bool { return sub.count() == 1 })
	evs := sub.take()
	if len(evs) != 1 || evs[0].Kind != KindReasoningDelta || evs[0].Payload != ",并发要收紧" {
		t.Fatalf("订阅者该收到后续增量本身: %+v", evs)
	}

	// 生成块收尾(完整正文落库):缓冲清空,再来的人没有快照可补。
	feed(Event{Kind: KindAssistantText, Payload: "结论是……"})
	eventually(t, "边界清空缓冲", func() bool {
		ls.mu.Lock()
		defer ls.mu.Unlock()
		return ls.partialText == "" && ls.partialThink == "" && ls.thinkStart.IsZero()
	})
	late := &snapSub{}
	if err := m.Subscribe(sess.ID, late); err != nil {
		t.Fatalf("订阅: %v", err)
	}
	if got := late.take(); len(got) != 0 {
		t.Fatalf("生成块结束后的订阅不该收到快照: %+v", got)
	}
}

// TestStreamBuffersSurviveMidStreamEvents 子 agent 通知 / API 重试进度 / running
// 状态落在一段思考中间:它们不是生成块边界,缓冲与秒表起点都不能清 —— 清了
// 秒表会以"刚刚"重新起表,读数永远停在 1 秒(与前端 midStream 判断成对)。
func TestStreamBuffersSurviveMidStreamEvents(t *testing.T) {
	s := newTestStore(t)
	sess := createTestSession(t, s)
	m, ls, feed := pumpRig(t, s, sess.ID)

	feed(Event{Kind: KindReasoningDelta, Payload: "先理一下"})
	eventually(t, "增量累进缓冲", func() bool {
		ls.mu.Lock()
		defer ls.mu.Unlock()
		return ls.partialThink == "先理一下"
	})
	// 起点时间戳要原样留着(中间事件不能把它抹掉)。
	ls.mu.Lock()
	start := ls.thinkStart
	ls.mu.Unlock()

	time.Sleep(50 * time.Millisecond)
	feed(Event{Kind: KindStatus, Payload: &StatusPayload{State: StatusRunning}})
	feed(Event{Kind: KindSystemInfo, Payload: &SystemInfoPayload{Type: "task_notification", Text: "子任务完成"}})
	feed(Event{Kind: KindSystemInfo, Payload: &SystemInfoPayload{Type: "api_error", Retry: 1, MaxRetry: 3}})
	time.Sleep(50 * time.Millisecond)

	ls.mu.Lock()
	buffered, kept := ls.partialThink, !ls.thinkStart.IsZero() && ls.thinkStart.Equal(start)
	ls.mu.Unlock()
	if buffered != "先理一下" || !kept {
		t.Fatalf("中间事件不该清缓冲/重置起点:缓冲 %q,起点保持 %v", buffered, kept)
	}
	_ = m
}
