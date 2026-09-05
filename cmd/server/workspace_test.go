package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lightremote/internal/config"
	"lightremote/internal/db"
	fsvc "lightremote/internal/fs"
)

// 新建工作区这条路上要区分四种路径状态,而"目录不存在"和"存在但是个文件"从前是同一个
// 400 —— 前端因此没法只对前者提议创建。这些用例把区分钉住,顺带钉住只读模式不建目录。
func newWorkspaceApp(t *testing.T) (*App, string) {
	t.Helper()
	root := t.TempDir()
	// 每个用例一个独立 SQLite 文件,互不干扰。
	database, err := db.Open(t.TempDir(), "")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	cfg := &config.Config{Root: root}
	return &App{cfg: cfg, db: database.DB, fsSvc: fsvc.NewService(root, false)}, root
}

func createWorkspace(t *testing.T, a *App, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	a.handleCreateWorkspace(rec, httptest.NewRequest(http.MethodPost, "/api/workspaces", strings.NewReader(body)))
	return rec
}

// 目录不存在且没说要建:428,而且不能偷偷把目录建出来 —— 用户还没答应。
func TestCreateWorkspaceMissingDirIs428(t *testing.T) {
	a, root := newWorkspaceApp(t)
	rec := createWorkspace(t, a, `{"name":"新项目","path":"/new/deep"}`)
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("want 428, got %d (%s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(err) {
		t.Fatalf("428 时不该创建目录: err=%v", err)
	}
	// 也不该留下一条工作区记录。
	var n int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM workspaces`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("428 时不该落库: %d 条", n)
	}
}

// 用户点了"创建并继续":同一个请求带上 create 重放,目录建出来(含缺失的上级),工作区建好。
func TestCreateWorkspaceCreatesDir(t *testing.T) {
	a, root := newWorkspaceApp(t)
	rec := createWorkspace(t, a, `{"name":"新项目","path":"/new/deep","create":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	fi, err := os.Stat(filepath.Join(root, "new", "deep"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("目录该被建出来: %v", err)
	}
	var p string
	if err := a.db.QueryRow(`SELECT path FROM workspaces`).Scan(&p); err != nil {
		t.Fatal(err)
	}
	if p != filepath.ToSlash(filepath.Join(root, "new", "deep")) {
		t.Fatalf("落库路径不对: %q", p)
	}
}

// 路径是个文件:400 而不是 428。重试或者"帮你建"都解决不了,不该弹创建确认框。
func TestCreateWorkspaceFilePathIs400(t *testing.T) {
	a, root := newWorkspaceApp(t)
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"name":"n","path":"/f.txt"}`,
		// 带上 create 也一样:文件挡在那儿,MkdirAll 也建不出目录。
		`{"name":"n","path":"/f.txt","create":true}`,
	} {
		rec := createWorkspace(t, a, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d (%s)", body, rec.Code, rec.Body.String())
		}
	}
}

// 路径中间某段是文件(ENOTDIR):同样是 400,别提议创建。
func TestCreateWorkspaceNotDirParentIs400(t *testing.T) {
	a, root := newWorkspaceApp(t)
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := createWorkspace(t, a, `{"name":"n","path":"/f.txt/sub"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// 目录本来就在:一次就成,和从前一样。
func TestCreateWorkspaceExistingDir(t *testing.T) {
	a, root := newWorkspaceApp(t)
	if err := os.Mkdir(filepath.Join(root, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if rec := createWorkspace(t, a, `{"name":"n","path":"/proj"}`); rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	// 同一路径再建一次是 409(path 是唯一键)。
	if rec := createWorkspace(t, a, `{"name":"n2","path":"/proj"}`); rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// 只读模式:目录不存在就是 400 而不是 428 —— 这里建不出目录,弹框问了也白问。
func TestCreateWorkspaceReadOnlyMissingDirIs400(t *testing.T) {
	a, root := newWorkspaceApp(t)
	a.cfg.ReadOnly = true
	a.fsSvc = fsvc.NewService(root, true)
	rec := createWorkspace(t, a, `{"name":"n","path":"/new"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	// 就算前端硬带 create 也不许建。
	rec = createWorkspace(t, a, `{"name":"n","path":"/new","create":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("带 create 时 want 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(err) {
		t.Fatalf("只读模式不该创建目录: err=%v", err)
	}
}

// 越界路径仍然先被 root 边界挡掉,不会走到"要不要建"那一步。
func TestCreateWorkspaceOutsideRoot(t *testing.T) {
	a, _ := newWorkspaceApp(t)
	rec := createWorkspace(t, a, `{"name":"n","path":"../etc"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (%s)", rec.Code, rec.Body.String())
	}
}
