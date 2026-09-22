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

// read/download 必须带 Cache-Control: no-cache:否则浏览器按 Last-Modified 做
// 启发式缓存,文件被外部(终端/agent)改动后预览短时间不更新。
// no-cache 只强制每次 revalidate,未变时仍然 304,不吃流量。
func TestReadCacheControl(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(NewService(dir, false))

	w := httptest.NewRecorder()
	h.Read(w, httptest.NewRequest("GET", "/api/fs/read?path=a.txt", nil))
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("read: Cache-Control = %q, want no-cache", cc)
	}

	w = httptest.NewRecorder()
	h.Download(w, httptest.NewRequest("GET", "/api/fs/download?path=a.txt", nil))
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("download: Cache-Control = %q, want no-cache", cc)
	}

	// revalidate 的廉价路径没被破坏:If-Modified-Since 命中时仍回 304。
	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/fs/read?path=a.txt", nil)
	req.Header.Set("If-Modified-Since", fi.ModTime().UTC().Format(http.TimeFormat))
	w = httptest.NewRecorder()
	h.Read(w, req)
	if w.Code != http.StatusNotModified {
		t.Errorf("ims: code = %d, want 304", w.Code)
	}
}

// html 的「外部打开」:inline 直出,但必须带 CSP sandbox。
// 少了这个头就是同源直出 —— 页面里的脚本能拿着 Cookie 调本应用的 API,
// 也就是 inlineTypes 上面那句注释要堵的洞,所以这里按不变量钉死。
func TestDownloadHTMLInlineSandboxed(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "report.html")
	if err := os.WriteFile(file, []byte("<h1>hi</h1><script>1</script>"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(NewService(dir, false))

	w := httptest.NewRecorder()
	h.Download(w, httptest.NewRequest("GET", "/api/fs/download?path=report.html&inline=1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("inline: code = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("inline: Content-Type = %q, want text/html", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "inline") {
		t.Errorf("inline: Content-Disposition = %q, want inline", cd)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "sandbox") {
		t.Errorf("inline: CSP = %q, want 含 sandbox(同源直出即可执行脚本改文件)", csp)
	}
	// sandbox 里绝不能出现 allow-same-origin:它一出现,上面那条沙箱就形同虚设。
	if strings.Contains(csp, "allow-same-origin") {
		t.Errorf("inline: CSP = %q, 不该给 allow-same-origin", csp)
	}
	// 外链别把 query 里的本机路径当 Referer 发给第三方。
	if rp := w.Header().Get("Referrer-Policy"); rp != "no-referrer" {
		t.Errorf("inline: Referrer-Policy = %q, want no-referrer", rp)
	}

	// 不带 inline 参数时照旧走下载:html 不该有任何"默认直出"的路径。
	w = httptest.NewRecorder()
	h.Download(w, httptest.NewRequest("GET", "/api/fs/download?path=report.html", nil))
	if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Errorf("plain: Content-Disposition = %q, want attachment", cd)
	}
	if csp := w.Header().Get("Content-Security-Policy"); csp != "" {
		t.Errorf("plain: CSP = %q, want 空", csp)
	}
}
