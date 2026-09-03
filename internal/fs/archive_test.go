package fs

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ulikunitz/xz"
)

// archiveKind 的顺序是有坑的:.tar.gz 必须在 .gz 之前判掉,否则整包会被当成
// 单文件压缩解成一个 tar。把这条和几个边界钉住。
func TestArchiveKind(t *testing.T) {
	cases := []struct {
		name, mode, comp string
		ok               bool
	}{
		{"x.zip", "zip", "", true},
		{"x.ZIP", "zip", "", true},
		{"x.tar", "tar", "", true},
		{"x.tar.gz", "tar", "gz", true},
		{"x.tgz", "tar", "gz", true},
		{"x.tar.bz2", "tar", "bz2", true},
		{"x.tbz2", "tar", "bz2", true},
		{"x.tar.xz", "tar", "xz", true},
		{"x.txz", "tar", "xz", true},
		{"x.rar", "rar", "", true},
		{"x.7z", "7z", "", true},
		{"note.txt.gz", "single", "gz", true},
		{"note.txt.bz2", "single", "bz2", true},
		{"note.txt.xz", "single", "xz", true},
		{"note.txt", "", "", false},
		{"x.gzip", "", "", false},
	}
	for _, c := range cases {
		mode, comp, ok := archiveKind(c.name)
		if mode != c.mode || comp != c.comp || ok != c.ok {
			t.Errorf("archiveKind(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.name, mode, comp, ok, c.mode, c.comp, c.ok)
		}
	}
}

// 压缩→解压走一圈。重点有三个:包内每项以自己的名字为顶层、可执行位没丢、
// 包落在被压缩的目录里时不会把正在写的自己装进去。
func TestCreateArchiveFileRoundTrip(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "src", "sub"))
	mustWrite(t, filepath.Join(root, "src", "a.txt"), "aaa", 0o644)
	mustWrite(t, filepath.Join(root, "src", "sub", "run.sh"), "#!/bin/sh\n", 0o755)
	mustWrite(t, filepath.Join(root, "top.txt"), "ttt", 0o644)

	s := NewService(root, false)
	if err := s.CreateArchiveFile("out.zip", []string{"src", "top.txt"}); err != nil {
		t.Fatalf("CreateArchiveFile: %v", err)
	}
	names := zipNames(t, filepath.Join(root, "out.zip"))
	for _, want := range []string{"src/", "src/a.txt", "src/sub/", "src/sub/run.sh", "top.txt"} {
		if !names[want] {
			t.Errorf("包里缺 %q(实际:%v)", want, names)
		}
	}
	// 临时包自己不能出现在包里(它就在同一个目录下被 Walk 扫到)。
	for n := range names {
		if filepath.Ext(n) == ".tmp" {
			t.Errorf("临时文件被装进包里: %q", n)
		}
	}

	if err := s.ExtractArchive("dest", "out.zip"); err != nil {
		t.Fatalf("ExtractArchive: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "dest", "src", "a.txt")); got != "aaa" {
		t.Errorf("dest/src/a.txt = %q, want %q", got, "aaa")
	}
	if got := readFile(t, filepath.Join(root, "dest", "top.txt")); got != "ttt" {
		t.Errorf("dest/top.txt = %q, want %q", got, "ttt")
	}
	fi, err := os.Stat(filepath.Join(root, "dest", "src", "sub", "run.sh"))
	if err != nil {
		t.Fatalf("stat run.sh: %v", err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("run.sh 解出来丢了可执行位: %v", fi.Mode())
	}
}

// 每种压得出来的格式都压一遍再解一遍:内容、层级、可执行位都得原样回来。
// 后缀决定格式,所以这张表同时也钉住了"名字和内容对得上"。
func TestCreateArchiveFileFormats(t *testing.T) {
	for _, name := range []string{"p.zip", "p.tar", "p.tar.gz", "p.tgz", "p.tar.xz", "p.txz"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			mustMkdir(t, filepath.Join(root, "src", "sub"))
			mustWrite(t, filepath.Join(root, "src", "a.txt"), "aaa", 0o644)
			mustWrite(t, filepath.Join(root, "src", "sub", "run.sh"), "#!/bin/sh\n", 0o755)
			s := NewService(root, false)
			if err := s.CreateArchiveFile(name, []string{"src"}); err != nil {
				t.Fatalf("压 %s: %v", name, err)
			}
			if err := s.ExtractArchive("dest", name); err != nil {
				t.Fatalf("解 %s: %v", name, err)
			}
			if got := readFile(t, filepath.Join(root, "dest", "src", "a.txt")); got != "aaa" {
				t.Errorf("dest/src/a.txt = %q, want %q", got, "aaa")
			}
			fi, err := os.Stat(filepath.Join(root, "dest", "src", "sub", "run.sh"))
			if err != nil {
				t.Fatalf("stat run.sh: %v", err)
			}
			if fi.Mode().Perm()&0o111 == 0 {
				t.Errorf("run.sh 丢了可执行位: %v", fi.Mode())
			}
		})
	}
}

