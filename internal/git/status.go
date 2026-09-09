package git

import (
	"strconv"
	"strings"
)

// StatusEntry 一条 git status 记录。Add/Del 是该文件在所属那一侧的增/删行数
// (numstat,-1 表示二进制或统计不到,前端不显示);同一文件两侧各有一份,数字只在自己
// 那一侧有意义:暂存侧是已暂存的改动量,工作区侧是暂存之后又改的量。
type StatusEntry struct {
	Raw  string `json:"raw"`
	X    string `json:"x"` // 索引状态
	Y    string `json:"y"` // 工作区状态
	Path string `json:"path"`
	Orig string `json:"orig,omitempty"` // 重命名/复制原路径
	Kind string `json:"kind"`           // staged | unstaged | untracked | ignored
	Add  int    `json:"add,omitempty"`  // 新增行数(numstat)
	Del  int    `json:"del,omitempty"`  // 删除行数(numstat)
}

// Status 分组后的状态。
type Status struct {
	Branch     string        `json:"branch"`
	Upstream   string        `json:"upstream,omitempty"`
	Ahead      int           `json:"ahead"`
	Behind     int           `json:"behind"`
	Staged     []StatusEntry `json:"staged"`
	Unstaged   []StatusEntry `json:"unstaged"`
	Untracked  []StatusEntry `json:"untracked"`
	Conflicted []StatusEntry `json:"conflicted"`
	Clean      bool          `json:"clean"`
	Initial    bool          `json:"initial"`
	Detached   bool          `json:"detached"`
	// Reverting 表示有一次 revert 卡在冲突里(REVERT_HEAD 还在),前端据此给出"放弃回滚"。
	Reverting bool `json:"reverting"`
}

// Status 返回当前仓库的状态(porcelain=v1 -b -uall)。
// -uall:未跟踪目录逐文件列出,而不是折叠成一条目录记录 —— 前端要按文件
// 暂存/删除/进二级页,折叠形态对不上。
func (s *Service) Status(p string) (Status, error) {
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return Status{}, err
	}
	if !info.Repo {
		return Status{}, ErrNotRepo
	}
	out, err := s.run(info.Root, nil, "status", "--porcelain=v1", "-b", "-uall")
	if err != nil {
		return Status{}, err
	}
	st := Status{Staged: []StatusEntry{}, Unstaged: []StatusEntry{},
		Untracked: []StatusEntry{}, Conflicted: []StatusEntry{}}
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		l = strings.TrimRight(l, "\r")
		if l == "" {
			continue
		}
		if i == 0 && strings.HasPrefix(l, "## ") {
			parseBranchLine(&st, l)
			continue
		}
		ent := parsePorcelain(l)
		classify(&st, ent)
	}
	if len(st.Staged) == 0 && len(st.Unstaged) == 0 && len(st.Untracked) == 0 && len(st.Conflicted) == 0 {
		st.Clean = true
	}
	// revert 成功会自动提交并清掉 REVERT_HEAD;还在说明中途冲突了。
	if _, err := s.run(info.Root, nil, "rev-parse", "--verify", "-q", "REVERT_HEAD"); err == nil {
		st.Reverting = true
	}
	s.fillNumstat(info.Root, &st)
	return st, nil
}

// fillNumstat 给每条状态记录补增删行数。两次 numstat 对应两个比较面:
// --cached 是 HEAD→索引(暂存侧),不带参数是索引→工作区(工作区侧)。
// 数字挂到 Path 上,两侧各查各的;同一次 mv 改名两侧都记 Path=新路径,取得到就对得上。
// 未跟踪文件 numstat 看不见,保持 0;冲突文件 numstat 不出数,也保持 0。
func (s *Service) fillNumstat(root string, st *Status) {
	// numstat 的 adds/dels 是 Go 保留不了 "-" 的 int,先用 string 收再转。
	fill := func(side map[string][2]string) func(StatusEntry) StatusEntry {
		return func(e StatusEntry) StatusEntry {
			if v, ok := side[e.Path]; ok {
				e.Add, e.Del = atoiOrMinus1(v[0]), atoiOrMinus1(v[1])
			}
			return e
		}
	}
	sided := func(entries []StatusEntry, side map[string][2]string) {
		for i := range entries {
			entries[i] = fill(side)(entries[i])
		}
	}
	staged := s.numstatSide(root, "--cached")
	unstaged := s.numstatSide(root)
	sided(st.Staged, staged)
	sided(st.Unstaged, unstaged)
	sided(st.Conflicted, unstaged) // 冲突行也能在工作区侧拿到数字(以冲突标记计)
}

