package push

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDeliverEndToEnd 用一个 mock 推送服务端到端验证:
// 校验 VAPID Authorization(JWT 算法/aud/exp/签名),并对密文解密断言消息内容。
func TestDeliverEndToEnd(t *testing.T) {
	clientECDH, _ := ecdh.P256().GenerateKey(rand.Reader)
	clientRaw := clientECDH.PublicKey().Bytes()
	authSecret := make([]byte, 16)
	rand.Read(authSecret)

	var (
		mu        sync.Mutex
		gotAuth   string
		gotEnc    string
		gotTTL    string
		gotBody   []byte
		authCalls = 0
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		authCalls++
		gotAuth = r.Header.Get("Authorization")
		gotEnc = r.Header.Get("Content-Encoding")
		gotTTL = r.Header.Get("TTL")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	vapid := &VAPID{private: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}}
	sender := &Sender{
		HTTP:    NewClient(),
		VAPID:   vapid,
		Subject: "mailto:admin@localhost",
	}

	sub := Subscription{
		Endpoint: srv.URL + "/send",
		P256DH:   base64.RawURLEncoding.EncodeToString(clientRaw),
		Auth:     base64.RawURLEncoding.EncodeToString(authSecret),
	}
	msg := Message{Title: "LightRemote", Body: "测试通知", Tag: "test", URL: "/"}

	sent, gone, err := sender.Deliver(context.Background(), sub, msg)
	if err != nil || !sent || gone {
		t.Fatalf("Deliver: sent=%v gone=%v err=%v", sent, gone, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotEnc != "aes128gcm" {
		t.Fatalf("Content-Encoding = %q", gotEnc)
	}
	if gotTTL == "" {
		t.Fatal("缺少 TTL 头")
	}
	// VAPID 校验。
	if !strings.HasPrefix(gotAuth, "vapid t=") {
		t.Fatalf("Authorization 头错误: %q", gotAuth)
	}
	verifyVapid(t, gotAuth, srv.URL, vapid)

	// 解密 body。
	decryptAndAssert(t, gotBody, clientECDH, authSecret, clientRaw, `{"title":"LightRemote","body":"测试通知","tag":"test","url":"/"}`)
	if authCalls != 1 {
		t.Fatalf("期望 1 次请求,实际 %d", authCalls)
	}
}

// TestDeliverGoneAndTransient 测试 410(删除)与 429(保留)处理。
func TestDeliverGoneAndTransient(t *testing.T) {
	clientECDH, _ := ecdh.P256().GenerateKey(rand.Reader)
	authSecret := make([]byte, 16)
	rand.Read(authSecret)
	vapid := &VAPID{private: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}}
	sender := &Sender{HTTP: NewClient(), VAPID: vapid, Subject: "mailto:x"}
	mkSub := func(srv *httptest.Server) Subscription {
		return Subscription{
			Endpoint: srv.URL,
			P256DH:   base64.RawURLEncoding.EncodeToString(clientECDH.PublicKey().Bytes()),
			Auth:     base64.RawURLEncoding.EncodeToString(authSecret),
		}
	}
	msg := Message{Title: "t", Body: "b"}

	// 410 Gone → gone=true
	s410 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer s410.Close()
	sent, gone, err := sender.Deliver(context.Background(), mkSub(s410), msg)
	if sent || !gone || err != nil {
		t.Fatalf("410: sent=%v gone=%v err=%v", sent, gone, err)
	}

	// 429 Too Many Requests → 保留,报错
	s429 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer s429.Close()
	sent, gone, err = sender.Deliver(context.Background(), mkSub(s429), msg)
	if sent || gone || err == nil {
		t.Fatalf("429: sent=%v gone=%v err=%v", sent, gone, err)
	}
}

// TestVAPIDAuthorization 直接验证 JWT 结构(ES256、aud、exp、签名)。
func TestVAPIDAuthorization(t *testing.T) {
	vapid := &VAPID{private: make([]byte, 32)}
	for i := range vapid.private {
		vapid.private[i] = byte(i + 1)
	}
	aud := "https://fcm.googleapis.com"
	now := time.Now()
	header, err := vapid.Authorization(aud, "mailto:admin@localhost", now)
	if err != nil {
		t.Fatal(err)
	}
	// 解析 t=... , k=...
	parts := strings.Split(header, ",")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "vapid t=") || !strings.HasPrefix(parts[1], "k=") {
		t.Fatalf("格式错误: %q", header)
	}
	jwt := strings.TrimPrefix(parts[0], "vapid t=")
	seg := strings.Split(jwt, ".")
	if len(seg) != 3 {
		t.Fatalf("JWT 段数错误: %d", len(seg))
	}
	hdrB, _ := base64.RawURLEncoding.DecodeString(seg[0])
	var hd map[string]string
	json.Unmarshal(hdrB, &hd)
	if hd["alg"] != "ES256" {
		t.Fatalf("alg = %q", hd["alg"])
	}
	claimsB, _ := base64.RawURLEncoding.DecodeString(seg[1])
	var cl struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	json.Unmarshal(claimsB, &cl)
	if cl.Aud != aud || cl.Sub != "mailto:admin@localhost" {
		t.Fatalf("claims 错误: %+v", cl)
	}
	if cl.Exp <= now.Unix() || cl.Exp > now.Add(24*time.Hour).Unix() {
		t.Fatalf("exp 异常: %d", cl.Exp)
	}
	// 校验签名。
	sigB, _ := base64.RawURLEncoding.DecodeString(seg[2])
	if len(sigB) != 64 {
		t.Fatalf("签名长度 %d", len(sigB))
	}
	priv, _ := vapid.privateECDSA()
	pub := &priv.PublicKey
	r := new(big.Int).SetBytes(sigB[:32])
	s := new(big.Int).SetBytes(sigB[32:])
	digest := sha256.Sum256([]byte(seg[0] + "." + seg[1]))
	if !ecdsa.Verify(pub, digest[:], r, s) {
		t.Fatal("JWT 签名校验失败")
	}
}

