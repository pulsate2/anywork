package git

import (
	"strconv"
	"strings"
)

// CommitNode 一个提交。
type CommitNode struct {
	Hash    string   `json:"hash"`
	Short   string   `json:"short"`
	Parents []string `json:"parents"`
	Refs    []string `json:"refs"`
	Subject string   `json:"subject"`
	Date    string   `json:"date"`
}

// Log 返回从 skip 开始的 n 个提交(skip 用于翻页)。用 0x1f 作字段分隔符
// (不在真实 git 文本中出现),可靠解析 hash/parents/date/refs/subject。
func (s *Service) Log(p string, n, skip int) ([]CommitNode, error) {
	if n <= 0 {
		n = 50
	}
	const sep = "\x1f"
	pretty := "%H" + sep + "%p" + sep + "%ad" + sep + "%d" + sep + "%s"
	args := []string{"log", "--date=short", "--pretty=format:" + pretty,
		"-n", strconv.Itoa(n), "--all"}
	if skip > 0 {
		args = append(args, "--skip="+strconv.Itoa(skip))
	}
	out, err := s.Repo(p, args...)
	if err != nil {
		return nil, err
	}
	nodes := []CommitNode{}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, "\r")
		if l == "" {
			continue
		}
		parts := strings.Split(l, sep)
		if len(parts) < 5 {
			continue
		}
		node := CommitNode{
			Hash:    parts[0],
			Parents: splitParents(parts[1]),
			Date:    parts[2],
			Refs:    parseRefs(parts[3]),
			Subject: strings.TrimSpace(parts[4]),
		}
		if len(node.Hash) > 7 {
			node.Short = node.Hash[:7]
		} else {
			node.Short = node.Hash
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func splitParents(p string) []string {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil
	}
	return strings.Split(p, " ")
}

// parseRefs 解析 ` (HEAD -> main, tag: v1.0)` 这种 %d 片段。
func parseRefs(d string) []string {
	d = strings.TrimSpace(d)
	if d == "" {
		return nil
	}
	d = strings.TrimPrefix(d, "(")
	d = strings.TrimSuffix(d, ")")
	d = strings.TrimPrefix(d, "HEAD -> ")
	refs := []string{}
	for _, r := range strings.Split(d, ", ") {
		r = strings.TrimSpace(r)
		if r == "" || r == "HEAD" {
			continue
		}
		r = strings.TrimPrefix(r, "HEAD -> ")
		refs = append(refs, r)
	}
	return refs
}
