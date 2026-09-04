//go:build linux

package terminal

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// cgroupPeriod cpu.max 的周期(µs),用内核默认的 100ms。
	cgroupPeriod = 100000
	// cgroupPrefix 会话子组目录前缀。带前缀是为了下次启动能认出上次留下的目录并清掉。
	cgroupPrefix = "lr-term-"
	// cgroupHolder 专门用来装会话子组的中间目录。会话不直接建在分配者名下:
	// 分配者可能是别人的地盘(systemd 的 slice),留一个目录比撒一堆干净。
	// 不用 .slice 后缀 —— 那是 systemd 的单元命名,它会当成自己的东西去管。
	cgroupHolder = "lr-terminals"
	// cgroupLeaf 腾空本进程所在组时,自己搬进去的叶子组名。
	cgroupLeaf = "lr-main"
)

// cgroupEnv 本进程所处的 cgroup v2 环境。只读探测,没有副作用。
type cgroupEnv struct {
	mount  string // cgroup2 层级的挂载点
	dir    string // 本进程 cgroup 的绝对路径
	isRoot bool   // 是否就是(命名空间的)根组 —— 根组不受"无内部进程"规则约束
	memory bool   // 能给子组分配 memory(本组已有,或父组手里有、可以让它下放)
	cpu    bool   // ... 分配 cpu
	err    string // 不可用的原因,直接给用户看
}

var cgroupProbe = sync.OnceValue(func() cgroupEnv {
	mount := cgroup2Mount()
	if mount == "" {
		return cgroupEnv{err: "内核未挂载 cgroup v2(unified)层级"}
	}
	rel := cgroupSelfPath()
	dir := filepath.Join(mount, filepath.FromSlash(strings.TrimPrefix(rel, "/")))
	if _, err := os.Stat(dir); err != nil {
		// cgroup 命名空间下 /proc/self/cgroup 给的路径可能和挂载点对不上,退回挂载点本身。
		dir, rel = mount, "/"
	}
	env := cgroupEnv{mount: mount, dir: dir, isRoot: rel == "" || rel == "/"}
	own, err := controllers(dir)
	if err != nil {
		env.err = "读不到 cgroup.controllers: " + err.Error()
		return env
	}
	env.memory, env.cpu = own["memory"], own["cpu"]
	// 本组缺的控制器,父组手里可能有、只是没往下放(systemd 默认不开 cpu 就是这样)。
	// 我们能写父组的 subtree_control 时它就算可用,真正的下放留到 cgroupBase 再做。
	if p := parentDir(env); p != "" && (!env.memory || !env.cpu) {
		if up, err := controllers(p); err == nil {
			env.memory, env.cpu = env.memory || up["memory"], env.cpu || up["cpu"]
		}
	}
	if !env.memory && !env.cpu {
		env.err = "当前 cgroup 未获得 memory/cpu 控制器"
		return env
	}
	// 会话子组要么建在父组名下,要么建在本组名下,有一处能写就够了。
	if err := writableDir(dir); err != nil {
		p := parentDir(env)
		if p == "" {
			env.err = "cgroup 目录不可写(" + err.Error() + ")"
			return env
		}
		if perr := writableDir(p); perr != nil {
			env.err = "cgroup 目录不可写(" + perr.Error() + ")"
		}
	}
	return env
})

// controllers 读一个组的 cgroup.controllers —— 即"这个组能给它的子组分配什么"。
func controllers(dir string) (map[string]bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, "cgroup.controllers"))
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, 4)
	for _, c := range strings.Fields(string(b)) {
		set[c] = true
	}
	return set, nil
}

// parentDir 本组的上一级,没有(已经是根 / 到了挂载点)时返回空。只往上找一级:
// 再往上就是别人的资源边界了,不该动。
func parentDir(env cgroupEnv) string {
	if env.isRoot || env.dir == "" || env.dir == env.mount {
		return ""
	}
	p := filepath.Dir(env.dir)
	if p == env.dir || !strings.HasPrefix(p, env.mount) {
		return ""
	}
	return p
}

