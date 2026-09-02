package git

import (
	"path/filepath"
	"strings"
)

// Diff 返回 unified 文本。scope: "worktree"|"staged"|"head"|"commit";
// file 可选限定单个文件;commit 时 ref 指定提交哈希(取该提交相对父的改动)。
func (s *Service) Diff(p, scope, file, ref string) (string, error) {
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return "", err
	}
	if !info.Repo {
		return "", ErrNotRepo
	}
	var args []string
	switch scope {
	case "staged":
		args = []string{"diff", "--cached"}
	case "head":
		args = []string{"diff", "HEAD"}
	case "commit":
		if ref == "" {
			return "", errBadRefArg
		}
		// git show 输出提交元信息 + unified diff,前端 parseDiff 吃 diff --git 头,
		// 元信息行会被当成 meta 展示,正好能看到提交说明。
		args = []string{"show", ref}
	default: // worktree
		args = []string{"diff"}
	}
	if file != "" {
		args = append(args, "--", cleanRelPath(file))
	}
	return s.run(info.Root, nil, args...)
}

// cleanRelPath 让 diff 路径相对仓库根,去除非仓库前缀及前导斜杠。
func cleanRelPath(f string) string {
	// 尝试把绝对路径换算为仓库根外相对路径(直接交给 git,git 会自行裁剪)。
	// 仅去掉用户传入的 `repo路径:` 这类误拼写 —— 保持原样最稳妥。
	f = filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(f), "/"))
	if f == "" || f == "." {
		return "."
	}
	return f
}
