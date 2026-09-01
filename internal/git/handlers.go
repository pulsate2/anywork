package git

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Handlers 承载 git HTTP 端点。
type Handlers struct {
	svc *Service
}

func NewHandlers(svc *Service) *Handlers {
	return &Handlers{svc: svc}
}

// repoInfo 返回仓库解析信息。
func (h *Handlers) RepoInfo(w http.ResponseWriter, r *http.Request) {
	info, err := h.svc.ResolveToRepo(r.URL.Query().Get("path"))
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (h *Handlers) Status(w http.ResponseWriter, r *http.Request) {
	st, err := h.svc.Status(r.URL.Query().Get("path"))
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// diff 返回 unified 文本(纯文本,便于前端直接展示与着色)。
func (h *Handlers) Diff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope := q.Get("scope")
	if scope == "" {
		scope = "worktree"
	}
	txt, err := h.svc.Diff(q.Get("path"), scope, q.Get("file"))
	if err != nil {
		h.httpErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(txt))
}

func (h *Handlers) Log(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	n, _ := strconv.Atoi(q.Get("n"))
	skip, _ := strconv.Atoi(q.Get("skip"))
	nodes, err := h.svc.Log(q.Get("path"), n, skip)
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nodes)
}

// Branches 返回结构化的本地/远端分支列表。
func (h *Handlers) Branches(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.Branches(r.URL.Query().Get("path"))
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) Remotes(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.Remotes(r.URL.Query().Get("path"))
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type stageReq struct {
	Path  string   `json:"path"`
	Files []string `json:"files"`
}

// stage 暂存/取消暂存。?op=reset 取消,否则添加。
func (h *Handlers) Stage(w http.ResponseWriter, r *http.Request) {
	var body stageReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var err error
	if r.URL.Query().Get("op") == "reset" {
		err = h.svc.StageReset(body.Path, body.Files)
	} else {
		err = h.svc.StageAdd(body.Path, body.Files)
	}
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handlers) Commit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path    string `json:"path"`
		Message string `json:"message"`
		AddAll  bool   `json:"addAll"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	out, err := h.svc.Commit(body.Path, body.Message, body.AddAll)
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"out": out})
}
func (h *Handlers) Push(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path        string `json:"path"`
		Remote      string `json:"remote"`
		Branch      string `json:"branch"`
		SetUpstream bool   `json:"setUpstream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	out, err := h.svc.Push(body.Path, body.Remote, body.Branch, body.SetUpstream)
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"out": out})
}

func (h *Handlers) Pull(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	out, err := h.svc.Pull(body.Path)
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"out": out})
}
func (h *Handlers) Branch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path  string `json:"path"`
		Op    string `json:"op"`
		Name  string `json:"name"`
		Start string `json:"start"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	out, err := h.svc.Branch(body.Path, body.Op, body.Name, body.Start)
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"out": out})
}
func (h *Handlers) Stash(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path    string `json:"path"`
		Op      string `json:"op"`
		Message string `json:"message"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	out, err := h.svc.Stash(body.Path, body.Op, body.Message)
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"out": out})
}
func (h *Handlers) Worktree(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path  string `json:"path"`
		Op    string `json:"op"`
		Path2 string `json:"path2"`
		Ref   string `json:"ref"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	out, err := h.svc.Worktree(body.Path, body.Op, body.Path2, body.Ref)
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"out": out})
}
