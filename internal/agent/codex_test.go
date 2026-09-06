package agent

import (
	"os/exec"
	"testing"
	"time"
)

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
	deadline := time.After(90 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-d.Events():
			if !ok {
				break loop
			}
			switch ev.Kind {
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
	if !gotIdle {
		t.Error("没收到 idle 状态")
	}
}
