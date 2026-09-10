package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withFakeHome 把 HOME 指到临时目录,返回假的 ~/.claude / ~/.codex 根。
func withFakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// TestListClaudeSessions 假转录文件:标题提炼(summary 优先/用户消息兜底)、
// 非 jsonl 条目忽略、按修改时间倒序。
func TestListClaudeSessions(t *testing.T) {
	home := withFakeHome(t)
	cwd := "/w/proj"
	dir := filepath.Join(home, ".claude", "projects", projectSlug(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, lines []string, age time.Duration) {
		var b []byte
		for _, l := range lines {
			b = append(b, []byte(l+"\n")...)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
		past := time.Now().Add(-age)
		os.Chtimes(filepath.Join(dir, name), past, past)
	}

	// 普通会话:开头是 queue-operation,第一条用户消息当标题。
	write("aaaaaaaa-0000.jsonl", []string{
		`{"type":"queue-operation","operation":"enqueue","sessionId":"aaaaaaaa-0000","content":"hi"}`,
		`{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"修复登录超时的 bug"},"uuid":"u1","timestamp":"2026-09-01T00:00:00Z"}`,
	}, 2*time.Hour)
	// 压缩/续聊过的会话:summary 行优先。
	write("bbbbbbbb-1111.jsonl", []string{
		`{"type":"summary","summary":"重构数据库迁移流程","leafUuid":"u9"}`,
		`{"parentUuid":null,"isSidechain":true,"type":"user","message":{"role":"user","content":"子 agent 的消息不算"},"uuid":"u2"}`,
	}, 1*time.Hour) // 更新,应排第一
	// sidechain 用户消息不配当标题:落回继续找,没有真实用户消息 → 空标题。
	write("cccccccc-2222.jsonl", []string{
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"meta 注入不算"},"uuid":"u3"}`,
	}, 3*time.Hour)
	// 无关文件:忽略。
	if err := os.MkdirAll(filepath.Join(dir, "dddddddd-3333"), 0o755); err != nil {
		t.Fatal(err)
	}

	list, err := listClaudeSessions(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("应列出 3 条,得到 %d: %+v", len(list), list)
	}
	if list[0].ID != "bbbbbbbb-1111" || list[0].Title != "重构数据库迁移流程" {
		t.Fatalf("最新的一条应是 summary 会话: %+v", list[0])
	}
	if list[1].ID != "aaaaaaaa-0000" || list[1].Title != "修复登录超时的 bug" {
		t.Fatalf("第二条应是用户消息标题: %+v", list[1])
	}
	if list[2].ID != "cccccccc-2222" || list[2].Title != "" {
		t.Fatalf("只有 meta 用户的会话标题应为空: %+v", list[2])
	}

	// 目录不存在 = 空列表,不是错误(新工作区还没跑过会话)。
	empty, err := listClaudeSessions("/w/never")
	if err != nil || len(empty) != 0 {
		t.Fatalf("没跑过的目录应返回空列表: %v, %v", empty, err)
	}
}

// TestListCodexSessions rollout 首行 session_meta 按 cwd 过滤,标题取
// 第一条 user_message。
func TestListCodexSessions(t *testing.T) {
	home := withFakeHome(t)
	cwd := "/w/proj"
	day := filepath.Join(home, ".codex", "sessions", "2026", "09", "10")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	mk := func(name, dir, msg string) {
		lines := []string{
			`{"type":"session_meta","payload":{"session_id":"` + name + `","cwd":"` + dir + `"}}`,
		}
		if msg != "" {
			lines = append(lines, `{"type":"event_msg","payload":{"type":"user_message","message":"`+msg+`"}}`)
		}
		var b []byte
		for _, l := range lines {
			b = append(b, []byte(l+"\n")...)
		}
		if err := os.WriteFile(filepath.Join(day, "rollout-"+name+".jsonl"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("id-in-proj", cwd, "帮我看看为什么构建变慢了")
	mk("id-other-dir", "/other", "别的目录的")
	mk("id-empty", cwd, "") // 没有用户消息:列出但无标题

	list, err := listCodexSessions(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("只该列出本目录的 2 条,得到 %d: %+v", len(list), list)
	}
	found := map[string]string{}
	for _, s := range list {
		found[s.ID] = s.Title
	}
	if found["id-in-proj"] != "帮我看看为什么构建变慢了" {
		t.Fatalf("标题没取到 user_message: %+v", found)
	}
	if found["id-empty"] != "" {
		t.Fatalf("无用户消息的会话标题应为空: %+v", found)
	}

	// sessions 目录不存在 = 空列表。
	empty, err := listCodexSessions("/w/never")
	if err != nil || len(empty) != 0 {
		t.Fatalf("没有 codex 历史应返回空列表: %v, %v", empty, err)
	}
}
