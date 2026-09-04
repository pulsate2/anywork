//go:build !linux && !windows

package sysmon

import "runtime"

// 其余平台(darwin/bsd 等)没有实现采集:内存/磁盘返回 0,CPU 百分比由上层给出 -1,
// 进程列表标记为不支持,前端据此提示"当前平台不支持"而不是显示一堆空数据。
const (
	defaultDiskPath = "/"
	procSupported   = false
)

func readLoad() float64      { return 0 }
func cores() int             { return runtime.NumCPU() }
func readCPUTimes() cpuTimes { return cpuTimes{} }
func readMemory() Memory     { return Memory{} }
func readDisk(string) Disk   { return Disk{} }
func readProcs() []rawProc   { return nil }
func enrich(p *Process)      { _ = p }