// numstatSide 跑一次 git diff --numstat -z,把输出按 NUL 切成 path→[adds,dels]。
// 普通记录是一个完整段 `adds\tdels\tpath`;重命名记录被拆成三段:计数段(以 \t 结尾、
// 路径位为空)→ 原路径段 → 新路径段。取新路径当 key —— status 的 Path 记的也是
// 新路径,对得上。二进制文件计数是 `-`,atoiOrMinus1 会归成 -1。
// 解析不了的段跳过,顶多少个数字,不至于让整个状态接口挂掉。
func (s *Service) numstatSide(root string, extra ...string) map[string][2]string {
	args := append([]string{"diff", "--numstat", "-z"}, extra...)
	out, err := s.run(root, nil, args...)
	if err != nil {
		return nil
	}
	m := map[string][2]string{}
	fields := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	for i := 0; i < len(fields); i++ {
		parts := strings.SplitN(fields[i], "\t", 3)
		if len(parts) < 3 {
			continue
		}
		if parts[2] != "" {
			m[parts[2]] = [2]string{parts[0], parts[1]}
			continue
		}
		// 重命名:后面两段是 原路径/新路径,数字挂到新路径上。
		if i+2 < len(fields) {
			m[fields[i+2]] = [2]string{parts[0], parts[1]}
			i += 2
		}
	}
	return m
}

func atoiOrMinus1(s string) int {
	if s == "-" {
		return -1
	}
	n, _ := strconv.Atoi(s)
	return n
}

func parseBranchLine(st *Status, l string) {
	rest := strings.TrimPrefix(l, "## ")
	// `branch...upstream [ahead N, behind M]` 或 `No commits yet on branch`
	if strings.Contains(rest, "No commits yet ") {
		st.Initial = true
		st.Branch = strings.TrimPrefix(rest, "No commits yet on ")
		return
	}
	name := rest
	if i := strings.Index(rest, "..."); i >= 0 {
		name = rest[:i]
		rest = rest[i+3:]
		if j := strings.Index(rest, " "); j >= 0 {
			st.Upstream = rest[:j]
			meta := rest[j+1:]
			if strings.Contains(meta, "ahead") {
				st.Ahead = grabInt(meta, "ahead")
			}
			if strings.Contains(meta, "behind") {
				st.Behind = grabInt(meta, "behind")
			}
		} else {
			st.Upstream = rest
		}
	}
	if strings.HasPrefix(name, "HEAD (") {
		st.Detached = true
		st.Branch = name
	} else {
		st.Branch = name
	}
}

// parsePorcelain 解析 `XY path` 或 `XY path -> orig` 一行。
func parsePorcelain(l string) StatusEntry {
	ent := StatusEntry{Raw: l}
	if len(l) < 2 {
		return ent
	}
	ent.X = l[0:1]
	ent.Y = l[1:2]
	rest := strings.TrimSpace(l[2:])
	// 重命名/复制:porcelain v1 输出 `ORIG -> NEW`(v2 才是新路径在前)。
	if arrow := strings.LastIndex(rest, " -> "); arrow >= 0 {
		ent.Orig = unquotePath(rest[:arrow])
		ent.Path = unquotePath(rest[arrow+4:])
	} else {
		ent.Path = unquotePath(rest)
	}
	return ent
}

func unquotePath(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if u, err := strconvUnquote(s); err == nil {
			return u
		}
	}
	return s
}

// classify 依据 XY 码归组。
func classify(st *Status, e StatusEntry) {
	if e.X == "U" || e.Y == "U" || (e.X == "A" && e.Y == "A") || (e.X == "D" && e.Y == "D") || (e.X == "C" && e.Y == "C") {
		e.Kind = "conflicted"
		st.Conflicted = append(st.Conflicted, e)
		return
	}
	switch {
	case e.X == "?":
		e.Kind = "untracked"
		st.Untracked = append(st.Untracked, e)
	case e.X != " " && e.X != "?":
		e.Kind = "staged"
		st.Staged = append(st.Staged, e)
	default:
		e.Kind = "unstaged"
		st.Unstaged = append(st.Unstaged, e)
	}
}
