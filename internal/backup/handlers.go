package backup

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

// Handlers 承载备份 HTTP 端点。
type Handlers struct {
	mgr *Manager
}

func NewHandlers(mgr *Manager) *Handlers {
	return &Handlers{mgr: mgr}
}

// List 返回所有任务。
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.mgr.List())
}

// Save 新建/更新任务。
func (h *Handlers) Save(w http.ResponseWriter, r *http.Request) {
	var c JobConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	j, err := h.mgr.Save(c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

// Delete 删除任务。
func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.mgr.Delete(r.URL.Query().Get("id")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Run 立即执行备份。
func (h *Handlers) Run(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	go h.mgr.RunBackup(id)
	writeJSON(w, http.StatusOK, map[string]bool{"started": true})
}

// Snapshots 列远程快照历史(PROPFIND 实时)。
func (h *Handlers) Snapshots(w http.ResponseWriter, r *http.Request) {
	j := h.mgr.Get(r.URL.Query().Get("id"))
	if j == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	c := newWebdav(j.WebDAVURL, j.WebDAVUser, j.WebDAVPass)
	entries, err := c.propfind(remoteDirFor(j))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	type snap struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	snaps := []snap{}
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		name := filepath.Base(e.Href)
		snaps = append(snaps, snap{Name: name, Size: e.Size})
	}
	writeJSON(w, http.StatusOK, snaps)
}

// Restore 从指定快照(或最新)恢复。
func (h *Handlers) Restore(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	var body struct {
		Snapshot string `json:"snapshot"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var err error
	if body.Snapshot != "" {
		err = h.mgr.RestoreSnapshot(id, body.Snapshot)
	} else {
		err = h.mgr.RestoreLatest(id)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Download 代理流式下载远程快照。
func (h *Handlers) Download(w http.ResponseWriter, r *http.Request) {
	j := h.mgr.Get(r.URL.Query().Get("id"))
	if j == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	snap := r.URL.Query().Get("snapshot")
	href := snap
	if !strings.Contains(snap, "/") {
		href = remoteDirFor(j) + "/" + snap
	}
	c := newWebdav(j.WebDAVURL, j.WebDAVUser, j.WebDAVPass)
	rc, err := c.get(href)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Disposition", "attachment; filename="+snap)
	_, _ = io.Copy(w, rc)
}
