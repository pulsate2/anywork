package git

import (
	"os"
	"strings"
)

// Init 在这个目录上 git init,返回初始化之后的仓库信息。
//
// 只在"当前工作区不是仓库"时用得上,前端把按钮放在那句提示中间。之所以在服务端也拦一道
// info.Repo:目录本身或它的某个祖先已经是仓库时再 init,git 会照做,结果是一个**嵌套**
// 仓库 —— 从此这个目录里的改动归内层那个管,外层看不见,而这两层的存在只能靠 .git 的
// 位置分辨,是个很难自己发现的坑。所以已经在仓库里就直接拒绝,不给 git 这个机会。
//
// 不传目录参数给 git,而是把它当工作目录(run 的 dir):初始分支名也不指定,跟着这台
// 机器的 init.defaultBranch 走 —— 在终端里敲 git init 得到什么,这个按钮就给什么。
func (s *Service) Init(p string) (RepoInfo, error) {
	if err := s.allowWrite(); err != nil {
		return RepoInfo{}, err
	}
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return RepoInfo{}, err
	}
	if info.Repo {
		return RepoInfo{}, errAlreadyRepo
	}
	// ResolveToRepo 对文件路径会退到它所在的目录,那样 init 出来的仓库位置和用户
	// 点的东西不是一个,这里明确要求是目录。
	fi, err := os.Stat(info.Dir)
	if err != nil {
		return RepoInfo{}, err
	}
	if !fi.IsDir() {
		return RepoInfo{}, errInitNotDir
	}
	if _, err := s.run(info.Dir, nil, "init", "-q"); err != nil {
		return RepoInfo{}, err
	}
	// 重新解析一遍:分支名和仓库根由 git 定,照抄它的结果而不是自己拼。
	return s.ResolveToRepo(p)
}

// StageAdd 把 paths 加入暂存(index)。
func (s *Service) StageAdd(p string, paths []string) error {
	if err := s.allowWrite(); err != nil {
		return err
	}
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return err
	}
	if !info.Repo {
		return ErrNotRepo
	}
	args := []string{"add", "--"}
	if len(paths) == 0 {
		args = append(args, ".")
	} else {
		for _, fp := range paths {
			args = append(args, cleanRelPath(fp))
		}
	}
	_, err = s.run(info.Root, nil, args...)
	return err
}

// StageReset 从暂存区移除 paths(保留工作区改动)。
func (s *Service) StageReset(p string, paths []string) error {
	if err := s.allowWrite(); err != nil {
		return err
	}
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return err
	}
	if !info.Repo {
		return ErrNotRepo
	}
	args := []string{"reset", "-q", "--"}
	args = append(args, cleanRelPathList(paths)...)
	_, err = s.run(info.Root, nil, args...)
	return err
}

// Commit 提交所有已暂存改动。
func (s *Service) Commit(p, message string, addAll bool) (string, error) {
	if err := s.allowWrite(); err != nil {
		return "", err
	}
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return "", err
	}
	if !info.Repo {
		return "", ErrNotRepo
	}
	if strings.TrimSpace(message) == "" {
		return "", errEmptyMessage
	}
	// 身份要在动手之前问:add -A 已经改了暂存区,等 git commit 自己报
	// "Please tell me who you are" 就得让用户面对一个"提交失败但文件已经暂存"的局面。
	if !s.identityOK(info.Root) {
		return "", ErrNoIdentity
	}
	if addAll {
		if _, err := s.run(info.Root, nil, "add", "-A"); err != nil {
			return "", err
		}
	}
	// hooksPath 指向仓库内不存在的相对路径,彻底关掉钩子(--no-verify 只挡
	// pre-commit / commit-msg,prepare-commit-msg、post-commit 照跑,可能要交互)。
	// 不能用 /dev/null:Git for Windows 会把 path 类型配置里的绝对 POSIX 路径当成
	// 未迁移的写法,提交时警告 "should be '%(prefix)//dev/null'"。
	out, err := s.run(info.Root, nil, "-c", "core.hooksPath=.git/lightremote-no-hooks",
		"commit", "-m", message, "--no-verify")
	if err != nil {
		return "", err
	}
	return out, nil
}

