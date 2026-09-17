package backup

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 上传必须带准确的 Content-Length。长度未知时 Go 会退化成 chunked,而不少
// WebDAV 服务端(或它们前面的 nginx)处理不了 chunked 的 PUT:体积一到上限就
// 不等 body 直接应答并关连接,客户端还在写,报出来的是
// io.ErrClosedPipe("io: read/write on closed pipe"),真正的原因被吃掉。
func TestPutSendsContentLength(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 4096)
	var gotLen int64
	var chunked bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunked = len(r.TransferEncoding) > 0
		gotLen = r.ContentLength
		n, _ := io.Copy(io.Discard, r.Body)
		if n != int64(len(payload)) {
			t.Errorf("服务端只收到 %d 字节,期望 %d", n, len(payload))
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c := newWebdav(srv.URL, "u", "p")
	if err := c.put("backup-a.tar.gz", bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("PUT 该成功: %v", err)
	}
	if chunked {
		t.Error("不该用 chunked:服务端会拿不到长度,撞上限时直接关连接")
	}
	if gotLen != int64(len(payload)) {
		t.Errorf("Content-Length = %d,期望 %d", gotLen, len(payload))
	}
}

// 服务端拒绝时,报错必须是它自己说的原因,而不是被管道/连接层盖掉。
// 备注信息在响应体里:WebDAV 的 413/507 基本都会写一行说明。
func TestPutReportsServerStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte("quota exceeded\n"))
	}))
	defer srv.Close()

	c := newWebdav(srv.URL, "", "")
	err := c.put("backup-a.tar.gz", strings.NewReader("body"), 4)
	if err == nil {
		t.Fatal("413 必须报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "413") {
		t.Errorf("报错里该有服务端状态码,得到: %s", msg)
	}
	if !strings.Contains(msg, "quota exceeded") {
		t.Errorf("报错里该带上响应体说明,得到: %s", msg)
	}
	if strings.Contains(msg, "closed pipe") {
		t.Errorf("不该退化成管道错误: %s", msg)
	}
}

// 别再给 client 加回整体超时。http.Client.Timeout 管的是"整个请求",
// 一次大备份传上几分钟很正常,到点就掐 —— 而且掐在 body 写到一半时,
// 报出来的是 io.ErrClosedPipe,看着像网络坏了,实际是超时。
func TestNoBlanketClientTimeout(t *testing.T) {
	c := newWebdav("http://example.invalid", "", "")
	if c.client.Timeout != 0 {
		t.Errorf("不该设整体超时(现为 %v):大备份会被拦腰掐断", c.client.Timeout)
	}
	tr, ok := c.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 该是 *http.Transport,得到 %T", c.client.Transport)
	}
	if tr.DialContext == nil || tr.TLSHandshakeTimeout == 0 {
		t.Error("连不上/握手仍要有超时,否则卡在拨号阶段")
	}
	if tr.ResponseHeaderTimeout != 0 {
		t.Errorf("响应头也该由按体量给的 ctx 兜,不该用固定值(%v)", tr.ResponseHeaderTimeout)
	}
}

// 上传的兜底时限按体量给:大包要传很久,用固定值是"文件一多就失败"的根源。
func TestBodyTimeoutScalesWithSize(t *testing.T) {
	small := bodyTimeout(0)
	if small != 5*time.Minute {
		t.Errorf("小体量该是 5 分钟底,得到 %v", small)
	}
	big := bodyTimeout(100 << 20) // 100MB:按 50KB/s 估 ≈ 35 分钟
	if big <= small {
		t.Errorf("大体量该放宽时限: 小包 %v, 大包 %v", small, big)
	}
	if huge := bodyTimeout(1 << 40); huge != 12*time.Hour {
		t.Errorf("再大也该封顶 12 小时,得到 %v", huge)
	}
}
