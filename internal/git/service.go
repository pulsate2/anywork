// Package git 封装 git CLI,提供状态/差异/提交/推送/分支/stash/worktree/提交树。
// 采用"走 git CLI"(见 DESIGN 5.3),不用 go-git。所有仓库路径均先经 fs 边界校验,
// 命令以受限的仓库目录为工作目录执行,避免任意命令注入。
package git

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Service 持有文件根边界,提供 git 操作。
type Service struct {
	root string
	// resolve 把用户路径解析为受限绝对路径(注入 fs.Service.Resolve)。
	resolve func(string) (string, error)
}

// New 构造 Git 服务。root 为文件根;resolve 复用 fs 的边界校验。
func New(root string, resolve func(string) (string, error)) *Service {
	return &Service{root: root, resolve: resolve}
}

// ErrNotRepo 表示目录不是 git 仓库(或找不到 root 边界内的最近仓库)。
var ErrNotRepo = errors.New("not a git repository")

var (
	errEmptyMessage      = errors.New("commit message required")
	errUnknownBranchOp   = errors.New("unknown branch op")
	errUnknownStashOp    = errors.New("unknown stash op")
	errUnknownWorktreeOp = errors.New("unknown worktree op")
	errBadRefArg         = errors.New("invalid branch or remote name")
)

// RepoInfo 描述解析出的仓库信息。
type RepoInfo struct {
	Dir    string `json:"dir"`    // 用户请求的目录
	Root   string `json:"root"`   // 仓库根(解析到 .git 的上层)
	Branch string `json:"branch"` // 当前分支 / detached
	Repo   bool   `json:"repo"`   // 是否在仓库内
	Short  string `json:"short"`  // 相对文件根的展示路径
}

// ResolveToRepo 解析目录并向上定位仓库根(dir 为最早含 .git 的祖先)。
func (s *Service) ResolveToRepo(p string) (RepoInfo, error) {
	dir, err := s.resolve(p)
	if err != nil {
		return RepoInfo{}, err
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return RepoInfo{}, err
	}
	var target string
	if fi.IsDir() {
		target = dir
	} else {
		target = filepath.Dir(dir)
	}
	// 向上找含 .git 的目录(含 worktree 的 .git 文件)。
	root := findRepoRoot(target)
	info := RepoInfo{Dir: dir, Root: root, Short: relToRoot(dir, s.root)}
	if root != "" {
		info.Repo = true
		info.Branch = currentBranch(root)
		if info.Root == info.Dir {
			info.Short = relToRoot(root, s.root)
		}
	}
	return info, nil
}

// findRepoRoot 从 dir(或其后代)向上查找第一个含 .git 的目录。
func findRepoRoot(dir string) string {
	d := dir
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// run 以工作目录 dir 执行 git 命令,返回 stdout。stderr 并入错误信息。
// env 追加额外环境变量(如独占配置)。
func (s *Service) run(dir string, env []string, args ...string) (string, error) {
	if dir == "" {
		return "", ErrNotRepo
	}
	ctx := newCommandContext()
	defer ctx.cancel()
	cmd := exec.CommandContext(ctx.ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = mergedEnv(env)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return strings.TrimRight(out.String(), "\n"), errors.New(msg)
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// Repo 在解析出的仓库根上执行只读命令。
func (s *Service) Repo(p string, args ...string) (string, error) {
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return "", err
	}
	if !info.Repo {
		return "", ErrNotRepo
	}
	return s.run(info.Root, nil, args...)
}

func currentBranch(root string) string {
	b, err := exec.Command("git", "-C", root, "symbolic-ref", "--short", "HEAD").Output()
	if err == nil && len(b) > 0 {
		return strings.TrimSpace(string(b))
	}
	// detached HEAD
	if b, err := exec.Command("git", "-C", root, "rev-parse", "--short", "HEAD").Output(); err == nil {
		return "detached@" + strings.TrimSpace(string(b))
	}
	return ""
}

func relToRoot(p, root string) string {
	p = filepath.Clean(p)
	root = filepath.Clean(root)
	if root == "/" {
		return filepath.ToSlash(p)
	}
	if p == root {
		return "/"
	}
	if strings.HasPrefix(p, root+string(filepath.Separator)) {
		return filepath.ToSlash(p[len(root):])
	}
	return filepath.ToSlash(p)
}
