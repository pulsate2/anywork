package terminal

import (
	"context"
	"encoding/json"
	"time"

	"github.com/coder/websocket"
)

// 帧类型字节。
const (
	frameTypeCreate     = 'c'
	frameTypeAttach     = 'a'
	frameTypeInput      = 'i'
	frameTypeResize     = 'r'
	frameTypeKill       = 'k'
	frameTypeList       = 'l'
	frameTypeDetach     = 'd'
	frameTypeOutput     = 'o' // 服务端 → 客户端
	frameTypeText       = 't' // 服务端 → 客户端
	frameTypeSession    = 's' // 服务端 → 客户端:会话列表
	frameTypeSessionNew = 'n' // 服务端 → 客户端:会话被创建/附加
	frameTypeExit       = 'e' // 服务端 → 客户端:会话退出
)

// 第一字节是帧类型,其余是 JSON 负载(输出帧除外:纯二进制负载)。

// inFrame 客户端 → 服务端。
type inFrame struct {
	Type  string `json:"type"`           // create|attach|input|resize|kill|list|detach
	SID   string `json:"sid,omitempty"`  // 目标会话
	Data  string `json:"data,omitempty"` // input 的文本(已 base64? 不,WS 文本帧)
	Cols  int    `json:"cols,omitempty"`
	Rows  int    `json:"rows,omitempty"`
	Dir   string `json:"dir,omitempty"`
	Shell string `json:"shell,omitempty"`
}

// outFrame 服务端 → 客户端。
type outFrame struct {
	text bool   // true = JSON 文本帧;false = 二进制输出帧
	data []byte // 二进制帧:终端输出
	msg  string // 文本帧:JSON 负载
}

// Summary 会话摘要(列表 / 创建响应)。
type Summary struct {
	ID        string `json:"id"`
	Dir       string `json:"dir"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
	CreatedAt string `json:"createdAt"`
	Dead      bool   `json:"dead"`
	ExitCode  int    `json:"exitCode"`
	// 以下是实际生效的资源限制,0 = 该项没限。LimitMode 是所用机制
	// (cgroup2 / rlimit / job),前端拿它决定提示文案。
	MemoryMB   int    `json:"memoryMB,omitempty"`
	CPUPercent int    `json:"cpuPercent,omitempty"`
	LimitMode  string `json:"limitMode,omitempty"`
}

// Client 封装一个 WS 连接:接收客户端命令,并接收广播帧。
type Client struct {
	conn *websocket.Conn
	send chan outFrame
}

func newClient(conn *websocket.Conn) *Client {
	return &Client{conn: conn, send: make(chan outFrame, 256)}
}

// push 向该客户端投递一帧(非阻塞,满则丢弃输出保护连接)。
func (c *Client) push(f outFrame) {
	select {
	case c.send <- f:
	default:
		// 发送队列已满:客户端消费不过来,丢弃最旧输出。
		select {
		case <-c.send:
		default:
		}
		select {
		case c.send <- f:
		default:
		}
	}
}

// writeLoop 从 send 队列写帧到 WS。
func (c *Client) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-c.send:
			var err error
			if f.text {
				err = c.conn.Write(ctx, websocket.MessageText, []byte(f.msg))
			} else {
				// 二进制输出帧:首字节为类型 'o',其余为终端字节流。
				buf := make([]byte, 1+len(f.data))
				buf[0] = frameTypeOutput
				copy(buf[1:], f.data)
				err = c.conn.Write(ctx, websocket.MessageBinary, buf)
			}
			if err != nil {
				return
			}
		}
	}
}

// sendText 发送 JSON 文本帧。
func (c *Client) sendText(t byte, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	// 文本帧:首字节类型 + JSON
	buf := make([]byte, 1+len(b))
	buf[0] = t
	copy(buf[1:], b)
	c.conn.Write(context.Background(), websocket.MessageText, buf)
}

// pingTicker 定期发 ping 保活。
func (c *Client) pingTicker(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.conn.Ping(ctx); err != nil {
				return
			}
		}
	}
}
