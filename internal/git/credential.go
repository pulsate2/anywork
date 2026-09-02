package git

import (
	"errors"
	"sync"
	"time"
)

// 给 git 的 GIT_ASKPASS 回调提供"浏览器弹窗回填"的进程内登记簿。
//
// 机制(见 DESIGN 交互式认证):
//   - runWithCreds 启动 git push/pull 前,注入 GIT_ASKPASS=<本二进制>、GIT_TERMINAL_PROMPT=0,
//     以及一个只回环 TCP listener 地址 + 随机 token 到 git 的 env。
//   - git 需要凭据时 fork 我们的二进制(askpass 子进程)。子进程检测到
//     LR_ASKPASS_MODE=1 即进入代理模式:连接回 listener,发 token+prompt,
//     读回答案打印到 stdout(git 从 askpass 的 stdout 读答案)。
//   - 服务端 serveOnce 校验 token 后调 b.await(token, prompt, field) 阻塞;git 为
//     用户名、密码各调用一次 askpass,field 由 prompt 文字推断。
//   - await 把 "需要凭据" 事件经 SetAskHandler 推给前端弹窗;用户输入后由
//     POST /api/git/auth/answer 调 b.answer(token, username, password) 一次性回填两字段,
//     放行先后两次等待。取消则调 answerError / cancelAll 让等待方走失败路径。
//   - 兜底超时:每次 await 起一个 timer,到期清掉 pending,await 返回 errCredTimeout,
//     push/pull 各自的 10 分钟 command timeout 兜底,不会永久挂死。

// askEvent 推给前端(经 WS)的"需要凭据"通知。
type askEvent struct {
	Token  string `json:"token"`
	Prompt string `json:"prompt"`
	Field  string `json:"field"`
}

// pendingCred 一次待回填的凭据(一个 token)。
type pendingCred struct {
	username string
	password string
	done     chan struct{} // answer/取消/超时时关闭
	timer    *time.Timer   // 兜底超时:清理 pending
}

// CredentialBroker 进程内注册表:把 git 的凭据询问与前端弹窗连接起来。
type CredentialBroker struct {
	mu      sync.Mutex
	pending map[string]*pendingCred
	onAsk   func(askEvent) // 当前 WS 连接的推送回调;nil 表示无人监听
}

var (
	errCredTimeout = errors.New("credential prompt timed out")
	errCredUnknown = errors.New("unknown credential token")
)

// NewCredentialBroker 构造空注册表。
func NewCredentialBroker() *CredentialBroker {
	return &CredentialBroker{pending: map[string]*pendingCred{}}
}

// SetAskHandler 由 WS 端点调用:每次有新凭据请求都会回调 f,把事件推到浏览器。
// 连接断开时传 nil 清空,并 CancelAll 取消所有 pending,防止 git 挂起。
func (b *CredentialBroker) SetAskHandler(f func(askEvent)) {
	b.mu.Lock()
	b.onAsk = f
	b.mu.Unlock()
	if f == nil {
		b.cancelAll()
	}
}

// await 由 askpass 的 serveOnce 调用:按 token+field 阻塞等答案,超时返回错误。
// token 是每次 runWithCreds 的随机值,首次遇到就登记一份 pending(后续字段共用)。
func (b *CredentialBroker) await(token, prompt, field string, timeout time.Duration) (string, error) {
	b.mu.Lock()
	p, ok := b.pending[token]
	if !ok {
		p = &pendingCred{done: make(chan struct{})}
		b.pending[token] = p
	}
	if b.onAsk != nil {
		b.onAsk(askEvent{Token: token, Prompt: prompt, Field: field})
	}
	b.mu.Unlock()

	if p.timer == nil {
		// 首个字段起兜底定时器;两个字段共用同一 timeout(见 runWithCreds)。
		p.timer = time.AfterFunc(timeout, func() {
			b.answerError(token)
		})
	}

	select {
	case <-p.done:
	case <-time.After(timeout):
		return "", errCredTimeout
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if field == "password" {
		return p.password, nil
	}
	return p.username, nil
}

// answer 由 POST /api/git/auth/answer 调用,一次性回填用户名+密码并放行等待者。
// 幂等:已回填/未知 token 返回错误但不 panic,不覆盖已填值。
func (b *CredentialBroker) answer(token, username, password string) error {
	b.mu.Lock()
	p, ok := b.pending[token]
	if !ok {
		b.mu.Unlock()
		return errCredUnknown
	}
	select {
	case <-p.done:
		b.mu.Unlock()
		return errCredUnknown // 已回填
	default:
	}
	p.username = username
	p.password = password
	if p.timer != nil {
		p.timer.Stop()
	}
	close(p.done)
	b.mu.Unlock()
	return nil
}

// answerError 由取消/失败时调用:让等待的 await 返回错误而非答案。
// 函数返回落后于等待方唤醒,后续 answer 会因 token 已清除而报错,不会二次放行。
// 调用方无需继续使用 token(错误只作观测)。
func (b *CredentialBroker) answerError(token string) error {
	b.mu.Lock()
	p, ok := b.pending[token]
	if !ok {
		b.mu.Unlock()
		return errCredUnknown
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	delete(b.pending, token)
	select {
	case <-p.done:
		b.mu.Unlock()
		return errCredUnknown // 已回填,不重复关闭
	default:
		close(p.done)
	}
	b.mu.Unlock()
	return nil
}

// cancelAll 取消所有 pending(WS 断开时):让每个 await 走失败路径,避免 git 挂着。
func (b *CredentialBroker) cancelAll() {
	b.mu.Lock()
	ps := make([]*pendingCred, 0, len(b.pending))
	for _, p := range b.pending {
		ps = append(ps, p)
	}
	b.pending = map[string]*pendingCred{}
	b.mu.Unlock()
	for _, p := range ps {
		if p.timer != nil {
			p.timer.Stop()
		}
		select {
		case <-p.done:
		default:
			close(p.done)
		}
	}
}