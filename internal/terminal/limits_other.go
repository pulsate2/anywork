//go:build !linux && !windows

package terminal

import (
	"errors"
	"runtime"
)

// 其余平台(darwin/bsd)没有等价的会话级限额机制:macOS 没有 cgroup,
// FreeBSD 的 rctl 要额外内核配置。如实报"不支持",前端就不显示这两个输入框。
func limitSupport() Support {
	return Support{
		Mode:   "none",
		Cores:  runtime.NumCPU(),
		Detail: "当前平台不支持会话级资源限制(仅 Linux cgroup v2 与 Windows Job 对象可用)。",
	}
}

func newLimiter(_ string, l Limits) (limiter, error) {
	if l.isZero() {
		return nil, nil
	}
	return nil, errors.New("当前平台不支持会话级资源限制")
}
