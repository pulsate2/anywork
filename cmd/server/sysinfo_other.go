//go:build !linux && !windows

package main

// 其余平台(darwin/bsd)暂未实现采集,返回空值 + Percent -1 表示不可用。
func (a *App) readSysInfo() sysInfo {
	return sysInfo{CPU: cpuInfo{Percent: -1}}
}
