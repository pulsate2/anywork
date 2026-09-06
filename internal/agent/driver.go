package agent

import (
	"fmt"
)

// 支持的 agent 应用。
const (
	AppClaude = "claude"
	AppCodex  = "codex"
)

// 思考强度档位。
const (
	EffortNone   = "none"
	EffortLow    = "low"
	EffortMedium = "medium"
	EffortHigh   = "high"
)

// StartOpts Driver 启动参数。
type StartOpts struct {
	App            string // AppClaude | AppCodex
	Cwd            string
	ResumeID       string // claude session_id / codex thread id;空 = 新会话
	PermissionMode string // PermAsk | PermAccept
	Model          string // 空 = CLI 默认
	Effort         string // EffortNone…EffortHigh;空 = 默认
	// Env 覆盖项(driver 自己注入的之外再追加)。
	Env []string
}

// SettingsUpdate 运行中调整会话设置;空串 = 不改。
type SettingsUpdate struct {
	Model          string
	Effort         string
	PermissionMode string
}

// Driver 一个 agent 子进程的驱动。实现方负责把原生协议归一成 Event 流,
// 并处理权限请求的挂起/答复。所有方法并发安全(Manager 会从 REST handler
// 与事件泵两个 goroutine 调)。
type Driver interface {
	// Start spawn 子进程。失败立即返回;成功后事件从 Events() 出来。
	Start(opts StartOpts) error
	// Send 注入用户消息。回合进行中即插话。
	Send(text string) error
	// Interrupt 打断当前回合。
	Interrupt() error
	// Resolve 答复一个权限请求。session=true 表示"本会话允许":
	// driver 记住规则,同类请求后续自动放行不再询问(claude 内存规则 /
	// codex acceptForSession)。请求不存在/已答复返回错误。
	Resolve(reqID string, allow, session bool) error
	// PendingRequests 当前挂起的权限请求(快照)。
	PendingRequests() []PermissionReqPayload
	// ExternalID 会话恢复凭据(claude 在 init 消息里返回后才有值)。
	ExternalID() string
	// CurrentModel 实际在用的模型(claude init / codex thread 响应回填),未知为空。
	CurrentModel() string
	// ApplySettings 运行中调整设置。codex 下一回合生效;claude 不支持,
	// 返回错误(Manager 仍会落库,续聊时生效)。
	ApplySettings(u SettingsUpdate) error
	// Events 归一化事件流;进程退出后关闭。
	Events() <-chan Event
	// Done 进程退出(正常或被杀)后关闭。
	Done() <-chan struct{}
	// ExitErr 进程退出原因;Done 未关闭前调用无意义。
	ExitErr() error
	// Close 终止进程树并释放资源。幂等。
	Close() error
}

// newDriver 按应用构造 driver。
func newDriver(app string) (Driver, error) {
	switch app {
	case AppClaude:
		return newClaudeDriver(), nil
	case AppCodex:
		return newCodexDriver(), nil
	default:
		return nil, fmt.Errorf("未知 agent: %s", app)
	}
}

// resolveAgentBin 供 Manager 在建会话前探测可执行文件(不存在时 409)。
func resolveAgentBin(app string) (string, error) {
	switch app {
	case AppClaude:
		return claudeBin()
	case AppCodex:
		return codexBin()
	default:
		return "", fmt.Errorf("未知 agent: %s", app)
	}
}
