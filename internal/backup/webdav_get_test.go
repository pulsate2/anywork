package backup

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// get 返回的 body 必须在 get() 之后还能读完。GET 的 ctx 一旦在返回时就 cancel,
// 响应体会跟着被关掉,调用方(下载/恢复)读到的就是 "context canceled" ——
// 现场看着像服务器把连接掐了,实际是自己取消的。取消权交给 Close。
func TestGetBodyReadableAfterReturn(t *testing.T) {
	payload := bytes.Repeat([]byte("snapshot"), 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	c := newWebdav(srv.URL, "", "")
	rc, err := c.get("backup-a.tar.gz", int64(len(payload)))
	if err != nil {
		t.Fatalf("GET 该成功: %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("读响应体失败(ctx 被提前取消了?): %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("读到 %d 字节,期望 %d", len(got), len(payload))
	}
	if err := rc.Close(); err != nil {
		t.Errorf("Close 不该报错: %v", err)
	}
}

// 非 200 时错误信息里要带状态码和响应体说明,和 PUT 一致。
func TestGetReportsServerStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("no such snapshot\n"))
	}))
	defer srv.Close()

	c := newWebdav(srv.URL, "", "")
	_, err := c.get("backup-missing.tar.gz", 0)
	if err == nil {
		t.Fatal("404 必须报错")
	}
	if msg := err.Error(); !bytes.Contains([]byte(msg), []byte("404")) || !bytes.Contains([]byte(msg), []byte("no such snapshot")) {
		t.Errorf("报错该带状态码与响应体: %s", msg)
	}
}
