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

// 提交身份这条路上有三处只能靠真 git 才能确认的行为,所以这个测试不 mock:
//   - git 缺 user.email 时到底会不会拒绝提交(会,退出码 128)
//   - --local 写进去的值,和"继承来的生效值",能不能分开读出来(能,靠退出码 1)
//   - Commit/Revert 有没有在动手之前就把身份问掉(必须,否则工作区被弄脏)
//
// 全局/系统配置一律指到 /dev/null,免得跑测试的机器上正好设了 user.email 让用例失效。
func newTestService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	// 这几个必须是**没有**而不是空串:git 把空的 GIT_AUTHOR_NAME 当成"显式设成空",
	// 直接 fatal: empty ident name。t.Setenv 只能设不能删,所以自己存旧值再删。
	unsetEnv(t, "EMAIL", "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL")
	s := New(root, false, func(p string) (string, error) {
		return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(p, "/"))), nil
	})
	if _, err := s.run(root, nil, "init", "-q", "."); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return s, root
}

func unsetEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		old, had := os.LookupEnv(k)
		if !had {
			continue
		}
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Setenv(k, old) })
	}
}

// 缺身份时 Commit 必须在 add 之前就回 ErrNoIdentity:让前端认出 428 去弹框,
// 同时保证暂存区没被动过 —— 用户取消弹框后仓库状态和点提交之前一模一样。
func TestCommitWithoutIdentity(t *testing.T) {
	s, root := newTestService(t)
	if s.identityOK(root) {
		t.Fatal("identityOK 应为 false:测试环境里不该有 user.email")
	}
	if _, err := s.Commit("/", "msg", true); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("want ErrNoIdentity, got %v", err)
	}
	st, err := s.Status("/")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Staged) != 0 || len(st.Untracked) != 1 {
		t.Fatalf("提交被拦下后暂存区不该有东西: staged=%d untracked=%d", len(st.Staged), len(st.Untracked))
	}
}

// SetIdentity 只能写这一个仓库:写完 --local 读得到,而且提交真的能过。
func TestSetIdentityIsRepoLocal(t *testing.T) {
	s, root := newTestService(t)
	id, err := s.SetIdentity("/", "  张三  ", " zhang@example.com ")
	if err != nil {
		t.Fatal(err)
	}
	// 前后空格应被去掉(用户在手机上输入很容易带一个)。
	if id.LocalName != "张三" || id.LocalEmail != "zhang@example.com" {
		t.Fatalf("local 值不对: %+v", id)
	}
	if id.Name != "张三" || !id.OK {
		t.Fatalf("生效值/OK 不对: %+v", id)
	}
	// 写的是 .git/config 而不是别处。
	raw, err := os.ReadFile(filepath.Join(root, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "zhang@example.com") {
		t.Fatalf(".git/config 里没有邮箱:\n%s", raw)
	}
	if _, err := s.Commit("/", "第一次提交", true); err != nil {
		t.Fatalf("补上身份后提交仍失败: %v", err)
	}
}

// 继承来的身份不该被当成"本仓库设的":Identity 要能把两者分开,
// 前端才能说清楚这次改动只作用于当前仓库。
func TestIdentityInheritedNotLocal(t *testing.T) {
	s, root := newTestService(t)
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[user]\n\tname = 全局\n\temail = g@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)

	id, err := s.Identity("/")
	if err != nil {
		t.Fatal(err)
	}
	if id.Name != "全局" || id.Email != "g@example.com" || !id.OK {
		t.Fatalf("生效值该来自全局: %+v", id)
	}
	if id.LocalName != "" || id.LocalEmail != "" {
		t.Fatalf("仓库级本该是空的: %+v", id)
	}
	// 身份拼得出来,提交就不该再被 ErrNoIdentity 拦(即使仓库自己没设)。
	if _, err := s.Commit("/", "继承身份提交", true); err != nil {
		t.Fatalf("继承身份下提交失败: %v", err)
	}
	_ = root
}

