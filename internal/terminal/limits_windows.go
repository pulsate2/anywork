//go:build windows

package terminal

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobCPURateControl JOBOBJECT_CPU_RATE_CONTROL_INFORMATION(x/sys/windows 没导出)。
// 结构末尾在 C 里是个联合(CpuRate / Weight / MinRate+MaxRate),这里按 HARD_CAP
// 的用法只取 CpuRate。
type jobCPURateControl struct {
	ControlFlags uint32
	CpuRate      uint32
}

const (
	jobCPURateEnable  = 0x1
	jobCPURateHardCap = 0x4
)

// limitSupport Job 对象是 Windows 自带的,不需要额外权限。CPU 硬上限要
// Windows 8 / Server 2012 以上,老系统上那次 SetInformationJobObject 会失败,
// 由 applied() 如实回报,不假装限住了。
func limitSupport() Support {
	return Support{
		Memory: true,
		CPU:    true,
		Mode:   "job",
		Cores:  runtime.NumCPU(),
		Detail: "Windows Job 对象:内存按整个会话(shell 及其子进程)合计计算,超出后申请内存的调用失败;" +
			"CPU 上限按整机百分比硬性封顶(需 Windows 8 / Server 2012 以上)。",
	}
}

// jobLimiter 一个会话一个 Job 对象。
type jobLimiter struct {
	job windows.Handle
	got Limits
}

func newLimiter(_ string, l Limits) (limiter, error) {
	if l.isZero() {
		return nil, nil
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("创建 Job 对象失败: %w", err)
	}
	h := &jobLimiter{job: job}

	// KILL_ON_JOB_CLOSE:release() 关掉句柄时,组里赖着不走的子进程一并带走,
	// 免得限额没了进程还在。内存上限只在用户填了的时候加。
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if l.MemoryMB > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_JOB_MEMORY
		info.JobMemoryLimit = memBytes(l.MemoryMB)
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err == nil {
		h.got.MemoryMB = l.MemoryMB
	}

	if l.CPUPercent > 0 {
		// CpuRate 的单位是万分之一整机 CPU,所以百分数要乘 100。
		rc := jobCPURateControl{
			ControlFlags: jobCPURateEnable | jobCPURateHardCap,
			CpuRate:      uint32(l.CPUPercent) * 100,
		}
		if _, err := windows.SetInformationJobObject(job, windows.JobObjectCpuRateControlInformation,
			uintptr(unsafe.Pointer(&rc)), uint32(unsafe.Sizeof(rc))); err == nil {
			h.got.CPUPercent = l.CPUPercent
		}
	}

	if h.got.isZero() {
		windows.CloseHandle(job)
		return nil, errors.New("Job 对象限额设置失败")
	}
	return h, nil
}

// memBytes MB → 字节,顺手挡住 32 位下的溢出。
func memBytes(mb int) uintptr {
	b := uint64(mb) << 20
	if max := uint64(^uintptr(0)); b > max {
		b = max
	}
	return uintptr(b)
}

// wrap Windows 没有"exec 前先入组"的手段(ConPTY 起进程,拿不到 CREATE_SUSPENDED),
// 原样返回,靠 attach 兜。
func (h *jobLimiter) wrap(shell string) (string, []string) { return shell, nil }

func (h *jobLimiter) attach(pid int) error {
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ph)
	return windows.AssignProcessToJobObject(h.job, ph)
}

func (h *jobLimiter) release() { windows.CloseHandle(h.job) }

func (h *jobLimiter) applied() (Limits, string) { return h.got, "job" }
