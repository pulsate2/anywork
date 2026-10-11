package agent

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// piCapture 一个线程安全的 io.WriteCloser,收 sendExtResponse 写出的行。
type piCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *piCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}
func (c *piCapture) Close() error { return nil }
func (c *piCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// raw 把 map 序列化成一行事件字节(测试里喂给 handleEvent)。
func piRaw(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// nextEvent 取一条事件,超时即失败。
func nextEvent(t *testing.T, d *piDriver) Event {
	t.Helper()
	select {
	case ev := <-d.events:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("事件没到")
		return Event{}
	}
}

// TestPiStreamingAndFlush 流式增量即时广播,message_end 把攒着的分段按
// contentIndex 升序拼成完整 reasoning / assistant_text(reasoning 在前)。
func TestPiStreamingAndFlush(t *testing.T) {
	d := newPiDriver()
	defer func() { d.doneOnce.Do(func() { close(d.done) }) }()

	update := func(typ, delta string, idx int) {
		d.handleEvent("message_update", piRaw(t, map[string]any{
			"type": "message_update",
			"assistantMessageEvent": map[string]any{
				"type": typ, "delta": delta, "contentIndex": idx,
			},
		}))
	}

	d.handleEvent("message_start", piRaw(t, map[string]any{"type": "message_start"}))
	update("text_delta", "你", 0)
	update("text_delta", "好", 0)
	update("thinking_delta", "想一想", 1)
	d.handleEvent("message_end", piRaw(t, map[string]any{"type": "message_end"}))

	expect := []struct {
		kind string
		text string
	}{
		{KindAssistantDelta, "你"},
		{KindAssistantDelta, "好"},
		{KindReasoningDelta, "想一想"},
		{KindReasoning, "想一想"},
		{KindAssistantText, "你好"},
	}
	for i, w := range expect {
		ev := nextEvent(t, d)
		if ev.Kind != w.kind {
			t.Fatalf("事件 %d kind=%q 想要 %q", i, ev.Kind, w.kind)
		}
		if s, _ := ev.Payload.(string); s != w.text {
			t.Fatalf("事件 %d payload=%q 想要 %q", i, s, w.text)
		}
	}
	// flush 幂等:再来一次 message_end 不该补发空块。
	d.handleEvent("message_end", piRaw(t, map[string]any{"type": "message_end"}))
	select {
	case ev := <-d.events:
		t.Fatalf("空 flush 多发了事件: %+v", ev)
	default:
	}
}

// TestPiToolCall tool_execution_start 出一张 running 卡,tool_execution_end 按
// 同一 toolCallId 回结果卡(前端据此合并)。
func TestPiToolCall(t *testing.T) {
	d := newPiDriver()
	defer func() { d.doneOnce.Do(func() { close(d.done) }) }()

	d.handleEvent("tool_execution_start", piRaw(t, map[string]any{
		"type":       "tool_execution_start",
		"toolCallId": "call-1",
		"toolName":   "read_file",
		"args":       map[string]any{"path": "a.go"},
	}))
	ev := nextEvent(t, d)
	p, ok := ev.Payload.(*ToolCallPayload)
	if ev.Kind != KindToolCall || !ok {
		t.Fatalf("想要 KindToolCall,拿到 kind=%q payload=%T", ev.Kind, ev.Payload)
	}
	if p.Tool != "read_file" || p.ToolUseID != "call-1" || p.State != "running" {
		t.Fatalf("start 卡字段不对: %+v", p)
	}
	if !strings.Contains(p.Args, "a.go") {
		t.Fatalf("start 卡 args 丢了: %q", p.Args)
	}

	d.handleEvent("tool_execution_end", piRaw(t, map[string]any{
		"type":       "tool_execution_end",
		"toolCallId": "call-1",
		"result":     "file contents",
		"isError":    false,
	}))
	ev = nextEvent(t, d)
	p, ok = ev.Payload.(*ToolCallPayload)
	if ev.Kind != KindToolCall || !ok {
		t.Fatalf("想要 KindToolCall(结果),拿到 kind=%q payload=%T", ev.Kind, ev.Payload)
	}
	if p.ToolUseID != "call-1" || p.State != "ok" || p.Result != "file contents" {
		t.Fatalf("end 卡字段不对: %+v", p)
	}

	// isError=true → State=error。
	d.handleEvent("tool_execution_end", piRaw(t, map[string]any{
		"type":       "tool_execution_end",
		"toolCallId": "call-2",
		"result":     "boom",
		"isError":    true,
	}))
	ev = nextEvent(t, d)
	if p, _ := ev.Payload.(*ToolCallPayload); p == nil || p.State != "error" {
		t.Fatalf("isError 应为 error 态: %+v", ev.Payload)
	}
}

// TestPiResultText pi 的工具结果 {content:[{type,text}],details:{…}} 要抽成纯文本,
// 不能把整串 JSON 原样丢给前端;字符串直取,认不出的形态才回退整串。
func TestPiResultText(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{"字符串", `"plain text"`, "plain text"},
		{"content 文本块", `{"content":[{"type":"text","text":"Successfully wrote"}],"details":{"diff":"x"}}`, "Successfully wrote"},
		{"多段文本块按行拼", `{"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}`, "a\nb"},
		{"裸块数组", `[{"type":"text","text":"arr"}]`, "arr"},
		{"只有 image 块回退整串", `{"content":[{"type":"image","source":{}}]}`, `{"content":[{"type":"image","source":{}}]}`},
		{"非结构对象回退整串", `{"foo":1}`, `{"foo":1}`},
		{"空", ``, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := piResultText(json.RawMessage(c.raw)); got != c.want {
				t.Fatalf("piResultText(%s) = %q,想要 %q", c.raw, got, c.want)
			}
		})
	}
}

