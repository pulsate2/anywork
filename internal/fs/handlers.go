package fs

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Handlers 持有一组文件操作端点,由主应用挂载到 chi 路由。
type Handlers struct {
	svc *Service
}

func NewHandlers(svc *Service) *Handlers {
	return &Handlers{svc: svc}
}

func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	entries, err := h.svc.List(r.URL.Query().Get("path"))
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// MaxPreviewSize 文本预览的大小上限:全文读进浏览器再高亮,几 MB 就开始卡,
// 超过一律拒绝,引导下载。编辑另有更紧的 512KB 上限(前端 MAX_EDIT)。
const MaxPreviewSize = 5 << 20

func (h *Handlers) Read(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	f, size, binary, err := h.svc.ReadInfo(p)
	if err != nil {
		h.httpErr(w, err)
		return
	}
	defer f.Close()
	if binary {
		http.Error(w, "binary cannot be read", http.StatusBadRequest)
		return
	}
	if size > MaxPreviewSize {
		http.Error(w, fmt.Sprintf("文件超过 5MB(%s),不支持预览,请下载查看", sizeHuman(size)), http.StatusRequestEntityTooLarge)
		return
	}
	fi, _ := f.Stat()
	// 不带 Cache-Control 时浏览器会按 Last-Modified 做启发式缓存,文件被
	// 外部(终端/agent)改过之后预览可能短时间不更新。no-cache 只强制每次
	// revalidate,配合 Last-Modified 未变时的 304 依然不浪费流量。
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, filepath.Base(p), fi.ModTime(), f)
}

func (h *Handlers) Write(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.svc.Write(body.Path, strings.NewReader(body.Text)); err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handlers) Upload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dir := r.FormValue("dir")
	fh, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer fh.Close()
	if err := h.svc.Write(filepath.Join(dir, filepath.Base(header.Filename)), fh); err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// inlineTypes 是允许以 inline 直出的类型白名单(前端图片预览用)。
// 白名单之外一律回落 attachment:任意文件都能在同源下直接渲染的话,
// 上传一个 html 就等于同源 XSS。
var inlineTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".ico":  "image/x-icon",
	".avif": "image/avif",
	".svg":  "image/svg+xml",
}

// sandboxedTypes 是"可以 inline 打开,但必须配 CSP sandbox"的类型。html 是唯一
// 渲染即执行的类型:内容里的脚本要能跑(单文件报告/图表全指望它),所以关不掉脚本;
// 那就用 CSP 的 sandbox 指令把文档按进不透明源 —— 拿不到本应用的 cookie/DOM/storage,
// 也没有表单、弹窗、顶层跳转。与前端预览那张 iframe(sandbox="allow-scripts")同一套
// 取舍,差别只是这里换成响应头,因为"外部打开"是浏览器新标签里的顶层文档,框不住。
//
// 少了这一条,「外部打开」就只能退回同源直出 —— 那正是 inlineTypes 上面那句注释要堵的洞:
// 一个能跑脚本的同源文档,可以拿着 Cookie 调 /api/fs/write 改任意文件。
var sandboxedTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".htm":  "text/html; charset=utf-8",
}

// sandboxCSP 里的 allow-scripts 有意保留;其余一律不给。
const sandboxCSP = "sandbox allow-scripts"

func (h *Handlers) Download(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	f, _, _, err := h.svc.ReadInfo(p)
	if err != nil {
		h.httpErr(w, err)
		return
	}
	defer f.Close()
	name := filepath.Base(p)
	ext := strings.ToLower(filepath.Ext(name))
	ct := inlineTypes[ext]
	inline := r.URL.Query().Get("inline") == "1"
	switch {
	case inline && ct != "":
		w.Header().Set("Content-Disposition", "inline; filename="+strconv.Quote(name))
		w.Header().Set("Content-Type", ct)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// <img> 里的 SVG 本来就不执行脚本,但地址栏直接打开这个 URL 会;sandbox 掉。
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	case inline && sandboxedTypes[ext] != "":
		w.Header().Set("Content-Disposition", "inline; filename="+strconv.Quote(name))
		w.Header().Set("Content-Type", sandboxedTypes[ext])
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", sandboxCSP)
		// 页面里的外链(CDN 的图表库、字体……)会带上 Referer,而这个 URL 的
		// query 里就是文件的绝对路径 —— 没理由把本机目录结构告诉第三方。
		w.Header().Set("Referrer-Policy", "no-referrer")
	default:
		w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(name))
		if ct == "" {
			ct = mime.TypeByExtension(filepath.Ext(name))
		}
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
	}
	fi, _ := f.Stat()
	// 同 /api/fs/read:内联预览(图片等)也会被启发式缓存卡住旧文件。
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

