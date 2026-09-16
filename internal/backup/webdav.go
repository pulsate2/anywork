package backup

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// webdavClient 极简 WebDAV 客户端:PROPFIND/PUT/GET/DELETE/MKCOL。
type webdavClient struct {
	base     string // 不含尾部斜杠
	basePath string // base 的路径部分(如 /remote.php/dav/files/u),用于还原 href
	user     string
	pass     string
	client   *http.Client
}

func newWebdav(base, user, pass string) *webdavClient {
	c := &webdavClient{
		base: strings.TrimRight(base, "/"),
		user: user, pass: pass,
		client: &http.Client{Timeout: 60 * time.Second},
	}
	// 服务器返回的 href 可能带上 base 的路径前缀,先记下来备 rel() 剥离。
	if u, err := url.Parse(c.base); err == nil {
		c.basePath = strings.TrimSuffix(u.Path, "/")
	}
	return c
}

func (c *webdavClient) setAuth(req *http.Request) {
	if c.user != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
}

// ensureDir 递归创建远程目录(MKCOL 逐层)。
func (c *webdavClient) ensureDir(path string) error {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	segments := strings.Split(path, "/")
	cur := ""
	for _, seg := range segments {
		cur = cur + "/" + seg
		req, err := http.NewRequest("MKCOL", c.base+cur, nil)
		if err != nil {
			return err
		}
		c.setAuth(req)
		resp, err := c.client.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		// 201=已建;405=按 RFC 的"已存在";200/204=不少真实服务器(坚果云、OpenList 等)
		// 对已存在集合返回的状态码;301=重定向到已有集合。都算成功,否则第二次备份必挂。
		switch resp.StatusCode {
		case 200, 201, 204, 301, 405:
		default:
			return fmt.Errorf("MKCOL %s: %s", cur, resp.Status)
		}
	}
	return nil
}

// propfind 列出目录条目,返回文件名→字节大小。
type davEntry struct {
	Href  string `xml:"href"`
	Size  int64  `xml:"propstat>prop>getcontentlength"`
	IsDir bool   `xml:"propstat>prop>resourcetype>collection"`
}

func (c *webdavClient) propfind(dir string) ([]davEntry, error) {
	// 集合路径必须以 / 结尾:否则部分服务器回 301,而 Go 会把重定向后的
	// 方法降级成 GET,拿回来的是 HTML 目录页,解析必失败 → 快照列表永远空。
	target := c.base + "/"
	if rel := strings.Trim(dir, "/"); rel != "" {
		target = c.base + "/" + rel + "/"
	}
	req, err := http.NewRequest("PROPFIND", target, nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	req.Header.Set("Depth", "1")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 207 && resp.StatusCode != 200 {
		return nil, fmt.Errorf("PROPFIND %s: %s", dir, resp.Status)
	}
	body, _ := io.ReadAll(resp.Body)
	// resourcetype 用指针判存在,不能用 bool:真实服务器写的是空元素
	// <D:collection/>,而 bool 字段按元素文本解析,空文本恒为 false ——
	// 那样集合会被当成文件混进快照列表。
	var ms struct {
		Responses []struct {
			Href string `xml:"href"`
			Prop []struct {
				Size    string `xml:"getcontentlength"`
				ResType struct {
					Collection *struct{} `xml:"collection"`
				} `xml:"resourcetype"`
			} `xml:"propstat>prop"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(body, &ms); err != nil {
		return nil, err
	}
	entries := []davEntry{}
	for _, r := range ms.Responses {
		e := davEntry{Href: r.Href}
		for _, p := range r.Prop {
			e.IsDir = e.IsDir || p.ResType.Collection != nil
			fmt.Sscanf(p.Size, "%d", &e.Size)
		}
		// 兜底:集合的 href 恒以 / 结尾。
		e.IsDir = e.IsDir || strings.HasSuffix(e.Href, "/")
		entries = append(entries, e)
	}
	return entries, nil
}

func (c *webdavClient) put(path string, r io.Reader) error {
	req, err := http.NewRequest("PUT", c.base+"/"+strings.Trim(path, "/"), r)
	if err != nil {
		return err
	}
	c.setAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 && resp.StatusCode != 200 && resp.StatusCode != 204 {
		return fmt.Errorf("PUT %s: %s", path, resp.Status)
	}
	return nil
}

func (c *webdavClient) get(path string) (io.ReadCloser, error) {
	req, err := http.NewRequest("GET", c.base+"/"+strings.Trim(path, "/"), nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return resp.Body, nil
}

// rel 把 PROPFIND 返回的 href 规约为 base 相对路径。
// href 可能是绝对 URL(带 host)、也可能只有路径;两者都要先取路径,
// 再剥掉 base 自带的路径前缀,否则快照名会多出 remote.php/dav/... 之类的层级。
func (c *webdavClient) rel(href string) string {
	if href == "" {
		return ""
	}
	if u, err := url.Parse(href); err == nil && u.Path != "" {
		href = u.Path
	}
	if c.basePath != "" {
		href = strings.TrimPrefix(href, c.basePath)
	}
	return strings.TrimPrefix(href, "/")
}

func (c *webdavClient) del(path string) error {
	req, err := http.NewRequest("DELETE", c.base+"/"+strings.Trim(path, "/"), nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 && resp.StatusCode != 200 && resp.StatusCode != 404 {
		return fmt.Errorf("DELETE %s: %s", path, resp.Status)
	}
	return nil
}