// Push 推送。remote 为空时走当前分支的上游配置;指定 remote 时 branch 缺省为当前分支,
// setUpstream 对应 -u(首次推送时把远端分支记为上游)。
func (s *Service) Push(p, remote, branch string, setUpstream bool) (string, error) {
	if err := s.allowWrite(); err != nil {
		return "", err
	}
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return "", err
	}
	if !info.Repo {
		return "", ErrNotRepo
	}
	if err := checkRefArgs(remote, branch); err != nil {
		return "", err
	}
	args := []string{"push"}
	if remote != "" {
		if setUpstream {
			args = append(args, "-u")
		}
		args = append(args, remote)
		if branch == "" {
			branch = info.Branch
		}
		// 游离 HEAD 没有分支名可推,交给 git 报错更清楚。
		if branch != "" && !strings.HasPrefix(branch, "detached@") {
			args = append(args, branch)
		}
	}
	return s.remoteOpRun(info.Root, args...)
}

// Pull 拉取。
func (s *Service) Pull(p string) (string, error) {
	if err := s.allowWrite(); err != nil {
		return "", err
	}
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return "", err
	}
	if !info.Repo {
		return "", ErrNotRepo
	}
	return s.remoteOpRun(info.Root, "pull", "--ff-only")
}

// Fetch 只更新远端跟踪引用(refs/remotes/*),不动工作区也不合并。
// 存在的意义是让状态里的 ↓behind 有意义:git status 从不联网,它比的是本地那份
// 远端跟踪引用,而这份引用只有 fetch 会更新 —— pull 虽然内部也 fetch,但紧接着就
// 合并掉了,所以光用 pull 的话 behind 永远是 0。
// --prune 顺手清掉远端已删除分支留下的本地引用;remote 为空时抓所有远端。
func (s *Service) Fetch(p, remote string) (string, error) {
	if err := s.allowWrite(); err != nil {
		return "", err
	}
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return "", err
	}
	if !info.Repo {
		return "", ErrNotRepo
	}
	if err := checkRefArgs(remote); err != nil {
		return "", err
	}
	args := []string{"fetch", "--prune"}
	if remote != "" {
		args = append(args, remote)
	} else {
		args = append(args, "--all")
	}
	return s.remoteOpRun(info.Root, args...)
}

// RemoteOp 远端管理。op: add|remove|rename|set-url。
// value 是第二个参数:add/set-url 时是 URL,rename 时是新名字。
// 名字和 URL 都过 checkRefArgs:以 - 开头会被 git 当成选项。
func (s *Service) RemoteOp(p, op, name, value string) (string, error) {
	if err := s.allowWrite(); err != nil {
		return "", err
	}
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return "", err
	}
	if !info.Repo {
		return "", ErrNotRepo
	}
	if err := checkRefArgs(name, value); err != nil {
		return "", err
	}
	if name == "" {
		return "", errBadRemoteArg
	}
	switch op {
	case "add":
		// 空 URL git 是收的,存下来就是个死远端,这里直接拦掉。
		if value == "" {
			return "", errBadRemoteArg
		}
		return s.run(info.Root, nil, "remote", "add", name, value)
	case "remove":
		return s.run(info.Root, nil, "remote", "remove", name)
	case "rename":
		if value == "" {
			return "", errBadRemoteArg
		}
		return s.run(info.Root, nil, "remote", "rename", name, value)
	case "set-url":
		if value == "" {
			return "", errBadRemoteArg
		}
		return s.run(info.Root, nil, "remote", "set-url", name, value)
	}
	return "", errUnknownRemoteOp
}

// remoteOpRun push/pull 执行 git;有 broker 时走交互式凭据(runWithCreds)。
func (s *Service) remoteOpRun(root string, args ...string) (string, error) {
	if s.broker != nil {
		return s.runWithCreds(root, s.broker, args...)
	}
	return s.run(root, nil, args...)
}

