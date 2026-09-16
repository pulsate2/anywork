package backup

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 真实 WebDAV 服务器在"集合已存在"时返回的状态码五花八门:RFC 是 405,
// 但坚果云/OpenList 这类回 200/204。旧实现只认 201/405/301,第二次备份
// 必定挂在 MKCOL 上(报 "MKCOL /x: 200 OK"),且因为失败在写指纹之前,
// 之后每一轮都会重复失败。
func TestEnsureDirAcceptsExistingCodes(t *testing.T) {
	for _, code := range []int{200, 201, 204, 301, 405} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "MKCOL" {
				t.Errorf("期望 MKCOL,得到 %s", r.Method)
			}
			w.WriteHeader(code)
		}))
		c := newWebdav(srv.URL, "", "")
		if err := c.ensureDir("projects/b-1"); err != nil {
			t.Errorf("MKCOL 返回 %d 应视为成功,却报错: %v", code, err)
		}
		srv.Close()
	}
}

func TestEnsureDirRejectsRealFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := newWebdav(srv.URL, "", "")
	if err := c.ensureDir("projects/b-1"); err == nil {
		t.Fatal("MKCOL 500 必须报错")
	}
}

// 集合路径必须以 / 结尾。不带的话部分服务器回 301,而 Go 的 http.Client
// 会把重定向后的方法降级成 GET —— 拿回来的是 HTML 目录页,解析失败,
// 界面于是永远显示"暂无快照"。
func TestPropfindSendsTrailingSlash(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(`<?xml version="1.0"?><D:multistatus xmlns:D="DAV:"></D:multistatus>`))
	}))
	defer srv.Close()
	c := newWebdav(srv.URL, "", "")
	if _, err := c.propfind("projects/b-1"); err != nil {
		t.Fatalf("propfind 失败: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/") {
		t.Fatalf("PROPFIND 路径需以 / 结尾,实际 %q", gotPath)
	}
}

// 真实服务器用 <D:href>/<D:getcontentlength> 这类带命名空间的标签,
// 解析必须按本地名匹配,否则快照列表恒为空。
func TestPropfindParsesNamespacedXML(t *testing.T) {
	body := `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/dav/projects/b-1/</D:href>
    <D:propstat>
      <D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/dav/projects/b-1/backup-20260916-120000.tar.gz</D:href>
    <D:propstat>
      <D:prop><D:getcontentlength>260</D:getcontentlength></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(body))
	}))
	defer srv.Close()
	// base 带路径前缀(Nextcloud/群晖这类 /remote.php/dav/... 的形态),
	// 服务器回给我们的 href 里会带上这段前缀,rel() 必须剥掉。
	c := newWebdav(srv.URL+"/dav", "", "")
	entries, err := c.propfind("projects/b-1")
	if err != nil {
		t.Fatalf("propfind 失败: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("应解析出 2 个条目,实际 %d", len(entries))
	}
	if !entries[0].IsDir {
		t.Error("首个条目是集合,IsDir 应为 true")
	}
	if entries[1].IsDir || entries[1].Size != 260 {
		t.Errorf("第二个条目应为 260 字节的文件,实际 IsDir=%v Size=%d", entries[1].IsDir, entries[1].Size)
	}
	// 快照列表靠 rel() 把 href 还原成相对路径,带 host 的绝对 URL 也要能处理。
	if got := c.rel(entries[1].Href); got != "projects/b-1/backup-20260916-120000.tar.gz" {
		t.Errorf("rel 解析错误: %q", got)
	}
}
