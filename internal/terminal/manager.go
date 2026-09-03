package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Manager 管理所有终端会话。
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	// clients 所有活动连接。会话列表/退出事件要发给每个连接(包括没附加任何会话的),
	// 不能只发给 Session.clients。
	clients map[*Client]struct{}
	// Root 会话默认工作目录边界(允许 "/")。
	Root string
	// ReadOnly 只读模式禁止创建/写入。
	ReadOnly bool
}

func NewManager(root string, readonly bool) *Manager {
	return &Manager{
		sessions: map[string]*Session{},
		clients:  map[*Client]struct{}{},
		Root:     root,
		ReadOnly: readonly,
	}
}

// AddClient/RemoveClient 登记连接,供广播使用。
func (m *Manager) AddClient(c *Client) {
	m.mu.Lock()
	m.clients[c] = struct{}{}
	m.mu.Unlock()
}

func (m *Manager) RemoveClient(c *Client) {
	m.mu.Lock()
	delete(m.clients, c)
	m.mu.Unlock()
}

func (m *Manager) allClients() []*Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Client, 0, len(m.clients))
	for c := range m.clients {
		out = append(out, c)
	}
	return out
}

// Create 新建会话并启动。
func (m *Manager) Create(dir, shell string, cols, rows int) (*Summary, error) {
	if m.ReadOnly {
		return nil, fmt.Errorf("只读模式")
	}
	// 默认目录 = 根目录(必须存在)。
	if dir == "" {
		dir = m.Root
	}
	dir, err := m.resolve(dir)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("目录不可用: %s", dir)
	}
	if shell == "" {
		shell = defaultShell()
	}

	id := newID()
	s := NewSession(id, dir, shell, sessionEnv(), cols, rows)
	if err := s.start(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()
	go m.watchExit(s)
	return &Summary{ID: id, Dir: s.dir}, nil
}

// sessionEnv 会话环境 = 服务进程环境,但强制 TERM=xterm:父进程的 TERM 可能缺失
// (Windows 服务)或是前端 xterm.js 不认的类型,TUI 程序会据此发出解析不了的转义序列。
func sessionEnv() []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, "TERM") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "TERM=xterm")
}

// watchExit 等进程退出,然后把 exit 与最新会话列表推给所有连接。
// 没有它,kill/exit 只结束了 PTY:前端收不到任何帧,会话列表和终端都停在旧状态。
func (m *Manager) watchExit(s *Session) {
	<-s.ExitCh()
	code := s.ExitCode()
	m.RemoveDead(s.id)
	for _, c := range m.allClients() {
		c.sendText(frameTypeExit, exitMsg{Type: "exit", ID: s.id, ExitCode: code})
	}
	m.broadcastSessionList()
}

// Attach 附加客户端到会话,返回会话与需要回放的缓冲。
func (m *Manager) Attach(id string, c *Client) (*Session, []byte, error) {
	s := m.get(id)
	if s == nil {
		return nil, nil, fmt.Errorf("会话不存在: %s", id)
	}
	buf := s.attachClient(c)
	return s, buf, nil
}

// Detach 从会话移除客户端。
func (m *Manager) Detach(id string, c *Client) {
	if s := m.get(id); s != nil {
		s.detachClient(c)
	}
}

func (m *Manager) get(id string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// Get 供 handler 使用(如 kill)。
func (m *Manager) Get(id string) *Session { return m.get(id) }

// Kill 终止会话并从管理器移除(进程退出后)。
func (m *Manager) Kill(id string) error {
	s := m.get(id)
	if s == nil {
		return fmt.Errorf("会话不存在: %s", id)
	}
	s.kill()
	return nil
}

// RemoveDead 移除已退出会话(由 handler 在 exit 事件后调用)。
func (m *Manager) RemoveDead(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok && s.isDead() {
		delete(m.sessions, id)
	}
}

// List 返回所有会话摘要(含已退出),按创建时间倒序。
func (m *Manager) List() []Summary {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	// map 遍历顺序随机,会让前端列表每次刷新都跳动;倒序后第一条即"最新会话",
	// 前端进入终端页时直接附加它。
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].createdAt.After(sessions[j].createdAt)
	})

	out := make([]Summary, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.Summary())
	}
	return out
}

// GC 清理长时间不活动/已退出的会话(预留)。
func (m *Manager) GC() {
	m.mu.Lock()
	dead := []string{}
	for id, s := range m.sessions {
		if s.isDead() {
			dead = append(dead, id)
		}
	}
	m.mu.Unlock()
	for _, id := range dead {
		m.RemoveDead(id)
	}
}

// IdleWatcher 轮询各会话,当"自上次输出后安静超过 threshold 且无新输入"时
// 触发 fire(一次安静段只触发一次,由 idleFuel 锁存控制)。
// 用于 Web Push "命令完成"通知;threshold<=0 时不启动。
func (m *Manager) IdleWatcher(interval, threshold time.Duration, fire func(*Session)) {
	if threshold <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		list := make([]*Session, 0, len(m.sessions))
		for _, s := range m.sessions {
			list = append(list, s)
		}
		m.mu.Unlock()
		for _, s := range list {
			s.mu.Lock()
			if s.dead {
				s.mu.Unlock()
				continue
			}
			sinceOut := time.Since(s.lastOutputAt)
			sinceIn := time.Since(s.lastInputAt)
			fireNow := s.idleFuel && sinceOut >= threshold && sinceIn >= threshold
			if fireNow {
				s.idleFuel = false // 锁存:本安静段只触发一次
			}
			s.mu.Unlock()
			if fireNow {
				go fire(s) // 异步投递,不阻塞 watcher
			}
		}
	}
}

// resolve 把 dir 归一化到 root 内的绝对路径:入参可以是相对 root(前端 cwd = "/")
// 或 root 内的绝对路径(工作区表里存的形式)。与 fs.Service.Resolve 语义一致。
func (m *Manager) resolve(dir string) (string, error) {
	root := filepath.Clean(m.Root)
	if dir == "" || dir == "/" || dir == "." {
		return root, nil
	}
	in := filepath.FromSlash(dir)
	// 旧数据/前端可能给出 "/D:/x" 这种带前导斜杠的盘符路径,还原成 "D:\x"。
	if trimmed := strings.TrimPrefix(in, string(filepath.Separator)); filepath.VolumeName(trimmed) != "" {
		in = trimmed
	}
	// 单独的盘符("D:")Clean 后是 "D:."(该盘当前目录),补上分隔符当盘根处理。
	if vol := filepath.VolumeName(in); vol != "" && vol == in {
		in += string(filepath.Separator)
	}
	clean := filepath.Clean(in)
	if filepath.IsAbs(clean) && withinRoot(clean, root) {
		return clean, nil
	}
	// 带盘符/UNC 前缀的入参只可能是绝对路径,上面没通过就是越界。
	if filepath.VolumeName(clean) != "" {
		return "", fmt.Errorf("目录超出根边界: %s", dir)
	}
	abs := filepath.Join(root, strings.TrimPrefix(clean, string(filepath.Separator)))
	if withinRoot(abs, root) {
		return abs, nil
	}
	return "", fmt.Errorf("目录超出根边界: %s", dir)
}

// withinRoot:root 可能自带尾分隔符(如 "/" 或 "D:\")。
func withinRoot(abs, root string) bool {
	if abs == root {
		return true
	}
	sep := string(filepath.Separator)
	if !strings.HasSuffix(root, sep) {
		root += sep
	}
	return strings.HasPrefix(abs, root)
}

func (s *Session) isDead() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dead
}

func newID() string {
	return fmt.Sprintf("s-%d", time.Now().UnixNano())
}
