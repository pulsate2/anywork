package sysmon

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
)

// TestSnapshotShape 采集一次并检查形状:百分比在范围内、进程列表条数受 limit 约束、
// 排序真的按请求的键降序。数值本身随机器变化,只能验证不变量。
func TestSnapshotShape(t *testing.T) {
	m := New("", false)
	snap := m.Snapshot(10, "cpu")

	if snap.ProcSupported != procSupported {
		t.Fatalf("ProcSupported = %v, 平台常量是 %v", snap.ProcSupported, procSupported)
	}
	if snap.CPU.Percent != -1 && (snap.CPU.Percent < 0 || snap.CPU.Percent > 100) {
		t.Errorf("CPU.Percent 越界: %v", snap.CPU.Percent)
	}
	if snap.SampleMs < int64(minWindow/1e6) {
		t.Errorf("首次采样窗口应至少 %v,实际 %dms", minWindow, snap.SampleMs)
	}
	if snap.Memory.UsedPct < 0 || snap.Memory.UsedPct > 100 {
		t.Errorf("Memory.UsedPct 越界: %d", snap.Memory.UsedPct)
	}
	// Swap 可以整体为 0(机器没配 swap / 容器里关了统计),但填了总量就得自洽。
	if snap.Swap.UsedPct < 0 || snap.Swap.UsedPct > 100 {
		t.Errorf("Swap.UsedPct 越界: %d", snap.Swap.UsedPct)
	}
	if snap.Swap.UsedMB+snap.Swap.FreeMB != snap.Swap.TotalMB {
		t.Errorf("Swap 已用+空闲 != 总量: %+v", snap.Swap)
	}
	if !procSupported {
		if len(snap.Processes) != 0 {
			t.Errorf("平台不支持进程列表却返回了 %d 条", len(snap.Processes))
		}
		return
	}

	if snap.Memory.TotalMB == 0 {
		t.Error("Memory.TotalMB = 0")
	}
	if snap.ProcTotal == 0 {
		t.Fatal("一个进程都没采到")
	}
	if len(snap.Processes) > 10 {
		t.Errorf("limit=10 却返回 %d 条", len(snap.Processes))
	}
	for _, p := range snap.Processes {
		if p.PID <= 0 {
			t.Errorf("非法 pid: %+v", p)
		}
		if p.Cmd == "" {
			t.Errorf("pid %d 没有补全 Cmd", p.PID)
		}
		if p.CPU < 0 || p.CPU > 100 {
			t.Errorf("pid %d CPU 越界: %v", p.PID, p.CPU)
		}
	}

	// 第二次调用应复用上一次采样(不再睡 minWindow),且自己就是排序的检验样本。
	snap = m.Snapshot(200, "mem")
	for i := 1; i < len(snap.Processes); i++ {
		if snap.Processes[i-1].MemMB < snap.Processes[i].MemMB {
			t.Fatalf("sort=mem 没有降序: %v < %v", snap.Processes[i-1].MemMB, snap.Processes[i].MemMB)
		}
	}
	// 自己必须在按内存排的前 200 里(测试进程总有几 MB),顺带验证 enrich 找对了进程。
	me := os.Getpid()
	for _, p := range snap.Processes {
		if p.PID == me {
			if p.MemMB <= 0 {
				t.Errorf("自己的 MemMB = %v", p.MemMB)
			}
			return
		}
	}
	t.Logf("提示:本测试进程不在前 200 条内(共 %d 条),跳过自查", snap.ProcTotal)
}

// TestSnapshotNoProcs procLimit<=0 时不返回进程列表,但总数仍要报。
func TestSnapshotNoProcs(t *testing.T) {
	snap := New("", false).Snapshot(0, "")
	if len(snap.Processes) != 0 {
		t.Errorf("procLimit=0 却返回 %d 条", len(snap.Processes))
	}
	if procSupported && snap.ProcTotal == 0 {
		t.Error("ProcTotal 应始终填充(它是下一次差值的基线)")
	}
}

// TestSnapshotCanKill 前端靠 canKill 决定画不画结束按钮,所以它必须与 Kill 真正的
// 判断一致 —— 报 true 却拒绝执行(或反之)就是让用户白点一次。
func TestSnapshotCanKill(t *testing.T) {
	if got := New("", false).Snapshot(0, "").CanKill; got != procSupported {
		t.Errorf("可写模式下 CanKill = %v,平台支持是 %v", got, procSupported)
	}
	// 只读模式无论平台一律不给按钮。
	if New("", true).Snapshot(0, "").CanKill {
		t.Error("只读模式 CanKill 应为 false")
	}
	if got, want := New("", false).Snapshot(0, "").KillForceOnly, runtime.GOOS == "windows"; got != want {
		t.Errorf("KillForceOnly = %v,期望 %v", got, want)
	}
}

