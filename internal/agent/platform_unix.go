//go:build !windows

package agent

import (
	"os/exec"
	"syscall"
)

// setProcessGroup 独立进程组:kill 时整棵进程树一起收(claude 起的 bash/node 不留孤儿)。
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// 先 TERM 给收尾窗口,再 KILL 兜底;组内进程逐个退出,cmd.Wait 由 waitExit 收。
	syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