// 能解不能压的那几种:bz2 没有编码器,7z/rar 没有纯 Go 写入实现,单文件压缩装不下多项。
// 宁可明确报错,也不要压出一个后缀是 .7z 的 zip。
func TestCreateArchiveFileRejectsUnwritableFormats(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "a", 0o644)
	s := NewService(root, false)
	for _, name := range []string{"x.tar.bz2", "x.tbz2", "x.7z", "x.rar", "x.gz", "x.xz", "x.bin", "x"} {
		if err := s.CreateArchiveFile(name, []string{"a.txt"}); !errors.Is(err, ErrBadQuery) {
			t.Errorf("压成 %s 应报 ErrBadQuery,得到 %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("拒掉 %s 之后不该留下文件", name)
		}
	}
	// 临时文件也不该留在目录里。
	for _, e := range dirNames(t, root) {
		if filepath.Ext(e) == ".tmp" {
			t.Errorf("残留临时文件: %q", e)
		}
	}
}

// 顶层重名的两项在包里会撞车,宁可整个拒掉也不要压出一个"少一项"的包。
func TestCreateArchiveFileRejectsDuplicateNames(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "a"))
	mustMkdir(t, filepath.Join(root, "b"))
	mustWrite(t, filepath.Join(root, "a", "same.txt"), "1", 0o644)
	mustWrite(t, filepath.Join(root, "b", "same.txt"), "2", 0o644)
	s := NewService(root, false)
	if err := s.CreateArchiveFile("out.zip", []string{"a/same.txt", "b/same.txt"}); !errors.Is(err, ErrBadQuery) {
		t.Fatalf("重名应报 ErrBadQuery,得到 %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "out.zip")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("拒掉之后不该留下包文件")
	}
}

