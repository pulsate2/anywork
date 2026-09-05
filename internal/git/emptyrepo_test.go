package git

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 空仓库(git init 之后、第一次提交之前)是 git 里少见的"有分支名、没有 HEAD"状态:
// .git/HEAD 指着一个 refs/heads/ 下并不存在的引用。凡是把 HEAD 当对象用的命令在这种
// 仓库上都直接 fatal,而这恰好是新用户点完"初始化 Git 仓库"之后的第一站 —— 报错信息
// 还会引用一个用户根本没提过的分支名(fatal: not a valid object name: 'master'),
// 光看提示完全猜不到是"还没有提交"。
//
// 这组用例把三个入口钉在空仓库上:新建分支、撤回已暂存的改动、看"和 HEAD 的差异"。
func newEmptyRepo(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	s := New(root, false, func(p string) (string, error) {
		return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(p, "/"))), nil
	})
	if _, err := s.Init("/"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return s, root
}

// 空仓库上新建分支:git branch <name> 会拿当前分支当起点而它并不存在,所以改走
// git branch -m —— 把那个还没出生的分支改名,第一次提交就落在用户要的分支上。
func TestBranchCreateOnEmptyRepo(t *testing.T) {
	s, root := newEmptyRepo(t)
	if _, err := s.Branch("/", "create", "main", ""); err != nil {
		t.Fatalf("空仓库新建分支: %v", err)
	}
	info, err := s.ResolveToRepo("/")
	if err != nil {
		t.Fatal(err)
	}
	if info.Branch != "main" {
		t.Fatalf("当前分支该是 main: %+v", info)
	}
	// 改名而不是多一个:空仓库里 refs/heads 下始终是空的,只有 HEAD 指向变了。
	st, err := s.Status("/")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Initial || st.Branch != "main" {
		t.Fatalf("状态该是 main 上还没有提交: %+v", st)
	}
	// 第一次提交之后,分支才真的存在,而且就是 main。
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetIdentity("/", "张三", "zhang@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit("/", "first", true); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	list, err := s.Branches("/")
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Local) != 1 || list.Local[0].Name != "main" {
		t.Fatalf("提交后该只有 main 一个分支: %+v", list.Local)
	}
	// 有提交之后就走回正常那条路:再建一个是真的多一个分支,当前分支不动。
	if _, err := s.Branch("/", "create", "dev", ""); err != nil {
		t.Fatalf("有提交后新建分支: %v", err)
	}
	list, err = s.Branches("/")
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Local) != 2 || list.Current != "main" {
		t.Fatalf("该有 main+dev 且仍在 main 上: current=%s local=%+v", list.Current, list.Local)
	}
}

// switch 在空仓库上和 create 同样处理:switch -c 也要解析起点。
func TestBranchSwitchCreateOnEmptyRepo(t *testing.T) {
	s, _ := newEmptyRepo(t)
	if _, err := s.Branch("/", "switch", "main", ""); err != nil {
		t.Fatalf("空仓库 switch -c: %v", err)
	}
	info, err := s.ResolveToRepo("/")
	if err != nil {
		t.Fatal(err)
	}
	if info.Branch != "main" {
		t.Fatalf("当前分支该是 main: %+v", info)
	}
}

// 改成当前已经叫的那个名字:git branch -m 到同名会静默成功,那样前端会提示"已创建分支"
// 而什么都没发生。明确回一个 400。
func TestBranchCreateSameNameOnEmptyRepo(t *testing.T) {
	s, _ := newEmptyRepo(t)
	info, err := s.ResolveToRepo("/")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Branch("/", "create", info.Branch, ""); !errors.Is(err, errBranchExists) {
		t.Fatalf("want errBranchExists, got %v", err)
	}
}

// 起点是用户明确给的引用时不该绕:那时 git 不碰 HEAD,空仓库上也能直接建。
// 场景是"init 之后先加远端 fetch 下来,再从远端分支开本地分支"。
func TestBranchCreateWithStartOnEmptyRepo(t *testing.T) {
	s, root := newEmptyRepo(t)
	// 造一个有提交的源仓库当远端。
	src := t.TempDir()
	srcSvc := New(src, false, func(p string) (string, error) { return src, nil })
	if _, err := srcSvc.run(src, nil, "init", "-q", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := srcSvc.SetIdentity("/", "张三", "zhang@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "s.txt"), []byte("s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := srcSvc.Commit("/", "base", true); err != nil {
		t.Fatal(err)
	}
	srcInfo, err := srcSvc.ResolveToRepo("/")
	if err != nil {
		t.Fatal(err)
	}
	srcBranch := srcInfo.Branch

	if _, err := s.run(root, nil, "remote", "add", "origin", src); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Fetch("/", "origin"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := s.Branch("/", "create", "feat", "origin/"+srcBranch); err != nil {
		t.Fatalf("带起点新建分支: %v", err)
	}
	list, err := s.Branches("/")
	if err != nil {
		t.Fatal(err)
	}
	if !hasBranch(list.Local, "feat") {
		t.Fatalf("该有 feat 分支: %+v", list.Local)
	}
}

func hasBranch(list []BranchInfo, name string) bool {
	for _, b := range list {
		if b.Name == name {
			return true
		}
	}
	return false
}

// 空仓库上"撤回已暂存的改动":restore --staged 要拿 HEAD 当还原源,没有 HEAD 就 fatal。
// 拿空树当源,语义正好是"回到什么都没有",效果和有 HEAD 时撤回一个新文件一致。
func TestRestoreAllOnEmptyRepo(t *testing.T) {
	s, root := newEmptyRepo(t)
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.StageAdd("/", []string{"f.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Restore("/", []string{"f.txt"}, "all"); err != nil {
		t.Fatalf("空仓库撤回已暂存: %v", err)
	}
	st, err := s.Status("/")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Staged) != 0 {
		t.Fatalf("暂存区该空了: %+v", st.Staged)
	}
	// 和有 HEAD 时撤回一个新增文件一样:文件本身被删掉(它在 git 里没有别的版本)。
	if _, err := os.Stat(filepath.Join(root, "f.txt")); !os.IsNotExist(err) {
		t.Fatalf("新增文件该被一起撤掉: err=%v", err)
	}
}

// 空仓库上看"和 HEAD 的差异":diff HEAD 会 fatal,换空树后第一批文件整篇算新增。
func TestDiffHeadOnEmptyRepo(t *testing.T) {
	s, root := newEmptyRepo(t)
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.StageAdd("/", []string{"f.txt"}); err != nil {
		t.Fatal(err)
	}
	out, err := s.Diff("/", "head", "", "")
	if err != nil {
		t.Fatalf("空仓库 diff head: %v", err)
	}
	if !strings.Contains(out, "new file") || !strings.Contains(out, "+a") {
		t.Fatalf("该把 f.txt 显示成新增:\n%s", out)
	}
	// staged 那条 git 自己就按空树比,顺带确认没被改坏。
	out, err = s.Diff("/", "staged", "", "")
	if err != nil {
		t.Fatalf("空仓库 diff staged: %v", err)
	}
	if !strings.Contains(out, "+a") {
		t.Fatalf("staged diff 该有内容:\n%s", out)
	}
}
