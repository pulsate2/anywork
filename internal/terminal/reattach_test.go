package terminal

import (
	"bytes"
	"testing"
	"time"
)

// TestReattachReplaysBuffer 前端的断线重连整个建立在这条性质上:连接断了 PTY 不能跟着走,
// 新连接 attach 回同一个会话时,要拿到之前的输出(回放),之后的新输出也要能收到。
// 这里模拟"手机切后台被断网,回来重新连上":A 断开、B 接上同一个会话。
func TestReattachReplaysBuffer(t *testing.T) {
	m := NewManager(t.TempDir(), false)
	sum, err := m.Create("", "/bin/sh", 80, 24, Limits{})
	if err != nil {
		t.Skipf("本机起不了 shell:%v", err)
	}
	defer m.Kill(sum.ID)

	// A 是"断线前"的那条连接。conn 传 nil 没问题:push 只往 channel 里放,不碰 WS。
	a := newClient(nil)
	s, _, err := m.Attach(sum.ID, a)
	if err != nil {
		t.Fatalf("A attach: %v", err)
	}
	// 让 shell 吐一句认得出来的东西,进 ring buffer。
	if err := s.input([]byte("echo LR_MARK_1\n")); err != nil {
		t.Fatalf("写输入: %v", err)
	}
	if !waitFrame(a, "LR_MARK_1") {
		t.Fatal("A 没收到自己那句输出")
	}

	// 连接断了(服务端只做 Detach),会话必须还活着。
	m.Detach(sum.ID, a)
	if s.isDead() {
		t.Fatal("客户端断开把会话也带走了")
	}
	found := false
	for _, x := range m.List() {
		if x.ID == sum.ID && !x.Dead {
			found = true
		}
	}
	if !found {
		t.Fatal("断开后会话不在活动列表里,前端就没法接回去了")
	}

	// B 是重连后的新连接:回放里必须有断线前的输出。
	b := newClient(nil)
	_, replay, err := m.Attach(sum.ID, b)
	if err != nil {
		t.Fatalf("B attach: %v", err)
	}
	if !bytes.Contains(replay, []byte("LR_MARK_1")) {
		t.Errorf("回放里没有断线前的输出,重连后屏幕就是空的:%q", tail(replay))
	}
	// 重连之后的新输出也要照常广播给 B。
	if err := s.input([]byte("echo LR_MARK_2\n")); err != nil {
		t.Fatalf("写输入: %v", err)
	}
	if !waitFrame(b, "LR_MARK_2") {
		t.Error("重连后的新输出没广播到新连接")
	}
}

// TestPushNeverBlocks 队列满了 push 也必须立刻返回:watchExit / broadcastSessionList 是
// 同步循环,一个"人还在但不收"的连接(手机断网后的黑洞连接)一旦把它卡住,
// 别的客户端连会话退出都收不到。
func TestPushNeverBlocks(t *testing.T) {
	c := newClient(nil)
	done := make(chan struct{})
	go func() {
		// 塞两倍队列容量,一次都不读。
		for i := 0; i < 2*cap(c.send)+16; i++ {
			c.push(outFrame{data: []byte("x")})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("push 在队列满时阻塞了")
	}
}

// waitFrame 等这个客户端收到含 want 的输出帧。
func waitFrame(c *Client, want string) bool {
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f := <-c.send:
			if !f.text && bytes.Contains(f.data, []byte(want)) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// tail 出错信息里只留末尾一段,免得把整屏输出打进日志。
func tail(b []byte) []byte {
	if len(b) > 200 {
		return b[len(b)-200:]
	}
	return b
}
