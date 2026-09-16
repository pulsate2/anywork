package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 指纹:同树同指纹 / 内容变化 / mtime 变化 / excludes 变化。
func TestComputeFingerprint(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}
	write("a.txt", "hello")
	write("sub/b.txt", "world")

	scan := func(excl []string) []fileStamp {
		m := newIgnoreMatcher(excl)
		var out []fileStamp
		filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(dir, p)
			relSlash := filepath.ToSlash(rel)
			if m.shouldIgnore(relSlash, false) {
				return nil
			}
			out = append(out, fileStamp{rel: relSlash, size: fi.Size(), mtime: fi.ModTime().UnixNano()})
			return nil
		})
		return out
	}

	fp1 := computeFingerprint(dir, nil, scan(nil))
	if fp1 == "" {
		t.Fatal("指纹为空")
	}

	// 同一树再算:一致。
	if fp2 := computeFingerprint(dir, nil, scan(nil)); fp2 != fp1 {
		t.Error("同树两次指纹应一致")
	}

	// 换源目录标识:不同。
	if fp3 := computeFingerprint(dir+"/x", nil, scan(nil)); fp3 == fp1 {
		t.Error("换目录标识指纹应不同")
	}

	// 改 excludes:不同(即使文件清单一样)。
	if fp4 := computeFingerprint(dir, []string{"*.log"}, scan([]string{"*.log"})); fp4 == fp1 {
		t.Error("改 excludes 指纹应不同")
	}

	// touch 文件(内容不变):mtime 变化 → 指纹不同。
	p := filepath.Join(dir, "a.txt")
	newTime := timeLater(t, p)
	os.Chtimes(p, newTime, newTime)
	if fp5 := computeFingerprint(dir, nil, scan(nil)); fp5 == fp1 {
		t.Error("mtime 变化指纹应不同")
	}

	// 改内容(大小变化):不同。
	write("a.txt", "hello!")
	if fp6 := computeFingerprint(dir, nil, scan(nil)); fp6 == fp1 {
		t.Error("内容变化指纹应不同")
	}
}

// 指纹形态:确认是稳定序列化(可直接手工复算)。
func TestFingerprintDeterministic(t *testing.T) {
	files := []fileStamp{{rel: "a", size: 1, mtime: 2}, {rel: "b/c", size: 3, mtime: 4}}
	h := sha256.New()
	fmt.Fprintf(h, "dir:%s\n", "/d")
	fmt.Fprintf(h, "excl:%s\n", "x\x00y")
	fmt.Fprintf(h, "a|%d|%d\n", int64(1), int64(2))
	fmt.Fprintf(h, "b/c|%d|%d\n", int64(3), int64(4))
	want := hex.EncodeToString(h.Sum(nil))
	if got := computeFingerprint("/d", []string{"x", "y"}, files); got != want {
		t.Errorf("指纹序列化不稳定:\n got %s\nwant %s", got, want)
	}
}

func timeLater(t *testing.T, p string) time.Time {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.ModTime().Add(2 * time.Second)
}
