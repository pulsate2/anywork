//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

func (a *App) readSysInfo() sysInfo {
	info := sysInfo{
		CPU:    readCPU(),
		Memory: readMemory(),
		Disk:   readDisk("/"),
	}
	return info
}

func readCPU() cpuInfo {
	info := cpuInfo{Percent: -1}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		f, _ := strconv.ParseFloat(strings.Fields(string(b))[0], 64)
		info.Load = f
	}
	info.Cores = sysconfNProcessors()
	if info.Cores > 0 {
		info.Percent = info.Load / float64(info.Cores) * 100
	}
	return info
}

func sysconfNProcessors() int {
	// 读 /proc/cpuinfo 数 processor 行,避免 syscall.Getconf(不可移植)。
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "processor") {
			n++
		}
	}
	return n
}

func readMemory() memoryInfo {
	var m memoryInfo
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			val, _ := strconv.ParseUint(fields[1], 10, 64)
			switch fields[0] {
			case "MemTotal:":
				m.TotalMB = val / 1024
			case "MemAvailable:":
				m.FreeMB = val / 1024
			}
		}
	}
	if m.TotalMB > 0 {
		if m.FreeMB >= m.TotalMB {
			m.FreeMB = m.TotalMB
		}
		m.UsedMB = m.TotalMB - m.FreeMB
		m.UsedPct = int(m.UsedMB * 100 / m.TotalMB)
	}
	return m
}

func readDisk(path string) diskInfo {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return diskInfo{}
	}
	total := st.Blocks * uint64(st.Bsize) / (1 << 30)
	free := st.Bavail * uint64(st.Bsize) / (1 << 30)
	d := diskInfo{TotalGB: total, FreeGB: free}
	if total > 0 {
		d.UsedGB = total - free
		d.UsedPct = int(d.UsedGB * 100 / total)
	}
	return d
}
