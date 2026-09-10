package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// bufCloser 测试用 stdin:写进缓冲,Close 无操作。
type bufCloser struct{ bytes.Buffer }

func (b *bufCloser) Close() error { return nil }

// TestClaudeCompactionBoundary 喂合成 stream-json 的 compact_boundary /
// microcompact_boundary 行,验证归一成 KindCompaction 且 token 字段不丢。
func TestClaudeCompactionBoundary(t *testing.T) {
	d := newClaudeDriver()
	lines := []string{
		// 手动 /compact 的真实形状:status=compacting 先到,结果第二条(成功时
		// 只有边界事件说话;失败带 compact_error)。
		`{"type":"system","subtype":"status","status":"compacting"}`,
		`{"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"manual","preTokens":190000}}`,
		`{"type":"system","subtype":"microcompact_boundary","microcompactMetadata":{"trigger":"auto","preTokens":61000,"tokensSaved":18000}}`,
		`{"type":"system","subtype":"status","status":null,"compact_result":"failed","compact_error":"Not enough messages to compact."}`,
	}
	go d.readStdout(strings.NewReader(strings.Join(lines, "\n") + "\n"))

	expect := []*CompactionPayload{
		{Phase: "start"},
		{Trigger: "manual", PreTokens: 190000},
		{Micro: true, Trigger: "auto", PreTokens: 61000, TokensSaved: 18000},
		{Failed: true, Error: "Not enough messages to compact."},
	}
	for i, want := range expect {
		select {
		case ev := <-d.events:
			if ev.Kind != KindCompaction {
				t.Fatalf("第 %d 条:kind = %s,想要 %s", i, ev.Kind, KindCompaction)
			}
			got, ok := ev.Payload.(*CompactionPayload)
			if !ok {
				t.Fatalf("第 %d 条:payload 类型 %T", i, ev.Payload)
			}
			if *got != *want {
				t.Fatalf("第 %d 条:got %+v, want %+v", i, *got, *want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("第 %d 条:2 秒内没等到事件", i)
		}
	}
}

// TestClaudeAskUser AskUserQuestion 走 can_use_tool 协议:验证归一成 ask_user
// 事件、Answer 后回写的 control_response 带 updatedInput.answers(按问题文本
// 键、单选原样),以及取消路径回 deny。
func TestClaudeAskUser(t *testing.T) {
	d := newClaudeDriver()
	stdin := &bufCloser{}
	d.mu.Lock()
	d.stdin = stdin
	d.mu.Unlock()

	line := `{"type":"control_request","request_id":"ask1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[` +
		`{"header":"库","question":"用哪个库?","multiSelect":false,"options":[{"label":"vue","description":"渐进式","preview":"# vue 是渐进式框架"},{"label":"react"}]},` +
		`{"header":"主题","question":"还要什么主题?","multiSelect":true,"options":[{"label":"深色"},{"label":"浅色"}]}]}}}`
	go d.readStdout(strings.NewReader(line + "\n"))

	var ask *AskUserPayload
	select {
	case ev := <-d.events:
		if ev.Kind != KindAskUser {
			t.Fatalf("kind = %s,想要 %s", ev.Kind, KindAskUser)
		}
		ask = ev.Payload.(*AskUserPayload)
	case <-time.After(2 * time.Second):
		t.Fatal("2 秒内没等到 ask_user 事件")
	}
	if ask.ReqID != "ask1" || len(ask.Questions) != 2 {
		t.Fatalf("ask = %+v", ask)
	}
	if ask.Questions[0].ID != "0" || ask.Questions[0].Header != "库" ||
		len(ask.Questions[0].Options) != 2 || ask.Questions[0].Options[0].Description != "渐进式" {
		t.Fatalf("第 1 题归一不对: %+v", ask.Questions[0])
	}
	if ask.Questions[0].Options[0].Preview != "# vue 是渐进式框架" {
		t.Fatalf("preview 被剥离了: %+v", ask.Questions[0].Options[0])
	}
	if !ask.Questions[1].Multi || !ask.Questions[0].Required {
		t.Fatalf("multi/required 归一不对: %+v", ask.Questions)
	}

	// 回答:单选 vue,多选拼 深色+浅色。
	if err := d.Answer("ask1", map[string][]string{
		"0": {"vue"},
		"1": {"深色", "浅色"},
	}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	waitWritten(t, stdin)
	var resp struct {
		Type     string `json:"type"`
		Response struct {
			RequestID string `json:"request_id"`
			Response  struct {
				Behavior     string         `json:"behavior"`
				UpdatedInput map[string]any `json:"updatedInput"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(stdin.Bytes(), &resp); err != nil {
		t.Fatalf("回写不是合法 JSON: %v(%s)", err, stdin.String())
	}
	if resp.Type != "control_response" || resp.Response.RequestID != "ask1" ||
		resp.Response.Response.Behavior != "allow" {
		t.Fatalf("回写形状不对: %s", stdin.String())
	}
	answers, _ := resp.Response.Response.UpdatedInput["answers"].(map[string]any)
	if answers["用哪个库?"] != "vue" || answers["还要什么主题?"] != "深色,浅色" {
		t.Fatalf("answers 不对: %#v", answers)
	}
	// updatedInput 必须原样带上 questions 数组(claude 2.x 的要求)。
	if _, ok := resp.Response.Response.UpdatedInput["questions"]; !ok {
		t.Fatalf("updatedInput 丢了 questions: %s", stdin.String())
	}

	// 同一 reqID 再答:该报错(已答复)。
	if err := d.Answer("ask1", nil); err == nil {
		t.Fatal("重复回答没有报错")
	}
}

// TestClaudeAskUserCancel 取消提问:回 deny,不产生空 answers。
func TestClaudeAskUserCancel(t *testing.T) {
	d := newClaudeDriver()
	stdin := &bufCloser{}
	d.mu.Lock()
	d.stdin = stdin
	d.mu.Unlock()

	line := `{"type":"control_request","request_id":"ask2","request":{"subtype":"can_use_tool","tool_name":"ask_user_question","input":{"questions":[{"question":"继续吗?","options":[{"label":"是"},{"label":"否"}]}]}}}`
	go d.readStdout(strings.NewReader(line + "\n"))

	select {
	case ev := <-d.events:
		if ev.Kind != KindAskUser {
			t.Fatalf("kind = %s,想要 %s", ev.Kind, KindAskUser)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("2 秒内没等到 ask_user 事件")
	}
	if err := d.Answer("ask2", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	waitWritten(t, stdin)
	if !strings.Contains(stdin.String(), `"behavior":"deny"`) {
		t.Fatalf("取消应回 deny: %s", stdin.String())
	}
}

// TestClaudeTaskNotification 后台任务通知的两种形态:system/task_notification
// 子类型(新)与 user 消息字符串 content 里的 <task-notification> XML(旧)。
// 两者都归一成 system_info{type:task_notification};普通字符串回显不透出。
func TestClaudeTaskNotification(t *testing.T) {
	d := newClaudeDriver()
	lines := []string{
		`{"type":"system","subtype":"task_notification","summary":"Background task completed: sleep 2 (exit 0)","status":"completed"}`,
		`{"type":"user","message":{"role":"user","content":"<task-notification>\n<summary>Monitor 捕获 3 条事件</summary>\n<status>timeout</status>\n</task-notification>"}}`,
		`{"type":"user","message":{"role":"user","content":"普通回显,不该透出"}}`,
	}
	go d.readStdout(strings.NewReader(strings.Join(lines, "\n") + "\n"))

	expect := []*SystemInfoPayload{
		{Type: "task_notification", Text: "Background task completed: sleep 2 (exit 0)", Status: "completed"},
		{Type: "task_notification", Text: "Monitor 捕获 3 条事件", Status: "timeout"},
	}
	for i, want := range expect {
		select {
		case ev := <-d.events:
			if ev.Kind != KindSystemInfo {
				t.Fatalf("第 %d 条:kind = %s,想要 %s", i, ev.Kind, KindSystemInfo)
			}
			got, ok := ev.Payload.(*SystemInfoPayload)
			if !ok {
				t.Fatalf("第 %d 条:payload 类型 %T", i, ev.Payload)
			}
			if *got != *want {
				t.Fatalf("第 %d 条:got %+v, want %+v", i, *got, *want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("第 %d 条:2 秒内没等到事件", i)
		}
	}
	// 第三行(普通回显)不该再产事件:等一小段确认静默。
	select {
	case ev := <-d.events:
		t.Fatalf("普通回显不该透出: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestClaudeStreamEvent --include-partial-messages 的流式增量:text_delta /
// thinking_delta 归一成瞬态事件;子 agent 流(parent_tool_use_id 非空)与
// 工具参数增量(input_json_delta)不透出。
func TestClaudeStreamEvent(t *testing.T) {
	d := newClaudeDriver()
	lines := []string{
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"好"}}}`,
		`{"type":"stream_event","parent_tool_use_id":"toolu_01","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"子agent的字"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"先想想"}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
	}
	go d.readStdout(strings.NewReader(strings.Join(lines, "\n") + "\n"))

	type want struct {
		kind    string
		payload string
	}
	for _, w := range []want{
		{KindAssistantDelta, "你"},
		{KindAssistantDelta, "好"},
		{KindReasoningDelta, "先想想"},
	} {
		select {
		case ev := <-d.events:
			if ev.Kind != w.kind || ev.Payload != w.payload {
				t.Fatalf("got %s/%v,想要 %s/%v", ev.Kind, ev.Payload, w.kind, w.payload)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("2 秒内没等到 %s 事件", w.kind)
		}
	}
	select {
	case ev := <-d.events:
		t.Fatalf("不该再透出事件: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestClaudeTranscriptTailMonitor 转录 tailer:回合中入队的通知被
// absorbed_mid_turn 吸收,stdout 上永不出现,只能从转录的 queue-operation
// enqueue 条目补捞。验证:初始内容读取、增量追加、非通知条目过滤、
// 与 stdout 路径(maybeTaskNotificationXML)同内容去重不双发。
func TestClaudeTranscriptTailMonitor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	d := newClaudeDriver()
	d.cwd = "/root/proj"

	dir := filepath.Join(home, ".claude", "projects", projectSlug(d.cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sess1.jsonl")
	monitorXML := "<task-notification>\n<task-id>b8h</task-id>\n<summary>Monitor event: \"测试\"</summary>\n<event>EVENT-1\nEVENT-2</event>\n</task-notification>"
	line1, _ := json.Marshal(map[string]string{"type": "queue-operation", "operation": "enqueue", "content": monitorXML})
	os.WriteFile(path, append(line1, '\n'), 0o644)

	go d.tailTranscript("sess1")

	want := &SystemInfoPayload{
		Type:  "task_notification",
		Text:  `Monitor event: "测试"`,
		Event: "EVENT-1\nEVENT-2",
	}
	select {
	case ev := <-d.events:
		if ev.Kind != KindSystemInfo {
			t.Fatalf("kind = %s,想要 %s", ev.Kind, KindSystemInfo)
		}
		if got, ok := ev.Payload.(*SystemInfoPayload); !ok || *got != *want {
			t.Fatalf("got %+v, want %+v", ev.Payload, *want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("3 秒内没等到 tailer 的 Monitor 事件")
	}

	// stdout 路径又送来同一条(空闲时 dequeue 之后的 user 消息):去重,不双发。
	d.maybeTaskNotificationXML(monitorXML)
	select {
	case ev := <-d.events:
		t.Fatalf("同内容通知双发了: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}

	// 增量追加:新事件 + 非通知的 enqueue(排队用户消息),后者不该透出。
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	for _, content := range []string{
		"插话的排队消息",
		"<task-notification>\n<summary>Monitor event: \"二段\"</summary>\n<event>[Monitor timed out — re-arm if needed.]</event>\n</task-notification>",
	} {
		b, _ := json.Marshal(map[string]string{"type": "queue-operation", "operation": "enqueue", "content": content})
		f.Write(append(b, '\n'))
	}
	f.Close()
	want2 := &SystemInfoPayload{
		Type:  "task_notification",
		Text:  `Monitor event: "二段"`,
		Event: "[Monitor timed out — re-arm if needed.]",
	}
	select {
	case ev := <-d.events:
		if got, ok := ev.Payload.(*SystemInfoPayload); !ok || *got != *want2 {
			t.Fatalf("got %+v, want %+v", ev.Payload, *want2)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("3 秒内没等到追加的 Monitor 事件")
	}
	select {
	case ev := <-d.events:
		t.Fatalf("多透出了事件: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
	close(d.done)
}

// waitWritten 轮询等 handler goroutine 把答复写进 stdin 缓冲。
func waitWritten(t *testing.T, stdin *bufCloser) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if stdin.Len() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("2 秒内没等到 stdin 回写")
}

// TestClaudeSidechainFiltered parent_tool_use_id 非空的 assistant/user 行是
// 子 agent(Task 工具)自己的流:文本/思考/usage 不进主时间线;工具调用与
// 结果带 parentToolUseId 透出(前端挂到父 Task 卡下当"过程"),主 agent 的
// Task 卡(不带 parent id)照常透出。
func TestClaudeSidechainFiltered(t *testing.T) {
	d := newClaudeDriver()
	lines := []string{
		// 主 agent:发起 Task 工具(无 parent id)→ 正常发卡。
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t-main","name":"Task","input":{"prompt":"调研去"}}]}}`,
		// 子 agent 的 assistant 行:parent_tool_use_id 指向 Task 调用。
		`{"type":"assistant","parent_tool_use_id":"t-main","message":{"role":"assistant","model":"m","content":[{"type":"text","text":"子 agent 的碎碎念"},{"type":"thinking","thinking":"子 agent 的思考"}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
		// 子 agent 自己的工具调用与结果:同样带 parent id。
		`{"type":"assistant","parent_tool_use_id":"t-main","message":{"role":"assistant","content":[{"type":"tool_use","id":"t-sub","name":"Read","input":{"file_path":"/tmp/x"}}]}}`,
		`{"type":"user","parent_tool_use_id":"t-main","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t-sub","content":"子 agent 的读文件结果"}]}}`,
		// Task 收尾:tool_result 在父级(不带 parent id)→ 正常回填。
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t-main","content":"调研结论:可以"}]}}`,
	}
	go d.readStdout(strings.NewReader(strings.Join(lines, "\n") + "\n"))

	// 主时间线两张卡(Task 运行中 → 完成),子 agent 的工具过程挂 parent id
	// 穿插其间,文本/思考/usage 不露面。
	want := []struct {
		id     string
		tool   string
		state  string
		parent string
	}{
		{"t-main", "Task", "running", ""},
		{"t-sub", "Read", "running", "t-main"},
		{"t-sub", "", "ok", "t-main"},
		{"t-main", "Task", "ok", ""},
	}
	for i, w := range want {
		select {
		case ev := <-d.events:
			call, ok := ev.Payload.(*ToolCallPayload)
			if !ok || ev.Kind != KindToolCall {
				t.Fatalf("事件 %d: %+v(%T),想要工具卡", i, ev.Payload, ev.Payload)
			}
			if call.ToolUseID != w.id || call.Tool != w.tool || call.State != w.state {
				t.Fatalf("事件 %d: %+v,想要 {%s %s %s}", i, call, w.id, w.tool, w.state)
			}
			if call.ParentToolUseID != w.parent {
				t.Fatalf("事件 %d: parentToolUseId=%q,想要 %q", i, call.ParentToolUseID, w.parent)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("事件 %d:2 秒内没等到", i)
		}
	}
	select {
	case ev := <-d.events:
		t.Fatalf("子 agent 的文本/思考/usage 漏进了主时间线: %+v", ev.Payload)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestClaudeResumeError resume 失败的真实形状:result 行 is_error=true、
// result 文本为空、原因在 errors 数组里。此前只有 result 文本非空才报错,
// 失败被静默吞掉 —— 远端只看到会话「又离线了」,无从排查。
func TestClaudeResumeError(t *testing.T) {
	d := newClaudeDriver()
	line := `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"","errors":["No conversation found with session ID: deadbeef"]}`
	go d.readStdout(strings.NewReader(line + "\n"))

	select {
	case ev := <-d.events:
		if ev.Kind != KindError {
			t.Fatalf("kind = %s,想要 %s", ev.Kind, KindError)
		}
		p, ok := ev.Payload.(*ErrorPayload)
		if !ok {
			t.Fatalf("payload 类型 %T", ev.Payload)
		}
		if !strings.Contains(p.Message, "No conversation found") {
			t.Fatalf("message = %q,应含 resume 失败原因", p.Message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("resume 失败没透成 error 事件")
	}

	// 错误已从 result 行透出:ExitDetail 不能再拿 stderr 尾部重复报。
	d.mu.Lock()
	d.exitErr = errors.New("exit status 1")
	d.stderrTail = "some stderr noise"
	d.mu.Unlock()
	if got := d.ExitDetail(); got != "" {
		t.Fatalf("sawResult 后 ExitDetail 应为空,得到 %q", got)
	}
}

// TestClaudeExitDetail 崩溃退出(没有任何 result 行)时,stderr 尾部要能
// 透成可读原因;正常退出(ExitErr 为 nil)则不透。
func TestClaudeExitDetail(t *testing.T) {
	d := newClaudeDriver()
	if got := d.ExitDetail(); got != "" {
		t.Fatalf("未退出时 ExitDetail 应为空,得到 %q", got)
	}
	d.mu.Lock()
	d.exitErr = errors.New("exit status 1")
	d.stderrTail = "fatal: config parse failed"
	d.mu.Unlock()
	got := d.ExitDetail()
	if !strings.Contains(got, "config parse failed") {
		t.Fatalf("ExitDetail = %q,应含 stderr 尾部", got)
	}
}
