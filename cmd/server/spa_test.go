package main

import (
	"compress/gzip"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 前端产物由 npm run build 生成后才存在(dist 不入库),没有就跳过。
func firstAsset(t *testing.T) string {
	t.Helper()
	entries, err := webFS.ReadDir("dist/assets")
	if err != nil || len(entries) == 0 {
		t.Skip("dist/assets 为空,先跑 npm run build")
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".js") {
			return "assets/" + e.Name()
		}
	}
	t.Skip("dist/assets 里没有 js")
	return ""
}

func serve(t *testing.T, name string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/"+name, nil)
	maps.Copy(r.Header, header)
	w := httptest.NewRecorder()
	serveEmbed(w, r, name)
	return w
}

// 带哈希的资源必须强缓存 + 有 ETag,否则每次切页都要重下整个路由分块。
func TestServeEmbedHashedAssetIsImmutable(t *testing.T) {
	name := firstAsset(t)
	res := serve(t, name, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", res.Code)
	}
	if got := res.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q", got)
	}
	etag := res.Header().Get("ETag")
	if etag == "" {
		t.Fatal("缺少 ETag")
	}

	again := serve(t, name, http.Header{"If-None-Match": {etag}})
	if again.Code != http.StatusNotModified {
		t.Errorf("带 If-None-Match 的状态码 = %d,应为 304", again.Code)
	}
}

func TestServeEmbedGzip(t *testing.T) {
	name := firstAsset(t)
	res := serve(t, name, http.Header{"Accept-Encoding": {"gzip, deflate"}})
	if res.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q", res.Header().Get("Content-Encoding"))
	}
	if res.Header().Get("Vary") != "Accept-Encoding" {
		t.Errorf("Vary = %q", res.Header().Get("Vary"))
	}
	zr, err := gzip.NewReader(res.Body)
	if err != nil {
		t.Fatalf("响应不是合法 gzip: %v", err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	raw := serve(t, name, nil).Body.Bytes()
	if string(plain) != string(raw) {
		t.Error("解压结果与未压缩响应不一致")
	}
	if res.Body.Len() >= len(raw) {
		t.Errorf("压缩后 %d 字节,未压缩 %d 字节", res.Body.Len(), len(raw))
	}
}

// SPA 路径回退到 index.html,且外壳不能被强缓存,否则改版后客户端一直吃旧壳。
func TestServeEmbedSPAFallbackNotCached(t *testing.T) {
	if _, err := webFS.ReadFile("dist/index.html"); err != nil {
		t.Skip("dist/index.html 不存在,先跑 npm run build")
	}
	res := serve(t, "git", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", res.Code)
	}
	if ct := res.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	if got := res.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q,应为 no-cache", got)
	}
}
