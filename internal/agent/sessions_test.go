package agent

import (
	"fmt"
	"testing"
)

// createSessionAt 造一条指定工作区/更新时间的会话 —— 列表分页按 updated_at
// 倒序,时间戳得各不相同才能断言顺序。
func createSessionAt(t *testing.T, s *Store, id, ws, updatedAt string) {
	t.Helper()
	sess := &Session{
		ID: id, App: AppClaude, Workspace: ws, PermissionMode: PermAsk,
		Status: StatusIdle, CreatedAt: updatedAt, UpdatedAt: updatedAt,
	}
	if err := s.CreateSession(sess); err != nil {
		t.Fatalf("建会话 %s: %v", id, err)
	}
}

// TestListSessionsPaged 会话列表分页:每个工作区先回最近一页(updated_at
// 倒序),更早的按 workspace+offset 单独翻 —— 打开列表不再把历史全拉一遍。
func TestListSessionsPaged(t *testing.T) {
	s := newTestStore(t)
	// /a 只有 3 条(不满一页);/b 有 12 条(要翻第二页)。时间戳越大越新。
	for i := 1; i <= 3; i++ {
		createSessionAt(t, s, fmt.Sprintf("a%d", i), "/a", fmt.Sprintf("2026-09-01T00:00:%02dZ", i))
	}
	for i := 1; i <= 12; i++ {
		createSessionAt(t, s, fmt.Sprintf("b%02d", i), "/b", fmt.Sprintf("2026-09-02T00:00:%02dZ", i))
	}

	// 首屏:每个目录各一页(limit=10)。/a 全给,/b 给最近 10 条。
	first, err := s.ListRecentByWorkspace(10)
	if err != nil {
		t.Fatalf("首屏: %v", err)
	}
	if len(first) != 13 {
		t.Fatalf("首屏该是 3+10 条,得到 %d: %s", len(first), idsOf(first))
	}
	byWs := map[string][]Session{}
	for _, sess := range first {
		byWs[sess.Workspace] = append(byWs[sess.Workspace], sess)
	}
	if got := idsOf(byWs["/a"]); got != "a3,a2,a1" {
		t.Fatalf("/a 该按最近活动倒序给全 3 条: %s", got)
	}
	if got := idsOf(byWs["/b"]); got != "b12,b11,b10,b09,b08,b07,b06,b05,b04,b03" {
		t.Fatalf("/b 该给最近 10 条: %s", got)
	}

	// 翻页:某目录更早的一页(前端分组的"加载更多")。
	older, err := s.ListSessionsPage("/b", 10, 10)
	if err != nil {
		t.Fatalf("翻页: %v", err)
	}
	if got := idsOf(older); got != "b02,b01" {
		t.Fatalf("/b 第二页该剩两条: %s", got)
	}
	// 翻过尾:空数组(不是 nil,前端按长度判到底)。
	nothing, err := s.ListSessionsPage("/b", 10, 20)
	if err != nil || len(nothing) != 0 {
		t.Fatalf("翻过尾该给空数组: %v %+v", err, nothing)
	}
	// 别的目录互不干扰。
	only, err := s.ListSessionsPage("/a", 10, 0)
	if err != nil || len(only) != 3 {
		t.Fatalf("/a 单独翻页该是 3 条: %v %+v", err, only)
	}
}

func idsOf(list []Session) string {
	out := ""
	for i, s := range list {
		if i > 0 {
			out += ","
		}
		out += s.ID
	}
	return out
}
