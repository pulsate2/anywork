package agent

import (
	"strings"
	"testing"
	"time"
)

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
