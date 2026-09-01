// Package auth 实现单密码鉴权:登录限速、密码验证、内存 HMAC 签名 Cookie。
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	cookieName   = "lr_session"
	cookieMaxAge = 24 * 3600
)

// ErrInvalid 表示凭据无效。
var ErrInvalid = errors.New("invalid credentials")

// Store 持有签名密钥,负责签发/校验会话 Cookie 与密码哈希。
type Store struct {
	mu      sync.Mutex
	signKey []byte // 会话签名 HMAC 密钥(内存态)
	// 登录限速:按客户端 IP 的失败计数窗口
	attempts map[string]*attempt
	lockTTL  time.Duration
}

type attempt struct {
	fails    int
	firstAt  time.Time
	lockedAt time.Time
}

const (
	maxFails        = 5
	lockWindow      = time.Minute // 失败计数窗口
	lockDuration    = time.Minute // 达到上限后锁定时长
	fixedSignKeyLen = 32          // 签名密钥长度
)

// New 初始化签名密钥。secretFile 非空则持久化到该文件(0600),避免重启丢失会话。
func New(secretFile string) (*Store, error) {
	s := &Store{
		signKey:  make([]byte, fixedSignKeyLen),
		attempts: map[string]*attempt{},
		lockTTL:  lockDuration,
	}
	if err := s.loadOrCreateKey(secretFile); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) loadOrCreateKey(secretFile string) error {
	if secretFile != "" {
		if b, err := readSecret(secretFile); err == nil && len(b) == fixedSignKeyLen {
			copy(s.signKey, b)
		}
	}
	// 密钥未就绪则生成,并(在指定了文件时)落盘。
	allZero := true
	for _, b := range s.signKey {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		if _, err := rand.Read(s.signKey); err != nil {
			return err
		}
		if secretFile != "" {
			if err := writeSecret(secretFile, s.signKey); err != nil {
				return err
			}
		}
	}
	return nil
}

// EnsurePassword 首次启动且未提供密码时,生成随机密码并打印到日志。
// (main 已强制要求密码,此处仅作防御)

// HashPassword 生成 bcrypt 密码哈希。
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

// VerifyPassword 恒定时间比较 bcrypt 哈希。
func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// CheckLogin 校验密码,并做限速。通过后签发新的签名令牌。
func (s *Store) CheckLogin(ip, password, passwordHash string) (string, error) {
	if !s.allow(ip) {
		return "", ErrInvalid
	}
	if !VerifyPassword(passwordHash, password) {
		s.recordFail(ip)
		return "", ErrInvalid
	}
	s.recordSuccess(ip)
	return s.newToken(ip), nil
}

// middleware:校验会话 Cookie。

// NewCookie 签发会话 Cookie(防篡改 token,HttpOnly,SameSite=Strict)。
func (s *Store) NewCookie(token string) *http.Cookie {
	return &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   false, // 默认反代层终止 TLS,见 DESIGN 第 7 节
	}
}

// ClearCookie 返回清除会话的 Cookie。
func ClearCookie() *http.Cookie {
	return &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
}

// Middleware 校验请求中的会话 Cookie;未通过则 401。
func (s *Store) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil || !s.valid(c.Value) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// valid 校验令牌签名与时效。
func (s *Store) valid(token string) bool {
	parts := splitToken(token)
	if len(parts) != 3 {
		return false
	}
	ip, expHex, sigHex := parts[0], parts[1], parts[2]
	exp, err := strconv.ParseInt(expHex, 16, 64)
	if err != nil || exp < time.Now().Unix() {
		return false
	}
	want := s.sign([]byte(ip + "|" + expHex))
	got, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	return hmac.Equal(want, got)
}

func (s *Store) newToken(ip string) string {
	exp := time.Now().Add(cookieMaxAge * time.Second).Unix()
	expHex := strconv.FormatInt(exp, 16)
	payload := ip + "|" + expHex
	sig := s.sign([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload + "|" + hex.EncodeToString(sig)))
}

func (s *Store) sign(b []byte) []byte {
	m := hmac.New(sha256.New, s.signKey)
	m.Write(b)
	return m.Sum(nil)
}

func splitToken(token string) []string {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil
	}
	// payload|sig
	s := string(raw)
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '|' {
			payload, sig := s[:i], s[i+1:]
			parts := splitN(payload, "|", 2)
			if len(parts) != 2 {
				return nil
			}
			return []string{parts[0], parts[1], sig}
		}
	}
	return nil
}

func splitN(s, sep string, n int) []string {
	for i := 0; i < len(s); i++ {
		if s[i] == sep[0] {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}

// ---- 限速 ----

func (s *Store) allow(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.attempts[ip]
	if a == nil {
		return true
	}
	if !a.lockedAt.IsZero() {
		if time.Since(a.lockedAt) > s.lockTTL {
			delete(s.attempts, ip)
			return true
		}
		return false
	}
	if time.Since(a.firstAt) > lockWindow {
		delete(s.attempts, ip)
		return true
	}
	return a.fails < maxFails
}

func (s *Store) recordFail(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.attempts[ip]
	if a == nil {
		a = &attempt{firstAt: time.Now()}
		s.attempts[ip] = a
	}
	a.fails++
	if a.fails >= maxFails {
		a.lockedAt = time.Now()
	}
}

func (s *Store) recordSuccess(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.attempts, ip)
}

// ---- 密钥文件 ----

var osReadFile = os.ReadFile
var osWriteFile = func(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func readSecret(path string) ([]byte, error) {
	return osReadFile(path)
}

func writeSecret(path string, key []byte) error {
	return osWriteFile(path, key, 0o600)
}