// --- helpers ---

func verifyVapid(t *testing.T, authHeader, aud string, vapid *VAPID) {
	t.Helper()
	parts := strings.Split(authHeader, ",")
	if len(parts) != 2 {
		t.Fatalf("auth header: %q", authHeader)
	}
	jwt := strings.TrimPrefix(parts[0], "vapid t=")
	seg := strings.Split(jwt, ".")
	if len(seg) != 3 {
		t.Fatalf("JWT 段数 %d", len(seg))
	}
	claimsB, _ := base64.RawURLEncoding.DecodeString(seg[1])
	var cl struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
	}
	json.Unmarshal(claimsB, &cl)
	if cl.Aud != aud {
		t.Fatalf("aud = %q, want %q", cl.Aud, aud)
	}
	if cl.Exp < time.Now().Unix() {
		t.Fatal("exp 已过期")
	}
	// 用 k 里的公钥验签。
	pubB := strings.TrimPrefix(parts[1], "k=")
	pubRaw, _ := base64.RawURLEncoding.DecodeString(pubB)
	if len(pubRaw) != 65 || pubRaw[0] != 0x04 {
		t.Fatalf("k 长度 %d", len(pubRaw))
	}
	curve := elliptic.P256()
	x := new(big.Int).SetBytes(pubRaw[1:33])
	y := new(big.Int).SetBytes(pubRaw[33:65])
	pub := &ecdsa.PublicKey{Curve: curve, X: x, Y: y}
	sigB, _ := base64.RawURLEncoding.DecodeString(seg[2])
	r := new(big.Int).SetBytes(sigB[:32])
	s := new(big.Int).SetBytes(sigB[32:])
	digest := sha256.Sum256([]byte(seg[0] + "." + seg[1]))
	if !ecdsa.Verify(pub, digest[:], r, s) {
		t.Fatal("VAPID 签名校验失败")
	}
}

func decryptAndAssert(t *testing.T, body []byte, clientECDH *ecdh.PrivateKey, authSecret, clientRaw []byte, want string) {
	t.Helper()
	if len(body) < 86 {
		t.Fatalf("body 过短 %d", len(body))
	}
	if body[20] != 65 {
		t.Fatalf("idlen %d", body[20])
	}
	salt := body[0:16]
	serverPub := body[21:86]
	ct := body[86:]
	serverKey, err := ecdh.P256().NewPublicKey(serverPub)
	if err != nil {
		t.Fatalf("server pub: %v", err)
	}
	shared, err := clientECDH.ECDH(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	cek, nonce := DeriveKeys(shared, authSecret, salt, clientRaw, serverPub)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	out, err := gcm.Open(nil, nonce, ct, []byte{0, 0, 0x10, 0})
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if string(out[2:]) != want {
		t.Fatalf("明文 = %q, want %q", out[2:], want)
	}
}
