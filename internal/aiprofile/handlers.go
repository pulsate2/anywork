package aiprofile

import (
	"encoding/json"
	"net/http"
)

// Handlers 承载 AI 配置切换的 HTTP 端点。
type Handlers struct {
	svc *Service
}

func NewHandlers(svc *Service) *Handlers {
	return &Handlers{svc: svc}
}

// List 列出某个 app 的全部配置,并带上当前生效项的 id。
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	app := r.URL.Query().Get("app")
	list, err := h.svc.List(app)
	if err != nil {
		httpError(w, err, statusFor(err))
		return
	}
	current := ""
	for _, p := range list {
		if p.IsCurrent {
			current = p.ID
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"app": app, "current": current, "providers": list,
	})
}

// Create 新增一份配置。
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	var p Provider
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	out, err := h.svc.Create(p)
	if err != nil {
		httpError(w, err, statusFor(err))
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// Update 覆盖一份配置。
func (h *Handlers) Update(w http.ResponseWriter, r *http.Request) {
	var p Provider
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	out, err := h.svc.Update(p)
	if err != nil {
		httpError(w, err, statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Delete 删除一份配置。
func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := h.svc.Delete(q.Get("app"), q.Get("id")); err != nil {
		httpError(w, err, statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Switch 切换当前生效配置,即把它写回真实配置文件。
func (h *Handlers) Switch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		App string `json:"app"`
		ID  string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := h.svc.Switch(body.App, body.ID); err != nil {
		httpError(w, err, statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Export 下载全部配置的 JSON 清单。
func (h *Handlers) Export(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ai-providers.json"`)
	// 头一旦写出去就没法改状态码了,出错只能截断,由客户端解析时发现。
	_ = h.svc.Export(w)
}

// Import 从上传的 JSON 清单按名字合并配置。
func (h *Handlers) Import(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file required", http.StatusBadRequest)
		return
	}
	defer f.Close()
	n, err := h.svc.Import(f)
	if err != nil {
		httpError(w, err, statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"imported": n})
}