// TestPiStatusLifecycle agent_start 翻 running、agent_settled 翻 idle;一回合只
// 翻一次(agent_end 与 agent_settled 后到者空转)。
func TestPiStatusLifecycle(t *testing.T) {
	d := newPiDriver()
	defer func() { d.doneOnce.Do(func() { close(d.done) }) }()

	d.handleEvent("agent_start", piRaw(t, map[string]any{"type": "agent_start"}))
	ev := nextEvent(t, d)
	if p, _ := ev.Payload.(*StatusPayload); ev.Kind != KindStatus || p == nil || p.State != StatusRunning {
		t.Fatalf("想要 running,拿到 kind=%q payload=%+v", ev.Kind, ev.Payload)
	}
	// 重复 agent_start 不再翻。
	d.handleEvent("agent_start", piRaw(t, map[string]any{"type": "agent_start"}))
	select {
	case ev := <-d.events:
		t.Fatalf("重复 agent_start 多发了: %+v", ev)
	default:
	}

	// agent_end(!willRetry) 收尾:先 turn_duration 再 idle。
	d.handleEvent("agent_end", piRaw(t, map[string]any{"type": "agent_end", "willRetry": false}))
	ev = nextEvent(t, d)
	if p, _ := ev.Payload.(*SystemInfoPayload); ev.Kind != KindSystemInfo || p == nil || p.Type != "turn_duration" {
		t.Fatalf("想要 turn_duration,拿到 kind=%q payload=%+v", ev.Kind, ev.Payload)
	}
	ev = nextEvent(t, d)
	if p, _ := ev.Payload.(*StatusPayload); ev.Kind != KindStatus || p == nil || p.State != StatusIdle {
		t.Fatalf("想要 idle,拿到 kind=%q payload=%+v", ev.Kind, ev.Payload)
	}
	// 随后的 agent_settled 空转。
	d.handleEvent("agent_settled", piRaw(t, map[string]any{"type": "agent_settled"}))
	select {
	case ev := <-d.events:
		t.Fatalf("已 idle 后 agent_settled 多发了: %+v", ev)
	default:
	}
}

// TestPiTurnErrorDedup message_end 与 turn_end 带同一条回合错误,只发一次 KindError;
// aborted(用户打断)不算错。
func TestPiTurnErrorDedup(t *testing.T) {
	d := newPiDriver()
	defer func() { d.doneOnce.Do(func() { close(d.done) }) }()

	errMsg := map[string]any{
		"message": map[string]any{"stopReason": "error", "errorMessage": "模型超时"},
	}
	d.handleEvent("message_end", piRaw(t, mergeType("message_end", errMsg)))
	ev := nextEvent(t, d)
	if p, _ := ev.Payload.(*ErrorPayload); ev.Kind != KindError || p == nil || p.Message != "模型超时" {
		t.Fatalf("想要 KindError(模型超时),拿到 kind=%q payload=%+v", ev.Kind, ev.Payload)
	}
	// turn_end 带同一条:去重,不再发(turn_end 无 usage,不触发异步 usage)。
	d.handleEvent("turn_end", piRaw(t, mergeType("turn_end", errMsg)))
	select {
	case ev := <-d.events:
		t.Fatalf("同一回合错误重复上报: %+v", ev)
	default:
	}

	// aborted 不是错。
	d2 := newPiDriver()
	defer func() { d2.doneOnce.Do(func() { close(d2.done) }) }()
	d2.handleEvent("message_end", piRaw(t, mergeType("message_end", map[string]any{
		"message": map[string]any{"stopReason": "aborted"},
	})))
	select {
	case ev := <-d2.events:
		t.Fatalf("aborted 不该当错误: %+v", ev)
	default:
	}
}

