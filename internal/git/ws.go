package git

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

// git 认证的 WebSocket 与 answer 端点。
//
// GET  /api/git/auth       建连:连接存活期间作为 broker 的推送到浏览器通道,
//                          推到浏览器的 ask 事件会触发前端弹窗。
// POST /api/git/auth/answer 提交用户回填的用户名/密码 → broker.answer(token, ...)。
//
// 单用户工具,只关心"当前浏览器连接"这一个订阅者:每次建连 SetAskHandler 换上
// 本连接的写函数,断开时清空并 CancelAll,避免 git push 长期挂起。

// AuthWSHandler 承载 git 认证交互端点。
type AuthWSHandler struct {
	svc *Service
}

// NewAuthWSHandler 构造。
func NewAuthWSHandler(svc *Service) *AuthWSHandler {
	return &AuthWSHandler{svc: svc}
}

// Begin 处理 GET /api/git/auth 长连接。
func (h *AuthWSHandler) Begin(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // 反代层已处理 TLS
	})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "bye")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	broker := h.svc.Broker()
	if broker == nil {
		return
	}

	// 串行写,避免并发写 WS。
	var wmu sync.Mutex
	conn.SetReadLimit(1 << 12)

	// 向浏览器推送一个 ask 事件;写失败即关闭连接(readLoop 会因读错误/context 退出)。
	broker.SetAskHandler(func(ev askEvent) {
		wmu.Lock()
		defer wmu.Unlock()
		b, _ := json.Marshal(ev)
		_ = conn.Write(ctx, websocket.MessageText, b)
	})
	defer broker.SetAskHandler(nil) // 断开:清空回调 + CancelAll

	// 读循环:浏览器不回指令,读主要靠 context 取消收尾;保持连接与保活。
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
	}
}

// Answer 处理 POST /api/git/auth/answer。body: {token, username, password}。
// 用户名或密码为空视为"取消"(走 answerError)。
func (h *AuthWSHandler) Answer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string `json:"token"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Token == "" {
		http.Error(w, "token required", http.StatusBadRequest)
		return
	}
	broker := h.svc.Broker()
	if broker == nil {
		http.Error(w, "credential broker not configured", http.StatusInternalServerError)
		return
	}
	if body.Username == "" && body.Password == "" {
		_ = broker.answerError(body.Token)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if err := broker.answer(body.Token, body.Username, body.Password); err != nil {
		// 幂等:已回填/过期 token 不报错,前端正常关闭弹窗即可。
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}