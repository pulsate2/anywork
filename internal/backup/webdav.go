package backup

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// webdavClient 极简 WebDAV 客户端:PROPFIND/PUT/GET/DELETE/MKCOL。
type webdavClient struct {
	base   string // 不含尾部斜杠
	user   string
	pass   string
	client *http.Client
}

func newWebdav(base, user, pass string) *webdavClient {
	return &webdavClient{
		base: strings.TrimRight(base, "/"),
		user: user, pass: pass,
		client: &http.Client{Timeout: 60 * time.Second},
	}
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
		// 201=已建,405=已存在,其它=失败(部分服务器 301 重定向)。
		if resp.StatusCode != 201 && resp.StatusCode != 405 && resp.StatusCode != 301 {
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
	req, err := http.NewRequest("PROPFIND", c.base+"/"+strings.Trim(dir, "/"), nil)
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
	var ms struct {
		Responses []struct {
			Href string `xml:"href"`
			Prop []struct {
				Href  string `xml:"href"`
				Size  string `xml:"getcontentlength"`
				IsDir bool   `xml:"resourcetype>collection"`
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
			e.IsDir = e.IsDir || p.IsDir
			fmt.Sscanf(p.Size, "%d", &e.Size)
		}
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

// rel 把 PROPFIND 返回的 href(可能含 host)规约为 base 相对路径。
func (c *webdavClient) rel(href string) string {
	if i := strings.Index(href, c.base); i >= 0 {
		href = href[i+len(c.base):]
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
