package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// ---- 一、进程退出时的 emit(曾把整个服务进程带走)----

// TestClaudeEmitAfterExitDoesNotPanic 进程退出那一刻,读循环的 bufio 里还压着
// 没消化的行,仍会往 events 里发事件 —— 关流与发送必须互斥,否则
// `send on closed channel` 的 panic 会杀掉整个服务(非 HTTP goroutine 的 panic
// 没人 recover)。回归:假 claude 刷一大片 assistant 行后立刻退出,旧实现
// 10 次里 8 次复现 panic(栈是 emit → handleAssistant → readStdout)。
func TestClaudeEmitAfterExitDoesNotPanic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("用 /bin/sh 造假 CLI")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-claude")
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` +
		"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" + `"}]}}` + "\n"
	script := "#!/bin/sh\nfor i in $(seq 1 20000); do printf '%s' '" + line + "'; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LR_CLAUDE_BIN", bin)

	d := newClaudeDriver()
	if err := d.Start(StartOpts{App: AppClaude, Cwd: dir, PermissionMode: PermAsk}); err != nil {
		t.Fatalf("起假 claude: %v", err)
	}
	// 照 pump 的消费方式读到流关闭:退出时还有行在飞,旧实现这里就 panic 了。
	for range d.Events() {
	}
	// 关流之后再来一发(审批 goroutine/tailer 可能正好卡在这个时刻)。
	d.emit(Event{Kind: KindAssistantText, Payload: "迟到的"})
}

// TestClaudeWaitExitDrainsPending 进程退出时挂着的审批不能只清 map:等待方
// 卡在 <-ch 上,清 map 解不开,每挂起一个就永久漏一个 goroutine(旧实现如此,
// codex 的 waitExit 反而是排空的)。
func TestClaudeWaitExitDrainsPending(t *testing.T) {
	d := newClaudeDriver()
	d.cmd = &exec.Cmd{} // Wait() 对未启动的 cmd 直接返回错误,不 panic

	ch := make(chan *approveDecision, 1)
	d.mu.Lock()
	d.pending["r1"] = ch
	d.asks["r1"] = &askPending{}
	d.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.waitExit()
	}()
	select {
	case dec := <-ch:
		if dec.allow {
			t.Fatal("退出时挂起的审批该按拒绝收尾")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitExit 没给挂起的审批发收尾信号:等待的 goroutine 会永久泄漏")
	}
	<-done
	d.mu.Lock()
	np, na := len(d.pending), len(d.asks)
	d.mu.Unlock()
	if np != 0 || na != 0 {
		t.Fatalf("pending/asks 该清空: %d/%d", np, na)
	}
	// 关流之后迟到的 emit 不该 panic。
	d.emit(Event{Kind: KindAssistantText, Payload: "迟到的"})
}

// ---- 二、进程启动不在 Manager 锁里(曾冻住整个服务最长 60s)----

// slowStartDriver 起得"很慢"的 driver:Start 阻塞到测试放行,模拟 codex 的
// 协议握手(initialize + thread/start 各 30s 超时,对端不答话就拖满)。
type slowStartDriver struct {
	fakeDriver
	started chan struct{}
	release chan struct{}
}

func (s *slowStartDriver) Start(opts StartOpts) error {
	close(s.started)
	<-s.release
	return nil
}

