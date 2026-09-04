// Package terminal 实现多窗口终端:PTY 会话、服务端滚动缓冲(重连回放)、
// 多客户端广播。滚动缓冲在服务端 = "内存持久化" —— 断线/换设备重连先回放历史。
package terminal

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/aymanbagabas/go-pty"
)

// maxBufferBytes 服务端滚动缓冲上限(1MB)。超过则丢弃最旧字节。
const maxBufferBytes = 1 << 20

// killGrace 温和终止到强杀之间的宽限期。
const killGrace = 3 * time.Second

// RingBuffer 线程安全的字节环形缓冲,用于终端输出回放。
type RingBuffer struct {
	mu   sync.Mutex
	data []byte
	max  int
}

func newRingBuffer(max int) *RingBuffer {
	return &RingBuffer{max: max}
}

func (r *RingBuffer) Write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data = append(r.data, p...)
	if len(r.data) > r.max {
		excess := len(r.data) - r.max
		r.data = r.data[excess:]
	}
}

// Bytes 返回缓冲副本。
func (r *RingBuffer) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]byte, len(r.data))
	copy(out, r.data)
	return out
}

var (
	errDead  = errors.New("session is dead")
	errNoPty = errors.New("session has no pty")
)

// Session 表示一个运行中的终端会话。
type Session struct {
	id    string
	dir   string
	env   []string
	shell string

	ptmx pty.Pty
	cmd  *pty.Cmd

	mu       sync.Mutex
	buf      *RingBuffer
	clients  map[*Client]struct{}
	dead     bool
	exitCode int

	// 空闲检测(Web Push 命令完成通知):记录最近一次输出/输入,
	// idleFuel 置位表示"自上次重置后出现过输出"(避免对短暂停顿重复通知)。
	lastOutputAt time.Time
	lastInputAt  time.Time
	idleFuel     bool

	exitCh   chan struct{}
	exitOnce sync.Once

	createdAt  time.Time
	cols, rows int

	// 资源限制:limiter 是平台句柄(cgroup 目录 / Job 对象),limits/limitMode
	// 是"实际生效"的部分 —— 请求了 CPU 但内核没有 cpu 控制器时这里就只剩内存。
	limiter   limiter
	limits    Limits
	limitMode string
}

// NewSession 创建(尚未启动)的会话。调用方随后需调用 start()。
func NewSession(id, dir, shell string, env []string, cols, rows int) *Session {
	return &Session{
		id:        id,
		dir:       dir,
		shell:     shell,
		env:       env,
		buf:       newRingBuffer(maxBufferBytes),
		clients:   map[*Client]struct{}{},
		exitCh:    make(chan struct{}),
		createdAt: time.Now(),
		cols:      cols,
		rows:      rows,
	}
}

// setLimiter 在 start() 之前挂上资源限制句柄。
func (s *Session) setLimiter(h limiter) {
	if h == nil {
		return
	}
	s.limiter = h
	s.limits, s.limitMode = h.applied()
}

// releaseLimits 进程退出后归还限额(删 cgroup 子组 / 关 Job 句柄)。
func (s *Session) releaseLimits() {
	if s.limiter != nil {
		s.limiter.release()
	}
}

// start 打开 PTY 并启动 shell,随后进入读循环。
func (s *Session) start() error {
	ptmx, err := pty.New()
	if err != nil {
		return err
	}
	// 有限制时执行的是包装命令(见 limits.go):它先把自己塞进 cgroup 再 exec shell,
	// 这样 shell 的 rc 文件里拉起的东西也在限制内。
	name, args := s.shell, []string(nil)
	if s.limiter != nil {
		name, args = s.limiter.wrap(s.shell)
	}
	cmd := ptmx.Command(name, args...)
	cmd.Dir = s.dir
	cmd.Env = s.env
	if err := cmd.Start(); err != nil {
		ptmx.Close()
		return err
	}
	// Windows 只能启动后补挂(此时 shell 还没来得及执行任何命令);Linux 的
	// wrap 已经搞定,这里是空操作。
	if s.limiter != nil && cmd.Process != nil {
		if err := s.limiter.attach(cmd.Process.Pid); err != nil {
			cmd.Process.Kill()
			ptmx.Close()
			return fmt.Errorf("应用资源限制失败: %w", err)
		}
	}

	s.ptmx = ptmx
	s.cmd = cmd

	if s.cols > 0 && s.rows > 0 {
		_ = ptmx.Resize(s.cols, s.rows)
	}

	go s.readLoop(ptmx)
	go s.waitExit(ptmx)
	return nil
}

