package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---- 可恢复会话列表 ----
// 创建新会话时不只看 anywork 自己的记录:CLI 在终端里跑过的原生会话
// (claude 的 ~/.claude/projects/<slug>/*.jsonl、codex 的 ~/.codex/sessions/
// **/rollout-*.jsonl、pi 的 ~/.pi/agent/sessions/--<cwd>--/*.jsonl)也能按目录
// 列出来挑一个续聊 —— 重装/换工具后想接着之前终端里的进度,不用从头再来。

// resumableLimit 列表上限:按修改时间倒序取最新的这么多,弹窗里翻不完
// 的旧会话没有意义。
const resumableLimit = 30

// ResumableSession 一条可恢复的 CLI 原生会话。ID 是 external id
// (claude session id / codex thread id),创建时经 resumeExternal 传入。
type ResumableSession struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	App       string `json:"app"`
	UpdatedAt string `json:"updatedAt"`
}

// Resumable 列出某目录下指定 agent 的原生会话(按修改时间倒序)。
// workspace 与创建会话同一套校验(root 边界内)。
func (m *Manager) Resumable(app, workspace string) ([]ResumableSession, error) {
	cwd, err := m.resolve(workspace)
	if err != nil {
		return nil, err
	}
	switch app {
	case AppClaude:
		return listClaudeSessions(cwd)
	case AppCodex:
		return listCodexSessions(cwd)
	case AppPi:
		return listPiSessions(cwd)
	default:
		return nil, fmt.Errorf("未知 agent: %s", app)
	}
}

// claudeDir ~/.claude(驱动同款规则:不认 CLAUDE_CONFIG_DIR,保持一致)。
func claudeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// codexDir ~/.codex。
func codexDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// listClaudeSessions claude 的会话转录:~/.claude/projects/<slug>/<uuid>.jsonl,
// 文件名即 session id,slug 规则与 tailTranscript 一致(projectSlug)。
func listClaudeSessions(cwd string) ([]ResumableSession, error) {
	dir := filepath.Join(claudeDir(), "projects", projectSlug(cwd))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []ResumableSession{}, nil
		}
		return nil, err
	}
	out := []ResumableSession{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		st, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, ResumableSession{
			ID:        strings.TrimSuffix(e.Name(), ".jsonl"),
			Title:     claudeTranscriptTitle(path),
			App:       AppClaude,
			UpdatedAt: st.ModTime().UTC().Format(time.RFC3339),
		})
	}
	return sortResumable(out), nil
}

