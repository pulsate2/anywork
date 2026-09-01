// Package push 实现 Web Push(里程碑 7):
// VAPID 认证、RFC 8291 aes128gcm 消息加密、投递到推送服务。
// 全部使用标准库 + 已有的 golang.org/x/crypto,不引入新依赖。
package push

import (
	"encoding/json"
	"time"
)

// Subscription 是存储在数据库中的客户端 PushSubscription。
type Subscription struct {
	ID        string `json:"id"`
	Endpoint  string `json:"endpoint"`
	P256DH    string `json:"p256dh"`
	Auth      string `json:"auth"`
	CreatedAt string `json:"createdAt"`
	LastSeen  string `json:"lastSeen"`
}

// Message 是明文负载(Service Worker 展示为系统通知)。
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Tag   string `json:"tag,omitempty"` // 合并同标签通知
	URL   string `json:"url,omitempty"` // 点击打开的目标
}

// now 返回 ISO-8601 时间字符串,与项目其余部分一致。
func now() string { return time.Now().UTC().Format(time.RFC3339) }

// jsonMarshal 序列化消息,忽略错误(结构已知)。
func jsonMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
