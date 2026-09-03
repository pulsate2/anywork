package fs

import (
	"encoding/json"
	"errors"
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

func (h *Handlers) Read(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	f, _, binary, err := h.svc.ReadInfo(p)
	if err != nil {
		h.httpErr(w, err)
		return
	}
	defer f.Close()
	if binary {
		http.Error(w, "binary cannot be read", http.StatusBadRequest)
		return
	}
	fi, _ := f.Stat()
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

func (h *Handlers) Download(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	f, _, _, err := h.svc.ReadInfo(p)
	if err != nil {
		h.httpErr(w, err)
		return
	}
	defer f.Close()
	name := filepath.Base(p)
	ct := inlineTypes[strings.ToLower(filepath.Ext(name))]
	if r.URL.Query().Get("inline") == "1" && ct != "" {
		w.Header().Set("Content-Disposition", "inline; filename="+strconv.Quote(name))
		w.Header().Set("Content-Type", ct)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// <img> 里的 SVG 本来就不执行脚本,但地址栏直接打开这个 URL 会;sandbox 掉。
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	} else {
		w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(name))
		if ct == "" {
			ct = mime.TypeByExtension(filepath.Ext(name))
		}
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
	}
	fi, _ := f.Stat()
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
