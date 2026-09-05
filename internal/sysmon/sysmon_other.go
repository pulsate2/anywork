//go:build !linux && !windows

package sysmon

import "runtime"

// 其余平台(darwin/bsd 等)没有实现采集:内存/Swap/磁盘返回 0,CPU 百分比由上层给出 -1,
// 进程列表标记为不支持,前端据此提示"当前平台不支持"而不是显示一堆空数据。
const (
	defaultDiskPath = "/"
	procSupported   = false
)

func readLoad() float64          { return 0 }
func cores() int                 { return runtime.NumCPU() }
func readCPUTimes() cpuTimes     { return cpuTimes{} }
func readMemory() (Memory, Swap) { return Memory{}, Swap{} }
func readDisk(string) Disk       { return Disk{} }
func readProcs() []rawProc       { return nil }
func enrich(p *Process)          { _ = p }

// killProc 采集不到进程列表,也就没有"结束进程"可言。Monitor.Kill 在 procSupported
// 为 false 时就已经返回了,这里只是让各平台的函数集齐。
func killProc(int, bool) error { return ErrKillUnsupported }
