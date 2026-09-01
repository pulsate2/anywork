//go:build windows

package main

import (
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func (a *App) readSysInfo() sysInfo {
	return sysInfo{
		CPU:    readCPU(),
		Memory: readMemory(),
		Disk:   readDisk(a.cfg.Root),
	}
}

// readCPU 用两次 GetSystemTimes 的差值算使用率。Windows 没有 loadavg,Load 恒为 0。
func readCPU() cpuInfo {
	info := cpuInfo{Cores: runtime.NumCPU(), Percent: -1}
	idle1, total1, err := systemTimes()
	if err != nil {
		return info
	}
	time.Sleep(150 * time.Millisecond)
	idle2, total2, err := systemTimes()
	if err != nil {
		return info
	}
	dTotal := total2 - total1
	if dTotal == 0 {
		return info
	}
	busy := float64(dTotal-(idle2-idle1)) / float64(dTotal) * 100
	if busy < 0 {
		busy = 0
	} else if busy > 100 {
		busy = 100
	}
	info.Percent = busy
	return info
}

// systemTimes 返回 (idle, idle+kernel+user) 的 100ns 计数。kernel 已包含 idle。
func systemTimes() (idle, total uint64, err error) {
	var it, kt, ut windows.Filetime
	r, _, e := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&it)), uintptr(unsafe.Pointer(&kt)), uintptr(unsafe.Pointer(&ut)))
	if r == 0 {
		return 0, 0, e
	}
	i := filetimeTicks(it)
	return i, filetimeTicks(kt) + filetimeTicks(ut), nil
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

var (
	modkernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = modkernel32.NewProc("GlobalMemoryStatusEx")
	procGetSystemTimes       = modkernel32.NewProc("GetSystemTimes")
)

func readMemory() memoryInfo {
	var st memoryStatusEx
	st.Length = uint32(unsafe.Sizeof(st))
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&st)))
	if r == 0 {
		return memoryInfo{}
	}
	m := memoryInfo{
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
func readDisk(path string) diskInfo {
	vol := filepath.VolumeName(path)
	if vol == "" {
		vol = "C:"
	}
	p, err := windows.UTF16PtrFromString(vol + `\`)
	if err != nil {
		return diskInfo{}
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return diskInfo{}
	}
	d := diskInfo{TotalGB: total / (1 << 30), FreeGB: totalFree / (1 << 30)}
	if d.TotalGB > 0 {
		d.UsedGB = d.TotalGB - d.FreeGB
		d.UsedPct = int(d.UsedGB * 100 / d.TotalGB)
	}
	return d
}
