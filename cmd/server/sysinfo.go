package main

// sysinfo 的数据形状与平台无关;采集实现按平台分文件(sysinfo_linux/windows/other)。

type sysInfo struct {
	CPU    cpuInfo    `json:"cpu"`
	Memory memoryInfo `json:"memory"`
	Disk   diskInfo   `json:"disk"`
}

type cpuInfo struct {
	// Load 1 分钟平均负载(Windows 无此概念,恒为 0)。
	Load float64 `json:"load"`
	// Cores CPU 核数。
	Cores int `json:"cores"`
	// Percent CPU 使用率(0-100);-1 表示取不到。
	Percent float64 `json:"percent"`
}

type memoryInfo struct {
	TotalMB uint64 `json:"totalMB"`
	UsedMB  uint64 `json:"usedMB"`
	FreeMB  uint64 `json:"freeMB"`
	UsedPct int    `json:"usedPct"`
}

type diskInfo struct {
	TotalGB uint64 `json:"totalGB"`
	UsedGB  uint64 `json:"usedGB"`
	FreeGB  uint64 `json:"freeGB"`
	UsedPct int    `json:"usedPct"`
}