// cgroup2Mount 从 mountinfo 找 cgroup2 的挂载点。混合模式下它在
// /sys/fs/cgroup/unified,不能写死。
func cgroup2Mount() string {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		// 格式:<id> <parent> <maj:min> <root> <挂载点> <选项> ... - <类型> <源> <超级块选项>
		left, right, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		if f := strings.Fields(right); len(f) == 0 || f[0] != "cgroup2" {
			continue
		}
		if f := strings.Fields(left); len(f) >= 5 {
			// 挂载点里的空格等字符是 \040 这种八进制转义,cgroup 路径不会有,原样用。
			return f[4]
		}
	}
	return ""
}

// cgroupSelfPath /proc/self/cgroup 里 v2 那行:"0::/relative/path"。
func cgroupSelfPath() string {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "/"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			return strings.TrimSpace(p)
		}
	}
	return "/"
}

// writableDir 探目录是否真能写。root 下 unix 权限位不作数(想写就能写),
// 但只读挂载(容器里 /sys/fs/cgroup 常是 ro)会挡下来,必须实测。
func writableDir(dir string) error {
	name := filepath.Join(dir, cgroupPrefix+"probe")
	if err := os.Mkdir(name, 0o755); err != nil {
		if os.IsExist(err) {
			os.Remove(name)
			return nil
		}
		return err
	}
	os.Remove(name)
	return nil
}

// cgroupBase 惰性准备一个"能给子组分配资源"的目录,会话子组都建在它下面。
// 放在第一次真正要用限制时做:不用这个功能的部署不该被搬来搬去。
var cgroupBase = sync.OnceValues(func() (string, error) {
	env := cgroupProbe()
	if env.err != "" {
		return "", errors.New(env.err)
	}
	// 首选让父组来分配:slice 这类中间组本来就不放进程,把会话建成自己的兄弟,
	// 谁都不用搬。父组里有进程的话下面这步会被内核拒掉,再退回搬自己。
	//
	// 为什么不直接在本组上开 subtree_control:本组是 systemd 给这个服务建的,
	// 一旦它的 subtree_control 非空,内核就不再允许往里放进程,systemd 之后的
	// ExecReload/ExecStop 就进不来了。代价是会话组成了本服务的兄弟,不在本服务
	// 自己那份限额之内 —— 每个会话仍有自己的上限,只是总量不再受服务限额约束。
	if p := parentDir(env); p != "" {
		if base, err := prepareBase(p, env); err == nil {
			return base, nil
		}
	}
	if !env.isRoot {
		// cgroup v2 的"无内部进程"规则:非根组一旦有成员进程,就不能再给子组下放
		// 控制器。把本组里现有的进程(我们自己,以及已经开着的会话)挪进一个叶子
		// 组腾空 —— 限制是继承的,挪完它们该受的约束一点没变。
		if err := vacate(env.dir); err != nil {
			return "", err
		}
	}
	return prepareBase(env.dir, env)
})

// prepareBase 让 dist 把控制器下放下来,并在它名下建好装会话的目录。
func prepareBase(dist string, env cgroupEnv) (string, error) {
	if err := delegate(dist, env); err != nil {
		return "", err
	}
	base := filepath.Join(dist, cgroupHolder)
	if err := os.Mkdir(base, 0o755); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("创建 %s 失败: %w", cgroupHolder, err)
	}
	// base 里不放进程,所以它一定能继续往下放。
	if err := delegate(base, env); err != nil {
		os.Remove(base)
		return "", err
	}
	cgroupGC(base)
	return base, nil
}

// delegate 把需要的控制器写进 dir 的 subtree_control。分开写:内核少一个控制器时
// 不影响另一个可用。一个都没写进去才算失败。
func delegate(dir string, env cgroupEnv) error {
	file := filepath.Join(dir, "cgroup.subtree_control")
	var ok bool
	var last error
	for _, c := range []struct {
		want bool
		name string
	}{{env.memory, "+memory"}, {env.cpu, "+cpu"}} {
		if !c.want {
			continue
		}
		// 已经开着的再写一次也是成功,不用先读一遍。
		if err := os.WriteFile(file, []byte(c.name), 0); err == nil {
			ok = true
		} else {
			last = err
		}
	}
	if !ok {
		return fmt.Errorf("无法在 %s 下放 cgroup 控制器: %w", dir, last)
	}
	return nil
}

