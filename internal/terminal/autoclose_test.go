package terminal

import (
	"encoding/json"
	"testing"
	"time"
)

// TestCreateMsgAutoClose create 帧把档位放在顶层 autoCloseMin(分钟),别写错名字。
func TestCreateMsgAutoClose(t *testing.T) {
	var in inMsg
	raw := `{"type":"create","dir":"/tmp","cols":80,"rows":24,"autoCloseMin":30}`
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatalf("解析 create 帧: %v", err)
	}
	if in.AutoCloseMin != 30 {
		t.Errorf("autoCloseMin = %d,期望 30", in.AutoCloseMin)
	}
	// autoclose 帧与 create 帧共用一套字段,顺手钉住形状。
	in = inMsg{}
	raw = `{"type":"autoclose","sid":"s-1","autoCloseMin":0}`
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatalf("解析 autoclose 帧: %v", err)
	}
	if in.Type != "autoclose" || in.SID != "s-1" || in.AutoCloseMin != 0 {
		t.Errorf("autoclose 帧没解出来: %+v", in)
	}
}

// TestSummaryAutoCloseOmitted 没设定时关闭的会话,帧里不该冒出 autoCloseAt 噪音
// (前端据此决定要不要显示倒计时标签)。
func TestSummaryAutoCloseOmitted(t *testing.T) {
	b, _ := json.Marshal(sessMsg{Type: "session", Summary: Summary{ID: "x"}})
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	if _, ok := got["autoCloseAt"]; ok {
		t.Errorf("零值定时关闭不该出现在帧里: %s", b)
	}
	// 设了就要在,且是能被前端 new Date() 吃下去的格式。
	b, _ = json.Marshal(sessMsg{Type: "session", Summary: Summary{ID: "x", AutoCloseAt: "2026-09-15T12:00:00Z"}})
	got = nil
	_ = json.Unmarshal(b, &got)
	if got["autoCloseAt"] != "2026-09-15T12:00:00Z" {
		t.Errorf("autoCloseAt 没序列化成期望值: %s", b)
	}
}

// TestSetAutoCloseSummary 设档位 → Summary 带截止时刻;取消 → 清掉。
// 前端列表的倒计时标签完全建立在 autoCloseAt 上,这条性质断了标签就成了常驻谎话。
func TestSetAutoCloseSummary(t *testing.T) {
	m := NewManager(t.TempDir(), false)
	sum, err := m.Create("", "/bin/sh", 80, 24, Limits{}, 0)
	if err != nil {
		t.Skipf("本机起不了 shell:%v", err)
	}
	defer m.Kill(sum.ID)

	got, err := m.SetAutoClose(sum.ID, 30*time.Minute)
	if err != nil {
		t.Fatalf("设置定时关闭: %v", err)
	}
	if got.AutoCloseAt == "" {
		t.Fatal("设置了定时关闭,摘要里没有 autoCloseAt")
	}
	at, err := time.Parse(time.RFC3339, got.AutoCloseAt)
	if err != nil {
		t.Fatalf("autoCloseAt 不是 RFC3339: %q", got.AutoCloseAt)
	}
	if d := time.Until(at); d <= 0 || d > 30*time.Minute {
		t.Errorf("截止时刻不在 (now, now+30min] 内: %v", d)
	}

	// 换档位 = 重新起算,不是沿用旧时刻。
	first := got.AutoCloseAt
	got, err = m.SetAutoClose(sum.ID, 60*time.Minute)
	if err != nil {
		t.Fatalf("改档位: %v", err)
	}
	if got.AutoCloseAt == first {
		t.Error("改档位后截止时刻没变,倒计时标签会停在旧值上")
	}

	// 0 = 取消。
	got, err = m.SetAutoClose(sum.ID, 0)
	if err != nil {
		t.Fatalf("取消定时关闭: %v", err)
	}
	if got.AutoCloseAt != "" {
		t.Errorf("取消后 autoCloseAt 还在: %q", got.AutoCloseAt)
	}

	// 不存在的会话要报错而不是静默成功。
	if _, err := m.SetAutoClose("s-nope", time.Minute); err == nil {
		t.Error("对不存在的会话设置定时关闭没有报错")
	}
}

// TestAutoCloseKillsSession 到点真的要结束进程,而且 exit 广播带上 reason=autoclose
// —— 前端靠它把"定时关闭"和手动结束分开学舌。
func TestAutoCloseKillsSession(t *testing.T) {
	m := NewManager(t.TempDir(), false)
	sum, err := m.Create("", "/bin/sh", 80, 24, Limits{}, 100*time.Millisecond)
	if err != nil {
		t.Skipf("本机起不了 shell:%v", err)
	}

	s := m.Get(sum.ID)
	select {
	case <-s.ExitCh():
	case <-time.After(5 * time.Second):
		t.Fatal("到点了会话还活着")
	}
	if r := s.KillReason(); r != "autoclose" {
		t.Errorf("KillReason = %q,期望 autoclose", r)
	}
	// 死会话上再设档位必须失败。
	if _, err := m.SetAutoClose(sum.ID, time.Minute); err == nil {
		t.Error("会话结束后设置定时关闭没有报错")
	}
}
