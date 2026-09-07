package fs

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 文本预览的大小闸门:5MB 以内直出,超过回 413 且不读正文。
func TestReadSizeLimit(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "small.txt")
	if err := os.WriteFile(small, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 5MB+1 的纯文本(无 NUL,不会被判二进制)。
	big := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(big, []byte(strings.Repeat("a", MaxPreviewSize+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(NewService(dir, false))

	// 小文件:200,正文完整。
	w := httptest.NewRecorder()
	h.Read(w, httptest.NewRequest("GET", "/api/fs/read?path=small.txt", nil))
	if w.Code != http.StatusOK {
		t.Errorf("small: code = %d, want 200", w.Code)
	}
	if w.Body.String() != "hello" {
		t.Errorf("small: body = %q, want hello", w.Body.String())
	}

	// 大文件:413,带大小说明。
	w = httptest.NewRecorder()
	h.Read(w, httptest.NewRequest("GET", "/api/fs/read?path=big.txt", nil))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("big: code = %d, want 413", w.Code)
	}
	if !strings.Contains(w.Body.String(), "不支持预览") {
		t.Errorf("big: body = %q, want 含拒绝说明", w.Body.String())
	}
}