// vacate 把 dir 里的进程全部挪进 dir/<cgroupLeaf>。
func vacate(dir string) error {
	leaf := filepath.Join(dir, cgroupLeaf)
	if err := os.Mkdir(leaf, 0o755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("创建 %s 失败: %w", cgroupLeaf, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return err
	}
	target := filepath.Join(leaf, "cgroup.procs")
	for _, pid := range strings.Fields(string(b)) {
		// 逐个写:cgroup.procs 一次只收一个 pid。中途退出的进程会报 ESRCH,跳过。
		_ = os.WriteFile(target, []byte(pid), 0)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "cgroup.procs")); err == nil && len(strings.Fields(string(b))) > 0 {
		return errors.New("当前 cgroup 里有挪不走的进程,无法给子组分配资源")
	}
	return nil
}

// cgroupGC 清掉上次运行留下的会话子组。只有空目录能删掉,里面还有进程的会失败,
// 正好不用自己判断。
func cgroupGC(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), cgroupPrefix) {
			os.Remove(filepath.Join(base, e.Name()))
		}
	}
}

// shPath 找一个 POSIX shell 用来做 exec 前的包装。
var shPath = sync.OnceValue(func() string {
	if fi, err := os.Stat("/bin/sh"); err == nil && !fi.IsDir() {
		return "/bin/sh"
	}
	if p, err := exec.LookPath("sh"); err == nil {
		return p
	}
	return ""
})

// limitCores 换算"占整机 %"时用的核数。本进程已被上层 cgroup 限了 quota 时
// (容器里很常见)整机核数会高估:32 核宿主上只分到 2 核,填 50% 若按 32 核算出
// 16 核的 quota,父组卡在 2 核,等于没限制。quota 通常不在本组而在上面某一级
// (docker 的 scope),所以要一路往上取最紧的那个。
var limitCores = sync.OnceValue(func() int {
	n := runtime.NumCPU()
	env := cgroupProbe()
	if env.dir == "" || env.mount == "" {
		return n
	}
	for d := env.dir; strings.HasPrefix(d, env.mount); d = filepath.Dir(d) {
		if c := quotaCores(d); c > 0 && c < n {
			n = c
		}
		if d == env.mount {
			break
		}
	}
	return n
})

// quotaCores 一个组的 cpu.max 折算成几个核,没设上限返回 0。
func quotaCores(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, "cpu.max"))
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) != 2 || f[0] == "max" {
		return 0
	}
	quota, err1 := strconv.Atoi(f[0])
	period, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil || quota <= 0 || period <= 0 {
		return 0
	}
	if c := quota / period; c >= 1 {
		return c
	}
	return 1 // 不到一个核也按 1 算,否则百分比全被抹成 0
}

func limitSupport() Support {
	s := Support{Cores: limitCores()}
	env := cgroupProbe()
	switch {
	case env.err == "":
		s.Mode, s.Memory, s.CPU = "cgroup2", env.memory, env.cpu
		s.Detail = "cgroup v2:内存超限的进程由内核终止(OOM),CPU 上限按整机百分比折算成配额。"
		if !env.memory || !env.cpu {
			miss := "cpu"
			if !env.memory {
				miss = "memory"
			}
			s.Detail = "cgroup v2 可用,但本机没有 " + miss + " 控制器,该项限制不可用。"
		}
	case shPath() != "":
		s.Mode, s.Memory = "rlimit", true
		s.Detail = "无法使用 cgroup(" + env.err + "),退化为 ulimit -v:限的是虚拟地址空间," +
			"Node/Java 这类一上来就预留大片地址的程序可能起不来;CPU 限制不可用。"
	default:
		s.Mode = "none"
		s.Detail = "本机不支持资源限制:" + env.err
	}
	return s
}

// newLimiter 按 l 建一份限制。l 为零值返回 (nil, nil) —— 不限制就什么都不做。
func newLimiter(id string, l Limits) (limiter, error) {
	if l.isZero() {
		return nil, nil
	}
	base, err := cgroupBase()
	if err == nil {
		h, cerr := newCgroupLimiter(base, id, l)
		if cerr == nil {
			return h, nil
		}
		err = cerr
	}
	// 只有内存能兜底:RLIMIT_AS 没有"CPU 百分比"这种对应物。
	if l.CPUPercent <= 0 && l.MemoryMB > 0 && shPath() != "" {
		return &rlimitLimiter{memMB: l.MemoryMB}, nil
	}
	return nil, fmt.Errorf("无法应用资源限制: %w", err)
}