// Branch 分支操作。op: create|delete|switch|track。track 用于远端分支
// (origin/feat),会建立同名本地分支并跟踪它。
//
// 空仓库(还没有任何提交)要单独绕一下:那时候 .git/HEAD 里虽然写着分支名,
// refs/heads/ 下却是空的,git branch main 会去解析当前分支当起点,于是报
// "fatal: not a valid object name: 'master'" —— 一句话里两个分支名,看着像 bug。
// 分支在没有提交时本就无处可指,唯一说得通的做法是把当前那个还没出生的分支改名
// (git branch -m,git 自己 init 完改默认分支名也是这么干的),结果正是用户想要的:
// 第一次提交就落在 main 上。
func (s *Service) Branch(p, op, name, start string) (string, error) {
	if err := s.allowWrite(); err != nil {
		return "", err
	}
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return "", err
	}
	if !info.Repo {
		return "", ErrNotRepo
	}
	if err := checkRefArgs(name, start); err != nil {
		return "", err
	}
	// 起点是用户明确给的引用时不必绕:那时候 git 不去碰 HEAD,fetch 完在空仓库上
	// branch main origin/main 是能直接成的。
	if start == "" && (op == "create" || op == "switch") && !s.hasCommits(info.Root) {
		if name == info.Branch {
			// 已经叫这个名字了(git branch -m 到同名会静默成功,但那样等于什么都没说)。
			return "", errBranchExists
		}
		return s.run(info.Root, nil, "branch", "-m", name)
	}
	var args []string
	switch op {
	case "create":
		args = []string{"branch"}
		if start != "" {
			args = append(args, name, start)
		} else {
			args = append(args, name)
		}
	case "delete":
		args = []string{"branch", "-D", name}
	case "switch":
		args = []string{"switch"}
		if start != "" {
			args = append(args, "-c", name, start)
		} else {
			args = append(args, name)
		}
	case "track":
		args = []string{"switch", "--track", name}
	default:
		return "", errUnknownBranchOp
	}
	return s.run(info.Root, nil, args...)
}

// checkRefArgs 拦住以 - 开头的分支/远端名:它们会被 git 当成选项。
func checkRefArgs(names ...string) error {
	for _, n := range names {
		if strings.HasPrefix(n, "-") {
			return errBadRefArg
		}
	}
	return nil
}

// Stash 操作。op: push|pop|list|clear。
func (s *Service) Stash(p, op, message string) (string, error) {
	// list 只是查看,只读模式下照常放行。
	if op != "list" {
		if err := s.allowWrite(); err != nil {
			return "", err
		}
	}
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return "", err
	}
	if !info.Repo {
		return "", ErrNotRepo
	}
	switch op {
	case "push":
		if message != "" {
			return s.run(info.Root, nil, "stash", "push", "-m", message)
		}
		return s.run(info.Root, nil, "stash", "push")
	case "pop":
		return s.run(info.Root, nil, "stash", "pop")
	case "list":
		return s.run(info.Root, nil, "stash", "list")
	case "clear":
		return s.run(info.Root, nil, "stash", "clear")
	}
	return "", errUnknownStashOp
}

// Worktree 操作。op: list|add|remove。path/name 为新 worktree 位置与分支。
func (s *Service) Worktree(p, op, path, branchOrCommit string) (string, error) {
	// list 只是查看,只读模式下照常放行。
	if op != "list" {
		if err := s.allowWrite(); err != nil {
			return "", err
		}
	}
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return "", err
	}
	if !info.Repo {
		return "", ErrNotRepo
	}
	switch op {
	case "list":
		return s.run(info.Root, nil, "worktree", "list")
	case "add":
		args := []string{"worktree", "add"}
		if branchOrCommit != "" {
			args = append(args, "-b", branchOrCommit)
		}
		args = append(args, path)
		return s.run(info.Root, nil, args...)
	case "remove":
		return s.run(info.Root, nil, "worktree", "remove", "--force", path)
	}
	return "", errUnknownWorktreeOp
}

func cleanRelPathList(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, cleanRelPath(p))
	}
	return out
}