// TestMemUsage 总量为 0(没配 swap)不能除零;可用量反超总量也不能下溢。
func TestMemUsage(t *testing.T) {
	if free, used, pct := memUsage(0, 0); free != 0 || used != 0 || pct != 0 {
		t.Errorf("总量 0 应全零,得到 %d/%d/%d", free, used, pct)
	}
	if free, used, pct := memUsage(1000, 250); free != 250 || used != 750 || pct != 75 {
		t.Errorf("750/1000 应算出 75%%,得到 %d/%d/%d", free, used, pct)
	}
	// MemAvailable 偶尔略大于 MemTotal:截到总量,已用为 0,而不是下溢成天文数字。
	if free, used, pct := memUsage(1000, 1200); free != 1000 || used != 0 || pct != 0 {
		t.Errorf("可用量超总量应截断,得到 %d/%d/%d", free, used, pct)
	}
}

// TestKillGuards Kill 在真的发信号之前就该拒掉的那几种输入。这些分支与平台无关,
// 必须在所有平台上都成立 —— 尤其"不许杀自己",它错一次就是整个服务下线。
func TestKillGuards(t *testing.T) {
	m := New("", false)
	cases := []struct {
		name string
		pid  int
		want error
	}{
		{"pid 0", 0, ErrBadPID},
		{"负 pid(-pid 在 kill(2) 里是整个进程组)", -1234, ErrBadPID},
		{"1 号进程", 1, ErrBadPID},
		{"本服务自己", os.Getpid(), ErrBadPID},
	}
	for _, c := range cases {
		if err := m.Kill(c.pid, "", false); !errors.Is(err, c.want) {
			t.Errorf("%s: Kill(%d) = %v, 期望 %v", c.name, c.pid, err, c.want)
		}
	}
	// 只读模式下连合法 pid 也不许动,且这一关要排在其他检查之前。
	ro := New("", true)
	if err := ro.Kill(os.Getpid(), "", false); !errors.Is(err, ErrReadOnly) {
		t.Errorf("只读模式应返回 ErrReadOnly,得到 %v", err)
	}
}

// TestKillNameMismatch 名字对不上就拒绝 —— 这条护栏防的是 pid 被复用后杀错进程。
func TestKillNameMismatch(t *testing.T) {
	if !procSupported {
		t.Skip("本平台不列进程")
	}
	// 拿本进程的父进程当靶子:它一定存在,而且名字不会是下面这个。
	err := New("", false).Kill(os.Getppid(), "绝不可能是这个名字", false)
	if !errors.Is(err, ErrBadPID) {
		t.Errorf("名字不符应返回 ErrBadPID,得到 %v", err)
	}
}

// TestKillTerminatesChild 真的杀两个自己起的子进程,SIGTERM 与 SIGKILL 各走一遍 ——
// 默认按钮走的是 SIGTERM 那条路,只测 force 等于没测到主路径。
func TestKillTerminatesChild(t *testing.T) {
	if !procSupported {
		t.Skip("本平台不支持结束进程")
	}
	if runtime.GOOS == "windows" {
		t.Skip("这段用的是 POSIX sleep")
	}
	for _, force := range []bool{false, true} {
		cmd := exec.Command("sleep", "600")
		if err := cmd.Start(); err != nil {
			t.Skipf("起不了子进程: %v", err)
		}
		pid := cmd.Process.Pid

		if err := New("", false).Kill(pid, "", force); err != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			t.Fatalf("force=%v: Kill(%d) = %v", force, pid, err)
		}
		// Wait 返回即证明进程已死(被信号打死时它返回 "signal: ..." 错误,
		// 这里只关心它不再运行,不关心怎么死的)。
		_, _ = cmd.Process.Wait()
		// 回收之后 pid 已不存在,再杀一次应报"进程不存在"。
		if err := New("", false).Kill(pid, "", force); !errors.Is(err, ErrNoSuchProc) {
			t.Errorf("force=%v: 已回收的子进程应报 ErrNoSuchProc,得到 %v", force, err)
		}
	}
}

func TestCPUPercentUnavailable(t *testing.T) {
	if got := cpuPercent(cpuTimes{}, cpuTimes{busy: 1, total: 2, ok: true}); got != -1 {
		t.Errorf("基线缺失时应返回 -1,得到 %v", got)
	}
	// 总时间没往前走(两次采样撞在同一 tick)也算取不到,不能除以 0。
	same := cpuTimes{busy: 10, total: 100, ok: true}
	if got := cpuPercent(same, same); got != -1 {
		t.Errorf("窗口内无进展时应返回 -1,得到 %v", got)
	}
	prev := cpuTimes{busy: 100, total: 1000, ok: true}
	cur := cpuTimes{busy: 150, total: 1100, ok: true}
	if got := cpuPercent(prev, cur); got != 50 {
		t.Errorf("50/100 应算出 50,得到 %v", got)
	}
}