// cgroupLimiter 一个会话一个 cgroup 子组。
type cgroupLimiter struct {
	dir     string
	got     Limits
	preExec bool
}

func newCgroupLimiter(base, id string, l Limits) (*cgroupLimiter, error) {
	dir := filepath.Join(base, cgroupPrefix+id)
	if err := os.Mkdir(dir, 0o755); err != nil && !os.IsExist(err) {
		return nil, err
	}
	h := &cgroupLimiter{dir: dir}
	if l.MemoryMB > 0 {
		if err := h.write("memory.max", strconv.Itoa(l.MemoryMB<<20)); err == nil {
			h.got.MemoryMB = l.MemoryMB
			// 不禁 swap 的话超限的进程会被换出去而不是被拦住,上限就形同虚设。
			// 没有 swap 控制器时这一步失败,不影响 memory.max 本身。
			_ = h.write("memory.swap.max", "0")
		}
	}
	if l.CPUPercent > 0 {
		quota := cgroupPeriod * limitCores() * l.CPUPercent / 100
		if quota < 1000 {
			quota = 1000 // 内核允许的最小配额
		}
		if err := h.write("cpu.max", strconv.Itoa(quota)+" "+strconv.Itoa(cgroupPeriod)); err == nil {
			h.got.CPUPercent = l.CPUPercent
		}
	}
	if h.got.isZero() {
		os.Remove(dir)
		return nil, errors.New("cgroup 限额文件写入失败")
	}
	return h, nil
}

func (h *cgroupLimiter) write(name, val string) error {
	return os.WriteFile(filepath.Join(h.dir, name), []byte(val), 0)
}

// wrap 让 shell 在 exec 之前先把自己写进子组。$$ 是这个 sh 的 pid,exec 之后
// shell 沿用同一个 pid,所以 shell 从第一条命令起就在组里 —— 启动后再挪的话,
// rc 文件里拉起的后台进程可能已经跑在组外了。
func (h *cgroupLimiter) wrap(shell string) (string, []string) {
	sh := shPath()
	if sh == "" {
		return shell, nil
	}
	h.preExec = true
	script := "echo $$ > " + shQuote(filepath.Join(h.dir, "cgroup.procs")) +
		` 2>/dev/null || { echo '资源限制未能生效,已中止会话' >&2; exit 1; }; exec "$0"`
	return sh, []string{"-c", script, shell}
}

func (h *cgroupLimiter) attach(pid int) error {
	if h.preExec {
		return nil
	}
	return h.write("cgroup.procs", strconv.Itoa(pid))
}

// release 组里最后一个进程消失后才删得掉,子进程可能还在收尾,退避重试。
func (h *cgroupLimiter) release() {
	go func() {
		for i := range 12 {
			if err := os.Remove(h.dir); err == nil || os.IsNotExist(err) {
				return
			}
			time.Sleep(time.Duration(i+1) * 200 * time.Millisecond)
		}
		// 还删不掉说明里头有赖着不走的进程(nohup 之类)。留着不动:
		// 等它们退了,下次启动时 cgroupGC 会把空目录收走。
	}()
}

func (h *cgroupLimiter) applied() (Limits, string) { return h.got, "cgroup2" }

// rlimitLimiter 没有 cgroup 时的内存兜底。ulimit -v 限的是虚拟地址空间,
// 不等于 RSS,但至少能挡住失控增长。
type rlimitLimiter struct{ memMB int }

func (h *rlimitLimiter) wrap(shell string) (string, []string) {
	return shPath(), []string{"-c", "ulimit -v " + strconv.Itoa(h.memMB*1024) + " 2>/dev/null; exec \"$0\"", shell}
}

func (h *rlimitLimiter) attach(int) error          { return nil }
func (h *rlimitLimiter) release()                  {}
func (h *rlimitLimiter) applied() (Limits, string) { return Limits{MemoryMB: h.memMB}, "rlimit" }

// shQuote 单引号包起来给 sh -c 用,内部的单引号按 '\” 拆开。
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
