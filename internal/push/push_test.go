package push

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"testing"
)

// TestDeriveKeysAgainstManualHMAC 交叉校验 hkdf 实现与 RFC 8291 手工 HMAC 形式一致,
// 防止 hkdf 参数写错。
func TestDeriveKeysAgainstManualHMAC(t *testing.T) {
	auth := make([]byte, 16)
	rand.Read(auth)
	shared := make([]byte, 32)
	rand.Read(shared)
	salt := make([]byte, 16)
	rand.Read(salt)
	client := make([]byte, 65)
	server := make([]byte, 65)
	rand.Read(client)
	rand.Read(server)
	client[0], server[0] = 0x04, 0x04

	// hkdf 实现。
	cek, nonce := DeriveKeys(shared, auth, salt, client, server)

	// 手工 HMAC 参考形式(RFC 5869:Expand 追加 counter=0x01)。
	ikm := append(append([]byte{}, auth...), shared...)
	prkKey := hmacSHA256(salt, ikm)
	cekInfo := buildInfo(0x0a, 16, auth, client, server)
	nonceInfo := buildInfo(0x0b, 12, auth, client, server)
	prkCek := hmacSHA256(prkKey, shared)
	prkNonce := hmacSHA256(prkKey, shared)
	wantCek := hmacSHA256(prkCek, append(append([]byte{}, cekInfo...), 0x01))[:16]
	wantNonce := hmacSHA256(prkNonce, append(append([]byte{}, nonceInfo...), 0x01))[:12]

	if !bytes.Equal(cek, wantCek) {
		t.Fatalf("CEK 不匹配: %x != %x", cek, wantCek)
	}
	if !bytes.Equal(nonce, wantNonce) {
		t.Fatalf("NONCE 不匹配: %x != %x", nonce, wantNonce)
	}
}

func hmacSHA256(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

// TestEncryptRoundTrip 用"浏览器侧"密钥对加密再解密,验证整条管线
// (EC 点解析、ECDH、HKDF、GCM AAD、tag、body 布局)。
func TestEncryptRoundTrip(t *testing.T) {
	clientECDH, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientRaw := clientECDH.PublicKey().Bytes() // 65B
	authSecret := make([]byte, 16)
	rand.Read(authSecret)

	p256dh := base64.RawURLEncoding.EncodeToString(clientRaw)
	auth := base64.RawURLEncoding.EncodeToString(authSecret)

	plaintext := []byte(`{"title":"测试","body":"hello"}`)
	body, err := Encrypt(p256dh, auth, plaintext, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	// 布局校验。
	if len(body) < 16+4+1+65+16 {
		t.Fatalf("body 过短: %d", len(body))
	}
	if !bytes.Equal(body[16:20], []byte{0, 0, 0x10, 0}) {
		t.Fatalf("rs 段错误: %x", body[16:20])
	}
	if body[20] != 65 {
		t.Fatalf("idlen != 65: %d", body[20])
	}

	// 从客户端视角解密。
	salt := body[0:16]
	serverPub := body[21:86]
	ct := body[86:]

	serverKey, err := ecdh.P256().NewPublicKey(serverPub)
	if err != nil {
		t.Fatalf("server 公钥解析失败: %v", err)
	}
	shared, err := clientECDH.ECDH(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	cek, nonce := DeriveKeys(shared, authSecret, salt, clientRaw, serverPub)

	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	aad := []byte{0, 0, 0x10, 0}
	out, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	// 去掉 2 字节填充头。
	if len(out) < 2 {
		t.Fatal("明文过短")
	}
	if out[0] != 0 || out[1] != 0 {
		t.Fatalf("填充头应为 0x0000: %x", out[:2])
	}
	got := out[2:]
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("明文不匹配: %q != %q", got, plaintext)
	}
}

// TestEncryptDifferentSalt 每次调用产生不同密文(随机盐生效)。
func TestEncryptDifferentSalt(t *testing.T) {
	clientECDH, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)
	p256dh := base64.RawURLEncoding.EncodeToString(clientECDH.PublicKey().Bytes())
	authB := base64.RawURLEncoding.EncodeToString(auth)

	a, err := Encrypt(p256dh, authB, []byte("x"), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Encrypt(p256dh, authB, []byte("x"), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("两次加密结果应不同(随机盐)")
	}
}

// TestEncryptInvalidInputs 非法 p256dh/auth 应报错。
func TestEncryptInvalidInputs(t *testing.T) {
	if _, err := Encrypt("AAAA", "AAAA", []byte("x"), nil); err == nil {
		t.Fatal("非法 p256dh 应报错")
	}
	clientECDH, _ := ecdh.P256().GenerateKey(rand.Reader)
	p256dh := base64.RawURLEncoding.EncodeToString(clientECDH.PublicKey().Bytes())
	if _, err := Encrypt(p256dh, "AAAA", []byte("x"), nil); err == nil {
		t.Fatal("非法 auth 应报错")
	}
}

var _ = io.Discard
