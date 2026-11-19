package git

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fullDiffContent(version string) string {
	lines := make([]string, 31)
	for i := range lines {
		lines[i] = fmt.Sprintf("  unchanged line %02d", i+1)
	}
	lines[15] = version
	return strings.Join(lines, "\n") + "\n"
}

type fullDiffFixture struct {
	svc       *Service
	root      string
	initial   string
	committed string
	run       func([]string, ...string) string
}

func newFullDiffFixture(t *testing.T) fullDiffFixture {
	t.Helper()
	root, run := historyTestRepo(t)
	run(nil, "config", "diff.context", "3")
	write := func(version string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "demo.txt"), []byte(fullDiffContent(version)), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("base version")
	run(nil, "add", "demo.txt")
	run(nil, "commit", "-m", "base")
	initial := run(nil, "rev-parse", "HEAD")
	write("committed version")
	run(nil, "commit", "-am", "change")
	committed := run(nil, "rev-parse", "HEAD")
	write("staged version")
	run(nil, "add", "demo.txt")
	write("worktree version")
	return fullDiffFixture{
		svc:  New(root, true, func(string) (string, error) { return root, nil }),
		root: root, initial: initial, committed: committed, run: run,
	}
}

func TestFullDiffPreservesScopeAndExpandsEveryContextLine(t *testing.T) {
	f := newFullDiffFixture(t)
	before := f.run(nil, "status", "--porcelain=v1")
	for _, tc := range []struct{ scope, ref, old, new string }{
		{"worktree", "", "staged version", "worktree version"},
		{"staged", "", "committed version", "staged version"},
		{"head", "", "committed version", "worktree version"},
		{"commit", f.committed, "base version", "committed version"},
	} {
		t.Run(tc.scope, func(t *testing.T) {
			partial, err := f.svc.Diff(f.root, tc.scope, "demo.txt", tc.ref)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(partial, "unchanged line 01") || strings.Contains(partial, "unchanged line 31") {
				t.Fatalf("default diff unexpectedly expanded the whole file: %s", partial)
			}
			full, err := f.svc.FullDiff(f.root, tc.scope, "demo.txt", tc.ref)
			if err != nil {
				t.Fatal(err)
			}
			for i := 1; i <= 31; i++ {
				if i != 16 && !strings.Contains(full, fmt.Sprintf("   unchanged line %02d", i)) {
					t.Fatalf("full diff omitted unchanged line %d: %s", i, full)
				}
			}
			if !strings.Contains(full, "\n-"+tc.old+"\n+"+tc.new+"\n") || strings.Count(full, "\n@@ ") != 1 {
				t.Fatalf("wrong comparison versions or incomplete hunk: %s", full)
			}
		})
	}
	if after := f.run(nil, "status", "--porcelain=v1"); after != before {
		t.Fatalf("read-only comparison changed repository status: %q -> %q", before, after)
	}
}

func TestFullDiffRootAddedDeletedAndBinary(t *testing.T) {
	f := newFullDiffFixture(t)
	rootDiff, err := f.svc.FullDiff(f.root, "commit", "demo.txt", f.initial)
	if err != nil || !strings.Contains(rootDiff, "+  unchanged line 01") || !strings.Contains(rootDiff, "+  unchanged line 31") {
		t.Fatalf("root commit = %q, %v", rootDiff, err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "added.txt"), []byte(fullDiffContent("added version")), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "image.bin"), []byte("\x00binary\x00"), 0644); err != nil {
		t.Fatal(err)
	}
	f.run(nil, "add", "added.txt", "image.bin")
	added, err := f.svc.FullDiff(f.root, "staged", "added.txt", "")
	if err != nil || !strings.Contains(added, "+  unchanged line 01") || !strings.Contains(added, "+  unchanged line 31") {
		t.Fatalf("added file = %q, %v", added, err)
	}
	binary, err := f.svc.FullDiff(f.root, "staged", "image.bin", "")
	if err != nil || !strings.Contains(binary, "Binary files") || strings.ContainsRune(binary, 0) {
		t.Fatalf("binary file = %q, %v", binary, err)
	}
	if err := os.Remove(filepath.Join(f.root, "demo.txt")); err != nil {
		t.Fatal(err)
	}
	f.run(nil, "add", "demo.txt")
	deleted, err := f.svc.FullDiff(f.root, "staged", "demo.txt", "")
	if err != nil || !strings.Contains(deleted, "-  unchanged line 01") || !strings.Contains(deleted, "-  unchanged line 31") {
		t.Fatalf("deleted file = %q, %v", deleted, err)
	}
}

func TestFullDiffHTTPFlagAndValidation(t *testing.T) {
	f := newFullDiffFixture(t)
	h := NewHandlers(f.svc)
	for _, full := range []string{"", "0", "1"} {
		rec := httptest.NewRecorder()
		h.Diff(rec, httptest.NewRequest("GET", "/api/git/diff?"+url.Values{
			"path": {f.root}, "scope": {"staged"}, "file": {"demo.txt"}, "full": {full},
		}.Encode(), nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Fatalf("full=%q HTTP = %d %s", full, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "unchanged line 01") != (full == "1") {
			t.Fatalf("full=%q did not select the requested context: %s", full, rec.Body.String())
		}
	}
	for _, file := range []string{"", ".", "/"} {
		rec := httptest.NewRecorder()
		h.Diff(rec, httptest.NewRequest("GET", "/api/git/diff?"+url.Values{
			"path": {f.root}, "file": {file}, "full": {"1"},
		}.Encode(), nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("full comparison without a file = %d %s", rec.Code, rec.Body.String())
		}
	}
	if _, err := f.svc.FullDiff(f.root, "commit", "demo.txt", "--output=bad"); err != errBadCommitArg {
		t.Fatalf("unsafe full comparison ref = %v", err)
	}
}
