//go:build windows

package agent

import (
	"os/exec"
)

// setProcessGroup Windows 无进程组;CREATE_NEW_PROCESS_GROUP 让 taskkill /T 可用。
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup taskkill /T 按进程树终止,等效 Unix 的组杀。
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	exec.Command("taskkill", "/T", "/F", "/PID", itoa(cmd.Process.Pid)).Run()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
