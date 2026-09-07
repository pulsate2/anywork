package agent

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestCodexTurnIdTracking turn/interrupt 必须指名回合(缺 turnId 直接被
// app-server 拒):task_started 记下 turn_id,终态事件清掉。新旧通知格式都认。
func TestCodexTurnIdTracking(t *testing.T) {
	d := newCodexDriver()
	defer func() { d.doneOnce.Do(func() { close(d.done) }) }()

	wrapped := func(inner string) {
		t.Helper()
		params, _ := json.Marshal(map[string]any{"msg": json.RawMessage(inner)})
		d.handleNotification("codex/event/task_started", params)
	}

	wrapped(`{"type":"task_started","turn_id":"turn-1"}`)
	if d.turnID != "turn-1" {
		t.Fatalf("task_started 后 turnID = %q,想要 turn-1", d.turnID)
	}
	// camelCase 兜底(版本间字段名有差异)。
	wrapped(`{"type":"task_started","turnId":"turn-2"}`)
	if d.turnID != "turn-2" {
		t.Fatalf("camelCase turnId 没认: %q", d.turnID)
	}
	// 老格式 turn/started:实测 0.144.5 的 turnId 嵌在 params.turn.id。
	params, _ := json.Marshal(map[string]any{"turn": map[string]any{"id": "turn-3", "status": "inProgress"}})
	d.handleNotification("turn/started", params)
	if d.turnID != "turn-3" {
		t.Fatalf("老格式 turn/started 没记 turnID: %q", d.turnID)
	}
	// 终态清空:新格式 task_complete / 老格式 turn/completed。
	params, _ = json.Marshal(map[string]any{"msg": json.RawMessage(`{"type":"task_complete"}`)})
	d.handleNotification("codex/event/task_complete", params)
	if d.turnID != "" {
		t.Fatalf("task_complete 后 turnID 应清空: %q", d.turnID)
	}
	params, _ = json.Marshal(map[string]any{"turn": map[string]any{"id": "turn-4"}})
	d.handleNotification("turn/started", params)
	if d.turnID != "turn-4" {
		t.Fatalf("turn/started 二次记录失败: %q", d.turnID)
	}
	params, _ = json.Marshal(map[string]any{"status": "complete"})
	d.handleNotification("turn/completed", params)
	if d.turnID != "" {
		t.Fatalf("turn/completed 后 turnID 应清空: %q", d.turnID)
	}
}

// TestCodexStreamingDeltas 流式增量:老格式 item/agentMessage/delta、
// 新格式包装 agent_message_delta 都要变成 assistant_delta 事件,reasoning
// 同理。完整消息随后落库,增量本身不落库(manager.pump 只广播)。
func TestCodexStreamingDeltas(t *testing.T) {
	d := newCodexDriver()
	defer func() { d.doneOnce.Do(func() { close(d.done) }) }()

	oldFmt := func(method, inner string) {
		t.Helper()
		params, _ := json.Marshal(json.RawMessage(inner))
		d.handleNotification(method, params)
	}
	wrapped := func(inner string) {
		t.Helper()
		params, _ := json.Marshal(map[string]any{"msg": json.RawMessage(inner)})
		d.handleNotification("codex/event/agent_message_delta", params)
	}

	oldFmt("item/agentMessage/delta", `{"threadId":"th","turnId":"t1","itemId":"m1","delta":"你"}`)
	oldFmt("item/agentMessage/delta", `{"threadId":"th","turnId":"t1","itemId":"m1","delta":"好"}`)
	oldFmt("item/reasoning/summaryTextDelta", `{"threadId":"th","turnId":"t1","itemId":"r1","delta":"想一想"}`)
	wrapped(`{"type":"agent_message_delta","delta":"!"}`)

	expect := []struct {
		kind string
		text string
	}{
		{KindAssistantDelta, "你"},
		{KindAssistantDelta, "好"},
		{KindReasoningDelta, "想一想"},
		{KindAssistantDelta, "!"},
	}
	for i, want := range expect {
		select {
		case ev := <-d.events:
			if ev.Kind != want.kind {
				t.Errorf("事件 %d: kind = %q,想要 %q", i, ev.Kind, want.kind)
			}
			if s, _ := ev.Payload.(string); s != want.text {
				t.Errorf("事件 %d: payload = %q,想要 %q", i, s, want.text)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("事件 %d 没到", i)
		}
	}
	// delta 为空的通知不该发事件。
	oldFmt("item/agentMessage/delta", `{"itemId":"m1"}`)
	select {
	case ev := <-d.events:
		t.Errorf("空 delta 多发了事件: %+v", ev)
	default:
	}
}

// TestCodexPlanUpdate update_plan 不出 item 事件,只有 turn/plan/updated
// 通知(实测 0.144.5):{plan:[{step,status}]}。合成 update_plan 工具调用
// (前端任务面板的形状),status 从 camelCase 归一成 snake_case。
func TestCodexPlanUpdate(t *testing.T) {
	d := newCodexDriver()
	defer func() { d.doneOnce.Do(func() { close(d.done) }) }()

	params, _ := json.Marshal(map[string]any{
		"threadId": "th", "turnId": "t1",
		"plan": []map[string]string{
			{"step": "创建两步计划并开始第一步", "status": "inProgress"},
			{"step": "创建 hello.txt 并写入 hi", "status": "pending"},
			{"step": "", "status": "completed"}, // 空 step 跳过
		},
	})
	d.handleNotification("turn/plan/updated", params)

	ev := <-d.events
	call, ok := ev.Payload.(*ToolCallPayload)
	if !ok {
		t.Fatalf("payload 类型 %T,想要 *ToolCallPayload", ev.Payload)
	}
	if call.Tool != "update_plan" || call.ToolUseID != "codex-plan" {
		t.Fatalf("tool = %q id = %q,想要 update_plan/codex-plan", call.Tool, call.ToolUseID)
	}
	var args struct {
		Plan []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"plan"`
	}
	if json.Unmarshal([]byte(call.Args), &args) != nil || len(args.Plan) != 2 {
		t.Fatalf("plan 解析失败或长度不对: %s", call.Args)
	}
	if args.Plan[0].Status != "in_progress" || args.Plan[1].Status != "pending" {
		t.Fatalf("status 归一不对: %+v", args.Plan)
	}

	// 新版包装格式(msgType plan_update)也走同一归一。
	wrapped, _ := json.Marshal(map[string]any{
		"msg": json.RawMessage(`{"type":"plan_update","plan":[{"step":"全部完成","status":"completed"}]}`),
	})
	d.handleNotification("codex/event/plan_update", wrapped)
	ev = <-d.events
	call, ok = ev.Payload.(*ToolCallPayload)
	if !ok || call.Tool != "update_plan" {
		t.Fatalf("包装格式没合成 update_plan: %+v", ev.Payload)
	}
	if err := json.Unmarshal([]byte(call.Args), &args); err != nil || len(args.Plan) != 1 || args.Plan[0].Status != "completed" {
		t.Fatalf("包装格式 plan 不对: %s (%v)", call.Args, err)
	}

	// 空快照不发事件。
	empty, _ := json.Marshal(map[string]any{"plan": []map[string]string{}})
	d.handleNotification("turn/plan/updated", empty)
	select {
	case ev := <-d.events:
		t.Errorf("空 plan 多发了事件: %+v", ev)
	default:
	}
}

// TestCodexGoalAndImageView thread/goal/updated 去重(token 计数每次都推,
// 只有目标/状态变化才发卡)与 imageView(view_image 工具的 item 形态)。
func TestCodexGoalAndImageView(t *testing.T) {
	d := newCodexDriver()
	defer func() { d.doneOnce.Do(func() { close(d.done) }) }()

	goal := func(objective, status string, used int) {
		t.Helper()
		params, _ := json.Marshal(map[string]any{
			"threadId": "th", "turnId": "t1",
			"goal": map[string]any{"objective": objective, "status": status, "tokensUsed": used},
		})
		d.handleNotification("thread/goal/updated", params)
	}
	goal("建立工作流", "active", 100)
	goal("建立工作流", "active", 9000)   // 纯 token 变化:不重复发卡
	goal("建立工作流", "complete", 9100) // 状态变了:发卡

	for i, wantStatus := range []string{"active", "complete"} {
		ev := <-d.events
		call, ok := ev.Payload.(*ToolCallPayload)
		if !ok || call.Tool != "CodexGoal" {
			t.Fatalf("事件 %d: %+v,想要 CodexGoal", i, ev.Payload)
		}
		var args struct {
			Objective string `json:"objective"`
			Status    string `json:"status"`
		}
		if json.Unmarshal([]byte(call.Args), &args) != nil || args.Status != wantStatus {
			t.Fatalf("事件 %d args 不对: %s", i, call.Args)
		}
	}

	// goal 清除后,同一目标再来也算新卡。
	params, _ := json.Marshal(map[string]any{"threadId": "th"})
	d.handleNotification("thread/goal/cleared", params)
	goal("新目标", "active", 1)
	ev := <-d.events
	if call, _ := ev.Payload.(*ToolCallPayload); call == nil || call.Tool != "CodexGoal" {
		t.Fatalf("cleared 后没重新发卡: %+v", ev.Payload)
	}

	// imageView:view_image 的 item 只有 path。
	itemParams := func() json.RawMessage {
		b, _ := json.Marshal(map[string]any{
			"item": map[string]any{"type": "imageView", "id": "exec-img-1", "path": "/tmp/img.png"},
		})
		return b
	}
	d.handleNotification("item/started", itemParams())
	ev = <-d.events
	call, ok := ev.Payload.(*ToolCallPayload)
	if !ok || call.Tool != "view_image" || !strings.Contains(call.Args, "/tmp/img.png") {
		t.Fatalf("imageView started 没发卡: %+v", ev.Payload)
	}
	b, _ := json.Marshal(map[string]any{
		"item": map[string]any{"type": "imageView", "id": "exec-img-1", "path": "/tmp/img.png"},
	})
	d.handleNotification("item/completed", b)
	ev = <-d.events
	if call, _ := ev.Payload.(*ToolCallPayload); call == nil || call.State != "ok" {
		t.Fatalf("imageView completed 没落终态: %+v", ev.Payload)
	}
}

// TestCodexMultiAgent 多智能体两条通道:v2 的 rawResponseItem/completed 裸
// 函数调用(functioncall 开卡 / functioncalloutput 收卡,namespace 与参数
// 形状过滤)和 v1 的 collabagenttoolcall item;同一 call_id 只认先到的。
func TestCodexMultiAgent(t *testing.T) {
	d := newCodexDriver()
	defer func() { d.doneOnce.Do(func() { close(d.done) }) }()

	notify := func(method, params string) {
		t.Helper()
		d.handleNotification(method, json.RawMessage(params))
	}

	// ---- v2 裸调用 ----
	// arguments 是 JSON 字符串;spawn 带 task_name 是 v2。
	notify("rawResponseItem/completed",
		`{"item":{"type":"functioncall","call_id":"c1","name":"spawn_agent","arguments":"{\"task_name\":\"调研\",\"prompt\":\"查资料\"}"}}`)
	// v1 固定命名空间:排除。
	notify("rawResponseItem/completed",
		`{"item":{"type":"functioncall","call_id":"c2","name":"spawn_agent","namespace":"multi_agent_v1","arguments":"{\"task_name\":\"x\"}"}}`)
	// spawn 没有 task_name(消息类之外全靠形状辨认):不是 v2。
	notify("rawResponseItem/completed",
		`{"item":{"type":"functioncall","call_id":"c3","name":"spawn_agent","arguments":"{\"prompt\":\"x\"}"}}`)
	// wait 带 targets 数组:不是 v2。
	notify("rawResponseItem/completed",
		`{"item":{"type":"functioncall","call_id":"c4","name":"wait_agent","arguments":"{\"targets\":[\"a1\"]}"}}`)
	// 非 agent 工具名。
	notify("rawResponseItem/completed",
		`{"item":{"type":"functioncall","call_id":"c5","name":"apply_patch","arguments":"{}"}}`)
	// output 收卡。
	notify("rawResponseItem/completed",
		`{"item":{"type":"functioncalloutput","call_id":"c1","output":"{\"agent_id\":\"a1\"}"}}`)
	// 同 call_id 的 functioncall 再来(重放):不重复发卡。
	notify("rawResponseItem/completed",
		`{"item":{"type":"functioncall","call_id":"c1","name":"spawn_agent","arguments":"{\"task_name\":\"again\"}"}}`)

	ev := <-d.events
	call, ok := ev.Payload.(*ToolCallPayload)
	if !ok || call.Tool != "spawn_agent" || call.ToolUseID != "c1" || call.State != "running" {
		t.Fatalf("v2 开卡不对: %+v", ev.Payload)
	}
	if !strings.Contains(call.Args, "task_name") {
		t.Fatalf("v2 args 没解开 JSON 字符串: %s", call.Args)
	}
	ev = <-d.events
	call, ok = ev.Payload.(*ToolCallPayload)
	if !ok || call.ToolUseID != "c1" || call.State != "ok" || call.Result == "" {
		t.Fatalf("v2 收卡不对: %+v", ev.Payload)
	}
	select {
	case ev := <-d.events:
		t.Fatalf("被过滤的调用多发了事件: %+v", ev.Payload)
	default:
	}

	// ---- v1 collabagenttoolcall item ----
	notify("item/started",
		`{"item":{"type":"collabagenttoolcall","id":"i1","tool":"spawn_agent","prompt":"干活","agentType":"generalist","fork_context":true,"receiver_thread_ids":["a1"]}}`)
	notify("item/completed",
		`{"item":{"type":"collabagenttoolcall","id":"i1","status":"success","receiver_thread_ids":["a1"],"agents_states":{"a1":{"status":"completed","message":"干完了"}}}}`)
	// v2 已发过卡的 call_id(raw 路径标记 seen):v1 item 同 id 整条丢弃。
	notify("item/started",
		`{"item":{"type":"collabagenttoolcall","id":"c1","tool":"spawn_agent","prompt":"重复"}}`)

	ev = <-d.events
	call, ok = ev.Payload.(*ToolCallPayload)
	if !ok || call.Tool != "spawn_agent" || call.ToolUseID != "i1" || call.State != "running" {
		t.Fatalf("v1 开卡不对: %+v", ev.Payload)
	}
	if !strings.Contains(call.Args, `"message":"干活"`) || !strings.Contains(call.Args, `"agent_type":"generalist"`) {
		t.Fatalf("v1 args 归一不对: %s", call.Args)
	}
	ev = <-d.events
	call, ok = ev.Payload.(*ToolCallPayload)
	if !ok || call.ToolUseID != "i1" || call.State != "ok" {
		t.Fatalf("v1 收卡不对: %+v", ev.Payload)
	}
	if !strings.Contains(call.Result, `"agent_id":"a1"`) || !strings.Contains(call.Result, "干完了") {
		t.Fatalf("v1 结果归一不对: %s", call.Result)
	}
	select {
	case ev := <-d.events:
		t.Fatalf("同 call_id 的 v1 item 多发了事件: %+v", ev.Payload)
	default:
	}
}

// fakeStdin 给 Send 拦截测试用:记下写进来的行,Close 空实现。
type fakeStdin struct{ bytes.Buffer }

func (f *fakeStdin) Close() error { return nil }

// TestCodexCompactSend /compact 必须拦成 thread/compact/start RPC(发给
// app-server 会被当普通文本喂给模型);其余文本照走 turn/start。
func TestCodexCompactSend(t *testing.T) {
	d := newCodexDriver()
	defer func() { d.doneOnce.Do(func() { close(d.done) }) }()
	in := &fakeStdin{}
	d.stdin = in
	d.threadID = "th"

	if err := d.Send("/compact"); err != nil {
		t.Fatalf("Send /compact: %v", err)
	}
	if s := in.String(); !strings.Contains(s, `"method":"thread/compact/start"`) || !strings.Contains(s, `"threadId":"th"`) {
		t.Fatalf("没有发 thread/compact/start: %s", s)
	}
	// 状态先转 running(StatusBar 有反馈),带参数的 /compact foo 也拦。
	ev := <-d.events
	if sp, ok := ev.Payload.(*StatusPayload); !ok || sp.State != StatusRunning {
		t.Fatalf("compact 没先发 running: %+v", ev.Payload)
	}
	in.Reset()
	if err := d.Send("/compact 顺便整理"); err != nil {
		t.Fatalf("Send /compact 带参数: %v", err)
	}
	if !strings.Contains(in.String(), "thread/compact/start") {
		t.Fatalf("带参数的 /compact 没拦: %s", in.String())
	}

	// 普通文本(包括长得像的)照走 turn/start。
	in.Reset()
	for _, text := range []string{"你好", "/compactx", "compact"} {
		if err := d.Send(text); err != nil {
			t.Fatalf("Send %q: %v", text, err)
		}
	}
	if !strings.Contains(in.String(), "turn/start") || strings.Contains(in.String(), "thread/compact/start") {
		t.Fatalf("普通文本没走 turn/start: %s", in.String())
	}
}

// TestCodexTokenUsageUpdated 新通知方法 thread/tokenUsage/updated(0.144+
// 实测形状):params.tokenUsage.total 是 camelCase 计数,modelContextWindow
// 直接透传给 StatusBar。
func TestCodexTokenUsageUpdated(t *testing.T) {
	d := newCodexDriver()
	defer func() { d.doneOnce.Do(func() { close(d.done) }) }()

	d.handleNotification("thread/tokenUsage/updated", json.RawMessage(
		`{"threadId":"th","turnId":"t1","tokenUsage":{"total":{"totalTokens":15391,"inputTokens":14713,"cachedInputTokens":6656,"outputTokens":678},"modelContextWindow":353400}}`))

	ev := <-d.events
	u, ok := ev.Payload.(*UsagePayload)
	if !ok {
		t.Fatalf("payload 类型 %T,想要 *UsagePayload", ev.Payload)
	}
	if u.Context != 14713 || u.Output != 678 || u.CacheRead != 6656 {
		t.Fatalf("用量不对: %+v", u)
	}
	if u.Window != 353400 {
		t.Fatalf("窗口没透传: %+v", u)
	}

	// 老 snake_case 形态照旧(tokenCount)。
	d.handleNotification("tokenCount", json.RawMessage(
		`{"info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":5}}}`))
	ev = <-d.events
	if u, ok = ev.Payload.(*UsagePayload); !ok || u.Context != 100 || u.Output != 5 {
		t.Fatalf("老格式解析坏了: %+v", ev.Payload)
	}
}

// TestCodexDriverRoundTrip 真 codex app-server 集成测试:握手 → 发一轮 → 收到
// assistant 文本与 idle。机器上没有 codex 时跳过(不希望在 CI 里强依赖)。
// 用它当协议探针:app-server 改通知形状时,这里最先红。
func TestCodexDriverRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex 未安装")
	}

	d := newCodexDriver()
	defer d.Close()
	if err := d.Start(StartOpts{App: AppCodex, Cwd: "/tmp", PermissionMode: PermAccept}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if d.ExternalID() == "" {
		t.Fatal("握手后没有 thread id")
	}
	if err := d.Send("reply with exactly: pong"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var (
		gotText bool
		gotIdle bool
	)
	// 流式增量:pong 这种短回复通常也有至少一段 agentMessage/delta。
	var gotDelta bool
	deadline := time.After(90 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-d.Events():
			if !ok {
				break loop
			}
			switch ev.Kind {
			case KindAssistantDelta:
				if s, _ := ev.Payload.(string); s != "" {
					gotDelta = true
				}
			case KindAssistantText:
				if s, _ := ev.Payload.(string); s != "" {
					gotText = true
				}
			case KindStatus:
				if sp, ok := ev.Payload.(*StatusPayload); ok && sp.State == StatusIdle {
					gotIdle = true
					break loop
				}
			case KindError:
				if ep, ok := ev.Payload.(*ErrorPayload); ok {
					t.Errorf("error 事件: %s", ep.Message)
				}
			}
		case <-deadline:
			t.Fatal("90 秒内没跑完一轮")
		}
	}
	if !gotText {
		t.Error("没收到 assistant 文本")
	}
	if !gotDelta {
		t.Error("没收到流式增量(assistant_delta)")
	}
	if !gotIdle {
		t.Error("没收到 idle 状态")
	}
}
