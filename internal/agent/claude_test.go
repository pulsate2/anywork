package agent

import (
	"bytes"
	"encoding/json"
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
