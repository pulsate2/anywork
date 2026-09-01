package git

import "strings"

// StatusEntry 一条 git status 记录。
type StatusEntry struct {
	Raw  string `json:"raw"`
	X    string `json:"x"` // 索引状态
	Y    string `json:"y"` // 工作区状态
	Path string `json:"path"`
	Orig string `json:"orig,omitempty"` // 重命名/复制原路径
	Kind string `json:"kind"`           // staged | unstaged | untracked | ignored
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
}

// Status 返回当前仓库的状态(porcelain=v1 -b)。
func (s *Service) Status(p string) (Status, error) {
	out, err := s.Repo(p, "status", "--porcelain=v1", "-b")
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
	return st, nil
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
	// 重命名/复制:`orig -> new`(porcelain v1 中 3 制: dest 顺带,新路径在前)
	if arrow := strings.LastIndex(rest, " -> "); arrow >= 0 {
		ent.Path = rest[:arrow]
		ent.Orig = rest[arrow+4:]
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
