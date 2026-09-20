package agent

import (
	"encoding/json"
	"testing"
	"time"
)

// pumpDriver 带事件通道的 driver:直接喂 pump,验证它落库前对事件的加工
// (思考耗时的记时)。fakeDriver 的 Events() 返回 nil,这里覆盖掉。
type pumpDriver struct {
	fakeDriver
	ch chan Event
}

func (p *pumpDriver) Events() <-chan Event { return p.ch }

// startPump 起一个 pump,返回喂事件的函数(事件按调用顺序进 channel,pump
// 串行消费,保序)。用完自动关流,pump 走退出路径。
func startPump(t *testing.T, s *Store, sessID string) func(evs ...Event) {
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
	return func(evs ...Event) {
		for _, ev := range evs {
			pd.ch <- ev
		}
	}
}

// readStored 轮询等落库:事件走 channel 给 pump,消费是异步的,不能靠 sleep 猜。
func readStored(t *testing.T, s *Store, sessID string, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(mustMessages(t, s, sessID)) >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等落库超时:想要 %d 条,只有 %d 条", n, len(mustMessages(t, s, sessID)))
}

func mustMessages(t *testing.T, s *Store, sessID string) []Event {
	t.Helper()
	msgs, err := s.Messages(sessID, 0, 100)
	if err != nil {
		t.Fatalf("读消息: %v", err)
	}
	return msgs
}

// reasoningOf 取出第一条 reasoning 事件的负载。
func reasoningOf(t *testing.T, msgs []Event) ReasoningPayload {
	t.Helper()
	for _, ev := range msgs {
		if ev.Kind != KindReasoning {
			continue
		}
		raw, ok := ev.Payload.(json.RawMessage)
		if !ok {
			t.Fatalf("reasoning 负载不是 JSON 原文: %T", ev.Payload)
		}
		var p ReasoningPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("解析 reasoning 负载失败: %v(原文 %s)", err, raw)
		}
		return p
	}
	t.Fatal("没有找到 reasoning 事件")
	return ReasoningPayload{}
}

// TestPumpRecordsReasoningDuration 思考耗时由后端在思考开始处「记」下来:
// 首个 reasoning_delta 起表,完整 thinking 块落库前换算成 durationMs 写进
// payload。前端不再拿相邻事件的 created_at 做差 —— 那个差受秒精度和穿插
// 进来的子 agent 通知双重干扰,推算不出真实跨度。
func TestPumpRecordsReasoningDuration(t *testing.T) {
	s := newTestStore(t)
	sess := createTestSession(t, s)
	feed := startPump(t, s, sess.ID)

	// 起点落在「首个增量」而不是「完整块」上:两处之间睡 150ms,记下的耗时
	// 该含这 150ms。增量事件本身不落库,前端推算时量不到这段时间。
	feed(Event{Kind: KindReasoningDelta, Payload: "先理一下"})
	time.Sleep(150 * time.Millisecond)
	feed(Event{Kind: KindReasoning, Payload: "先理一下思路:并发要收紧"})
	readStored(t, s, sess.ID, 1)

	p := reasoningOf(t, mustMessages(t, s, sess.ID))
	if p.Text != "先理一下思路:并发要收紧" {
		t.Fatalf("正文应原样保留,得到 %q", p.Text)
	}
	if p.DurationMs < 150 {
		t.Fatalf("耗时该含「首个增量 → 完整块」的 150ms,得到 %dms", p.DurationMs)
	}
	if p.DurationMs > 5000 {
		t.Fatalf("耗时明显偏大: %dms", p.DurationMs)
	}
}

// TestPumpReasoningDurationResetsAtTurnBoundary 一段思考被用户打断时没有收尾的
// reasoning 事件:起点不能带到下一段,否则下一段会读出一个虚高的耗时。用户
// 消息与回合结束(status idle)都作废起点。
func TestPumpReasoningDurationResetsAtTurnBoundary(t *testing.T) {
	s := newTestStore(t)
	sess := createTestSession(t, s)
	feed := startPump(t, s, sess.ID)

	feed(Event{Kind: KindReasoningDelta, Payload: "被打断的思考"})
	time.Sleep(200 * time.Millisecond)
	feed(Event{Kind: KindUser, Payload: "别想了,先停"})
	feed(Event{Kind: KindStatus, Payload: &StatusPayload{State: StatusIdle}})
	feed(Event{Kind: KindReasoningDelta, Payload: "新回合的思考"})
	feed(Event{Kind: KindReasoning, Payload: "新回合想过的一段"})
	// 增量是瞬态的(只广播不落库),落库的是 user / status / reasoning 三条。
	readStored(t, s, sess.ID, 3)

	p := reasoningOf(t, mustMessages(t, s, sess.ID))
	if p.Text != "新回合想过的一段" {
		t.Fatalf("取到的不是收尾的那段思考: %q", p.Text)
	}
	if p.DurationMs >= 150 {
		t.Fatalf("起点该在用户消息/idle 处作废,得到 %dms(200ms 前的旧起点被带过来了)", p.DurationMs)
	}
}