// Revert 缺身份时也必须先拦:git 自己会先把反向改动铺到工作区再在提交那步失败,
// 留下一个既没有 REVERT_HEAD、又脏着的工作区,补完身份重试会撞
// "your local changes would be overwritten by revert"。
func TestRevertWithoutIdentityKeepsTreeClean(t *testing.T) {
	s, root := newTestService(t)
	if _, err := s.SetIdentity("/", "张三", "zhang@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit("/", "base", true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit("/", "change", true); err != nil {
		t.Fatal(err)
	}
	head, err := s.run(root, nil, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	// 把身份撤掉,模拟"仓库没设身份也没继承"。
	if _, err := s.run(root, nil, "config", "--local", "--unset", "user.email"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.run(root, nil, "config", "--local", "--unset", "user.name"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revert("/", "revert", strings.TrimSpace(head)); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("want ErrNoIdentity, got %v", err)
	}
	st, err := s.Status("/")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Clean || st.Reverting {
		t.Fatalf("被拦下的 revert 不该动工作区: clean=%v reverting=%v", st.Clean, st.Reverting)
	}
}

// 只读模式下不给写身份 —— 它和别的写操作一样要过 allowWrite。
func TestSetIdentityReadOnly(t *testing.T) {
	s, _ := newTestService(t)
	s.readOnly = true
	if _, err := s.SetIdentity("/", "张三", "zhang@example.com"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("want ErrReadOnly, got %v", err)
	}
}

func TestCheckIdentity(t *testing.T) {
	long := strings.Repeat("a", identityMax+1)
	bad := []struct{ name, email, why string }{
		{"", "a@b.c", "空名字"},
		{"n", "", "空邮箱"},
		{"   ", "a@b.c", "全空格名字"},
		{"n", "   ", "全空格邮箱(git 会默默存成 <>)"},
		{"n\nname = x", "a@b.c", "换行(会被转义进 .git/config)"},
		{"n\tm", "a@b.c", "名字中间的制表符"},
		{"a<b>", "a@b.c", "名字里的尖括号"},
		{"n", "a<b>@c.d", "邮箱里的尖括号(git 会悄悄删掉)"},
		{"n", "a b@c.d", "邮箱里的空格"},
		{"n", "abc.d", "没有 @"},
		{long, "a@b.c", "名字过长"},
		{"n", long + "@b.c", "邮箱过长"},
	}
	for _, c := range bad {
		if _, _, err := checkIdentity(c.name, c.email); !errors.Is(err, errBadIdentity) {
			t.Errorf("%s 该被拒: got %v", c.why, err)
		}
	}
	// 这些得放过:分号/井号在 .git/config 里是注释符,但 git 写的时候会加引号;
	// 加号别名和中文名都是真实存在的用法。
	ok := []struct{ name, email string }{
		{"a;b#c", "a+tag@b.c"},
		{"张三", "zhang.san@example.com.cn"},
		{"-n", "-a@b.c"},
	}
	for _, c := range ok {
		n, e, err := checkIdentity(c.name, c.email)
		if err != nil {
			t.Errorf("%q/%q 不该被拒: %v", c.name, c.email, err)
			continue
		}
		if n != c.name || e != c.email {
			t.Errorf("规范化改了值: %q/%q -> %q/%q", c.name, c.email, n, e)
		}
	}
}

// 以 - 开头的值必须能原样写进去:git config 的 -- 分隔符就是为这个加的。
func TestSetIdentityDashValue(t *testing.T) {
	s, _ := newTestService(t)
	id, err := s.SetIdentity("/", "-n", "-a@b.c")
	if err != nil {
		t.Fatalf("以 - 开头的值应被当作值而不是选项: %v", err)
	}
	if id.LocalName != "-n" || id.LocalEmail != "-a@b.c" {
		t.Fatalf("值被改了: %+v", id)
	}
}

// 前端认的是状态码而不是错误文案(client.ts 里的 GIT_NO_IDENTITY = 428),
// 所以这个映射得钉住:掉回 500 的话弹框就不会出现,用户只看到一句"提交失败"。
func TestNoIdentityStatusIs428(t *testing.T) {
	s, _ := newTestService(t)
	h := NewHandlers(s)
	body := `{"path":"/","message":"msg","addAll":true}`
	rec := httptest.NewRecorder()
	h.Commit(rec, httptest.NewRequest(http.MethodPost, "/api/git/commit", strings.NewReader(body)))
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("want 428, got %d (%s)", rec.Code, rec.Body.String())
	}

	// 身份写好之后同一个请求就该过。
	if _, err := s.SetIdentity("/", "张三", "zhang@example.com"); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.Commit(rec, httptest.NewRequest(http.MethodPost, "/api/git/commit", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// 校验不通过是 400 而不是 428:重试解决不了,前端不该再弹一次同样的框。
func TestSetIdentityBadInputIs400(t *testing.T) {
	s, _ := newTestService(t)
	h := NewHandlers(s)
	rec := httptest.NewRecorder()
	h.SetIdentity(rec, httptest.NewRequest(http.MethodPost, "/api/git/identity",
		strings.NewReader(`{"path":"/","name":"n","email":"no-at-sign"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (%s)", rec.Code, rec.Body.String())
	}
}