// claudeTranscriptTitle 从转录前几行提炼标题:优先 summary 行(被压缩/
// 续聊过的会话开头有),否则第一条真实用户消息(content 是字符串或
// text 块;sidechain/meta/tool_result 都不算)。
func claudeTranscriptTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for i := 0; i < 80 && sc.Scan(); i++ {
		var line struct {
			Type        string          `json:"type"`
			Summary     string          `json:"summary"`
			IsSidechain bool            `json:"isSidechain"`
			IsMeta      bool            `json:"isMeta"`
			Message     json.RawMessage `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if line.Type == "summary" && line.Summary != "" {
			return titleFromText(line.Summary)
		}
		if line.Type != "user" || line.IsSidechain || line.IsMeta {
			continue
		}
		var msg struct {
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(line.Message, &msg) != nil {
			continue
		}
		// content 两种形态:纯字符串,或块数组(取第一个 text 块,
		// tool_result 块不是用户说的话)。
		var s string
		if json.Unmarshal(msg.Content, &s) == nil && s != "" {
			return titleFromText(s)
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(msg.Content, &blocks) == nil {
			for _, b := range blocks {
				if b.Type == "text" && b.Text != "" {
					return titleFromText(b.Text)
				}
			}
		}
	}
	return ""
}

// listCodexSessions codex 的会话转录:~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl。
// 第一行 session_meta 带 cwd 与 thread id —— 只能逐个文件读首行来按目录过滤。
func listCodexSessions(cwd string) ([]ResumableSession, error) {
	root := filepath.Join(codexDir(), "sessions")
	out := []ResumableSession{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil // 读不动的条目跳过,不让一个坏文件毁掉整个列表
		}
		meta, ok := readCodexMeta(path)
		if !ok || meta.cwd != cwd {
			return nil
		}
		st, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, ResumableSession{
			ID:        meta.id,
			Title:     codexRolloutTitle(path),
			App:       AppCodex,
			UpdatedAt: st.ModTime().UTC().Format(time.RFC3339),
		})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return sortResumable(out), nil
}

// readCodexMeta 读 rollout 首行的 session_meta(cwd + thread id)。
func readCodexMeta(path string) (meta struct {
	id  string
	cwd string
}, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return meta, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return meta, false
	}
	var line struct {
		Type    string `json:"type"`
		Payload struct {
			ID  string `json:"session_id"`
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	if json.Unmarshal(sc.Bytes(), &line) != nil || line.Type != "session_meta" {
		return meta, false
	}
	if line.Payload.ID == "" || line.Payload.Cwd == "" {
		return meta, false
	}
	meta.id = line.Payload.ID
	meta.cwd = line.Payload.Cwd
	return meta, true
}

// codexRolloutTitle 第一条 user_message 的文本。
func codexRolloutTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for i := 0; i < 80 && sc.Scan(); i++ {
		var line struct {
			Type    string `json:"type"`
			Payload struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if line.Type == "event_msg" && line.Payload.Type == "user_message" && line.Payload.Message != "" {
			return titleFromText(line.Payload.Message)
		}
	}
	return ""
}

// listPiSessions pi 的会话转录:~/.pi/agent/sessions/--<cwd 编码>--/*.jsonl。
// 目录名把 cwd 编进去(去掉开头一个斜杠,再把 /、\、: 都换成 -,首尾包 --),
// 与 pi 的 getDefaultSessionDirPath 一致。PI_CODING_AGENT_DIR 覆盖 agent 根;
// PI_CODING_AGENT_SESSION_DIR 则是平铺目录(多 cwd 共用,不再按 cwd 分子目录)。
// 两种布局都逐文件读首行 header 的 cwd 来过滤,平铺目录下这步才真正起作用。
// (settings.json 里手配的 sessionDir 不认 —— anywork 不写 pi 的 settings。)
func listPiSessions(cwd string) ([]ResumableSession, error) {
	cwd = filepath.Clean(cwd)
	dir := piSessionsDir(cwd)
	if dir == "" {
		return []ResumableSession{}, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []ResumableSession{}, nil
		}
		return nil, err
	}
	out := []ResumableSession{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		id, scwd, title, ok := readPiSession(path)
		if !ok || filepath.Clean(scwd) != cwd {
			continue
		}
		st, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, ResumableSession{
			ID:        id,
			Title:     title,
			App:       AppPi,
			UpdatedAt: st.ModTime().UTC().Format(time.RFC3339),
		})
	}
	return sortResumable(out), nil
}

// piAgentDir ~/.pi/agent(PI_CODING_AGENT_DIR 覆盖,与 pi getAgentDir 同规则)。
func piAgentDir() string {
	if d := os.Getenv("PI_CODING_AGENT_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".pi", "agent")
}

// piSessionsDir 某 cwd 的会话目录:PI_CODING_AGENT_SESSION_DIR 优先(平铺),
// 否则 <agentDir>/sessions/--<cwd 编码>--。
func piSessionsDir(cwd string) string {
	if env := os.Getenv("PI_CODING_AGENT_SESSION_DIR"); env != "" {
		return env
	}
	base := piAgentDir()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "sessions", piEncodeCwd(cwd))
}

// piEncodeCwd 把 cwd 编码成 pi 的目录名片段:去掉开头一个 / 或 \,再把每个
// /、\、: 换成 -,首尾各包 --(对齐 pi getDefaultSessionDirPath)。
func piEncodeCwd(cwd string) string {
	s := cwd
	if len(s) > 0 && (s[0] == '/' || s[0] == '\\') {
		s = s[1:]
	}
	s = strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(s)
	return "--" + s + "--"
}

// readPiSession 读 pi 会话文件:首行是 session header(id + cwd),随后扫一截
// 取标题 —— 优先 session_info 的 name(最新值为准,可被清空),否则第一条
// user 消息文本(content 是字符串或 text 块数组)。
func readPiSession(path string) (id, cwd, title string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !sc.Scan() {
		return "", "", "", false
	}
	var header struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Cwd  string `json:"cwd"`
	}
	if json.Unmarshal(sc.Bytes(), &header) != nil || header.Type != "session" || header.ID == "" {
		return "", "", "", false
	}
	name, firstMessage := "", ""
	for i := 0; i < 400 && sc.Scan(); i++ {
		var line struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		switch line.Type {
		case "session_info":
			name = strings.TrimSpace(line.Name) // 最新值为准(后一条清空就当没名字)
		case "message":
			if firstMessage == "" && line.Message.Role == "user" {
				firstMessage = piMessageText(line.Message.Content)
			}
		}
	}
	t := name
	if t == "" {
		t = firstMessage
	}
	return header.ID, header.Cwd, titleFromText(t), true
}

// piMessageText pi 消息文本化:content 是字符串就直取,否则取各 text 块拼起来。
func piMessageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		parts := make([]string, 0, len(blocks))
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// sortResumable 按修改时间倒序并截断到上限(切片按值传,截断要返回新头)。
func sortResumable(list []ResumableSession) []ResumableSession {
	sort.Slice(list, func(i, j int) bool { return list[i].UpdatedAt > list[j].UpdatedAt })
	if len(list) > resumableLimit {
		list = list[:resumableLimit]
	}
	return list
}
