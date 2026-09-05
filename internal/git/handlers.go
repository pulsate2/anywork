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

// Init 在当前目录上 git init。只读模式下被 allowWrite 拒掉(403),
// 目录已经在仓库里回 409。
func (h *Handlers) Init(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	info, err := h.svc.Init(body.Path)
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
	txt, err := h.svc.Diff(q.Get("path"), scope, q.Get("file"), q.Get("ref"))
	if err != nil {
		h.httpErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(txt))
}

// Show 返回某个提交里某个文件的全文(纯文本,和 fs/read 一样直出正文)。
func (h *Handlers) Show(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	txt, err := h.svc.Show(q.Get("path"), q.Get("ref"), q.Get("file"))
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

// Identity 读取当前仓库的提交身份(含"git 现在能不能提交"的判断)。
func (h *Handlers) Identity(w http.ResponseWriter, r *http.Request) {
	id, err := h.svc.Identity(r.URL.Query().Get("path"))
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, id)
}

// SetIdentity 把提交身份写进这一个仓库的 .git/config(不动全局)。
func (h *Handlers) SetIdentity(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path  string `json:"path"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	id, err := h.svc.SetIdentity(body.Path, body.Name, body.Email)
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, id)
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

// Fetch 只更新远端跟踪引用,不合并。body: {path, remote?}。
func (h *Handlers) Fetch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path   string `json:"path"`
		Remote string `json:"remote"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	out, err := h.svc.Fetch(body.Path, body.Remote)
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"out": out})
}

// Remote 远端管理。op: add|remove|rename|set-url;value 是 URL 或新名字。
func (h *Handlers) Remote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path  string `json:"path"`
		Op    string `json:"op"`
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	out, err := h.svc.RemoteOp(body.Path, body.Op, body.Name, body.Value)
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

// Restore 撤销改动。mode 见 Service.Restore(worktree|all|untracked)。
func (h *Handlers) Restore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path  string   `json:"path"`
		Files []string `json:"files"`
		Mode  string   `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	out, err := h.svc.Restore(body.Path, body.Files, body.Mode)
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"out": out})
}

// Revert 回滚提交。op: revert|abort。
func (h *Handlers) Revert(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
		Op   string `json:"op"`
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Op == "" {
		body.Op = "revert"
	}
	out, err := h.svc.Revert(body.Path, body.Op, body.Hash)
	if err != nil {
		h.httpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"out": out})
}
