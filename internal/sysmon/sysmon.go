// Package sysmon 采集机器概览(CPU/内存/磁盘)与进程列表,供设置页的"系统"面板
// 实时刷新使用。采集实现按平台分文件(sysmon_linux/windows/other)。
//
// 这里是有状态的:所有 CPU 百分比都必须由两次采样的差值算出,单次快照拿不到。
// Monitor 因此记住上一次采样,前端每隔一两秒来问一次时,直接用"上次→这次"这段
// 窗口算,既准又不用等;窗口不可用(首次访问/太旧/太短)才就地补采一次。
package sysmon

import (
	"sort"
	"sync"
	"time"
)

const (
	// minWindow 差值窗口下限。窗口只有几十毫秒时,几个 tick 的抖动就能算出几百个
	// 百分点,不如花这点时间采个准的。
	minWindow = 250 * time.Millisecond
	// maxAge 差值窗口上限。上次采样太久之前,算出来的是那段时间的均值,不是"此刻"。
	maxAge = 10 * time.Second
	// maxProcs 单次返回的进程条数上限。
	maxProcs = 200
)

// Info 机器概览。字段名与旧 /api/sysinfo 保持一致,前端无需适配。
type Info struct {
	CPU    CPU    `json:"cpu"`
	Memory Memory `json:"memory"`
	Disk   Disk   `json:"disk"`
}

type CPU struct {
	// Load 1 分钟平均负载(Windows 无此概念,恒为 0)。
	Load float64 `json:"load"`
	// Cores CPU 核数。
	Cores int `json:"cores"`
	// Percent 整机 CPU 使用率(0-100);-1 表示取不到。
	Percent float64 `json:"percent"`
}

type Memory struct {
	TotalMB uint64 `json:"totalMB"`
	UsedMB  uint64 `json:"usedMB"`
	FreeMB  uint64 `json:"freeMB"`
	UsedPct int    `json:"usedPct"`
}

type Disk struct {
	TotalGB uint64 `json:"totalGB"`
	UsedGB  uint64 `json:"usedGB"`
	FreeGB  uint64 `json:"freeGB"`
	UsedPct int    `json:"usedPct"`
}

// Process 一个进程的占用情况。CPU 是占整机的百分比(多核已平摊),与 Info.CPU.Percent
// 同一把尺子 —— 任务管理器里两个数字并排显示,不能一个按核算一个按整机算。
type Process struct {
	PID     int     `json:"pid"`
	PPID    int     `json:"ppid"`
	Name    string  `json:"name"`
	Cmd     string  `json:"cmd"`
	User    string  `json:"user"`
	State   string  `json:"state"`
	Threads int     `json:"threads"`
	CPU     float64 `json:"cpu"`
	MemMB   float64 `json:"memMB"`
	MemPct  float64 `json:"memPct"`
}

// Snapshot 一次采样结果。Info 内联展开,所以 JSON 顶层就是 cpu/memory/disk。
type Snapshot struct {
	Info
	// SampleMs 这批百分比是用多长的时间窗算出来的。
	SampleMs int64 `json:"sampleMs"`
	// ProcSupported 本平台是否支持进程列表。
	ProcSupported bool `json:"procSupported"`
	// ProcTotal 进程总数(裁剪前)。
	ProcTotal int `json:"procTotal"`
	// Processes 已按 sortBy 排序并裁剪到 limit 条。
	Processes []Process `json:"processes"`
}

// Monitor 有状态采集器:记住上一次采样用于算差值。可并发调用。
type Monitor struct {
	// root 磁盘容量统计的落点(工作根目录所在的卷)。
	root string

	mu   sync.Mutex
	prev sample
}

func New(root string) *Monitor {
	if root == "" {
		root = defaultDiskPath
	}
	return &Monitor{root: root}
}

// sample 一次原始采集。所有 tick 单位随平台,只参与同平台内的减法。
type sample struct {
	ok    bool
	at    time.Time
	cpu   cpuTimes
	procs []rawProc
}

// cpuTimes 整机 CPU 时间。Busy/Total 之比即使用率。
type cpuTimes struct {
	busy, total uint64
	ok          bool
}

// rawProc 平台层交出来的原始进程信息。ticks = 用户态+内核态 CPU 时间,
// start = 启动时刻(pid 复用时用来判断"还是不是上次那个进程")。
type rawProc struct {
	pid, ppid int
	name      string
	state     string
	threads   int
	rssKB     uint64
	ticks     uint64
	start     uint64
}