// TestPiTodoSnapshot pi 的 todo 工具结果 details.tasks 是全量快照,handleToolEnd
// 要翻成前端 TodoWrite 同款 {todos:[{id,content,status}]} 塞进 Args(跳过 deleted),
// 顶部任务面板据此整表重建。
func TestPiTodoSnapshot(t *testing.T) {
	d := newPiDriver()
	defer func() { d.doneOnce.Do(func() { close(d.done) }) }()

	result := map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "Updated #1"}},
		"details": map[string]any{
			"action": "update",
			"tasks": []any{
				map[string]any{"id": 1, "subject": "写测试", "status": "completed"},
				map[string]any{"id": 2, "subject": "跑构建", "status": "in_progress"},
				map[string]any{"id": 3, "subject": "删掉的", "status": "deleted"},
			},
			"nextId": 4,
		},
	}
	d.handleEvent("tool_execution_end", piRaw(t, map[string]any{
		"type":       "tool_execution_end",
		"toolCallId": "call-todo",
		"toolName":   "todo",
		"result":     result,
		"isError":    false,
	}))
	ev := nextEvent(t, d)
	p, ok := ev.Payload.(*ToolCallPayload)
	if ev.Kind != KindToolCall || !ok {
		t.Fatalf("想要 KindToolCall,拿到 kind=%q payload=%T", ev.Kind, ev.Payload)
	}
	if p.Tool != "todo" {
		t.Fatalf("todo 结果该带 Tool=todo,拿到 %q", p.Tool)
	}
	if p.Result != "Updated #1" {
		t.Fatalf("todo 结果文本不对: %q", p.Result)
	}
	// Args 应是 {todos:[…]},deleted 的被跳过,保留 id/content/status。
	var got struct {
		Todos []struct {
			ID      int    `json:"id"`
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
	}
	if err := json.Unmarshal([]byte(p.Args), &got); err != nil {
		t.Fatalf("Args 不是合法 JSON: %v (%q)", err, p.Args)
	}
	if len(got.Todos) != 2 {
		t.Fatalf("想要 2 条(跳过 deleted),拿到 %d: %q", len(got.Todos), p.Args)
	}
	if got.Todos[0].ID != 1 || got.Todos[0].Content != "写测试" || got.Todos[0].Status != "completed" {
		t.Fatalf("第 1 条翻译不对: %+v", got.Todos[0])
	}
	if got.Todos[1].ID != 2 || got.Todos[1].Content != "跑构建" || got.Todos[1].Status != "in_progress" {
		t.Fatalf("第 2 条翻译不对: %+v", got.Todos[1])
	}
}

// TestPiApproveAutoAllow accept(全放行)模式:sentinel-select 不出审批卡,直接
// 代答 value=allow-once。
func TestPiApproveAutoAllow(t *testing.T) {
	d := newPiDriver()
	defer func() { d.doneOnce.Do(func() { close(d.done) }) }()
	cap := &piCapture{}
	d.stdin = cap
	d.autoAllow = true

	d.handleEvent("extension_ui_request", piRaw(t, map[string]any{
		"type":    "extension_ui_request",
		"id":      "ui-2",
		"method":  "select",
		"title":   `{"__anyworkApprove":1,"tool":"bash","input":{"command":"ls"}}`,
		"options": []any{"deny", "allow-once", "allow-session"},
	}))
	// 不出卡。
	select {
	case ev := <-d.events:
		t.Fatalf("accept 模式不该出审批卡: %+v", ev)
	default:
	}
	if !strings.Contains(cap.String(), `"value":"allow-once"`) {
		t.Fatalf("accept 模式该代答 allow-once: %q", cap.String())
	}
	if len(d.PendingRequests()) != 0 {
		t.Fatalf("accept 模式不该挂起: %+v", d.PendingRequests())
	}
}

// mergeType 把 type 字段并进事件 body。
func mergeType(typ string, m map[string]any) map[string]any {
	out := map[string]any{"type": typ}
	for k, v := range m {
		out[k] = v
	}
	return out
}