func (h *Handlers) Op(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Op   string `json:"op"`
		Path string `json:"path"`
		From string `json:"from,omitempty"`
		To   string `json:"to,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var err error
	switch body.Op {
	case "mkdir":
		err = h.svc.MkDir(body.Path)
	case "touch":
		err = h.svc.Touch(body.Path)
	case "rename":
		err = h.svc.Rename(body.From, body.To)
	case "copy":
		err = h.svc.Copy(body.From, body.To)
	case "move":
		err = h.svc.Move(body.From, body.To)
	case "delete":
		err = h.svc.Delete(body.Path)
	default:
		http.Error(w, "unknown op", http.StatusBadRequest)
		return
	}
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handlers) Search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	res, err := h.svc.Search(SearchOptions{
		Dir:   q.Get("path"),
		Query: q.Get("q"),
		// 默认按内容搜(mode=name 才切文件名),与旧前端调用兼容。
		Content: q.Get("mode") != "name",
		Regex:   q.Get("regex") == "1",
		Case:    q.Get("case") == "1",
		Limit:   limit,
	})
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handlers) Replace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Files   []string `json:"files"`
		Query   string   `json:"q"`
		Replace string   `json:"replace"`
		Regex   bool     `json:"regex"`
		Case    bool     `json:"case"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	res, err := h.svc.Replace(ReplaceOptions{
		Files:   body.Files,
		Query:   body.Query,
		Replace: body.Replace,
		Regex:   body.Regex,
		Case:    body.Case,
	})
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handlers) CreateArchive(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	name := filepath.Base(p) + ".zip"
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(name))
	w.Header().Set("Content-Type", "application/zip")
	_ = h.svc.CreateZip(p, w)
}

// ListArchive 只列压缩包内的条目(预览用),不落盘。
func (h *Handlers) ListArchive(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	entries, truncated, err := h.svc.ListArchive(q.Get("path"), limit)
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "truncated": truncated})
}

// SqliteInfo 数据库概览:表列表 + 每表行数(预览用,只读)。
func (h *Handlers) SqliteInfo(w http.ResponseWriter, r *http.Request) {
	tables, version, err := h.svc.SqliteInfo(r.URL.Query().Get("path"))
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tables": tables, "userVersion": version})
}

// SqliteRows 某表一页行数据(预览用,只读)。
func (h *Handlers) SqliteRows(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	data, err := h.svc.SqliteRows(q.Get("path"), SqliteRowsOptions{
		Table:  q.Get("table"),
		Offset: offset,
		Limit:  limit,
	})
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}


func (h *Handlers) ExtractArchive(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Dest    string `json:"dest"`
		Archive string `json:"archive"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := h.svc.ExtractArchive(body.Dest, body.Archive); err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Compress 把 paths 打成一个包落盘到 dest,格式看 dest 的后缀。和 CreateArchive
// 的区别是那个直接把包流给浏览器下载,这个是在服务器上生成一个文件。
func (h *Handlers) Compress(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Dest  string   `json:"dest"`
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := h.svc.CreateArchiveFile(body.Dest, body.Paths); err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handlers) httpErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrForbidden):
		code = http.StatusForbidden
	case errors.Is(err, os.ErrNotExist):
		code = http.StatusNotFound
	case errors.Is(err, errEscape):
		code = http.StatusBadRequest
	case errors.Is(err, ErrBadQuery):
		code = http.StatusBadRequest
	}
	http.Error(w, err.Error(), code)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// sizeHuman 错误信息里的字节数可读化(backup 包里有个同款,但跨包引用就为这一处不值得)。
func sizeHuman(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/1024/1024)
}
