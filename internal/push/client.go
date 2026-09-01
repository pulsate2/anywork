package push

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// defaultTTL 消息在推送服务上的存活时长(24h)。
const defaultTTL = 86400

// Sender 负责将加密消息投递到订阅端点。
type Sender struct {
	HTTP    *http.Client
	VAPID   *VAPID
	Subject string // VAPID subject,形如 mailto:xxx
}

// NewClient 构造投递用的 http.Client:短超时、不自动跟随重定向。
func NewClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Deliver 加密并投递一条消息。
// 返回 sent(是否送达)、gone(订阅是否失效需删除)、err。
func (s *Sender) Deliver(ctx context.Context, sub Subscription, msg Message) (sent, gone bool, err error) {
	plain := jsonMarshal(msg)
	body, err := Encrypt(sub.P256DH, sub.Auth, plain, rand.Reader)
	if err != nil {
		return false, false, fmt.Errorf("加密失败: %w", err)
	}

	aud := endpointOrigin(sub.Endpoint)
	if aud == "" {
		return false, false, errors.New("非法端点")
	}
	authHeader, err := s.VAPID.Authorization(aud, s.Subject, time.Now())
	if err != nil {
		return false, false, err
	}

	resp, err := s.post(ctx, sub.Endpoint, authHeader, body)
	if err != nil {
		return false, false, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	switch {
	case resp.StatusCode == 201 || resp.StatusCode == 204:
		return true, false, nil
	case resp.StatusCode == 404 || resp.StatusCode == 410:
		return false, true, nil
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		// 重定向:手动跟随一次(保持 body 精确)。
		loc := resp.Header.Get("Location")
		if loc == "" {
			return false, false, fmt.Errorf("推送服务重定向但无 Location: %d", resp.StatusCode)
		}
		r2, err := s.post(ctx, loc, authHeader, body)
		if err != nil {
			return false, false, err
		}
		defer r2.Body.Close()
		io.Copy(io.Discard, r2.Body)
		switch {
		case r2.StatusCode == 201 || r2.StatusCode == 204:
			return true, false, nil
		case r2.StatusCode == 404 || r2.StatusCode == 410:
			return false, true, nil
		default:
			return false, false, fmt.Errorf("推送服务错误: %d", r2.StatusCode)
		}
	default:
		return false, false, fmt.Errorf("推送服务错误: %d", resp.StatusCode)
	}
}

func (s *Sender) post(ctx context.Context, endpoint, authHeader string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", fmt.Sprint(defaultTTL))
	req.Header.Set("Urgency", "high")
	req.Header.Set("Content-Type", "application/octet-stream")
	return s.HTTP.Do(req)
}

// endpointOrigin 提取端点的 scheme://host 作为 VAPID aud。
func endpointOrigin(ep string) string {
	u, err := url.Parse(ep)
	if err != nil {
		return ""
	}
	if u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
