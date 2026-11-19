package git

import (
	"sort"
	"strconv"
	"strings"
)

// CommitNode 一个提交。父提交用完整 hash,前端才能准确连接分支图。
type CommitNode struct {
	Hash    string   `json:"hash"`
	Short   string   `json:"short"`
	Parents []string `json:"parents"`
	Refs    []string `json:"refs"`
	Subject string   `json:"subject"`
	Date    string   `json:"date"`
	Author  string   `json:"author"`
	Email   string   `json:"email"`
}

type LogOptions struct {
	Ref    string
	Search string
	Author string // 作者邮箱,而不是可能重名的显示名。
}

const commitSep = "\x1f"

var commitFormat = strings.Join([]string{"%H", "%P", "%aI", "%D", "%an", "%ae", "%s"}, commitSep)

// Log 返回从 skip 开始的 n 个提交。保持原接口,筛选由 LogFiltered 承载。
func (s *Service) Log(p string, n, skip int) ([]CommitNode, error) {
	return s.LogFiltered(p, n, skip, LogOptions{})
}

// LogFiltered 在 Git 端筛选后翻页,不会只搜索已经加载的第一页。
// topo-order 保证父提交出现在子提交之后,即使作者时间被改过也能画对分支图。
func (s *Service) LogFiltered(p string, n, skip int, opt LogOptions) ([]CommitNode, error) {
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return nil, err
	}
	if !info.Repo {
		return nil, ErrNotRepo
	}
	if n <= 0 {
		n = 50
	}
	if skip < 0 {
		skip = 0
	}
	if err := checkRefArgs(opt.Ref); err != nil {
		return nil, err
	}
	var ref string
	if opt.Ref != "" {
		ref, err = s.run(info.Root, nil, "rev-parse", "--verify", opt.Ref+"^{commit}")
		if err != nil {
			return nil, err
		}
	}
	args := []string{"log", "--topo-order", "--decorate=short", "--pretty=format:" + commitFormat + "%x00",
		"--fixed-strings", "--regexp-ignore-case", "-n", strconv.Itoa(n), "--skip=" + strconv.Itoa(skip)}
	if opt.Author != "" {
		args = append(args, "--author=<"+opt.Author+">")
	}
	search := strings.TrimSpace(opt.Search)
	var hash string
	if h, e := normCommitArg(search); e == nil {
		// 有效的短/完整提交号直接定位;普通说明仍按字面量搜索,不解释正则。
		hash, _ = s.run(info.Root, nil, "rev-parse", "--verify", "--quiet", h+"^{commit}")
	}
	if hash != "" {
		if skip > 0 {
			return []CommitNode{}, nil
		}
		if ref != "" {
			if _, err := s.run(info.Root, nil, "merge-base", "--is-ancestor", hash, ref); err != nil {
				return []CommitNode{}, nil
			}
		}
		args = append(args, "--no-walk", hash)
	} else {
		if search != "" {
			args = append(args, "--grep="+search)
		}
		if ref != "" {
			args = append(args, ref)
		} else {
			args = append(args, "--all")
		}
	}
	args = append(args, "--")
	out, err := s.run(info.Root, nil, args...)
	if err != nil {
		return nil, err
	}
	nodes := []CommitNode{}
	for _, record := range strings.Split(out, "\x00") {
		fields := strings.SplitN(strings.Trim(record, "\r\n"), commitSep, 7)
		if len(fields) == 7 {
			nodes = append(nodes, parseCommitFields(fields))
		}
	}
	return nodes, nil
}

func parseCommitFields(fields []string) CommitNode {
	hash := fields[0]
	return CommitNode{
		Hash: hash, Short: hash[:min(7, len(hash))], Parents: splitParents(fields[1]),
		Date: fields[2], Refs: parseRefs(fields[3]), Author: fields[4], Email: fields[5],
		Subject: strings.TrimSpace(fields[6]),
	}
}

func splitParents(p string) []string {
	if strings.TrimSpace(p) == "" {
		return []string{}
	}
	return strings.Fields(p)
}

// parseRefs 解析 ` (HEAD -> main, tag: v1.0)` 或 %D 的无括号片段。
func parseRefs(d string) []string {
	d = strings.TrimSpace(d)
	d = strings.TrimPrefix(d, "(")
	d = strings.TrimSuffix(d, ")")
	refs := []string{}
	for _, r := range strings.Split(d, ", ") {
		r = strings.TrimSpace(r)
		if r == "" || r == "HEAD" {
			continue
		}
		refs = append(refs, strings.TrimPrefix(r, "HEAD -> "))
	}
	return refs
}

type CommitAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Authors 返回整个仓库的作者,避免翻页后才出现新的作者筛选项。
func (s *Service) Authors(p string) ([]CommitAuthor, error) {
	out, err := s.Repo(p, "log", "--all", "--pretty=format:%an%x1f%ae%x00", "--")
	if err != nil {
		return nil, err
	}
	authors := []CommitAuthor{}
	seen := map[string]bool{}
	for _, record := range strings.Split(out, "\x00") {
		fields := strings.SplitN(strings.Trim(record, "\r\n"), commitSep, 2)
		if len(fields) != 2 || seen[fields[1]] {
			continue
		}
		seen[fields[1]] = true
		authors = append(authors, CommitAuthor{Name: fields[0], Email: fields[1]})
	}
	sort.Slice(authors, func(i, j int) bool {
		if authors[i].Name == authors[j].Name {
			return authors[i].Email < authors[j].Email
		}
		return strings.ToLower(authors[i].Name) < strings.ToLower(authors[j].Name)
	})
	return authors, nil
}