// TestPiApprovalGate gate 发来的 sentinel-select(extension_ui_request)被认成审批:
// 出一张 KindPermissionReq(带工具名与入参)、进 PendingRequests;Resolve 把三态
// 映射成 extension_ui_response.value 写回 gate,并清掉挂起;重复 Resolve 报错。
func TestPiApprovalGate(t *testing.T) {
	d := newPiDriver()
	defer func() { d.doneOnce.Do(func() { close(d.done) }) }()
	cap := &piCapture{}
	d.stdin = cap

	title := `{"__anyworkApprove":1,"tool":"bash","input":{"command":"ls -la"}}`
	d.handleEvent("extension_ui_request", piRaw(t, map[string]any{
		"type":    "extension_ui_request",
		"id":      "ui-1",
		"method":  "select",
		"title":   title,
		"options": []any{"deny", "allow-once", "allow-session"},
	}))

	ev := nextEvent(t, d)
	p, ok := ev.Payload.(*PermissionReqPayload)
	if ev.Kind != KindPermissionReq || !ok {
		t.Fatalf("想要 KindPermissionReq,拿到 kind=%q payload=%T", ev.Kind, ev.Payload)
	}
	if p.ReqID != "ui-1" || p.Tool != "bash" {
		t.Fatalf("审批卡字段不对: %+v", p)
	}
	if !strings.Contains(p.Args, "ls -la") {
		t.Fatalf("审批卡 args 丢了: %q", p.Args)
	}

	// 进 PendingRequests(断线重连补发)。
	pend := d.PendingRequests()
	if len(pend) != 1 || pend[0].ReqID != "ui-1" || pend[0].Tool != "bash" {
		t.Fatalf("PendingRequests 不对: %+v", pend)
	}

	// 允许+本会话 → value=allow-session,写回一条 extension_ui_response。
	if err := d.Resolve("ui-1", true, true); err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	var resp struct {
		Type  string `json:"type"`
		ID    string `json:"id"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(cap.String())), &resp); err != nil {
		t.Fatalf("回写不是合法 JSON: %v (%q)", err, cap.String())
	}
	if resp.Type != "extension_ui_response" || resp.ID != "ui-1" || resp.Value != "allow-session" {
		t.Fatalf("回写字段不对: %+v", resp)
	}

	// 挂起已清;重复 Resolve 报错。
	if len(d.PendingRequests()) != 0 {
		t.Fatalf("Resolve 后仍有挂起: %+v", d.PendingRequests())
	}
	if err := d.Resolve("ui-1", false, false); err == nil {
		t.Fatal("重复 Resolve 应报错")
	}
}

// TestPiApproveDenyValue 拒绝 → value=deny(gate 据此 block)。
func TestPiApproveDenyValue(t *testing.T) {
	d := newPiDriver()
	defer func() { d.doneOnce.Do(func() { close(d.done) }) }()
	cap := &piCapture{}
	d.stdin = cap
	d.approvals["ui-9"] = piApproval{tool: "write", args: "{}"}

	if err := d.Resolve("ui-9", false, false); err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if !strings.Contains(cap.String(), `"value":"deny"`) {
		t.Fatalf("拒绝该回 value=deny: %q", cap.String())
	}
}

// TestParsePiApprove 认 sentinel 标题,普通 select 标题不误伤。
func TestParsePiApprove(t *testing.T) {
	tool, args, ok := parsePiApprove(`{"__anyworkApprove":1,"tool":"write","input":{"path":"a.go"}}`)
	if !ok || tool != "write" || !strings.Contains(args, "a.go") {
		t.Fatalf("该认出 write: tool=%q args=%q ok=%v", tool, args, ok)
	}
	if _, _, ok := parsePiApprove("请选择一个分支"); ok {
		t.Fatal("普通标题不该被当审批")
	}
	if _, _, ok := parsePiApprove(`{"foo":1}`); ok {
		t.Fatal("无 sentinel 的 JSON 不该被当审批")
	}
}

// TestPiEditDiff 编辑类工具结果带 details.patch(标准 unified diff)时,handleToolEnd
// 把它挂到 Diff 上;patch 空则回落 details.diff;write 等无 details 的不挂。
func TestPiEditDiff(t *testing.T) {
	d := newPiDriver()
	defer func() { d.doneOnce.Do(func() { close(d.done) }) }()

	result := map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "Successfully replaced 1 block(s)."}},
		"details": map[string]any{
			"diff":  "注释型 diff",
			"patch": "--- a.md\n+++ a.md\n@@ -1 +1 @@\n-old\n+new\n",
		},
	}
	d.handleEvent("tool_execution_end", piRaw(t, map[string]any{
		"type":       "tool_execution_end",
		"toolCallId": "call-edit",
		"toolName":   "edit",
		"result":     result,
		"isError":    false,
	}))
	ev := nextEvent(t, d)
	p, _ := ev.Payload.(*ToolCallPayload)
	if p == nil || p.State != "ok" {
		t.Fatalf("edit 结果卡不对: %+v", ev.Payload)
	}
	if !strings.Contains(p.Diff, "+new") || !strings.Contains(p.Diff, "@@") {
		t.Fatalf("edit 该挂标准 patch: %q", p.Diff)
	}
	if p.Result != "Successfully replaced 1 block(s)." {
		t.Fatalf("edit 结果文本不对: %q", p.Result)
	}

	// patch 空 → 回落 details.diff。
	if got := piEditDiff(piRaw(t, map[string]any{
		"details": map[string]any{"patch": "", "diff": "回落内容"},
	})); got != "回落内容" {
		t.Fatalf("patch 空该回落 diff,拿到 %q", got)
	}

	// write 无 details → 不挂 Diff。
	d.handleEvent("tool_execution_end", piRaw(t, map[string]any{
		"type":       "tool_execution_end",
		"toolCallId": "call-write",
		"toolName":   "write",
		"result":     "Successfully wrote to a.md",
		"isError":    false,
	}))
	ev = nextEvent(t, d)
	if p, _ := ev.Payload.(*ToolCallPayload); p == nil || p.Diff != "" {
		t.Fatalf("write 不该挂 Diff: %+v", ev.Payload)
	}
}

// TestPiEditBatchBackfill pi 把连续编辑折成 batch:中间成员(last:false)patch 为空、
// 结果文本是 "In batch N (queued)";末尾成员(last:true)带覆盖整批的合并 patch。
// 中间成员先出空 Diff 卡,末尾 patch 到手后回填给它 —— 否则排在批中间的
// replace_match 这类编辑卡永远看不到改动(用户实测)。
func TestPiEditBatchBackfill(t *testing.T) {
	d := newPiDriver()
	defer func() { d.doneOnce.Do(func() { close(d.done) }) }()

	combined := "--- a.md\n+++ a.md\n@@ -1 +1 @@\n-old\n+new\n"

	// 批中间:replace_match,patch 空,batch.last=false。
	d.handleEvent("tool_execution_end", piRaw(t, map[string]any{
		"type": "tool_execution_end", "toolCallId": "c-rm", "toolName": "replace_match",
		"isError": false,
		"result": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "In batch 1 (queued)"}},
			"details": map[string]any{"patch": "", "diff": "",
				"batch": map[string]any{"id": 1, "size": 2, "last": false}},
		},
	}))
	ev := nextEvent(t, d)
	if p, _ := ev.Payload.(*ToolCallPayload); p == nil || p.ToolUseID != "c-rm" || p.Diff != "" {
		t.Fatalf("批中间成员该先出空 Diff 卡: %+v", ev.Payload)
	}
	if len(d.piBatch) != 1 {
		t.Fatalf("批缓冲该攒下中间成员,实有 %d", len(d.piBatch))
	}

	// 批末尾:insert,带合并 patch,batch.last=true。
	d.handleEvent("tool_execution_end", piRaw(t, map[string]any{
		"type": "tool_execution_end", "toolCallId": "c-ins", "toolName": "insert",
		"isError": false,
		"result": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "ok"}},
			"details": map[string]any{"patch": combined,
				"batch": map[string]any{"id": 1, "size": 2, "last": true}},
		},
	}))
	// 末尾卡自身带合并 patch。
	if p, _ := nextEvent(t, d).Payload.(*ToolCallPayload); p == nil || p.ToolUseID != "c-ins" || !strings.Contains(p.Diff, "+new") {
		t.Fatalf("批末尾卡该带合并 patch")
	}
	// 回填:先前的 replace_match 卡补上同一份合并 patch。
	if p, _ := nextEvent(t, d).Payload.(*ToolCallPayload); p == nil || p.ToolUseID != "c-rm" || !strings.Contains(p.Diff, "+new") {
		t.Fatalf("批中间成员该被回填合并 patch")
	}
	// 回填后缓冲清空,不串到下一批。
	if len(d.piBatch) != 0 {
		t.Fatalf("批缓冲该清空,还剩 %d", len(d.piBatch))
	}
}
