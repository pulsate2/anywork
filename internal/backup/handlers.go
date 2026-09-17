package backup

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sort"
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
		// 定时表达式不合法是用户输入问题,不该报 500。
		if errors.Is(err, ErrBadCron) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeMgrErr(w, err)
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

// Run 立即执行备份(异步)。force=1 时内容未变化也强制出快照。
func (h *Handlers) Run(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	force := r.URL.Query().Get("force") == "1"
	if err := h.mgr.RunBackup(id, force); err != nil {
		writeMgrErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"started": true})
}

// writeMgrErr 把管理器里的哨兵错误映射成状态码:争用 409、任务不存在 404、其余 500。
func writeMgrErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrBusy):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, ErrNoJob):
		http.Error(w, err.Error(), http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
		// 只列快照本身:.json 是元数据,列出来点"恢复"必然报错;
		// 扁平布局下同目录还可能躺着别人的文件,同样不列。
		if !isSnapshotName(name) {
			continue
		}
		snaps = append(snaps, snap{Name: name, Size: e.Size})
	}
	// 倒序:新的在前。PROPFIND 的返回顺序由服务器定(有的按名字升序,有的就乱着来),
	// 不能指望。名字里嵌的是定宽的 YYYYMMDD-HHMMSS,字典序倒排即时间倒排。
	sort.Slice(snaps, func(a, b int) bool { return snaps[a].Name > snaps[b].Name })
	writeJSON(w, http.StatusOK, snaps)
}

// Restore 从指定快照(或最新)恢复。只负责起头:下载解压要跑很久,
// 占着这个请求不放既没有进度可看,也会被反代的读超时掐断,于是就地转成异步,
// 进度读任务上的 restoring / restoreErr。
func (h *Handlers) Restore(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	var body struct {
		Snapshot string `json:"snapshot"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := h.mgr.StartRestore(id, body.Snapshot); err != nil {
		writeMgrErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"started": true})
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
		href = remotePath(remoteDirFor(j), snap)
	}
	c := newWebdav(j.WebDAVURL, j.WebDAVUser, j.WebDAVPass)
	// 这是给浏览器直接下载的,长度这里没有(要问 PROPFIND),走兜底时限。
	rc, err := c.get(href, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Disposition", "attachment; filename="+snap)
	_, _ = io.Copy(w, rc)
}
