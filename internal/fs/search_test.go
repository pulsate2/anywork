package fs

import (
	"os"
	"path/filepath"
	"testing"
)

// 搜索改成原生实现后,命中位置(UTF-16 下标)和文件名/内容两种模式都得钉住:
// 前端直接用 col/len 去 slice,一旦按字节算就会在中文行上错位。
func TestSearchAndReplace(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("alpha.txt", "hello world\n中文abc中文\n")
	write("sub/beta.go", "package main // TODO: hello\n")
	write("sub/.git/config", "hello\n")
	write("bin.dat", "a\x00hello\n")

	s := NewService(root, false)

	t.Run("name", func(t *testing.T) {
		out, err := s.Search(SearchOptions{Dir: "/", Query: "beta", Content: false, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Results) != 1 || out.Results[0].Rel != "sub/beta.go" {
			t.Fatalf("got %+v", out.Results)
		}
	})

	t.Run("content", func(t *testing.T) {
		out, err := s.Search(SearchOptions{Dir: "/", Query: "hello", Content: true, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		// 二进制文件与 .git 目录都要跳过,只剩 alpha.txt 与 sub/beta.go。
		if len(out.Results) != 2 {
			t.Fatalf("want 2 hits, got %+v", out.Results)
		}
		for _, r := range out.Results {
			if r.Line != 1 || len(r.Matches) != 1 {
				t.Errorf("bad hit %+v", r)
			}
			if r.Path == "" || filepath.IsAbs(r.Rel) {
				t.Errorf("bad path %+v", r)
			}
		}
	})

	t.Run("utf16 offset", func(t *testing.T) {
		out, err := s.Search(SearchOptions{Dir: "/", Query: "abc", Content: true, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Results) != 1 {
			t.Fatalf("want 1 hit, got %+v", out.Results)
		}
		m := out.Results[0].Matches
		// "中文abc中文":JS 里 abc 从下标 2 开始,不是字节偏移 6。
		if len(m) != 1 || m[0].Col != 2 || m[0].Len != 3 {
			t.Fatalf("want col=2 len=3, got %+v", m)
		}
	})

	t.Run("case sensitive", func(t *testing.T) {
		out, err := s.Search(SearchOptions{Dir: "/", Query: "TODO", Content: true, Case: true, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Results) != 1 {
			t.Fatalf("want 1 hit, got %+v", out.Results)
		}
		if out, _ = s.Search(SearchOptions{Dir: "/", Query: "todo", Content: true, Case: true, Limit: 50}); len(out.Results) != 0 {
			t.Fatalf("want 0 hits, got %+v", out.Results)
		}
	})

	t.Run("literal not regex", func(t *testing.T) {
		// 字面量模式下 "h.llo" 不该匹配 "hello"。
		out, err := s.Search(SearchOptions{Dir: "/", Query: "h.llo", Content: true, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Results) != 0 {
			t.Fatalf("want 0 hits, got %+v", out.Results)
		}
		if out, err = s.Search(SearchOptions{Dir: "/", Query: "h.llo", Content: true, Regex: true, Limit: 50}); err != nil {
			t.Fatal(err)
		} else if len(out.Results) != 2 {
			t.Fatalf("want 2 hits, got %+v", out.Results)
		}
	})

	t.Run("bad query", func(t *testing.T) {
		if _, err := s.Search(SearchOptions{Dir: "/", Query: ""}); err == nil {
			t.Error("empty query should fail")
		}
		if _, err := s.Search(SearchOptions{Dir: "/", Query: "a(", Regex: true}); err == nil {
			t.Error("invalid regex should fail")
		}
	})

	t.Run("replace", func(t *testing.T) {
		res, err := s.Replace(ReplaceOptions{
			Files: []string{"/alpha.txt", "/sub/beta.go", "/bin.dat"},
			Query: "hello", Replace: "HI",
		})
		if err != nil {
			t.Fatal(err)
		}
		// 二进制文件不动。
		if res.Files != 2 || res.Count != 2 {
			t.Fatalf("got %+v", res)
		}
		body, _ := os.ReadFile(filepath.Join(root, "alpha.txt"))
		if string(body) != "HI world\n中文abc中文\n" {
			t.Fatalf("got %q", body)
		}
		body, _ = os.ReadFile(filepath.Join(root, "bin.dat"))
		if string(body) != "a\x00hello\n" {
			t.Fatalf("binary file was rewritten: %q", body)
		}
	})

	t.Run("readonly blocks replace", func(t *testing.T) {
		ro := NewService(root, true)
		if _, err := ro.Replace(ReplaceOptions{Files: []string{"/alpha.txt"}, Query: "HI", Replace: "x"}); err == nil {
			t.Error("readonly should reject replace")
		}
	})
}
