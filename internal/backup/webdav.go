package backup

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
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
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	c := &webdavClient{
		base: strings.TrimRight(base, "/"),
		user: user, pass: pass,
		// 这里刻意不设 http.Client.Timeout:它管的是"整个请求",上传一份大备份
		// 动辄超过任何固定值,一到点连正在写的 body 一起掐 —— 此时
		// tar 那边还在往管道里写,拿到的错误就是 io.ErrClosedPipe
		// ("io: read/write on closed pipe"),而真正的原因是超时。
		// 卡死改用两层细粒度超时兜:连不上/握手由 Transport 管;
		// 单个请求的整体时限由 ctxOp/bodyTimeout 按体量给
		// (ResponseHeaderTimeout 也不设:它是"body 发完之后等响应头"的固定值,
		//  而大文件传完后服务端自己算哈希也可能算上一两分钟)。
		client: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 5 * time.Second,
			IdleConnTimeout:       90 * time.Second,
		}},
	}
	// 服务器返回的 href 可能带上 base 的路径前缀,先记下来备 rel() 剥离。
	if u, err := url.Parse(c.base); err == nil {
		c.basePath = strings.TrimSuffix(u.Path, "/")
	}
	return c
}

// ctxOp 元数据类请求(PROPFIND/MKCOL/DELETE)的时限。这类请求体量小,
// 又都卡在用户操作的路径上(点开快照、第二次备份的 MKCOL),不值得久等。
func ctxOp() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

// bodyTimeout 传输类请求(PUT/GET 的 body)的兜底时限。
// 不能用一个固定值:大备份本来就要传很久,掐早了就是"文件一多就失败"。
// 按最慢 50KB/s 估,5 分钟起底,封顶 12 小时。size 未知(如 GET)按 0 算。
func bodyTimeout(size int64) time.Duration {
	const (
		floor   = 5 * time.Minute
		minRate = 50 << 10 // 50 KB/s
		ceiling = 12 * time.Hour
	)
	if size < 0 {
		size = 0
	}
	d := floor + time.Duration(size/minRate)*time.Second
	if d > ceiling {
		return ceiling
	}
	return d
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
		ctx, cancel := ctxOp()
		req, err := http.NewRequestWithContext(ctx, "MKCOL", c.base+cur, nil)
		if err != nil {
			return err
		}
		c.setAuth(req)
		resp, err := c.client.Do(req)
		cancel()
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
	ctx, cancel := ctxOp()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", target, nil)
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

// put 上传一个已知长度的 body。size 必须给准:长度未知时 Go 会退化成 chunked,
// 而不少 WebDAV 服务端(或它们前面的 nginx)对 chunked 的 PUT 处理不了 ——
// 体积一到上限就不等 body 直接应答并关连接,客户端此时还在写,
// 报出来的却是 io.ErrClosedPipe,服务端真正的原因(413/507/401)全被吃掉。
func (c *webdavClient) put(path string, r io.Reader, size int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), bodyTimeout(size))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "PUT", c.base+"/"+strings.Trim(path, "/"), r)
	if err != nil {
		return err
	}
	req.ContentLength = size
	c.setAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 出错时把响应体里那几行也带上:WebDAV 的报错原因基本都写在那儿。
	if resp.StatusCode != 201 && resp.StatusCode != 200 && resp.StatusCode != 204 {
		return fmt.Errorf("PUT %s: %s%s", path, resp.Status, respSnippet(resp))
	}
	return nil
}

// respSnippet 取响应体开头一小段用于报错,避免把整页 HTML 塞进错误里。
func respSnippet(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	if len(strings.TrimSpace(string(b))) == 0 {
		return ""
	}
	return ": " + strings.TrimSpace(string(b))
}

// bodyReader 把 ctx 的取消权交给调用方。GET 的 ctx 不能在 get() 返回时就 cancel:
// 取消请求上下文会连响应体一起关掉,而 body 是调用方拿回去慢慢读的
// (下载快照、恢复时流式解包),提前取消读到的就是 "context canceled"。
type bodyReader struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b bodyReader) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// get 下载。size 是"预计多大",用来定兜底时限;不知道就传 0(走 5 分钟底)。
// 返回的 ReadCloser 必须 Close:时限靠它收尾,不关就漏一个定时器,
// 大快照那一条还会一直占着 ctx 到上限。
func (c *webdavClient) get(path string, size int64) (io.ReadCloser, error) {
	ctx, cancel := context.WithTimeout(context.Background(), bodyTimeout(size))
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/"+strings.Trim(path, "/"), nil)
	if err != nil {
		cancel()
		return nil, err
	}
	c.setAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode != 200 {
		// 先取摘要再关:respSnippet 是从 body 里读的,关掉就读不到了。
		snippet := respSnippet(resp)
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("GET %s: %s%s", path, resp.Status, snippet)
	}
	return bodyReader{ReadCloser: resp.Body, cancel: cancel}, nil
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
	ctx, cancel := ctxOp()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "DELETE", c.base+"/"+strings.Trim(path, "/"), nil)
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
