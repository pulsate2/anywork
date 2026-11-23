package agent

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
)

var (
	errArtifactPath      = errors.New("invalid artifact path")
	errArtifactForbidden = errors.New("artifact outside allowed directories")
)

// Artifact serves local links emitted by agents. Relative paths belong to the
// session workspace, not the file browser. Absolute paths must remain inside
// --root or this session's attachment directory. OpenRoot also prevents symlink
// escapes; sandbox: is a local URI, never a request to an external server.
func (h *Handlers) Artifact(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sess, err := h.store.GetSession(id)
	if err != nil || sess == nil {
		http.Error(w, "会话不存在", http.StatusNotFound)
		return
	}
	f, err := h.openArtifact(sess, r.URL.Query().Get("path"))
	if err != nil {
		code, message := http.StatusInternalServerError, "无法读取文件"
		switch {
		case errors.Is(err, errArtifactPath):
			code, message = http.StatusBadRequest, "无效的本地文件路径"
		case errors.Is(err, errArtifactForbidden), errors.Is(err, os.ErrPermission):
			code, message = http.StatusForbidden, "文件位于允许的目录之外或无权读取"
		case errors.Is(err, os.ErrNotExist):
			code, message = http.StatusNotFound, "文件不存在"
		}
		http.Error(w, message, code)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "只支持普通文件", http.StatusBadRequest)
		return
	}
	name := filepath.Base(f.Name())
	disposition, contentType := "attachment", "application/octet-stream"
	if r.URL.Query().Get("inline") == "1" {
		if ct := artifactImageType(f, name); ct != "" {
			disposition, contentType = "inline", ct
		}
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func artifactLocalPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	lower := strings.ToLower(p)
	if strings.HasPrefix(lower, "sandbox:") || strings.HasPrefix(lower, "file:") {
		u, err := url.Parse(p)
		if err != nil || (u.Host != "" && !strings.EqualFold(u.Host, "localhost")) || u.RawQuery != "" {
			return "", errArtifactPath
		}
		p = u.Path
		if p == "" && strings.EqualFold(u.Scheme, "sandbox") {
			p, err = url.PathUnescape(u.Opaque)
			if err != nil {
				return "", errArtifactPath
			}
		}
	} else if strings.Contains(p, "://") || strings.HasPrefix(p, "//") {
		return "", errArtifactPath
	}
	if p == "" || len(p) > 16*1024 || strings.ContainsAny(p, "\x00\r\n") {
		return "", errArtifactPath
	}
	// file:///C:/... becomes C:/... on Windows, never a UNC network request.
	if filepath.Separator == '\\' && len(p) > 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p), nil
}

func (h *Handlers) openArtifact(sess *Session, raw string) (*os.File, error) {
	p, err := artifactLocalPath(raw)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(sess.Workspace, p)
	}
	p, err = filepath.Abs(p)
	if err != nil {
		return nil, errArtifactPath
	}
	roots := []string{h.mgr.Root}
	if h.store.FilesDir != "" {
		roots = append(roots, filepath.Join(h.store.FilesDir, sess.ID))
	}
	for _, base := range roots {
		if base == "" {
			continue
		}
		base, err = filepath.Abs(base)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(base, p)
		if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		// OpenRoot intentionally rejects absolute symlinks, even when their
		// targets are inside the root. Resolve those first, check the physical
		// boundary, then open the resolved relative path through OpenRoot so
		// later symlink changes still cannot escape the allowed directory.
		base, err = filepath.EvalSymlinks(base)
		if err != nil {
			return nil, err
		}
		target, err := filepath.EvalSymlinks(p)
		if err != nil {
			return nil, err
		}
		rel, err = filepath.Rel(base, target)
		if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, errArtifactForbidden
		}
		root, err := os.OpenRoot(base)
		if err != nil {
			return nil, err
		}
		var f *os.File
		info, err := root.Stat(rel)
		if err == nil && !info.Mode().IsRegular() {
			err = errArtifactPath
		}
		if err == nil {
			f, err = root.Open(rel)
		}
		root.Close()
		if err != nil && strings.Contains(err.Error(), "path escapes") {
			return nil, errArtifactForbidden
		}
		return f, err
	}
	return nil, errArtifactForbidden
}

func artifactImageType(f *os.File, name string) string {
	images := map[string]string{
		".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
		".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp",
		".ico": "image/x-icon", ".avif": "image/avif", ".svg": "image/svg+xml",
	}
	if ct := images[strings.ToLower(filepath.Ext(name))]; ct != "" {
		return ct
	}
	// Some image tools use extensionless temporary files. Only raster MIME
	// types are inferred; HTML or arbitrary XML must never be served inline.
	var head [512]byte
	n, _ := f.Read(head[:])
	_, _ = f.Seek(0, io.SeekStart)
	ct := http.DetectContentType(head[:n])
	for _, safe := range images {
		if ct == safe && safe != "image/svg+xml" {
			return ct
		}
	}
	return ""
}
