package git

import (
	"strconv"
	"strings"
)

type CommitFile struct {
	Path   string `json:"path"`
	Orig   string `json:"orig,omitempty"`
	Add    int    `json:"add"`
	Del    int    `json:"del"`
	Binary bool   `json:"binary"`
}

type CommitDetail struct {
	CommitNode
	Body  string       `json:"body"`
	Files []CommitFile `json:"files"`
}

// Detail 只读取说明和 numstat,不把大提交的整个补丁搬到列表页面。
// 合并提交按第一父提交比较,与文件差异页及 revert -m 1 的口径一致。
func (s *Service) Detail(p, ref string) (CommitDetail, error) {
	hash, err := normCommitArg(ref)
	if err != nil {
		return CommitDetail{}, err
	}
	out, err := s.Repo(p, "show", "--no-patch", "--decorate=short", "--format="+commitFormat+commitSep+"%b", hash, "--")
	if err != nil {
		return CommitDetail{}, err
	}
	fields := strings.SplitN(out, commitSep, 8)
	if len(fields) != 8 {
		return CommitDetail{}, errBadCommitArg
	}
	stats, err := s.Repo(p, "show", "--format=", "--numstat", "-z", "--find-renames", "--diff-merges=first-parent", hash, "--")
	if err != nil {
		return CommitDetail{}, err
	}
	return CommitDetail{
		CommitNode: parseCommitFields(fields[:7]), Body: strings.TrimSpace(fields[7]), Files: parseCommitNumstat(stats),
	}, nil
}

// -z 不会引用/转义中文、空格或制表符路径;改名记录另跟旧、新路径两个 NUL 字段。
func parseCommitNumstat(out string) []CommitFile {
	files := []CommitFile{}
	records := strings.Split(out, "\x00")
	for i := 0; i < len(records); i++ {
		fields := strings.SplitN(records[i], "\t", 3)
		if len(fields) != 3 {
			continue
		}
		file := CommitFile{Path: fields[2], Binary: fields[0] == "-" || fields[1] == "-"}
		if !file.Binary {
			file.Add, _ = strconv.Atoi(fields[0])
			file.Del, _ = strconv.Atoi(fields[1])
		}
		if file.Path == "" {
			if i+2 >= len(records) {
				continue
			}
			file.Orig, file.Path = records[i+1], records[i+2]
			i += 2
		}
		if file.Path != "" {
			files = append(files, file)
		}
	}
	return files
}
