//go:build linux

package sysmon

import (
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

const (
	defaultDiskPath = "/"
	procSupported   = true
)

// pageKB 一页内存多少 KB(/proc/<pid>/stat 里的 rss 以页计)。
var pageKB = uint64(syscall.Getpagesize()) / 1024

// cores 核数不会变,数一次就够。
var cores = sync.OnceValue(func() int {
	// 读 /proc/cpuinfo 数 processor 行,避免 sysconf(不可移植)。
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
})

func readLoad() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	f, _ := strconv.ParseFloat(fields[0], 64)
	return f
}

// readCPUTimes 取 /proc/stat 首行的累计 jiffies。
// 字段:user nice system idle iowait irq softirq steal guest guest_nice。
// idle+iowait 算空闲(等 IO 时 CPU 确实没在干活),其余算忙。
func readCPUTimes() cpuTimes {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuTimes{}
	}
	line, _, _ := strings.Cut(string(b), "\n")
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuTimes{}
	}
	var total, idle uint64
	for i, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			continue
		}
		// guest/guest_nice(第 9、10 项)已经计入 user/nice,再加一遍会重复计数。
		if i >= 8 {
			continue
		}
		total += v
		if i == 3 || i == 4 {
			idle += v
		}
	}
	if total == 0 {
		return cpuTimes{}
	}
	return cpuTimes{busy: total - idle, total: total, ok: true}
}

// readMemory 一次 /proc/meminfo 同时得出物理内存与 swap —— 两组数就在同一个文件里,
// 分两次读只是多一次系统调用外加两份可能不一致的快照。
func readMemory() (Memory, Swap) {
	var m Memory
	var s Swap
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return m, s
	}
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
		case "SwapTotal:":
			s.TotalMB = val / 1024
		case "SwapFree:":
			s.FreeMB = val / 1024
		}
	}
	m.FreeMB, m.UsedMB, m.UsedPct = memUsage(m.TotalMB, m.FreeMB)
	s.FreeMB, s.UsedMB, s.UsedPct = memUsage(s.TotalMB, s.FreeMB)
	return m, s
}

func readDisk(path string) Disk {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Disk{}
	}
	total := st.Blocks * uint64(st.Bsize) / (1 << 30)
	free := st.Bavail * uint64(st.Bsize) / (1 << 30)
	d := Disk{TotalGB: total, FreeGB: free}
	if total > 0 {
		d.UsedGB = total - free
		d.UsedPct = int(d.UsedGB * 100 / total)
	}
	return d
}

// readProcs 遍历 /proc,每个进程只读一个 stat 文件。命令行、用户名留给 enrich ——
// 那是每个进程各一次额外读盘,只有最后要显示的那几十条值得做。
func readProcs() []rawProc {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	out := make([]rawProc, 0, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if p, ok := readProcStat(pid); ok {
			out = append(out, p)
		}
	}
	return out
}

// readProcStat 解析 /proc/<pid>/stat。字段位置见 proc(5);comm 被括号裹着且允许
// 含空格和括号("(Web Content)"),所以按最后一个 ')' 切,不能整行 Fields。
func readProcStat(pid int) (rawProc, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		// 进程在遍历期间退出了,跳过就好。
		return rawProc{}, false
	}
	line := string(b)
	open := strings.IndexByte(line, '(')
	closing := strings.LastIndexByte(line, ')')
	if open < 0 || closing < open+1 || closing+2 >= len(line) {
		return rawProc{}, false
	}
	p := rawProc{pid: pid, name: line[open+1 : closing]}
	// 切完从 state(第 3 个字段)开始,所以 f[i] 对应 proc(5) 里的第 i+3 项。
	f := strings.Fields(line[closing+2:])
	if len(f) < 22 {
		return rawProc{}, false
	}
	p.state = f[0]
	p.ppid, _ = strconv.Atoi(f[1])
	utime, _ := strconv.ParseUint(f[11], 10, 64)
	stime, _ := strconv.ParseUint(f[12], 10, 64)
	p.ticks = utime + stime
	p.threads, _ = strconv.Atoi(f[17])
	p.start, _ = strconv.ParseUint(f[19], 10, 64)
	pages, _ := strconv.ParseUint(f[21], 10, 64)
	p.rssKB = pages * pageKB
	return p, true
}

// enrich 补全要显示的那几条:完整命令行 + 属主,顺带用 argv[0] 纠正名字。
func enrich(p *Process) {
	dir := "/proc/" + strconv.Itoa(p.PID)
	if b, err := os.ReadFile(dir + "/cmdline"); err == nil && len(b) > 0 {
		// cmdline 用 NUL 分隔参数,末尾还有一个。
		args := strings.TrimRight(string(b), "\x00")
		p.Cmd = strings.TrimSpace(strings.ReplaceAll(args, "\x00", " "))
		argv0, _, _ := strings.Cut(args, "\x00")
		p.Name = procName(p.Name, argv0)
	}
	if p.Cmd == "" {
		// 内核线程没有命令行,按 top 的习惯用方括号标出来。
		p.Cmd = "[" + p.Name + "]"
	}
	if fi, err := os.Stat(dir); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			p.User = userName(st.Uid)
		}
	}
}

// procName 定下进程表里显示的名字。
//
// stat 里的 comm 是**主线程**的名字,而线程名进程自己想怎么改就怎么改:Node.js 把主
// 线程叫 MainThread,于是一排 node 程序(vite、codex……)在进程表里全叫 MainThread,
// 谁是谁完全看不出来。这种对不上的情况就改用 argv[0] 的基名。
//
// 反过来,comm 出现在 argv[0] 里时保留 comm:那要么是被 comm 的 15 字上限截断
// (systemd-journal ← systemd-journald),要么是 argv[0] 自己带了前缀后缀或干脆被改写
// ("-bash" 表示登录 shell、"@dbus-daemon" 是 systemd 的写法、"sshd: root@pts/0" 是
// 服务自己写的状态串),这些情况 comm 反而更干净。注意要拿整个 argv[0] 去比而不是它
// 的基名 —— 状态串里也可能有斜杠,按基名切会剩下 "0" 这种碎片。
func procName(comm, argv0 string) string {
	base := argv0
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if comm == "" {
		return base
	}
	if base == "" || strings.Contains(argv0, comm) {
		return comm
	}
	return base
}

// userName uid → 用户名。同一批进程里 uid 高度重复,查一次记住。
var (
	userMu    sync.Mutex
	userCache = map[uint32]string{}
)

func userName(uid uint32) string {
	userMu.Lock()
	defer userMu.Unlock()
	if n, ok := userCache[uid]; ok {
		return n
	}
	name := strconv.FormatUint(uint64(uid), 10)
	if u, err := user.LookupId(name); err == nil && u.Username != "" {
		name = u.Username
	}
	userCache[uid] = name
	return name
}
