package git

import "strings"

// BranchInfo 一个分支引用。
type BranchInfo struct {
	Name     string `json:"name"`
	Current  bool   `json:"current"`
	Remote   bool   `json:"remote"`
	Upstream string `json:"upstream,omitempty"`
	Date     string `json:"date,omitempty"`
	Subject  string `json:"subject,omitempty"`
}

// BranchList 本地 + 远端分支,按提交时间倒序。
type BranchList struct {
	Current string       `json:"current"`
	Local   []BranchInfo `json:"local"`
	Remote  []BranchInfo `json:"remote"`
}

// Remote 一个远端及其 fetch 地址。
type Remote struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Branches 列出本地与远端分支。用 for-each-ref 而不是解析 `git branch -a`:
// 后者的输出带缩进/星号/箭头,还会随语言环境变化。
func (s *Service) Branches(p string) (*BranchList, error) {
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return nil, err
	}
	if !info.Repo {
		return nil, ErrNotRepo
	}
	const sep = "\x1f"
	format := strings.Join([]string{
		"%(refname)", "%(refname:short)", "%(HEAD)",
		"%(upstream:short)", "%(committerdate:short)", "%(contents:subject)",
	}, sep)
	out, err := s.run(info.Root, nil, "for-each-ref", "--sort=-committerdate",
		"--format="+format, "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	list := &BranchList{Current: info.Branch, Local: []BranchInfo{}, Remote: []BranchInfo{}}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, "\r")
		if l == "" {
			continue
		}
		f := strings.Split(l, sep)
		if len(f) < 6 {
			continue
		}
		b := BranchInfo{
			Name: f[1], Current: f[2] == "*", Upstream: f[3],
			Date: f[4], Subject: strings.TrimSpace(f[5]),
		}
		if strings.HasPrefix(f[0], "refs/remotes/") {
			// origin/HEAD 是指向默认分支的符号引用,不能当分支切换。
			if strings.HasSuffix(b.Name, "/HEAD") {
				continue
			}
			b.Remote = true
			list.Remote = append(list.Remote, b)
		} else {
			list.Local = append(list.Local, b)
		}
	}
	return list, nil
}

// Remotes 列出远端。`git remote -v` 每个远端两行(fetch/push),按名字去重取第一条。
func (s *Service) Remotes(p string) ([]Remote, error) {
	out, err := s.Repo(p, "remote", "-v")
	if err != nil {
		return nil, err
	}
	list := []Remote{}
	seen := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(strings.TrimSpace(l))
		if len(f) < 2 || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		list = append(list, Remote{Name: f[0], URL: f[1]})
	}
	return list, nil
}
