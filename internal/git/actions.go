package git

import "strings"

// StageAdd 把 paths 加入暂存(index)。
func (s *Service) StageAdd(p string, paths []string) error {
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
	info, err := s.ResolveToRepo(p)
	if err != nil {
		return "", err
	}
	if !info.Repo {
		return "", ErrNotRepo
	}
	return s.remoteOpRun(info.Root, "pull", "--ff-only")
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
func (s *Service) Branch(p, op, name, start string) (string, error) {
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
