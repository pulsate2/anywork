//go:build windows

package sysmon

import (
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

func readMemory() Memory {
	var st memoryStatusEx
	st.Length = uint32(unsafe.Sizeof(st))
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&st)))
	if r == 0 {
		return Memory{}
	}
	m := Memory{
		TotalMB: st.TotalPhys / (1 << 20),
		FreeMB:  st.AvailPhys / (1 << 20),
	}
	if m.TotalMB > 0 {
		if m.FreeMB > m.TotalMB {
			m.FreeMB = m.TotalMB
		}
		m.UsedMB = m.TotalMB - m.FreeMB
		m.UsedPct = int(m.UsedMB * 100 / m.TotalMB)
	}
	return m
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
