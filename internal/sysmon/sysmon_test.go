package sysmon

import (
	"os"
	"testing"
)

// TestSnapshotShape 采集一次并检查形状:百分比在范围内、进程列表条数受 limit 约束、
// 排序真的按请求的键降序。数值本身随机器变化,只能验证不变量。
func TestSnapshotShape(t *testing.T) {
	m := New("")
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
	snap := New("").Snapshot(0, "")
	if len(snap.Processes) != 0 {
		t.Errorf("procLimit=0 却返回 %d 条", len(snap.Processes))
	}
	if procSupported && snap.ProcTotal == 0 {
		t.Error("ProcTotal 应始终填充(它是下一次差值的基线)")
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
