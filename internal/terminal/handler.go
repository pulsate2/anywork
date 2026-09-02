package terminal

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

// 客户端命令帧。
type inMsg struct {
	Type  string `json:"type"`
	SID   string `json:"sid,omitempty"`
	Data  string `json:"data,omitempty"`
	Cols  int    `json:"cols,omitempty"`
	Rows  int    `json:"rows,omitempty"`
	Dir   string `json:"dir,omitempty"`
	Shell string `json:"shell,omitempty"`
}

// 会话状态帧。
type sessMsg struct {
	Type     string `json:"type"` // session | sessionList
	ID       string `json:"id"`
	Dir      string `json:"dir"`
	Cols     int    `json:"cols"`
	Rows     int    `json:"rows"`
	Dead     bool   `json:"dead"`
	ExitCode int    `json:"exitCode"`
	// List 不能用 omitempty:结束最后一个会话后列表为空,整个字段会被省略,
	// 前端收到的 list 就是 undefined,渲染时直接崩。空列表必须序列化成 []。
	List []Summary `json:"list"`
}

// exitMsg 会话退出事件。
type exitMsg struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	ExitCode int    `json:"exitCode"`
}

// ServeWS 处理单个 /api/term 连接。
func (m *Manager) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // 反代层已处理 TLS
	})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "bye")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	c := newClient(conn)
	m.AddClient(c)
	defer m.RemoveClient(c)
	go c.writeLoop(ctx)
	go c.pingTicker(ctx)

	var (
		mu    sync.Mutex
		curID string // 当前附加的会话
	)

	readLoop(ctx, m, c, &curID, &mu)

	// 连接关闭:从当前会话分离。
	mu.Lock()
	id := curID
	mu.Unlock()
	if id != "" {
		m.Detach(id, c)
	}
}

func readLoop(ctx context.Context, m *Manager, c *Client, curID *string, mu *sync.Mutex) {
	for {
		typ, data, err := c.conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			// 客户端→服务端一律文本帧。
			continue
		}
		var in inMsg
		if err := json.Unmarshal(data, &in); err != nil {
			continue
		}

		switch in.Type {
		case "create":
			sum, err := m.Create(in.Dir, in.Shell, in.Cols, in.Rows)
			if err != nil {
				c.sendText(frameTypeText, map[string]any{"type": "error", "message": err.Error()})
				continue
			}
			// 附加到新会话,回放(空)。
			// 先释放当前附加的旧会话,再附加新会话:否则同一连接会同时挂载在
			// 新旧两个会话上,旧会话继续向此连接广播输出,前端看到的就是"新建了
			// 却还在旧会话里"。与 detach 分支同样的清理逻辑。
			mu.Lock()
			if *curID != "" {
				m.Detach(*curID, c)
			}
			mu.Unlock()
			if _, _, err := m.Attach(sum.ID, c); err != nil {
				c.sendText(frameTypeText, map[string]any{"type": "error", "message": err.Error()})
				continue
			}
			mu.Lock()
			*curID = sum.ID
			mu.Unlock()
			c.sendText(frameTypeSession, sessMsg{Type: "session", ID: sum.ID, Dir: sum.Dir, Cols: sum.Cols, Rows: sum.Rows})
			// 广播创建事件给其它客户端,使会话列表实时更新。
			m.broadcastSessionList()

		case "attach":
			s, replay, err := m.Attach(in.SID, c)
			if err != nil {
				c.sendText(frameTypeText, map[string]any{"type": "error", "message": err.Error()})
				continue
			}
			// 回放历史缓冲。
			for len(replay) > 0 {
				n := len(replay)
				if n > 32*1024 {
					n = 32 * 1024
				}
				c.push(outFrame{data: replay[:n]})
				replay = replay[n:]
			}
			mu.Lock()
			*curID = in.SID
			mu.Unlock()
			c.sendText(frameTypeSession, sessMsg{Type: "session", ID: in.SID, Dir: s.dir, Cols: s.cols, Rows: s.rows, Dead: s.isDead(), ExitCode: s.ExitCode()})
			// 若已死则不再等待输入,立即发 exit。
			if s.isDead() {
				c.sendText(frameTypeExit, exitMsg{Type: "exit", ID: in.SID, ExitCode: s.ExitCode()})
			}

		case "list":
			c.sendText(frameTypeSession, sessMsg{Type: "sessionList", List: m.List()})

		case "input":
			if in.SID == "" {
				continue
			}
			if s := m.Get(in.SID); s != nil {
				if err := s.input([]byte(in.Data)); err != nil {
					// 会话已退出:发 exit。
					c.sendText(frameTypeExit, exitMsg{Type: "exit", ID: in.SID, ExitCode: s.ExitCode()})
				}
			}

		case "resize":
			if in.SID == "" {
				continue
			}
			if s := m.Get(in.SID); s != nil {
				s.resize(in.Cols, in.Rows)
			}

		case "kill":
			if in.SID == "" {
				continue
			}
			// 进程退出是异步的:Manager.watchExit 会在真正退出后广播 exit + 会话列表。
			if err := m.Kill(in.SID); err != nil {
				c.sendText(frameTypeText, map[string]any{"type": "error", "message": err.Error()})
			}

		case "detach":
			mu.Lock()
			if *curID != "" {
				m.Detach(*curID, c)
				*curID = ""
			}
			mu.Unlock()
		}
	}
}

// broadcastSessionList 向所有连接广播最新会话列表。
func (m *Manager) broadcastSessionList() {
	list := m.List()
	for _, c := range m.allClients() {
		c.sendText(frameTypeSession, sessMsg{Type: "sessionList", List: list})
	}
}
