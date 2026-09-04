package terminal

// 会话资源限制。实现按平台分文件:limits_linux.go(cgroup v2 / RLIMIT_AS)、
// limits_windows.go(Job 对象)、limits_other.go(不支持)。
//
// 限制必须在 shell exec 之前生效,否则它 rc 文件里拉起的东西就跑在限制之外了。
// Linux 用 `sh -c '<入组>; exec "$0"' <shell>` 做到这点;Windows 没有等价手段,
// 只能启动后立刻把进程塞进 Job 对象(此时 shell 还没来得及执行任何命令)。

// minLimitMemoryMB 内存上限下界。给得太小 shell 自己都起不来(直接被 OOM 杀掉),
// 用户看到的是"新建会话就闪退",不如挡在这里。
const minLimitMemoryMB = 16

// maxLimitMemoryMB 上界只用来挡手滑(比如把 GB 当 MB 填成 1024000)。
const maxLimitMemoryMB = 1 << 20 // 1 TiB

// Limits 新建会话时申请的资源上限,0 = 不限。
//
// CPUPercent 是"占整机的百分比",与设置页系统面板里那些 CPU 数字同一把尺子:
// 8 核机器上填 25,就是最多用满 2 个核。若按"占单核"算,同一个百分数在两个
// 页面上含义不同,用户没法对照着调。
type Limits struct {
	MemoryMB   int `json:"memoryMB"`
	CPUPercent int `json:"cpuPercent"`
}

func (l Limits) isZero() bool { return l.MemoryMB <= 0 && l.CPUPercent <= 0 }

// clamp 服务端兜底。前端输入框已经限了范围,但 WS 帧是可以手工构造的。
func (l Limits) clamp() Limits {
	if l.MemoryMB < 0 {
		l.MemoryMB = 0
	}
	if l.MemoryMB > 0 && l.MemoryMB < minLimitMemoryMB {
		l.MemoryMB = minLimitMemoryMB
	}
	if l.MemoryMB > maxLimitMemoryMB {
		l.MemoryMB = maxLimitMemoryMB
	}
	if l.CPUPercent < 0 {
		l.CPUPercent = 0
	}
	if l.CPUPercent > 100 {
		l.CPUPercent = 100
	}
	return l
}

// Support 本机能限什么、用什么机制限。前端据此决定新建会话时显示哪些输入框,
// 以及要不要给一句提醒 —— 悄悄忽略用户填的上限比不提供这个功能更糟。
type Support struct {
	Memory bool   `json:"memory"`
	CPU    bool   `json:"cpu"`
	Mode   string `json:"mode"`   // cgroup2 | rlimit | job | none
	Detail string `json:"detail"` // 给用户看的一句话说明
	Cores  int    `json:"cores"`  // "占整机 %" 换算成核数的分母
}

// LimitSupport 供 /api/term/limits 查询。
func LimitSupport() Support { return limitSupport() }

// limiter 一份已经建好的限制(cgroup 目录 / Job 对象)。
type limiter interface {
	// wrap 改写待执行的命令,让子进程在 exec 之前就落进限制里。
	// 不支持前置的平台原样返回,由 attach 兜底。
	wrap(shell string) (string, []string)
	// attach 进程已启动后补挂。wrap 已经搞定的平台这里是空操作。
	attach(pid int) error
	// release 进程退出后清理(删 cgroup 目录 / 关 Job 句柄)。
	release()
	// applied 实际生效的上限与机制。请求了 CPU 但内核没有 cpu 控制器时,
	// 这里返回的就是"内存生效、CPU 没生效",照实回报给前端。
	applied() (Limits, string)
}
