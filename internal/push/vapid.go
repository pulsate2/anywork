package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// VAPID 持有用于签名推送请求的 P-256 ECDSA 私钥。
type VAPID struct {
	private []byte // 32 字节 P-256 私钥标量
}

// LoadOrCreate 加载或生成 VAPID 密钥对。
// 优先级:cfgPrivate(已解码的 32 字节标量) > <dataDir>/vapid.key > 生成。
// 文件以 0600 权限持久化,避免重启丢失(与 auth.session.key 一致)。
func LoadOrCreate(dataDir string, cfgPrivate string) (*VAPID, error) {
	// 配置提供的私钥优先。
	if cfgPrivate != "" {
		scalar, err := decodeScalar(cfgPrivate)
		if err != nil {
			return nil, fmt.Errorf("vapid 私钥: %w", err)
		}
		return &VAPID{private: scalar}, nil
	}

	path := filepath.Join(dataDir, "vapid.key")
	if b, err := os.ReadFile(path); err == nil {
		if scalar, ok := normalizeScalar(b); ok {
			return &VAPID{private: scalar}, nil
		}
	}

	// 生成并落盘。
	scalar := make([]byte, 32)
	if _, err := rand.Read(scalar); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, scalar, 0o600); err != nil {
		return nil, err
	}
	return &VAPID{private: scalar}, nil
}

// decodeScalar 解析配置私钥:优先 base64url,其次原样 32 字节。
func decodeScalar(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	if normalize, ok := normalizeScalar([]byte(s)); ok {
		return normalize, nil
	}
	return nil, fmt.Errorf("无法解析为 32 字节 P-256 私钥")
}

// normalizeScalar 校验 32 字节私钥标量(非全零)。
func normalizeScalar(b []byte) ([]byte, bool) {
	if len(b) != 32 {
		return nil, false
	}
	allZero := true
	for _, x := range b {
		if x != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return nil, false
	}
	out := make([]byte, 32)
	copy(out, b)
	return out, true
}

// privateECDSA 将 32 字节标量还原为 ecdsa.PrivateKey。
func (v *VAPID) privateECDSA() (*ecdsa.PrivateKey, error) {
	d := new(big.Int).SetBytes(v.private)
	curve := elliptic.P256()
	x, y := curve.ScalarBaseMult(v.private)
	return &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y},
		D:         d,
	}, nil
}

// publicRaw 返回未压缩的 65 字节公钥点(0x04 || X || Y)。
func (v *VAPID) publicRaw() ([]byte, error) {
	priv, err := v.privateECDSA()
	if err != nil {
		return nil, err
	}
	return elliptic.Marshal(priv.Curve, priv.X, priv.Y), nil
}

// PublicB64URL 返回 base64url 编码的公钥(用于 subscribe 的 applicationServerKey)。
func (v *VAPID) PublicB64URL() (string, error) {
	raw, err := v.publicRaw()
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Authorization 生成 VAPID Authorization 头:vapid t=<jwt>,k=<b64url public>。
// aud 为推送服务来源(scheme+host);subject 为 VAPID subject(mailto:)。
func (v *VAPID) Authorization(aud, subject string, now time.Time) (string, error) {
	priv, err := v.privateECDSA()
	if err != nil {
		return "", err
	}
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims := base64.RawURLEncoding.EncodeToString([]byte(
		fmt.Sprintf(`{"aud":%q,"exp":%d,"sub":%q}`, aud, now.Unix()+12*3600, subject)))
	payload := hdr + "." + claims
	digest := sha256.Sum256([]byte(payload))
	r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	jwt := payload + "." + base64.RawURLEncoding.EncodeToString(sig)
	pub, err := v.PublicB64URL()
	if err != nil {
		return "", err
	}
	return "vapid t=" + jwt + ",k=" + pub, nil
}
