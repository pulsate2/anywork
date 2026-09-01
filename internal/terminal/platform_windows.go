package terminal

import (
	"os"
	"os/exec"
)

// terminateProcess Windows 无 SIGHUP,直接终止进程(force 无区别)。
// 残留的子进程由随后的 PTY 关闭(ClosePseudoConsole)带走。
func terminateProcess(p *os.Process, _ bool) {
	_ = p.Kill()
}

func defaultShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	if p, err := exec.LookPath("powershell.exe"); err == nil {
		return p
	}
	if s := os.Getenv("COMSPEC"); s != "" {
		return s
	}
	return "cmd.exe"
}
