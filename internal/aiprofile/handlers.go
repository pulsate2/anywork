package aiprofile

import (
	"encoding/json"
	"net/http"
)

// Handlers 承载 AI 档案 HTTP 端点。
type Handlers struct {
	svc *service
}

func NewHandlers(svc *service) *Handlers {
	return &Handlers{svc: svc}
}

// List 列出全部档案。
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	profiles, err := h.svc.List()
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, profiles)
}

// Get 读取单个档案。
func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.Get(r.URL.Query().Get("name"))
	if err != nil {
		httpError(w, err, statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// Create 新建档案。
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string            `json:"name"`
		Env       map[string]string `json:"env"`
		Preset    string            `json:"preset"`
		CloneFrom string            `json:"cloneFrom"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	p, err := h.svc.Create(body.Name, body.Env, body.Preset, body.CloneFrom)
	if err != nil {
		httpError(w, err, statusFor(err))
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// Update 覆盖档案 env。
func (h *Handlers) Update(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string            `json:"name"`
		Env  map[string]string `json:"env"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	p, err := h.svc.Update(body.Name, body.Env)
	if err != nil {
		httpError(w, err, statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// Delete 删除档案。
func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Delete(r.URL.Query().Get("name")); err != nil {
		httpError(w, err, statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Active 返回当前生效档案。
func (h *Handlers) Active(w http.ResponseWriter, r *http.Request) {
	name, err := h.svc.Active()
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

// SetActive 切换当前生效档案。
func (h *Handlers) SetActive(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := h.svc.SetActive(body.Name); err != nil {
		httpError(w, err, statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Export 以 tar.gz 下载档案。
func (h *Handlers) Export(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition",
		"attachment; filename="+name+".tar.gz")
	if err := h.svc.ExportWriter(name, w); err != nil {
		// 头部已写,只能 200;内容错误由客户端校验。
		return
	}
}

// Import 从上传的 tar.gz 导入档案。
func (h *Handlers) Import(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	f, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file required", http.StatusBadRequest)
		return
	}
	defer f.Close()
	if err := h.svc.Import(name, f); err != nil {
		httpError(w, err, statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// SessionEnv 返回 new terminal 会话应注入的 env(供主应用 EnvProvider 调用)。
func (h *Handlers) SessionEnv(base []string) []string {
	return h.svc.SessionEnv(base)
}
