package git

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type historyFixture struct {
	svc    *Service
	root   string
	hashes map[string]string
}

func historyTestRepo(t *testing.T) (string, func([]string, ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	run := func(env []string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(root, "no-global"),
			"GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@example.com",
			"GIT_COMMITTER_NAME=Alice", "GIT_COMMITTER_EMAIL=alice@example.com")
		cmd.Env = append(cmd.Env, env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(nil, "init", "--initial-branch=main")
	run(nil, "config", "commit.gpgsign", "false")
	return root, run
}

func newHistoryFixture(t *testing.T) historyFixture {
	t.Helper()
	root, run := historyTestRepo(t)
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(subject, body, name, email, date string) string {
		t.Helper()
		run(nil, "add", "-A")
		env := []string{"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email,
			"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
		run(env, "commit", "-m", subject, "-m", body)
		return run(nil, "rev-parse", "HEAD")
	}
	hashes := map[string]string{}
	write("src/hello.ts", "first\n")
	write("中文 文档.md", "one\n")
	hashes["root"] = commit("initial commit", "说明第一行\n说明第二行", "Alice", "alice@example.com", "2026-01-01T08:00:00+08:00")
	run(nil, "tag", "v1")
	run(nil, "checkout", "-b", "feature")
	write("src/feature.rs", "feature\n")
	hashes["feature"] = commit("feat: feature branch", "", "Bob", "bob@example.com", "2026-01-03T08:00:00+08:00")
	run(nil, "checkout", "main")
	write("src/main.go", "main\n")
	hashes["main"] = commit("feat: main branch", "", "Alice", "myalice@example.com", "2026-01-02T08:00:00+08:00")
	run([]string{"GIT_AUTHOR_DATE=2026-01-04T08:00:00+08:00", "GIT_COMMITTER_DATE=2026-01-04T08:00:00+08:00"},
		"merge", "--no-ff", "feature", "-m", "Merge feature")
	hashes["merge"] = run(nil, "rev-parse", "HEAD")
	write("docs/.gitkeep", "")
	run(nil, "mv", "中文 文档.md", "docs/中文 新文档.md")
	if err := os.Remove(filepath.Join(root, "docs/.gitkeep")); err != nil {
		t.Fatal(err)
	}
	write("image.bin", "\x00\x01binary\x00")
	hashes["rename"] = commit("fix: move docs.*", "", "Bob", "bob@example.com", "2026-01-05T08:00:00+08:00")
	svc := New(root, true, func(string) (string, error) { return root, nil })
	return historyFixture{svc: svc, root: root, hashes: hashes}
}

func TestLogMetadataTopologyAndPagination(t *testing.T) {
	f := newHistoryFixture(t)
	rows, err := f.svc.Log(f.root, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want 5", len(rows))
	}
	if rows[0].Hash != f.hashes["rename"] || rows[0].Author != "Bob" || rows[0].Email != "bob@example.com" ||
		rows[0].Date != "2026-01-05T08:00:00+08:00" || !reflect.DeepEqual(rows[0].Refs, []string{"main"}) {
		t.Fatalf("metadata = %+v", rows[0])
	}
	index := map[string]int{}
	for i, row := range rows {
		index[row.Hash] = i
	}
	for i, row := range rows {
		for _, parent := range row.Parents {
			if len(parent) != 40 || index[parent] <= i {
				t.Fatalf("parent %q of row %d is not a full hash later in topo order", parent, i)
			}
		}
	}
	root := rows[index[f.hashes["root"]]]
	if root.Parents == nil || len(root.Parents) != 0 || !reflect.DeepEqual(root.Refs, []string{"tag: v1"}) {
		t.Fatalf("root = %+v", root)
	}
	first, _ := f.svc.Log(f.root, 2, 0)
	second, _ := f.svc.Log(f.root, 2, 2)
	last, _ := f.svc.Log(f.root, 2, 4)
	paged := append(append(first, second...), last...)
	if !reflect.DeepEqual(paged, rows) {
		t.Fatal("pagination does not match the complete log")
	}
}

func TestLogSearchAndFilters(t *testing.T) {
	f := newHistoryFixture(t)
	tests := []struct {
		name string
		opt  LogOptions
		want []string
	}{
		{"search past first page", LogOptions{Search: "INITIAL"}, []string{f.hashes["root"]}},
		{"literal grep", LogOptions{Search: "docs.*"}, []string{f.hashes["rename"]}},
		{"not regexp", LogOptions{Search: "move.*"}, nil},
		{"short hash", LogOptions{Search: f.hashes["feature"][:7]}, []string{f.hashes["feature"]}},
		{"full hash", LogOptions{Search: f.hashes["root"]}, []string{f.hashes["root"]}},
		{"branch ancestry", LogOptions{Ref: "feature"}, []string{f.hashes["feature"], f.hashes["root"]}},
		{"hash outside branch", LogOptions{Ref: "feature", Search: f.hashes["rename"]}, nil},
		{"exact author email", LogOptions{Author: "alice@example.com"}, []string{f.hashes["merge"], f.hashes["root"]}},
		{"combined filters", LogOptions{Ref: "main", Author: "bob@example.com", Search: "feature"}, []string{f.hashes["feature"]}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := f.svc.LogFiltered(f.root, 50, 0, tc.opt)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, row := range rows {
				got = append(got, row.Hash)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("hashes = %v, want %v", got, tc.want)
			}
		})
	}
	rows, err := f.svc.LogFiltered(f.root, 30, 1, LogOptions{Search: f.hashes["root"]})
	if err != nil || len(rows) != 0 {
		t.Fatalf("hash page 2 = %v, %v", rows, err)
	}
	if _, err := f.svc.LogFiltered(f.root, 30, 0, LogOptions{Ref: "--output=bad"}); err != errBadRefArg {
		t.Fatalf("option injection error = %v", err)
	}
	authors, err := f.svc.Authors(f.root)
	if err != nil || len(authors) != 3 {
		t.Fatalf("authors = %v, %v", authors, err)
	}
}

func TestCommitDetailRootRenameBinaryAndMerge(t *testing.T) {
	f := newHistoryFixture(t)
	root, err := f.svc.Detail(f.root, f.hashes["root"])
	if err != nil {
		t.Fatal(err)
	}
	if root.Body != "说明第一行\n说明第二行" || len(root.Files) != 2 {
		t.Fatalf("root detail = %+v", root)
	}
	for _, file := range root.Files {
		if file.Add != 1 || file.Del != 0 {
			t.Fatalf("root stat = %+v", file)
		}
	}
	chineseDiff, err := f.svc.Diff(f.root, "commit", "中文 文档.md", f.hashes["root"])
	if err != nil || !strings.Contains(chineseDiff, "diff --git a/中文 文档.md b/中文 文档.md") {
		t.Fatalf("Chinese file diff = %q, %v", chineseDiff, err)
	}
	rename, err := f.svc.Detail(f.root, f.hashes["rename"])
	if err != nil {
		t.Fatal(err)
	}
	if len(rename.Files) != 2 {
		t.Fatalf("rename files = %+v", rename.Files)
	}
	if rename.Files[0].Path != "docs/中文 新文档.md" || rename.Files[0].Orig != "中文 文档.md" || !rename.Files[1].Binary {
		t.Fatalf("rename/binary = %+v", rename.Files)
	}
	merge, err := f.svc.Detail(f.root, f.hashes["merge"])
	if err != nil {
		t.Fatal(err)
	}
	if len(merge.Parents) != 2 || len(merge.Files) != 1 || merge.Files[0].Path != "src/feature.rs" {
		t.Fatalf("merge detail = %+v", merge)
	}
	diff, err := f.svc.Diff(f.root, "commit", "src/feature.rs", f.hashes["merge"])
	if err != nil || !strings.Contains(diff, "diff --git a/src/feature.rs b/src/feature.rs") {
		t.Fatalf("merge diff = %q, %v", diff, err)
	}
	if _, err := f.svc.Detail(f.root, "--output=bad"); err != errBadCommitArg {
		t.Fatalf("unsafe detail ref = %v", err)
	}
	if _, err := f.svc.Diff(f.root, "commit", "", "--output=bad"); err != errBadCommitArg {
		t.Fatalf("unsafe diff ref = %v", err)
	}
}

func TestParseCommitNumstat(t *testing.T) {
	out := "4\t2\tpath with spaces.ts\x000\t0\t\x00old.md\x00new.md\x00-\t-\timage.png\x007\t1\tfile\twithtab\x00"
	want := []CommitFile{
		{Path: "path with spaces.ts", Add: 4, Del: 2},
		{Path: "new.md", Orig: "old.md"},
		{Path: "image.png", Binary: true},
		{Path: "file\twithtab", Add: 7, Del: 1},
	}
	if got := parseCommitNumstat(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("stats = %+v, want %+v", got, want)
	}
}

func TestEmptyRepositoryHistory(t *testing.T) {
	root, _ := historyTestRepo(t)
	svc := New(root, true, func(string) (string, error) { return root, nil })
	rows, err := svc.Log(root, 30, 0)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("empty log = %v, %v", rows, err)
	}
	authors, err := svc.Authors(root)
	if err != nil || authors == nil || len(authors) != 0 {
		t.Fatalf("empty authors = %v, %v", authors, err)
	}
}

func TestHistoryHTTPReadEndpoints(t *testing.T) {
	f := newHistoryFixture(t)
	h := NewHandlers(f.svc)
	request := httptest.NewRequest("GET", "/api/git/log?"+url.Values{
		"path": {f.root}, "n": {"1"}, "search": {"initial"},
	}.Encode(), nil)
	rec := httptest.NewRecorder()
	h.Log(rec, request)
	var rows []CommitNode
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil || rec.Code != 200 || len(rows) != 1 || rows[0].Hash != f.hashes["root"] {
		t.Fatalf("log HTTP = %d %s, %v", rec.Code, rec.Body.String(), err)
	}
	rec = httptest.NewRecorder()
	h.CommitDetail(rec, httptest.NewRequest("GET", "/api/git/commit?"+url.Values{
		"path": {f.root}, "ref": {f.hashes["merge"]},
	}.Encode(), nil))
	var detail CommitDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil || rec.Code != 200 || len(detail.Files) != 1 {
		t.Fatalf("detail HTTP = %d %s, %v", rec.Code, rec.Body.String(), err)
	}
	rec = httptest.NewRecorder()
	h.Authors(rec, httptest.NewRequest("GET", "/api/git/authors?"+url.Values{"path": {f.root}}.Encode(), nil))
	var authors []CommitAuthor
	if err := json.Unmarshal(rec.Body.Bytes(), &authors); err != nil || rec.Code != 200 || len(authors) != 3 {
		t.Fatalf("authors HTTP = %d %s, %v", rec.Code, rec.Body.String(), err)
	}
}