// tar 系列 + 单文件压缩:每种格式解出来都得能读到原内容。
// bz2 没有标准库编码器,用一段现成的包(内容 "hello bz2\n")覆盖它的解码路径。
func TestExtractFormats(t *testing.T) {
	const bz2Base64 = "QlpoOTFBWSZTWfKFJPAAAALZgAAQQAAQABJEgBAgADEGTEEA09JY9EOH4u5IpwoSHlCkngA="
	root := t.TempDir()
	s := NewService(root, false)

	writeTar := func(name string, wrap func(*bytes.Buffer) (*bytes.Buffer, error)) {
		var body bytes.Buffer
		tw := tar.NewWriter(&body)
		if err := tw.WriteHeader(&tar.Header{Name: "d/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			t.Fatal(err)
		}
		if err := tw.WriteHeader(&tar.Header{Name: "d/f.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 3}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("xyz")); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		out := &body
		if wrap != nil {
			var err error
			if out, err = wrap(&body); err != nil {
				t.Fatal(err)
			}
		}
		mustWrite(t, filepath.Join(root, name), out.String(), 0o644)
	}
	gzWrap := func(b *bytes.Buffer) (*bytes.Buffer, error) {
		var out bytes.Buffer
		zw := gzip.NewWriter(&out)
		if _, err := zw.Write(b.Bytes()); err != nil {
			return nil, err
		}
		return &out, zw.Close()
	}
	xzWrap := func(b *bytes.Buffer) (*bytes.Buffer, error) {
		var out bytes.Buffer
		zw, err := xz.NewWriter(&out)
		if err != nil {
			return nil, err
		}
		if _, err := zw.Write(b.Bytes()); err != nil {
			return nil, err
		}
		return &out, zw.Close()
	}
	writeTar("p.tar", nil)
	writeTar("p.tar.gz", gzWrap)
	writeTar("p.tar.xz", xzWrap)
	for _, name := range []string{"p.tar", "p.tar.gz", "p.tar.xz"} {
		dest := "out-" + name
		if err := s.ExtractArchive(dest, name); err != nil {
			t.Fatalf("解压 %s: %v", name, err)
		}
		if got := readFile(t, filepath.Join(root, dest, "d", "f.txt")); got != "xyz" {
			t.Errorf("%s 解出的内容 = %q, want %q", name, got, "xyz")
		}
	}

	// 单文件压缩:包里没有文件名,去掉压缩后缀就是名字。
	blob, err := base64.StdEncoding.DecodeString(bz2Base64)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "note.txt.bz2"), string(blob), 0o644)
	if err := s.ExtractArchive("out-bz2", "note.txt.bz2"); err != nil {
		t.Fatalf("解压 bz2: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "out-bz2", "note.txt")); got != "hello bz2\n" {
		t.Errorf("bz2 解出的内容 = %q", got)
	}

	var gzOne bytes.Buffer
	zw := gzip.NewWriter(&gzOne)
	if _, err := zw.Write([]byte("plain")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "one.txt.gz"), gzOne.String(), 0o644)
	if err := s.ExtractArchive("out-gz", "one.txt.gz"); err != nil {
		t.Fatalf("解压 gz: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "out-gz", "one.txt")); got != "plain" {
		t.Errorf("gz 解出的内容 = %q", got)
	}

	// 认不出的格式要明确报错,而不是拿别的解码器去猜。
	mustWrite(t, filepath.Join(root, "x.bin"), "?", 0o644)
	if err := s.ExtractArchive("out-bin", "x.bin"); !errors.Is(err, ErrBadQuery) {
		t.Errorf("未知格式应报 ErrBadQuery,得到 %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "out-bin")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("认不出格式时不该先建好目标目录")
	}
}

// zip-slip:包内 ../ 路径必须被拦住,而不是写到目标目录外面去。
func TestExtractZipSlip(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("../evil.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("pwned")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "evil.zip"), buf.String(), 0o644)
	s := NewService(root, false)
	if err := s.ExtractArchive("dest", "evil.zip"); !errors.Is(err, errEscape) {
		t.Fatalf("应报 errEscape,得到 %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "evil.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("目标目录外面被写出了文件")
	}
}

// 只读模式下压缩和解压都是写操作,一律拒绝。
func TestArchiveReadOnly(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "a", 0o644)
	s := NewService(root, true)
	if err := s.CreateArchiveFile("out.zip", []string{"a.txt"}); err == nil {
		t.Error("只读模式下 CreateArchiveFile 应报错")
	}
	if err := s.ExtractArchive("dest", "out.zip"); err == nil {
		t.Error("只读模式下 ExtractArchive 应报错")
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, body string, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), perm); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func zipNames(t *testing.T, p string) map[string]bool {
	t.Helper()
	r, err := zip.OpenReader(p)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	names := map[string]bool{}
	for _, f := range r.File {
		names[f.Name] = true
	}
	return names
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}
