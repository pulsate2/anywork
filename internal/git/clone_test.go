package git

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// clone 这条路和 init 是一对:都只在"当前工作区不是仓库"时用得上,都作用在当前目录上。
// 需要真 git 才能确认的是:原地克隆(git clone <url> .)确实落地、非空目录被挡住、
// 已经在仓库里时不许再克隆出嵌套仓库,以及 url 里的伪选项进不去。
//
// 与 identity 那组测试一样,root 一开始**不是**仓库。
func newCloneService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	s := New(root, false, func(p string) (string, error) {
		return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(p, "/"))), nil
	})
	return s, root
}

// makeSourceRepo 造一个可克隆的源仓库(有提交、有文件),返回它的绝对路径 —— 本地路径
// 也是 git clone 认的 url,拿它当远端省去联网。
func makeSourceRepo(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", src}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("-c", "user.name=src", "-c", "user.email=src@example.com", "commit", "-qm", "init")
	return src
}

// 最基本的一条:空目录上克隆,落地的就是那个源仓库,origin 也配好了。
func TestCloneCreatesRepo(t *testing.T) {
	s, root := newCloneService(t)
	src := makeSourceRepo(t)

	info, err := s.Clone("/", src)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if !info.Repo {
		t.Fatalf("返回的信息该是仓库: %+v", info)
	}
	if info.Root != root {
		t.Fatalf("仓库根该是目标目录: %+v", info)
	}
	if _, err := os.Stat(filepath.Join(root, "hello.txt")); err != nil {
		t.Fatalf("源仓库的文件该被克隆过来: %v", err)
	}
	out, err := s.run(root, nil, "remote", "get-url", "origin")
	if err != nil {
		t.Fatalf("origin 该被配上: %v", err)
	}
	if strings.TrimSpace(out) != src {
		t.Fatalf("origin url 不对: %q", out)
	}
}

// 目录非空:git clone 到 "." 本就要求空目录,这里要的是我们自己的错误(而不是让 git
// 报一句 destination path '.' already exists),且不能留下半个仓库。
func TestCloneRejectsNonEmptyDir(t *testing.T) {
	s, root := newCloneService(t)
	src := makeSourceRepo(t)
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Clone("/", src); !errors.Is(err, errCloneNotEmpty) {
		t.Fatalf("want errCloneNotEmpty, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("被拦下时不该建出仓库: err=%v", err)
	}
}

// 已经在仓库里就不许再克隆 —— 会留下嵌套仓库,理由同 Init。
func TestCloneRejectsExistingRepo(t *testing.T) {
	s, _ := newCloneService(t)
	src := makeSourceRepo(t)
	if _, err := s.Init("/"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Clone("/", src); !errors.Is(err, errAlreadyRepo) {
		t.Fatalf("want errAlreadyRepo, got %v", err)
	}
}

// 路径是文件:同 Init,明确拒绝而不是"顺手在旁边建一个"。
func TestCloneOnFileRejected(t *testing.T) {
	s, root := newCloneService(t)
	src := makeSourceRepo(t)
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Clone("/f.txt", src); !errors.Is(err, errInitNotDir) {
		t.Fatalf("want errInitNotDir, got %v", err)
	}
}

// 只读模式:克隆是写操作,和别的写操作一样要过 allowWrite。
func TestCloneReadOnly(t *testing.T) {
	s, root := newCloneService(t)
	src := makeSourceRepo(t)
	s.readOnly = true
	if _, err := s.Clone("/", src); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("want ErrReadOnly, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("只读模式不该建仓库: err=%v", err)
	}
}

// 地址首尾带空白(手输/粘贴常见):校验用的是规整后的值,交给 git 的也必须是同一个,
// 否则会出现"校验过了、git 却拿着带空白的地址报错"。
func TestCloneTrimsURL(t *testing.T) {
	s, _ := newCloneService(t)
	src := makeSourceRepo(t)
	if _, err := s.Clone("/", "  "+src+"  "); err != nil {
		t.Fatalf("首尾空白的地址该被规整后照常克隆: %v", err)
	}
}

// url 校验:- 开头会被 git 当选项(--upload-pack=… 能直接执行命令);ext:: transport
// 同样会执行任意命令。两种都必须在动 git 之前拦掉。
func TestCloneBadURL(t *testing.T) {
	s, root := newCloneService(t)
	for _, url := range []string{"", "   ", "-x", "--upload-pack=/bin/sh", "ext::sh -c true"} {
		if _, err := s.Clone("/", url); !errors.Is(err, errBadCloneURL) {
			t.Errorf("url %q: want errBadCloneURL, got %v", url, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("坏 url 不该建出仓库: err=%v", err)
	}
}

// 状态码要钉住:成功 200,已经在仓库里 409(前端据此说"刷新就能看到")。
func TestCloneStatusCodes(t *testing.T) {
	s, _ := newCloneService(t)
	h := NewHandlers(s)
	src := makeSourceRepo(t)

	body := `{"path":"/","url":` + strconv.Quote(src) + `}`
	rec := httptest.NewRecorder()
	h.Clone(rec, httptest.NewRequest(http.MethodPost, "/api/git/clone", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.Clone(rec, httptest.NewRequest(http.MethodPost, "/api/git/clone", strings.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("重复 clone: want 409, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// 非空目录回 400(输入不对,重试也没用),单开一个 root:上一个测试里 root 克隆完已经是
// 仓库了,那种情况下先撞上的是 409,钉不住这一条。
func TestCloneNonEmptyStatusIs400(t *testing.T) {
	s, root := newCloneService(t)
	h := NewHandlers(s)
	src := makeSourceRepo(t)
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `{"path":"/","url":` + strconv.Quote(src) + `}`
	rec := httptest.NewRecorder()
	h.Clone(rec, httptest.NewRequest(http.MethodPost, "/api/git/clone", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非空目录: want 400, got %d (%s)", rec.Code, rec.Body.String())
	}
}