// readLoop 持续读取 PTY 输出:写入缓冲 + 广播给所有客户端。
// 只负责泵数据;会话收尾由 waitExit 驱动。读出错(EOF/PTY 已关)即退出。
func (s *Session) readLoop(ptmx pty.Pty) {
	buf := make([]byte, 32*1024)
	for {
		n, err := ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			s.buf.Write(chunk)
			s.broadcast(outFrame{text: false, data: chunk})
			s.mu.Lock()
			s.lastOutputAt = time.Now()
			s.idleFuel = true
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// waitExit 等 shell 进程退出,记录退出码后关闭 PTY 并通知 Manager.watchExit。
// 必须由 Wait 而不是"读到 EOF"来判定退出:Windows ConPTY 的输出管道写端握在
// conhost 手里,子进程死了 Read 也不会返回,只有 ClosePseudoConsole(即 ptmx.Close)
// 才会让它 EOF —— 否则 kill 之后前端永远收不到 exit。
func (s *Session) waitExit(ptmx pty.Pty) {
	waitErr := s.cmd.Wait()
	code := 0
	// 退出码优先从 ProcessState 取:Windows 下 go-pty 包的是 os.Process.Wait,
	// 进程以非 0 退出时它也返回 nil error,只看 err 会把退出码误报成 0。
	var ee *exec.ExitError
	switch {
	case s.cmd.ProcessState != nil:
		code = s.cmd.ProcessState.ExitCode()
	case errors.As(waitErr, &ee):
		code = ee.ExitCode()
	case waitErr != nil:
		code = -1
	}
	s.mu.Lock()
	s.dead = true
	s.exitCode = code
	s.ptmx = nil
	s.mu.Unlock()
	// go-pty 的 Close 不可重入(Windows 会二次 ClosePseudoConsole),只在这里调用一次。
	// 此时 readLoop 仍在读,ClosePseudoConsole 需要输出被排空才能返回,不能提前停读。
	ptmx.Close()
	s.exitOnce.Do(func() { close(s.exitCh) })
}

// input 向 PTY 写入用户输入。
func (s *Session) input(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead || s.ptmx == nil {
		return errDead
	}
	_, err := s.ptmx.Write(b)
	if err == nil {
		s.lastInputAt = time.Now()
	}
	return err
}

// resize 调整 PTY 尺寸。
func (s *Session) resize(cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ptmx == nil {
		return errNoPty
	}
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	s.cols, s.rows = cols, rows
	return s.ptmx.Resize(cols, rows)
}

// kill 请求终止会话进程;宽限期内没退出就强杀。
// 收尾(标记 dead、关 PTY、关 exitCh)一律走 waitExit,这里不碰状态。
func (s *Session) kill() {
	s.mu.Lock()
	dead := s.dead
	var proc *os.Process
	if s.cmd != nil {
		proc = s.cmd.Process
	}
	s.mu.Unlock()
	if dead || proc == nil {
		return
	}
	terminateProcess(proc, false)
	// shell 可能忽略 SIGHUP,或前台子进程赖着不走:超时后强杀,
	// 否则 exitCh 永不关闭,前端看到的就是"结束会话没反应"。
	go func() {
		select {
		case <-s.exitCh:
		case <-time.After(killGrace):
			terminateProcess(proc, true)
		}
	}()
}

// attachClient 注册一个客户端并返回需要回放的缓冲。
func (s *Session) attachClient(c *Client) []byte {
	s.mu.Lock()
	s.clients[c] = struct{}{}
	buf := s.buf.Bytes()
	s.mu.Unlock()
	return buf
}

// detachClient 移除客户端。
func (s *Session) detachClient(c *Client) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
}

// broadcast 向所有已附加客户端推送一帧。
func (s *Session) broadcast(f outFrame) {
	s.mu.Lock()
	clients := make([]*Client, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()
	for _, c := range clients {
		c.push(f)
	}
}

// ExitCode 安全读取进程退出码(带锁)。
func (s *Session) ExitCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitCode
}

// ExitCh 在进程退出时关闭。
func (s *Session) ExitCh() <-chan struct{} { return s.exitCh }

// ID 返回会话标识。
func (s *Session) ID() string { return s.id }

// Dir 返回会话工作目录。
func (s *Session) Dir() string { return s.dir }

// Summary 供列表/创建响应的会话摘要。
func (s *Session) Summary() Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Summary{
		ID:         s.id,
		Dir:        s.dir,
		Cols:       s.cols,
		Rows:       s.rows,
		CreatedAt:  s.createdAt.UTC().Format(time.RFC3339),
		Dead:       s.dead,
		ExitCode:   s.exitCode,
		MemoryMB:   s.limits.MemoryMB,
		CPUPercent: s.limits.CPUPercent,
		LimitMode:  s.limitMode,
	}
}