// Snapshot 采一次。procLimit<=0 表示不需要进程列表(首页只要三张卡)。
// sortBy: "mem" 按内存,其余按 CPU。
func (m *Monitor) Snapshot(procLimit int, sortBy string) Snapshot {
	if procLimit > maxProcs {
		procLimit = maxProcs
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	cur := m.read()
	prev := m.prev
	// 没有可用基线:就地补采一次,窗口正好是 minWindow。首次访问、离开页面很久
	// 再回来、以及两个请求撞在一起(窗口几毫秒)都走这条路。
	if !prev.ok || !usableWindow(prev.at, cur.at) {
		prev = cur
		time.Sleep(minWindow)
		cur = m.read()
	}
	m.prev = cur

	win := cur.at.Sub(prev.at)
	snap := Snapshot{
		Info: Info{
			CPU:    CPU{Load: readLoad(), Cores: cores(), Percent: cpuPercent(prev.cpu, cur.cpu)},
			Memory: readMemory(),
			Disk:   readDisk(m.root),
		},
		SampleMs:      win.Milliseconds(),
		ProcSupported: procSupported,
		ProcTotal:     len(cur.procs),
		Processes:     []Process{},
	}
	if procLimit > 0 && procSupported {
		snap.Processes = buildProcs(prev, cur, snap.Memory.TotalMB, procLimit, sortBy)
	}
	return snap
}

// read 采一次原始数据。进程列表始终采集(即使调用方不要):它同时是下一次
// 差值的基线,漏一次就要让下一次请求全体 0%。单次遍历只读每个进程一个文件,
// 千把个进程也在十几毫秒量级。
func (m *Monitor) read() sample {
	return sample{ok: true, at: time.Now(), cpu: readCPUTimes(), procs: readProcs()}
}

func usableWindow(prev, cur time.Time) bool {
	d := cur.Sub(prev)
	return d >= minWindow && d <= maxAge
}

// cpuPercent 整机使用率 = 忙时间增量 / 总时间增量。取不到返回 -1。
func cpuPercent(prev, cur cpuTimes) float64 {
	if !prev.ok || !cur.ok || cur.total <= prev.total {
		return -1
	}
	total := cur.total - prev.total
	var busy uint64
	if cur.busy > prev.busy {
		busy = cur.busy - prev.busy
	}
	return clampPct(float64(busy) / float64(total) * 100)
}

// buildProcs 把两次采样合成进程占用列表,排序后裁剪,只对留下来的几条补全
// 命令行/用户名(那部分要额外读文件,对上千个进程做一遍是纯浪费)。
func buildProcs(prev, cur sample, totalMB uint64, limit int, sortBy string) []Process {
	// 上一次的 ticks 按 pid 索引。start 一并比对:pid 会被复用,拿旧进程的 ticks
	// 去减新进程的,能算出一个巨大的负数或正数。
	before := make(map[int]rawProc, len(prev.procs))
	for _, p := range prev.procs {
		before[p.pid] = p
	}
	totalDelta := float64(0)
	if cur.cpu.ok && prev.cpu.ok && cur.cpu.total > prev.cpu.total {
		totalDelta = float64(cur.cpu.total - prev.cpu.total)
	}

	out := make([]Process, 0, len(cur.procs))
	for _, p := range cur.procs {
		proc := Process{
			PID:     p.pid,
			PPID:    p.ppid,
			Name:    p.name,
			State:   p.state,
			Threads: p.threads,
			// 一位小数就够看。这个接口是秒级轮询的,几十条 246.73828125 白占带宽。
			MemMB: round1(float64(p.rssKB) / 1024),
		}
		if totalMB > 0 {
			proc.MemPct = clampPct(proc.MemMB / float64(totalMB) * 100)
		}
		if b, ok := before[p.pid]; ok && b.start == p.start && p.ticks > b.ticks && totalDelta > 0 {
			proc.CPU = clampPct(float64(p.ticks-b.ticks) / totalDelta * 100)
		}
		out = append(out, proc)
	}

	byMem := sortBy == "mem"
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if byMem {
			if a.MemMB != b.MemMB {
				return a.MemMB > b.MemMB
			}
			return a.CPU > b.CPU
		}
		if a.CPU != b.CPU {
			return a.CPU > b.CPU
		}
		return a.MemMB > b.MemMB
	})
	if len(out) > limit {
		out = out[:limit]
	}
	for i := range out {
		enrich(&out[i])
	}
	return out
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	// 保留一位小数就够看,少传几个字节。
	return round1(v)
}

func round1(v float64) float64 {
	if v < 0 {
		return 0
	}
	return float64(int64(v*10+0.5)) / 10
}
