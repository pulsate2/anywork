package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func codexRetryRig(t *testing.T) (*codexDriver, *fakeStdin) {
	t.Helper()
	d := newCodexDriver()
	in := &fakeStdin{}
	d.stdin, d.threadID = in, "thread"
	t.Cleanup(func() {
		d.Close()
		d.doneOnce.Do(func() { close(d.done) })
	})
	return d, in
}

func drainCodexEvents(d *codexDriver) []Event {
	var out []Event
	for {
		select {
		case ev := <-d.events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func codexStarted(d *codexDriver, id string) {
	d.handleNotification("turn/started", json.RawMessage(fmt.Sprintf(`{"turn":{"id":%q,"status":"inProgress"}}`, id)))
}

func codexFailed(d *codexDriver, id, info string) {
	d.handleNotification("turn/completed", json.RawMessage(fmt.Sprintf(`{"turn":{"id":%q,"status":"failed","error":{"message":"upstream failed","codexErrorInfo":%s}}}`, id, info)))
}

func assertNoCodexIdle(t *testing.T, evs []Event) {
	t.Helper()
	for _, ev := range evs {
		if p, ok := ev.Payload.(*StatusPayload); ok && p.State == StatusIdle {
			t.Fatalf("retry must not release queued messages: %+v", evs)
		}
		if p, ok := ev.Payload.(*SystemInfoPayload); ok && p.Type == "turn_duration" {
			t.Fatal("retry must not end turn duration")
		}
	}
}

func TestCodexNativeRetryNotifications(t *testing.T) {
	for _, tc := range []struct{ name, method, raw string }{
		{"v2", "error", `{"error":{"message":"reconnecting"},"willRetry":true,"turnId":"turn"}`},
		{"legacy", "error", `{"message":"reconnecting","will_retry":true}`},
		{"wrapped", "codex/event/error", `{"msg":{"type":"error","message":"reconnecting","will_retry":true}}`},
		{"wrapped-string", "codex/event/error", `{"msg":{"type":"error","error":"reconnecting","will_retry":true}}`},
		{"stream-warning", "codex/event/stream_error", `{"msg":{"type":"stream_error","message":"reconnecting"}}`},
		{"retrying-task", "codex/event/task_failed", `{"msg":{"type":"task_failed","error":"reconnecting","will_retry":true}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, in := codexRetryRig(t)
			if err := d.Send("original request"); err != nil {
				t.Fatal(err)
			}
			codexStarted(d, "turn")
			d.mu.Lock()
			started := d.turnStart
			d.mu.Unlock()
			drainCodexEvents(d)
			d.handleNotification(tc.method, json.RawMessage(tc.raw))
			evs := drainCodexEvents(d)
			assertNoCodexIdle(t, evs)
			visible := false
			for _, ev := range evs {
				if p, ok := ev.Payload.(*SystemInfoPayload); ok && p.Type == "api_error" && p.Status == "retrying" && p.Error == "reconnecting" {
					visible = true
				}
				if ev.Kind == KindError {
					t.Fatal("native retry is not terminal error")
				}
			}
			if !visible {
				t.Fatalf("missing retry notice: %+v", evs)
			}
			d.mu.Lock()
			preserved := d.turnID == "turn" && d.turnStart.Equal(started)
			d.mu.Unlock()
			if !preserved {
				t.Fatal("native retry changed turn state or scheduled app retry")
			}
			if strings.Count(in.String(), `"method":"turn/start"`) != 1 {
				t.Fatal("native retry must not send another turn")
			}
		})
	}
}

func TestCodexFinalErrorCompatibility(t *testing.T) {
	for _, raw := range []string{
		`{"error":{"message":"authentication failed"},"willRetry":false,"turnId":"turn"}`,
		`{"message":"authentication failed","will_retry":false}`,
		// Explicit camelCase false takes precedence over old true.
		`{"message":"authentication failed","willRetry":false,"will_retry":true}`,
	} {
		d, _ := codexRetryRig(t)
		codexStarted(d, "turn")
		drainCodexEvents(d)
		d.handleNotification("error", json.RawMessage(raw))
		codexFailed(d, "turn", `"unauthorized"`)
		errors, idle := 0, 0
		for _, ev := range drainCodexEvents(d) {
			if p, ok := ev.Payload.(*ErrorPayload); ok {
				errors++
				if p.Message != "authentication failed" {
					t.Fatalf("lost nested error: %q", p.Message)
				}
			}
			if p, ok := ev.Payload.(*StatusPayload); ok && p.State == StatusIdle {
				idle++
			}
		}
		if errors != 1 || idle != 1 {
			t.Fatalf("final error should be reported once and idle once: %d/%d", errors, idle)
		}
	}
}

func TestCodexStartRPCFailureReturnsIdle(t *testing.T) {
	d, in := codexRetryRig(t)
	if err := d.Send("original"); err != nil {
		t.Fatal(err)
	}
	id := d.nextID
	drainCodexEvents(d)
	var msg jsonrpcLine
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"id":%d,"error":{"message":"turn rejected"}}`, id)), &msg); err != nil {
		t.Fatal(err)
	}
	d.handleRPCResponse(&msg)
	errorSeen, idle := false, false
	for _, ev := range drainCodexEvents(d) {
		if p, ok := ev.Payload.(*ErrorPayload); ok && p.Message == "turn rejected" {
			errorSeen = true
		}
		if p, ok := ev.Payload.(*StatusPayload); ok && p.State == StatusIdle {
			idle = true
		}
	}
	if !errorSeen || !idle {
		t.Fatal("rejected turn/start must report error and leave running")
	}
	if strings.Count(in.String(), `"method":"turn/start"`) != 1 {
		t.Fatal("unconfirmed input must not be replayed")
	}
}

func TestCodexInternalRetryExhaustionDoesNotResend(t *testing.T) {
	for _, terminal := range []string{"v2", "legacy"} {
		t.Run(terminal, func(t *testing.T) {
			d, in := codexRetryRig(t)
			if err := d.Send("original"); err != nil {
				t.Fatal(err)
			}
			codexStarted(d, "turn")
			drainCodexEvents(d)
			for i := 0; i < 3; i++ {
				d.handleNotification("error", json.RawMessage(`{"error":{"message":"reconnecting"},"willRetry":true,"turnId":"turn"}`))
				assertNoCodexIdle(t, drainCodexEvents(d))
			}
			if terminal == "v2" {
				codexFailed(d, "turn", `{"responseTooManyFailedAttempts":{"httpStatusCode":502}}`)
			} else {
				d.handleNotification("codex/event/task_failed", json.RawMessage(`{"msg":{"type":"task_failed","turn_id":"turn","error":"upstream failed","will_retry":false}}`))
			}
			errors, idle := 0, 0
			for _, ev := range drainCodexEvents(d) {
				if ev.Kind == KindError {
					errors++
				}
				if p, ok := ev.Payload.(*StatusPayload); ok && p.State == StatusIdle {
					idle++
				}
			}
			if errors != 1 || idle != 1 {
				t.Fatalf("exhaustion must report error and idle once: %d/%d", errors, idle)
			}
			codexFailed(d, "turn", `"serverOverloaded"`)
			d.handleNotification("codex/event/task_complete", json.RawMessage(`{"msg":{"type":"task_complete"}}`))
			if evs := drainCodexEvents(d); len(evs) != 0 {
				t.Fatalf("duplicate terminal events: %+v", evs)
			}
			if strings.Count(in.String(), `"method":"turn/start"`) != 1 {
				t.Fatal("internal retry exhaustion must not send continuation or replay input")
			}
			if strings.Contains(in.String(), "继续") {
				t.Fatal("agent must not append a continuation message")
			}
		})
	}
}

func TestCodexLateFinalErrorIsNotLost(t *testing.T) {
	d, _ := codexRetryRig(t)
	codexStarted(d, "turn")
	drainCodexEvents(d)
	d.handleNotification("codex/event/task_complete", json.RawMessage(`{"msg":{"type":"task_complete","turn_id":"turn"}}`))
	drainCodexEvents(d)
	codexFailed(d, "turn", `"serverOverloaded"`)
	evs := drainCodexEvents(d)
	if len(evs) != 1 || evs[0].Kind != KindError {
		t.Fatalf("late v2 failure must be shown without another idle: %+v", evs)
	}
	codexFailed(d, "turn", `"serverOverloaded"`)
	d.handleNotification("error", json.RawMessage(`{"error":{"message":"upstream failed"},"willRetry":false,"turnId":"turn"}`))
	if evs := drainCodexEvents(d); len(evs) != 0 {
		t.Fatalf("late error duplicated: %+v", evs)
	}
}

func TestCodexRejectedInputDoesNotEndActiveTurn(t *testing.T) {
	d, _ := codexRetryRig(t)
	if err := d.Send("original"); err != nil {
		t.Fatal(err)
	}
	codexStarted(d, "active")
	if err := d.Send("insertion"); err != nil {
		t.Fatal(err)
	}
	drainCodexEvents(d)
	var msg jsonrpcLine
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"id":%d,"error":{"message":"active turn cannot accept input"}}`, d.nextID)), &msg); err != nil {
		t.Fatal(err)
	}
	d.handleRPCResponse(&msg)
	assertNoCodexIdle(t, drainCodexEvents(d))
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.turnID != "active" || !d.turnActive {
		t.Fatal("rejected insertion must not end existing turn")
	}
}
