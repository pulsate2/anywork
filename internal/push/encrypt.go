package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// rsValue 记录大小(RFC 8188),同时用作 GCM 的 AAD。
const rsValue = 4096

// DeriveKeys 计算 RFC 8291 的 CEK(16B)与 NONCE(12B)。
//
// 参考 web-push-libs 标准实现:
//
//	ikm      = auth_secret || shared_secret            (48B)
//	prk      = HKDF-Extract(salt=msgSalt, IKM=ikm)     (32B)
//	cek/nonce= HKDF(secret=shared_secret, salt=prk,
//	                info=buildInfo(type,len,...))      (16B/12B)
//
// 注意第二段 HKDF 的 secret 是 shared_secret、salt 是 prk(而非相反)。
func DeriveKeys(sharedSecret, authSecret, msgSalt, clientRaw, serverRaw []byte) (cek, nonce []byte) {
	ikm := append(append([]byte{}, authSecret...), sharedSecret...)
	prk := hkdf.Extract(sha256.New, ikm, msgSalt)

	cek = make([]byte, 16)
	io.ReadFull(hkdf.New(sha256.New, sharedSecret, prk,
		buildInfo(0x0a, 16, authSecret, clientRaw, serverRaw)), cek)

	nonce = make([]byte, 12)
	io.ReadFull(hkdf.New(sha256.New, sharedSecret, prk,
		buildInfo(0x0b, 12, authSecret, clientRaw, serverRaw)), nonce)
	return
}

// buildInfo 按 RFC 8291 §3.3 组合 info 串:
// "WebPush: info" || L2(147) || type||auth||client||server || L2(keyLen) || "WebPush: info" || context
func buildInfo(typ byte, keyLen int, auth, client, server []byte) []byte {
	ci := append(append(append([]byte{typ}, auth...), client...), server...) // 147B
	var info []byte
	info = append(info, []byte("WebPush: info")...)
	info = append(info, byte(len(ci)>>8), byte(len(ci)))
	info = append(info, ci...)
	info = append(info, byte(keyLen>>8), byte(keyLen))
	info = append(info, []byte("WebPush: info")...)
	info = append(info, client...)
	info = append(info, server...)
	return info
}

// Encrypt 生成完整的 aes128gcm 消息体(RFC 8291 §3 + RFC 8188)。
// p256dh/auth 是订阅里的 base64url 字符串。返回可直接 POST 的 body。
func Encrypt(p256dh, auth string, plaintext []byte, rng io.Reader) ([]byte, error) {
	clientRaw, err := base64.RawURLEncoding.DecodeString(p256dh)
	if err != nil || len(clientRaw) != 65 || clientRaw[0] != 0x04 {
		return nil, fmt.Errorf("非法 p256dh")
	}
	clientKey, err := ecdh.P256().NewPublicKey(clientRaw)
	if err != nil {
		return nil, fmt.Errorf("非法 p256dh 公钥: %w", err)
	}

	authSecret, err := base64.RawURLEncoding.DecodeString(auth)
	if err != nil || len(authSecret) != 16 {
		return nil, fmt.Errorf("非法 auth secret")
	}

	if rng == nil {
		rng = rand.Reader
	}
	eph, err := ecdh.P256().GenerateKey(rng)
	if err != nil {
		return nil, err
	}
	serverRaw := eph.PublicKey().Bytes() // 65B 未压缩
	sharedSecret, err := eph.ECDH(clientKey)
	if err != nil {
		return nil, err
	}

	msgSalt := make([]byte, 16)
	if _, err := io.ReadFull(rng, msgSalt); err != nil {
		return nil, err
	}

	cek, nonce := DeriveKeys(sharedSecret, authSecret, msgSalt, clientRaw, serverRaw)

	// GCM AAD = 记录头:0x00 分隔符 || rs(4B BE = 4096)。
	aad := []byte{0x00, 0x00, 0x10, 0x00}
	// 明文 = 2 字节大端填充长度(0) || 消息。
	input := append([]byte{0x00, 0x00}, plaintext...)

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, input, aad) // 已附 16B tag

	// aes128gcm body(RFC 8188)。
	var body []byte
	body = append(body, msgSalt...)             // 16
	body = append(body, 0x00, 0x00, 0x10, 0x00) // rs=4096, 4B BE
	body = append(body, 65)                     // idlen = 65
	body = append(body, serverRaw...)           // 65
	body = append(body, ct...)                  // ciphertext || tag
	return body, nil
}
