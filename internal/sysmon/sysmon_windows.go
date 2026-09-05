//go:build windows

package sysmon

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	defaultDiskPath = `C:\`
	procSupported   = true
)

var (
	modkernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = modkernel32.NewProc("GlobalMemoryStatusEx")
	procGetSystemTimes       = modkernel32.NewProc("GetSystemTimes")
	// x/sys/windows 没有导出 GetProcessMemoryInfo,自己声明。psapi.dll 在现代 Windows
	// 上只是转发到 kernel32 的 K32 系列,用它兼容性最好。
	modpsapi                 = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessMemoryInfo = modpsapi.NewProc("GetProcessMemoryInfo")
)

// readLoad Windows 没有 loadavg 这个概念。
func readLoad() float64 { return 0 }

func cores() int { return runtime.NumCPU() }

// readCPUTimes GetSystemTimes 给的是全部逻辑核累加的 100ns 计数,
// 且 kernel 里已经含 idle,所以 total = kernel+user、busy = total-idle。
func readCPUTimes() cpuTimes {
	var it, kt, ut windows.Filetime
	r, _, _ := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&it)), uintptr(unsafe.Pointer(&kt)), uintptr(unsafe.Pointer(&ut)))
	if r == 0 {
		return cpuTimes{}
	}
	idle := filetimeTicks(it)
	total := filetimeTicks(kt) + filetimeTicks(ut)
	if total == 0 || idle > total {
		return cpuTimes{}
	}
	return cpuTimes{busy: total - idle, total: total, ok: true}
}

func filetimeTicks(f windows.Filetime) uint64 {
	return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime)
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// readMemory 物理内存 + 页面文件(swap 的 Windows 对应物)。
//
// MEMORYSTATUSEX 里的 TotalPageFile/AvailPageFile 并不是"页面文件大小",而是提交
// 限额(物理内存 + 所有页面文件)与还能提交的量。所以页面文件本身要减掉物理内存
// 那一份;已用量同理,用总提交量减去物理已用量估出来。这是任务管理器"已提交"
// 那一栏的同一套算术,取不到就退化成 0(前端显示"未启用")。
func readMemory() (Memory, Swap) {
	var st memoryStatusEx
	st.Length = uint32(unsafe.Sizeof(st))
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&st)))
	if r == 0 {
		return Memory{}, Swap{}
	}
	m := Memory{
		TotalMB: st.TotalPhys / (1 << 20),
		FreeMB:  st.AvailPhys / (1 << 20),
	}
	m.FreeMB, m.UsedMB, m.UsedPct = memUsage(m.TotalMB, m.FreeMB)

	var s Swap
	if st.TotalPageFile > st.TotalPhys {
		s.TotalMB = (st.TotalPageFile - st.TotalPhys) / (1 << 20)
	}
	// 已提交总量减去物理已用量 ≈ 落在页面文件里的那部分。
	committed := sub(st.TotalPageFile, st.AvailPageFile)
	inPageFileMB := sub(committed, sub(st.TotalPhys, st.AvailPhys)) / (1 << 20)
	if inPageFileMB > s.TotalMB {
		inPageFileMB = s.TotalMB
	}
	s.FreeMB, s.UsedMB, s.UsedPct = memUsage(s.TotalMB, s.TotalMB-inPageFileMB)
	return m, s
}

// sub 不下溢的减法(两个计数分别采自不同瞬间时,后者可能反超前者)。
func sub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

// killProc 结束单个进程。Windows 没有"礼貌地请你退出"这种通用机制 —— 控制台程序能收
// Ctrl+C 事件,GUI 程序要收 WM_CLOSE 消息,两条路都要求发起方与目标共处一个控制台或桌面,
// 服务进程给不出。所以 force 无论真假都是 TerminateProcess:目标没有清理的机会,
// 这一点前端的确认框里要写清楚。
func killProc(pid int, force bool) error {
	_ = force
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		switch {
		case errors.Is(err, windows.ERROR_INVALID_PARAMETER):
			// 进程不存在时 OpenProcess 报的就是这个,不是 not found。
			return fmt.Errorf("%w: %d", ErrNoSuchProc, pid)
		case errors.Is(err, windows.ERROR_ACCESS_DENIED):
			return fmt.Errorf("%w: 无权结束 pid %d", ErrKillDenied, pid)
		}
		return err
	}
	defer windows.CloseHandle(h)
	// 退出码 1:约定俗成的"被外部终止",与正常退出的 0 区分开。
	if err := windows.TerminateProcess(h, 1); err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return fmt.Errorf("%w: 无权结束 pid %d", ErrKillDenied, pid)
		}
		return err
	}
	return nil
}

// readDisk 统计 path 所在卷的容量(root 可能在任意盘)。
func readDisk(path string) Disk {
	vol := filepath.VolumeName(path)
	if vol == "" {
		vol = "C:"
	}
	p, err := windows.UTF16PtrFromString(vol + `\`)
	if err != nil {
		return Disk{}
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return Disk{}
	}
	d := Disk{TotalGB: total / (1 << 30), FreeGB: totalFree / (1 << 30)}
	if d.TotalGB > 0 {
		d.UsedGB = d.TotalGB - d.FreeGB
		d.UsedPct = int(d.UsedGB * 100 / d.TotalGB)
	}
	return d
}

// processMemoryCounters PROCESS_MEMORY_COUNTERS(psapi.h)。SIZE_T = uintptr。
type processMemoryCounters struct {
	cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// readProcs 快照拿到 pid/父 pid/名字/线程数,CPU 时间与内存要逐个开句柄问。
// 命令行、属主留给 enrich —— 那还要再开一次句柄加一次 SID 查询。
func readProcs() []rawProc {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)

	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	out := make([]rawProc, 0, 256)
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		p := rawProc{
			pid:     int(e.ProcessID),
			ppid:    int(e.ParentProcessID),
			name:    windows.UTF16ToString(e.ExeFile[:]),
			threads: int(e.Threads),
		}
		fillProcUsage(&p)
		out = append(out, p)
	}
	return out
}

// fillProcUsage 拿不到句柄(System、Idle、更高完整性级别的进程)时保留名字和线程数,
// CPU/内存留 0 —— 比整条丢掉好,任务管理器也是这么显示的。
func fillProcUsage(p *rawProc) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, uint32(p.pid))
	if err != nil {
		// 没有 VM_READ 也能取到时间和工作集(Win7+ 的 K32 系列放宽了要求)。
		h, err = windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(p.pid))
		if err != nil {
			return
		}
	}
	defer windows.CloseHandle(h)

	var ct, et, kt, ut windows.Filetime
	if err := windows.GetProcessTimes(h, &ct, &et, &kt, &ut); err == nil {
		p.ticks = filetimeTicks(kt) + filetimeTicks(ut)
		// 创建时刻用来识别 pid 复用。
		p.start = filetimeTicks(ct)
	}
	var pmc processMemoryCounters
	pmc.cb = uint32(unsafe.Sizeof(pmc))
	if r, _, _ := procGetProcessMemoryInfo.Call(
		uintptr(h), uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.cb)); r != 0 {
		p.rssKB = uint64(pmc.WorkingSetSize) / 1024
	}
}

// enrich 补全完整路径与属主。Windows 取真正的命令行要读目标进程的 PEB(未公开且
// 跨位数容易翻车),这里用映像全路径代替,足够看出是哪个程序。
func enrich(p *Process) {
	if p.Cmd == "" {
		p.Cmd = p.Name
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(p.PID))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)

	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err == nil && n > 0 {
		p.Cmd = windows.UTF16ToString(buf[:n])
	}
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return
	}
	if acct, _, _, err := u.User.Sid.LookupAccount(""); err == nil {
		p.User = acct
	} else {
		p.User = u.User.Sid.String()
	}
}
