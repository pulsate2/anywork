package push

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Handlers 暴露 Web Push 的 HTTP 接口。
type Handlers struct {
	store  *Store
	sender *Sender
	// IdleSeconds 终端安静多久后触发"命令完成"推送;0 = 禁用。
	IdleSeconds int
}

// NewHandlers 构造处理器。
func NewHandlers(store *Store, sender *Sender) *Handlers {
	return &Handlers{store: store, sender: sender}
}

// Status GET /api/push/status
// 返回 {vapidPublicKey, count, configured}。
func (h *Handlers) Status(w http.ResponseWriter, r *http.Request) {
	pub, err := h.sender.VAPID.PublicB64URL()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	count, _ := h.store.Count()
	writeJSON(w, http.StatusOK, map[string]any{
		"vapidPublicKey": pub,
		"count":          count,
		"configured":     true,
		"idleSeconds":    h.IdleSeconds,
	})
}

// subscribeReq 浏览器 PushSubscription 的 JSON 形式。
type subscribeReq struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// Subscribe POST /api/push/subscribe
// 校验并保存订阅,幂等。
func (h *Handlers) Subscribe(w http.ResponseWriter, r *http.Request) {
	var body subscribeReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
		return
	}
	if body.Endpoint == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "endpoint required"})
		return
	}
	pub, err := base64.RawURLEncoding.DecodeString(body.Keys.P256DH)
	if err != nil || len(pub) != 65 || pub[0] != 0x04 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid p256dh"})
		return
	}
	auth, err := base64.RawURLEncoding.DecodeString(body.Keys.Auth)
	if err != nil || len(auth) != 16 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid auth"})
		return
	}
	isNew, err := h.store.Upsert(Subscription{
		ID:       randomID(),
		Endpoint: body.Endpoint,
		P256DH:   body.Keys.P256DH,
		Auth:     body.Keys.Auth,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	code := http.StatusOK
	if isNew {
		code = http.StatusCreated
	}
	writeJSON(w, code, map[string]any{"ok": true})
}

// Unsubscribe POST /api/push/unsubscribe
// body {endpoint} —— 省略/空 endpoint 时清空全部。
func (h *Handlers) Unsubscribe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var removed int64
	var err error
	if body.Endpoint == "" {
		removed, err = h.store.DeleteAll()
	} else {
		removed, err = h.store.DeleteByEndpoint(body.Endpoint)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
}

// Test POST /api/push/test —— 向所有订阅发送测试通知。
func (h *Handlers) Test(w http.ResponseWriter, r *http.Request) {
	subs, err := h.store.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	sent, failed := h.deliverAll(r.Context(), subs, Message{
		Title: "LightRemote",
		Body:  "测试通知:Web Push 已启用 ✓",
		Tag:   "test",
		URL:   "/",
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sent": sent, "failed": failed})
}

// NotifyTerminal 终端空闲时调用:向所有订阅发送"命令已静默"通知。
func (h *Handlers) NotifyTerminal(sessionID, dir string) {
	subs, err := h.store.List()
	if err != nil || len(subs) == 0 {
		return
	}
	h.deliverAll(context.Background(), subs, Message{
		Title: "命令已静默",
		Body:  fmt.Sprintf("终端会话 %s 已停止输出 %d 秒(可能已完成)。目录: %s", sessionID, h.IdleSeconds, dir),
		Tag:   "term-" + sessionID,
		URL:   "/",
	})
}

// deliverAll 逐个投递;对 gone(失效)的订阅清理删除。返回 sent/failed。
func (h *Handlers) deliverAll(ctx context.Context, subs []Subscription, msg Message) (sent, failed int) {
	for _, sub := range subs {
		s, gone, err := h.sender.Deliver(ctx, sub, msg)
		if s {
			sent++
		} else if err != nil || gone {
			failed++
		}
		if gone {
			_, _ = h.store.DeleteByEndpoint(sub.Endpoint)
		}
	}
	return
}

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
