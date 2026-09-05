package git

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// git init 这条路上要靠真 git 才能确认的是:嵌套 init 会不会被拦住、以及空仓库上
// Git 视图那三个请求(repo/status/log)还能不能正常返回 —— 空仓库是 git 里少有的
// "有仓库但没有 HEAD"状态,log 在这种仓库上不带 --all 会直接 fatal。
//
// 与 identity 那组测试不同,这里的 root 一开始**不是**仓库。
func newInitService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	s := New(root, false, func(p string) (string, error) {
		return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(p, "/"))), nil
	})
	return s, root
}

// 最基本的一条:目录不是仓库,init 之后是,而且返回的信息就是新仓库的信息。
func TestInitCreatesRepo(t *testing.T) {
	s, root := newInitService(t)
	if err := os.Mkdir(filepath.Join(root, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := s.Init("/proj")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !info.Repo {
		t.Fatalf("返回的信息该是仓库: %+v", info)
	}
	if info.Root != filepath.Join(root, "proj") {
		t.Fatalf("仓库根不对: %+v", info)
	}
	// 分支名跟着机器的 init.defaultBranch 走,不断言具体叫什么,但必须有。
	if info.Branch == "" {
		t.Fatalf("分支名不该是空的: %+v", info)
	}
	if fi, err := os.Stat(filepath.Join(root, "proj", ".git")); err != nil || !fi.IsDir() {
		t.Fatalf(".git 该被建出来: %v", err)
	}
}

// 已经在仓库里就不许再 init。子目录这一条是重点:git 会照做并留下一个嵌套仓库,
// 从此这个子目录的改动归内层管、外层看不见,而且只能靠 .git 的位置才能发现。
func TestInitRejectsExistingRepo(t *testing.T) {
	s, root := newInitService(t)
	if _, err := s.Init("/"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Init("/"); !errors.Is(err, errAlreadyRepo) {
		t.Fatalf("同一个目录重复 init: want errAlreadyRepo, got %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Init("/sub"); !errors.Is(err, errAlreadyRepo) {
		t.Fatalf("仓库内的子目录: want errAlreadyRepo, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", ".git")); !os.IsNotExist(err) {
		t.Fatalf("被拦下时不该建出嵌套仓库: err=%v", err)
	}
}

// 路径是文件:ResolveToRepo 会退到它所在的目录,那样 init 出来的位置和用户点的
// 不是一个东西,所以明确拒绝而不是"顺手在旁边建一个"。
func TestInitOnFileRejected(t *testing.T) {
	s, root := newInitService(t)
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Init("/f.txt"); !errors.Is(err, errInitNotDir) {
		t.Fatalf("want errInitNotDir, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("不该在上级目录建仓库: err=%v", err)
	}
}

// 目录不存在:照常报 stat 的错(前端会显示),不替用户建目录 —— 建目录是新建工作区那步的事。
func TestInitMissingDir(t *testing.T) {
	s, root := newInitService(t)
	if _, err := s.Init("/nope"); err == nil {
		t.Fatal("目录不存在时该报错")
	}
	if _, err := os.Stat(filepath.Join(root, "nope")); !os.IsNotExist(err) {
		t.Fatalf("不该创建目录: err=%v", err)
	}
}

// 只读模式:init 是写操作,和别的写操作一样要过 allowWrite。
func TestInitReadOnly(t *testing.T) {
	s, root := newInitService(t)
	s.readOnly = true
	if _, err := s.Init("/"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("want ErrReadOnly, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("只读模式不该建仓库: err=%v", err)
	}
}

// 刚 init 出来的空仓库上,Git 视图开屏那三个请求都得能过。
// 空仓库还没有 HEAD:git log 不带 --all 会 fatal,一旦回归,初始化完的页面就会白着报错。
func TestInitEmptyRepoIsUsable(t *testing.T) {
	s, root := newInitService(t)
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Init("/"); err != nil {
		t.Fatal(err)
	}
	st, err := s.Status("/")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Initial {
		t.Fatalf("空仓库该标记 Initial: %+v", st)
	}
	if len(st.Untracked) != 1 {
		t.Fatalf("那个文件该是未跟踪: %+v", st.Untracked)
	}
	log, err := s.Log("/", 30, 0)
	if err != nil {
		t.Fatalf("空仓库的 Log 不该失败: %v", err)
	}
	if len(log) != 0 {
		t.Fatalf("空仓库没有提交: %+v", log)
	}
	if _, err := s.Branches("/"); err != nil {
		t.Fatalf("Branches: %v", err)
	}
}

// 状态码要钉住:成功 200,已经是仓库 409(前端据此说"刷新就能看到"而不是"操作失败")。
func TestInitStatusCodes(t *testing.T) {
	s, _ := newInitService(t)
	h := NewHandlers(s)
	body := `{"path":"/"}`

	rec := httptest.NewRecorder()
	h.Init(rec, httptest.NewRequest(http.MethodPost, "/api/git/init", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.Init(rec, httptest.NewRequest(http.MethodPost, "/api/git/init", strings.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("重复 init: want 409, got %d (%s)", rec.Code, rec.Body.String())
	}
}
