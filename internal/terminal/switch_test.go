package terminal

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestSwitchSessionDetachesPrevious(t *testing.T) {
	m := NewManager(t.TempDir(), false)
	oldSession := NewSession("old", "", "", nil, 80, 24)
	newSession := NewSession("new", "", "", nil, 80, 24)
	m.sessions["old"], m.sessions["new"] = oldSession, newSession
	server := httptest.NewServer(http.HandlerFunc(m.ServeWS))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	for i, id := range []string{"old", "new"} {
		payload, _ := json.Marshal(inMsg{Type: "attach", SID: id, RequestID: uint32(i + 1)})
		if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
			t.Fatal(err)
		}
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var ack sessMsg
		if len(data) < 2 || data[0] != frameTypeSession || json.Unmarshal(data[1:], &ack) != nil || ack.ID != id || ack.RequestID != uint32(i+1) {
			t.Fatalf("unexpected attach response: %q", data)
		}
	}
	oldSession.mu.Lock()
	oldSubscribers := len(oldSession.clients)
	oldSession.mu.Unlock()
	if oldSubscribers != 0 {
		t.Fatalf("old session still has %d subscribers after switching", oldSubscribers)
	}
	oldSession.publishOutput([]byte("OLD_OUTPUT"))
	newSession.publishOutput([]byte("NEW_OUTPUT"))
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.MessageBinary || len(data) < 5 || data[0] != frameTypeOutput || binary.BigEndian.Uint32(data[1:5]) != 2 || string(data[5:]) != "NEW_OUTPUT" {
		t.Fatalf("switched session received wrong output: %q", data)
	}
}

func TestReattachSeparatesQueuedOutput(t *testing.T) {
	s := NewSession("same", "", "", nil, 80, 24)
	c := newClient(nil)
	s.attachClient(c, 1)
	s.publishOutput([]byte("before-switch"))
	s.detachClient(c)
	s.attachClient(c, 2)
	s.publishOutput([]byte("after-switch"))
	for i, want := range []struct {
		requestID uint32
		data      string
	}{{1, "before-switch"}, {2, "before-switch"}, {2, "after-switch"}} {
		f := <-c.send
		if f.requestID != want.requestID || string(f.data) != want.data {
			t.Fatalf("frame %d = request %d %q, want request %d %q", i, f.requestID, f.data, want.requestID, want.data)
		}
	}
}

func TestReplayAndLiveOutputStayOrdered(t *testing.T) {
	s := NewSession("ordered", "", "", nil, 80, 24)
	c := newClient(nil)
	s.publishOutput([]byte("history\n"))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			s.publishOutput([]byte{byte(i)})
		}
	}()
	s.attachClient(c, 3)
	wg.Wait()
	var got []byte
	for len(c.send) > 0 {
		f := <-c.send
		if f.requestID != 3 {
			t.Fatalf("unexpected request ID: %d", f.requestID)
		}
		got = append(got, f.data...)
	}
	if want := s.buf.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("replay/live output duplicated or reordered: got %q, want %q", got, want)
	}
}
