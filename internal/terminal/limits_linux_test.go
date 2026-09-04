//go:build linux

package terminal

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSessionCgroupLimits 端到端:建一个带限额的会话,确认 cgroup 文件写对了、
// shell 真的在组里(而不是"建了组但进程在外面"),结束后子组被回收。
func TestSessionCgroupLimits(t *testing.T) {
	sup := limitSupport()
	if sup.Mode != "cgroup2" || !sup.Memory || !sup.CPU {
		t.Skipf("本机不支持 cgroup v2 限额:%s", sup.Detail)
	}

	m := NewManager(t.TempDir(), false)
	sum, err := m.Create("", "/bin/sh", 80, 24, Limits{MemoryMB: 64, CPUPercent: 10})
	if err != nil {
		t.Fatalf("创建会话: %v", err)
	}
	s := m.Get(sum.ID)
	if s == nil {
		t.Fatal("会话不在管理器里")
	}
	if sum.MemoryMB != 64 || sum.CPUPercent != 10 || sum.LimitMode != "cgroup2" {
		t.Errorf("摘要里的限额不对: %+v", sum)
	}
	h, ok := s.limiter.(*cgroupLimiter)
	if !ok {
		t.Fatalf("limiter 类型 = %T,期望 *cgroupLimiter", s.limiter)
	}

	if got, want := readTrim(t, h.dir, "memory.max"), strconv.Itoa(64<<20); got != want {
		t.Errorf("memory.max = %q,期望 %q", got, want)
	}
	wantCPU := strconv.Itoa(cgroupPeriod*limitCores()*10/100) + " " + strconv.Itoa(cgroupPeriod)
	if got := readTrim(t, h.dir, "cpu.max"); got != wantCPU {
		t.Errorf("cpu.max = %q,期望 %q", got, wantCPU)
	}
	// exec 前入组是这套方案的关键:组建好了但 shell 在组外,等于没限制。先确认命令
	// 真的被包成了 "sh -c '入组; exec 原命令'",再等它把自己写进去 —— Create 只保证
	// 进程已 fork,那个 echo 是它自己跑的,得给几十毫秒。
	if got := s.cmd.Args; len(got) != 4 || got[1] != "-c" || !strings.Contains(got[2], "cgroup.procs") {
		t.Errorf("命令没被包装成入组形式: %q", got)
	}
	pid := strconv.Itoa(s.cmd.Process.Pid)
	if !waitFor(3*time.Second, func() bool {
		return slices.Contains(strings.Fields(readTrim(t, h.dir, "cgroup.procs")), pid)
	}) {
		t.Errorf("cgroup.procs = %q,里面没有 shell 的 pid %s", readTrim(t, h.dir, "cgroup.procs"), pid)
	}

	m.Kill(sum.ID)
	select {
	case <-s.ExitCh():
	case <-time.After(5 * time.Second):
		t.Fatal("会话没能在 5s 内退出")
	}
	// release 是异步退避重试的,给它一点时间。
	if !waitFor(5*time.Second, func() bool {
		_, err := os.Stat(h.dir)
		return os.IsNotExist(err)
	}) {
		t.Errorf("会话结束后 cgroup 子组没被回收: %s", h.dir)
	}
}

// waitFor 轮询等条件成立。cgroup 里很多状态是别的进程(shell 自己、内核回收)
// 改的,只能等,不能读一次就下结论。
func waitFor(d time.Duration, ok func() bool) bool {
	deadline := time.Now().Add(d)
	for {
		if ok() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestSessionWithoutLimits 不填上限时不该有任何限额机制介入。
func TestSessionWithoutLimits(t *testing.T) {
	m := NewManager(t.TempDir(), false)
	sum, err := m.Create("", "/bin/sh", 80, 24, Limits{})
	if err != nil {
		t.Fatalf("创建会话: %v", err)
	}
	s := m.Get(sum.ID)
	defer m.Kill(sum.ID)
	if s.limiter != nil {
		t.Errorf("零值 Limits 却建了 limiter: %T", s.limiter)
	}
	if sum.LimitMode != "" || sum.MemoryMB != 0 || sum.CPUPercent != 0 {
		t.Errorf("摘要不该带限额信息: %+v", sum)
	}
	// 没有限额时执行的就是 shell 本身,不套 sh -c 包装。
	if got := s.cmd.Args; len(got) != 1 || got[0] != "/bin/sh" {
		t.Errorf("命令被意外包装: %q", got)
	}
}

func readTrim(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("读 %s: %v", name, err)
	}
	return strings.TrimSpace(string(b))
}