// TestReviveSpawnNotUnderManagerLock 会话复活时进程启动必须在 Manager 锁外做:
// 持锁 spawn 一次(旧实现)在握手失败时会把整个服务冻住 —— 连会话列表
// (List → RunningIDs)都拿不到锁,页面整个刷不出来。
func TestReviveSpawnNotUnderManagerLock(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	newDriver = func(app string) (Driver, error) {
		return &slowStartDriver{started: started, release: release}, nil
	}
	t.Cleanup(func() {
		newDriver = func(app string) (Driver, error) {
			switch app {
			case AppClaude:
				return newClaudeDriver(), nil
			case AppCodex:
				return newCodexDriver(), nil
			default:
				return nil, fmt.Errorf("未知 agent: %s", app)
			}
		}
	})

	s := newTestStore(t)
	sess := createTestSession(t, s)
	m := NewManager(s, "/w", false)

	// 会话不在 sessions 里 → Send 走 revive → spawn:Start 卡住不放。
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		_, _ = m.Send(sess.ID, "醒醒")
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("Send 没走到 spawn")
	}

	// Start 还卡着:别的请求必须照常拿到 Manager 锁。
	got := make(chan struct{})
	go func() {
		m.RunningIDs()
		close(got)
	}()
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("进程启动期间 Manager 锁被占住了:整个服务会跟着卡")
	}

	close(release)
	select {
	case <-sendDone:
	case <-time.After(3 * time.Second):
		t.Fatal("放行后 Send 该走完")
	}
	if m.get(sess.ID) == nil {
		t.Fatal("复活后会话应装进 Manager")
	}
}

// ---- 三、放行排队消息不挡住事件泵(曾让会话连同审批/打断一起失联)----

// stuckSendDriver 第 2 次 Send 起卡住不返回:子进程半死时写 stdin 就是会
// 阻塞,把这一刻钉在测试里。
type stuckSendDriver struct {
	fakeDriver
	ch      chan Event
	n       int
	once    sync.Once
	stuck   chan struct{}
	release chan struct{}
}

func (d *stuckSendDriver) Events() <-chan Event { return d.ch }

func (d *stuckSendDriver) Send(text string) error {
	d.mu.Lock()
	d.n++
	n := d.n
	d.mu.Unlock()
	if n >= 2 {
		d.once.Do(func() { close(d.stuck) })
		<-d.release
	}
	return d.fakeDriver.Send(text)
}

// TestPumpNotBlockedByReleaseQueued 放行排队消息要写子进程 stdin,进程卡住
// 时写会阻塞 —— 这一步必须在 goroutine 里做。同步做(旧实现)会把 pump 自己
// 钉死:此后 driver 的每个事件都没人消费,这个会话的审批答复、打断按钮、
// 状态更新一起失去响应(而别的会话看着正常,最难排查)。
func TestPumpNotBlockedByReleaseQueued(t *testing.T) {
	s := newTestStore(t)
	sess := createTestSession(t, s)
	m := NewManager(s, "/w", false)
	sd := &stuckSendDriver{
		ch: make(chan Event, 16), stuck: make(chan struct{}), release: make(chan struct{}),
	}
	ls := &liveSession{
		id: sess.ID, driver: sd,
		subscribers: map[Subscriber]struct{}{},
		state:       StatusRunning,
	}
	m.mu.Lock()
	m.sessions[sess.ID] = ls
	m.mu.Unlock()
	go m.pump(ls)
	t.Cleanup(func() {
		close(sd.release) // 放行卡住的 Send,否则收尾时漏个 goroutine
		close(sd.ch)
	})

	if _, err := m.Send(sess.ID, "第一条"); err != nil {
		t.Fatalf("发第一条: %v", err)
	}
	queued, _, _, err := m.Enqueue(sess.ID, "第二条")
	if err != nil || !queued {
		t.Fatalf("回合进行中应入队: queued=%v err=%v", queued, err)
	}

	// 回合结束:pump 放行队首 → 第 2 次 Send 卡住。
	sd.ch <- Event{Kind: KindStatus, Payload: &StatusPayload{State: StatusIdle}}
	select {
	case <-sd.stuck:
	case <-time.After(3 * time.Second):
		t.Fatal("没走到放行(第 2 次 Send)")
	}

	// 卡住期间事件泵必须照常转:再喂一条 assistant 事件,它得落库。
	// 旧实现这里 pump 正堵在 Send 里,这条永远读不到,靠后的消息也就全丢。
	sd.ch <- Event{Kind: KindAssistantText, Payload: "还活着吗"}
	// 已落库:第一条 user、第二条 user、idle 状态,加上这条。
	readStored(t, s, sess.ID, 4)
}
