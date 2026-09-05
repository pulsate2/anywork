// Package git 封装 git CLI,提供状态/差异/提交/推送/分支/stash/worktree/提交树。
// 采用"走 git CLI"(见 DESIGN 5.3),不用 go-git。所有仓库路径均先经 fs 边界校验,
// 命令以受限的仓库目录为工作目录执行,避免任意命令注入。
package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Service 持有文件根边界,提供 git 操作。
type Service struct {
	root string
	// readOnly 对应 --readonly:所有改动仓库或远端的命令一律拒绝。
	readOnly bool
	// resolve 把用户路径解析为受限绝对路径(注入 fs.Service.Resolve)。
	resolve func(string) (string, error)
	// broker 交互式凭据登记簿;非 nil 时 push/pull 会经它开启 GIT_ASKPASS。
	broker *CredentialBroker
}

// New 构造 Git 服务。root 为文件根;resolve 复用 fs 的边界校验。
func New(root string, readOnly bool, resolve func(string) (string, error)) *Service {
	return &Service{root: root, readOnly: readOnly, resolve: resolve}
}

// allowWrite 只读模式下拒绝一切写操作(暂存/提交/推送/拉取/分支/stash/restore/revert)。
func (s *Service) allowWrite() error {
	if s.readOnly {
		return ErrReadOnly
	}
	return nil
}

// SetCredentialBroker 注入凭据登记簿(供 push/pull 交互式认证)。
func (s *Service) SetCredentialBroker(b *CredentialBroker) { s.broker = b }

// Broker 返回凭据登记簿(供 WS/handler 推送与 answer)。
func (s *Service) Broker() *CredentialBroker { return s.broker }

// ErrNotRepo 表示目录不是 git 仓库(或找不到 root 边界内的最近仓库)。
var ErrNotRepo = errors.New("not a git repository")

// ErrReadOnly 表示服务以 --readonly 启动,拒绝任何改动仓库或远端的操作。
// 用包级哨兵而非临时 errors.New,让 httpErr 能 errors.Is 出来映射成 403。
var ErrReadOnly = errors.New("readonly mode")

// ErrNoIdentity 表示这个仓库现在拼不出提交身份(user.name / user.email)。
// 映射成 428 Precondition Required —— 前端见到这个码就弹身份框,填完重放刚才那步。
var ErrNoIdentity = errors.New("git identity not configured")

var (
	errBadIdentity        = errors.New("invalid name or email")
	errAlreadyRepo        = errors.New("already a git repository")
	errInitNotDir         = errors.New("path is not a directory")
	errBranchExists       = errors.New("already on a branch with that name")
	errEmptyMessage       = errors.New("commit message required")
	errUnknownBranchOp    = errors.New("unknown branch op")
	errUnknownStashOp     = errors.New("unknown stash op")
	errUnknownWorktreeOp  = errors.New("unknown worktree op")
	errUnknownRestoreMode = errors.New("unknown restore mode")
	errUnknownRevertOp    = errors.New("unknown revert op")
	errUnknownRemoteOp    = errors.New("unknown remote op")
	errBadRemoteArg       = errors.New("remote name and url required")
	errBadRefArg          = errors.New("invalid branch or remote name")
	errBadCommitArg       = errors.New("invalid commit hash")
	errBinaryFile         = errors.New("binary cannot be read")
	errNoPaths            = errors.New("no files specified")
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

// runWithCreds 同 run,但为命令开启 GIT_ASKPASS 交互式凭据:git 需要账号密码时
// 经 WS 找浏览器要,期间命令阻塞等用户输入。仅 push/pull 这类远端操作使用。
// 超时对人工输入放宽到 10 分钟,但仍有兜底不会永久挂起。
func (s *Service) runWithCreds(dir string, broker *CredentialBroker, args ...string) (string, error) {
	if dir == "" {
		return "", ErrNotRepo
	}
	const timeout = 10 * time.Minute

	credServer, env, err := broker.newCredServer()
	if err != nil {
		return "", err
	}
	defer credServer.close()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = mergedEnv(env)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb

	// 后台 serve askpass 连接(用户名/密码各一次);命令退出后随 close 收尾。
	go func() {
		served := 0
		for served < 8 {
			if err := credServer.serveOnce(timeout); err != nil {
				return
			}
			served++
		}
	}()

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

// hasCommits 判断这个仓库有没有提交(HEAD 是不是还"未出生")。
// 刚 init 出来的仓库是 git 里少见的"有分支名、没有 HEAD"状态:分支名存在于
// .git/HEAD 的符号引用里,但 refs/heads/ 下什么都没有。凡是要拿 HEAD 当对象用的
// 命令(branch <name>、restore --staged、diff HEAD)在这种仓库上都会直接 fatal,
// 所以这些地方得先问一句。
func (s *Service) hasCommits(root string) bool {
	_, err := s.run(root, nil, "rev-parse", "--verify", "-q", "HEAD")
	return err == nil
}

// emptyTree 返回这个仓库的空树对象 ID,用来在没有提交时充当 HEAD 的替身 ——
// "和 HEAD 比"于是变成"和什么都没有比",diff/restore 想表达的意思恰好没变。
// 不写死那个著名的 4b825dc…:它只是 sha1 仓库的空树,--object-format=sha256
// 的仓库是另一个值。
// 不给 stdin,exec 会把 /dev/null 接上去,git 立刻读到 EOF,得到的就是空树。
func (s *Service) emptyTree(root string) (string, error) {
	return s.run(root, nil, "hash-object", "-t", "tree", "--stdin")
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
