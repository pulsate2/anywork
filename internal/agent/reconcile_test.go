package agent

import (
	"encoding/json"
	"testing"
)

// TestReconcilePending 新进程接管会话后收尾旧进程遗留的待审批/待回答:
// 只有 request 没有 result 的,补一条取消型 result(审批 Cancelled+Allow=false、
// 提问 Cancelled);已经答复过的不再重复补。这是用户实测 bug 的根治 —— 重启前
// 没审批的最后一张卡,重启后前端回放永远停在"等待审批",底部状态一直亮着。
func TestReconcilePending(t *testing.T) {
	s := newTestStore(t)
	sess := createTestSession(t, s)

	// 种三条:r1 待审批(无 result)、r2 已答复、a1 待回答(无 result)。
	seed := func(kind string, payload any) {
		if _, err := s.AppendMessage(sess.ID, &Event{Kind: kind, Payload: payload}); err != nil {
			t.Fatalf("种事件 %s: %v", kind, err)
		}
	}
	seed(KindPermissionReq, &PermissionReqPayload{ReqID: "r1", Tool: "edit"})
	seed(KindPermissionReq, &PermissionReqPayload{ReqID: "r2", Tool: "bash"})
	seed(KindPermissionResult, &PermissionResultPayload{ReqID: "r2", Allow: true})
	seed(KindAskUser, &AskUserPayload{ReqID: "a1"})

	m := NewManager(s, "/w", false)
	ls := &liveSession{
		id: sess.ID, driver: &fakeDriver{},
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

	m.reconcilePending(ls)

	msgs, err := s.Messages(sess.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	// 待审批 r1 补了一条取消型 result;已答复的 r2 不再补(其 result 仍是放行)。
	var r1Cancel, r2Results int
	var askCancel bool
	for _, ev := range msgs {
		switch ev.Kind {
		case KindPermissionResult:
			var p PermissionResultPayload
			if err := json.Unmarshal(ev.Payload.(json.RawMessage), &p); err != nil {
				t.Fatal(err)
			}
			if p.ReqID == "r1" {
				r1Cancel++
				if p.Allow || !p.Cancelled {
					t.Fatalf("r1 收尾应是 Allow=false+Cancelled,得到 %+v", p)
				}
			}
			if p.ReqID == "r2" {
				r2Results++
				if !p.Allow || p.Cancelled {
					t.Fatalf("r2 原放行结论被动过: %+v", p)
				}
			}
		case KindAskUserResult:
			var p AskUserResultPayload
			if err := json.Unmarshal(ev.Payload.(json.RawMessage), &p); err != nil {
				t.Fatal(err)
			}
			if p.ReqID == "a1" && p.Cancelled {
				askCancel = true
			}
		}
	}
	if r1Cancel != 1 {
		t.Fatalf("r1 应补且只补一条取消 result,得到 %d 条", r1Cancel)
	}
	if r2Results != 1 {
		t.Fatalf("r2 应只有原来那条 result,得到 %d 条", r2Results)
	}
	if !askCancel {
		t.Fatal("待回答 a1 应补一条 Cancelled 的 ask_user_result")
	}

	// 收尾事件也广播给了在线订阅者(前端在线时当场收卡,不必等刷新)。
	// 广播走的是 append 返回的事件,Payload 仍是落库前的强类型对象(非 RawMessage)。
	var bcastResult, bcastAsk bool
	for _, ev := range sub.got {
		if ev.Kind == KindPermissionResult {
			if p, ok := ev.Payload.(*PermissionResultPayload); ok && p.ReqID == "r1" && p.Cancelled {
				bcastResult = true
			}
		}
		if ev.Kind == KindAskUserResult {
			bcastAsk = true
		}
	}
	if !bcastResult || !bcastAsk {
		t.Fatalf("收尾应广播给订阅者: result=%v ask=%v", bcastResult, bcastAsk)
	}
}
