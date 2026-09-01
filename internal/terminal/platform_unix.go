//go:build !windows

package terminal

import (
	"os"
	"syscall"
)

// terminateProcess 向整个进程组发信号。go-pty 用 Setsid 启动 shell,shell 的
// PGID 就是它的 PID,组里还有前台子进程(如 AI CLI)。只发给 shell 的话,子进程
// 会继续占着 PTY 从端,读循环收不到 EOF、Wait 也不返回,会话就卡在"结束不了"。
// force 时升级为 SIGKILL。
func terminateProcess(p *os.Process, force bool) {
	sig := syscall.SIGHUP
	if force {
		sig = syscall.SIGKILL
	}
	if err := syscall.Kill(-p.Pid, sig); err != nil {
		_ = p.Signal(sig)
	}
}

func defaultShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/sh"
}