// Restore 撤销指定文件的改动。mode:
//
//	worktree  — git restore:丢弃工作区改动,回到暂存区的状态
//	all       — git restore --staged --worktree:暂存区与工作区一起回到 HEAD
//	untracked — git clean -fd:直接删除未跟踪的文件/目录(git 里没有副本可回退)
//
// paths 必须显式给出至少一个文件:cleanRelPath 会把空串折成 ".",若放过空列表
// 就等于"丢弃整个仓库的改动",风险太大,一律按 errNoPaths 拒绝。
func (s *Service) Restore(p string, paths []string, mode string) (string, error) {
	if err := s.allowWrite(); err != nil {
		return "", err
	}
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return "", err
	}
	if !info.Repo {
		return "", ErrNotRepo
	}
	rel := make([]string, 0, len(paths))
	for _, fp := range paths {
		if c := cleanRelPath(fp); c != "." {
			rel = append(rel, c)
		}
	}
	if len(rel) == 0 {
		return "", errNoPaths
	}
	var args []string
	switch mode {
	case "worktree":
		args = []string{"restore", "--"}
	case "all":
		args = []string{"restore", "--staged", "--worktree", "--"}
		// 空仓库上 restore --staged 会 fatal: could not resolve HEAD —— 它要拿 HEAD
		// 当还原源。这时候"回到 HEAD"的本意就是"回到什么都没有",用空树当源正好表达这个,
		// 效果是把刚 add 进去的新文件从暂存区和工作区一起清掉(和有 HEAD 时对新文件的
		// 行为一致)。
		if !s.hasCommits(info.Root) {
			et, err := s.emptyTree(info.Root)
			if err != nil {
				return "", err
			}
			args = []string{"restore", "--source=" + et, "--staged", "--worktree", "--"}
		}
	case "untracked":
		args = []string{"clean", "-fd", "--"}
	default:
		return "", errUnknownRestoreMode
	}
	return s.run(info.Root, nil, append(args, rel...)...)
}

// Revert 回滚提交。op: revert|abort。
// revert 生成一个反向提交(--no-edit 免得 git 拉起编辑器);合并提交自动补 -m 1,
// 否则 git 会以 "is a merge but no -m option was given" 拒绝。
// abort 对应 git revert --abort,用来放弃卡在冲突里的 revert。
func (s *Service) Revert(p, op, hash string) (string, error) {
	if err := s.allowWrite(); err != nil {
		return "", err
	}
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return "", err
	}
	if !info.Repo {
		return "", ErrNotRepo
	}
	if op == "abort" {
		return s.run(info.Root, nil, "revert", "--abort")
	}
	if op != "revert" {
		return "", errUnknownRevertOp
	}
	h, err := normCommitArg(hash)
	if err != nil {
		return "", err
	}
	// revert 缺身份的后果比 commit 重:git 会先把反向改动应用到工作区,再在提交那步
	// 失败,于是工作区留着一堆脏文件而 REVERT_HEAD 又没建立 —— 补好身份重试会撞上
	// "your local changes would be overwritten by revert",abort 也无从下手。所以先问。
	if !s.identityOK(info.Root) {
		return "", ErrNoIdentity
	}
	// hooksPath 的用意见 Commit:revert 同样会产生提交,钩子可能要交互而卡住命令。
	args := []string{"-c", "core.hooksPath=.git/lightremote-no-hooks", "revert", "--no-edit"}
	if s.isMergeCommit(info.Root, h) {
		args = append(args, "-m", "1")
	}
	return s.run(info.Root, nil, append(args, h)...)
}

// isMergeCommit 判断是否合并提交(父提交多于一个)。
// rev-list --parents 的输出形如 "<commit> <p1> [<p2> ...]"。
func (s *Service) isMergeCommit(root, hash string) bool {
	out, err := s.run(root, nil, "rev-list", "--parents", "-n", "1", hash)
	if err != nil {
		return false
	}
	return len(strings.Fields(out)) > 2
}

// normCommitArg 只接受 4~40 位十六进制提交号:既挡住以 - 开头的伪选项,
// 也挡住 HEAD~1 / 分支名这类会让 revert 目标失控的写法。
func normCommitArg(hash string) (string, error) {
	h := strings.TrimSpace(hash)
	if len(h) < 4 || len(h) > 40 {
		return "", errBadCommitArg
	}
	for _, c := range h {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return "", errBadCommitArg
		}
	}
	return h, nil
}
