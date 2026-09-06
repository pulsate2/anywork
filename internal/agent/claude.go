package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// claudeDriver:spawn `claude` 的 stream-json 模式,stdio 双向 JSON 行流。
// 协议形状与 Claude Code SDK / hapi 的移植实现一致(DESIGN-AGENT.md 4.1):
//
//	stdin  用户消息     {"type":"user","message":{"role":"user","content":"…"}}
//	stdin  打断请求     {"type":"control_request","request_id":"…","request":{"subtype":"interrupt"}}
//	stdout 权限请求     {"type":"control_request","request_id":"…","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{…}}}
//	stdin  权限答复     {"type":"control_response","response":{"subtype":"success","request_id":"…","response":{"behavior":"allow"|"deny",…}}}
type claudeDriver struct {
	mu      sync.Mutex // 保护 stdin 写入与 pending map
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	pending map[string]chan *approveDecision // reqID → 等待审批结果的通道
	// 会话级放行规则(本会话允许):Bash 存完整命令串(和 Bash(cmd) 语义对齐),
	// 其它工具存工具名。同类请求来时直接放行,不再推审批卡。
	sessionRules map[string]bool
	// 审批挂起时记下工具与参数,Resolve 时生成会话规则用。
	lastReqTool map[string]string
	lastReqArgs map[string]string
	// toolUseId → 工具名:tool_result 事件回填 Tool 用(stream-json 的
	// tool_result 块不带工具名,只有 id;没名字前端配对不上就单开卡)。
	toolNames  map[string]string
	external   string // claude session_id(system/init 里返回)
	model      string // 当前模型(system/init 里返回)
	stderrTail string // 退出原因排查用
	closeOnce  sync.Once
	exitErr    error

	events   chan Event
	done     chan struct{}
	doneOnce sync.Once
}

// approveDecision 一次审批的答复。Session=本会话允许。
type approveDecision struct {
	allow   bool
	session bool
}

func newClaudeDriver() *claudeDriver {
	return &claudeDriver{
		pending:      map[string]chan *approveDecision{},
		sessionRules: map[string]bool{},
		lastReqTool:  map[string]string{},
		lastReqArgs:  map[string]string{},
		toolNames:    map[string]string{},
		events:       make(chan Event, 128),
		done:         make(chan struct{}),
	}
}

// claudeBin 解析 claude 可执行文件;LR_CLAUDE_BIN 可覆盖。
func claudeBin() (string, error) {
	if p := os.Getenv("LR_CLAUDE_BIN"); p != "" {
		return p, nil
	}
	return exec.LookPath("claude")
}

func (d *claudeDriver) Start(opts StartOpts) error {
	bin, err := claudeBin()
	if err != nil {
		return fmt.Errorf("未找到 claude 可执行文件(可用 LR_CLAUDE_BIN 指定): %w", err)
	}

	args := []string{"--output-format", "stream-json", "--verbose", "--input-format", "stream-json"}
	// 权限询问走 stdio 控制协议(plan/ask/edits 都可能产生询问);
	// accept 直接 bypass,不会有询问。映射:plan/acceptEdits/bypassPermissions。
	switch opts.PermissionMode {
	case PermAccept:
		args = append(args, "--permission-mode", "bypassPermissions")
	case PermPlan:
		args = append(args, "--permission-mode", "plan", "--permission-prompt-tool", "stdio")
	case PermEdits:
		args = append(args, "--permission-mode", "acceptEdits", "--permission-prompt-tool", "stdio")
	default: // ask
		args = append(args, "--permission-prompt-tool", "stdio")
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.ResumeID != "" {
		args = append(args, "--resume", opts.ResumeID)
	}

	cmd := exec.Command(bin, args...)
	cmd.Dir = opts.Cwd
	cmd.Env = claudeEnv(opts.Env, opts.Effort)
	// 独立进程组:kill 时整棵进程树一起收(claude 起的 bash/node 不留孤儿)。
	setProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 claude 失败: %w", err)
	}

	d.mu.Lock()
	d.cmd = cmd
	d.stdin = stdin
	d.mu.Unlock()

	go d.readStdout(stdout)
	go d.readStderr(stderr)
	go d.waitExit()
	return nil
}

// claudeEnv 会话环境:DISABLE_AUTOUPDATER 防自动更新打断回合;
// CLAUDE_CODE_ENTRYPOINT 归一成 sdk —— 服务进程可能是从某个 claude 会话里启动的,
// 不归一会把宿主会话的标记带进去。
func claudeEnv(extra []string, effort string) []string {
	env := make([]string, 0, len(os.Environ())+4)
	for _, kv := range os.Environ() {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "CLAUDE_CODE_ENTRYPOINT", "MAX_THINKING_TOKENS":
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_ENTRYPOINT=sdk")
	// 思考强度档位 → 思考 token 预算(claude 官方档位语义,none = 不设,交给默认)。
	switch effort {
	case EffortLow:
		env = append(env, "MAX_THINKING_TOKENS=4000")
	case EffortMedium:
		env = append(env, "MAX_THINKING_TOKENS=10000")
	case EffortHigh:
		env = append(env, "MAX_THINKING_TOKENS=31999")
	}
	env = append(env, extra...)
	return env
}

func (d *claudeDriver) Send(text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stdin == nil {
		return fmt.Errorf("会话未运行")
	}
	// 回合开始:状态事件先于正文入队,Manager 落库时把会话置 running。
	d.emit(Event{Kind: KindStatus, Payload: &StatusPayload{State: StatusRunning}})
	msg := map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": text},
	}
	b, _ := json.Marshal(msg)
	_, err := d.stdin.Write(append(b, '\n'))
	return err
}

func (d *claudeDriver) Interrupt() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stdin == nil {
		return fmt.Errorf("会话未运行")
	}
	req := map[string]any{
		"request_id": fmt.Sprintf("intr-%d", os.Getpid()),
		"type":       "control_request",
		"request":    map[string]any{"subtype": "interrupt"},
	}
	b, _ := json.Marshal(req)
	_, err := d.stdin.Write(append(b, '\n'))
	return err
}

// Resolve 答复权限请求:结果送进等待中的 handleCanUseTool。
// session=true 时同时记一条会话规则,同类请求以后直接放行。
func (d *claudeDriver) Resolve(reqID string, allow, session bool) error {
	d.mu.Lock()
	ch := d.pending[reqID]
	if ch != nil {
		delete(d.pending, reqID)
	}
	if session && allow {
		d.sessionRules[reqRuleKey(d.lastReqTool[reqID], d.lastReqArgs[reqID])] = true
	}
	delete(d.lastReqTool, reqID)
	delete(d.lastReqArgs, reqID)
	d.mu.Unlock()
	if ch == nil {
		return fmt.Errorf("权限请求不存在或已答复: %s", reqID)
	}
	ch <- &approveDecision{allow: allow, session: session}
	return nil
}

// reqRuleKey 会话规则的键:Bash 用 Bash(命令) 语义(同命令自动放行),
// 其它工具用工具名(该工具整个会话放行)。
func reqRuleKey(tool string, args string) string {
	if tool == "Bash" && args != "" {
		var parsed struct {
			Command string `json:"command"`
		}
		if json.Unmarshal([]byte(args), &parsed) == nil && parsed.Command != "" {
			return "Bash(" + parsed.Command + ")"
		}
	}
	return tool
}

func (d *claudeDriver) PendingRequests() []PermissionReqPayload {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]PermissionReqPayload, 0, len(d.pending))
	for id := range d.pending {
		// args 在 handleCanUseTool 里,这里只给得出 id;补全靠历史消息。
		out = append(out, PermissionReqPayload{ReqID: id})
	}
	return out
}

func (d *claudeDriver) ExternalID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.external
}

func (d *claudeDriver) CurrentModel() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.model
}

// ApplySettings claude 的模型/思考强度是 spawn 级参数,运行中改不了。
// Manager 会把设置落库,续聊(resume)时生效 —— 这里只负责说实话。
func (d *claudeDriver) ApplySettings(u SettingsUpdate) error {
	return fmt.Errorf("claude 会话运行中不支持调整;设置已保存,结束后续聊时生效")
}

func (d *claudeDriver) Events() <-chan Event  { return d.events }
func (d *claudeDriver) Done() <-chan struct{} { return d.done }
func (d *claudeDriver) ExitErr() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.exitErr
}

func (d *claudeDriver) Close() error {
	d.closeOnce.Do(func() {
		// 进程组一起杀;杀完 waitExit 会关闭 done/events。
		if d.cmd != nil && d.cmd.Process != nil {
			killProcessGroup(d.cmd)
		}
	})
	return nil
}

func (d *claudeDriver) emit(ev Event) {
	select {
	case d.events <- ev:
	default:
		// 订阅端(Manager.pump)消费停滞时丢消息比卡死 driver 好:
		// 消息不落库就丢了,但历史还能靠 claude 原生转录续聊找回。
	}
}

func (d *claudeDriver) waitExit() {
	err := d.cmd.Wait()
	d.mu.Lock()
	d.exitErr = err
	d.stdin = nil
	// 进程死了,挂起的审批永远等不到用户答复,全部按拒绝收尾
	// (claude 已经收不到 control_response,只是清掉 pending map)。
	d.pending = map[string]chan *approveDecision{}
	d.mu.Unlock()
	d.doneOnce.Do(func() { close(d.done) })
	close(d.events)
}

// readStderr 聚合 stderr 尾部,退出异常时作为 error 事件给出可读原因。
func (d *claudeDriver) readStderr(r io.Reader) {
	var tail []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for sc.Scan() {
		line := sc.Text()
		tail = append(tail, line)
		if len(tail) > 20 {
			tail = tail[len(tail)-20:]
		}
		// stderr 里也可能有 JSON 错误输出,当作普通错误文本透出。
		if strings.Contains(line, `"type":"error"`) {
			d.emit(Event{Kind: KindError, Payload: &ErrorPayload{Message: truncate(line, 4000)}})
		}
	}
	if len(tail) > 0 {
		d.mu.Lock()
		d.stderrTail = strings.Join(tail, "\n")
		d.mu.Unlock()
	}
}

// ---- stdout 解析 ----

// claudeLine stream-json 一行的宽松形状。content 块与 control 协议各自再解。
type claudeLine struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype"` // system/result 用
	Session json.RawMessage `json:"session_id"`
	Model   json.RawMessage `json:"model"` // system/init:实际模型
	// system/init 的 slash_commands:本 CLI 支持的 / 指令名(不含斜杠)。
	SlashCommands []string `json:"slash_commands"`
	// system/compact_boundary 与 microcompact_boundary 的元数据。
	CompactMetadata      *claudeCompactMeta `json:"compactMetadata"`
	MicrocompactMetadata *claudeCompactMeta `json:"microcompactMetadata"`
	// system/status:压缩进度("compacting" 先到,compact_result 的结果后到)。
	Status        string     `json:"status"`
	CompactResult string     `json:"compact_result"`
	CompactError  string     `json:"compact_error"`
	Message       *claudeMsg `json:"message"`
	// control_request(权限询问)
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
	// result
	Result   string       `json:"result"`
	IsError  bool         `json:"is_error"`
	Duration float64      `json:"duration_ms"`
	CostUSD  float64      `json:"total_cost_usd"`
	Usage    *claudeUsage `json:"usage"`
}

// claudeCompactMeta compact_boundary 携带的元数据(触发方式与压缩前规模)。
type claudeCompactMeta struct {
	Trigger     string `json:"trigger"`     // auto | manual
	PreTokens   int    `json:"preTokens"`   // 压缩前上下文占用
	TokensSaved int    `json:"tokensSaved"` // 微压缩省下的量(整压不带)
}

// claudeUsage stream-json 的 usage 块(assistant 消息与 result 行都带)。
type claudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// emitUsage 把 usage 块归一成 KindUsage 事件(claude 的 input_tokens 不含
// 缓存,上下文占用 = input + cache_read + cache_creation)。全零不发声。
func (d *claudeDriver) emitUsage(u *claudeUsage, model string) {
	if u == nil {
		return
	}
	ctx := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	if ctx+u.OutputTokens <= 0 {
		return
	}
	d.emit(Event{Kind: KindUsage, Payload: &UsagePayload{
		Context:   ctx,
		Output:    u.OutputTokens,
		CacheRead: u.CacheReadInputTokens,
		Model:     model,
	}})
}

type claudeMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // 字符串或块数组
	// usage / model:assistant 消息附带的 token 计数与实际模型(StatusBar 用)。
	Model string       `json:"model"`
	Usage *claudeUsage `json:"usage"`
}

type contentBlock struct {
	Type      string          `json:"type"` // text | thinking | tool_use | tool_result
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`          // tool_use
	Name      string          `json:"name"`        // tool_use
	Input     json.RawMessage `json:"input"`       // tool_use
	ToolUseID string          `json:"tool_use_id"` // tool_result
	Content   json.RawMessage `json:"content"`     // tool_result(字符串或块数组)
	IsError   bool            `json:"is_error"`
}

type canUseToolRequest struct {
	Subtype  string          `json:"subtype"`
	ToolName string          `json:"tool_name"`
	Input    json.RawMessage `json:"input"`
}

func (d *claudeDriver) readStdout(r io.Reader) {
	sc := bufio.NewScanner(r)
	// 单行上限 8MB:超大的 tool_result(读大文件)会超过默认 64KB 行缓冲。
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg claudeLine
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue // stream-json 之外的杂音,忽略
		}
		switch msg.Type {
		case "system":
			// init:记录 session_id(续聊凭据)与实际模型(头部展示用)。
			if msg.Subtype == "init" {
				var sid string
				json.Unmarshal(msg.Session, &sid)
				if sid != "" {
					d.mu.Lock()
					d.external = sid
					d.mu.Unlock()
				}
				var m string
				json.Unmarshal(msg.Model, &m)
				if m != "" {
					d.mu.Lock()
					d.model = m
					d.mu.Unlock()
				}
				// slash_commands:本 CLI 支持的 / 指令(含自定义 skill 命令),
				// 透传给前端做输入提示。移动端没有本地 claude,列表必须服务端给。
				if len(msg.SlashCommands) > 0 {
					d.emit(Event{Kind: KindSlashCommands, Payload: msg.SlashCommands})
				}
			} else if msg.Subtype == "compact_boundary" || msg.Subtype == "microcompact_boundary" {
				// 上下文压缩边界:整压(清历史重述)或微压缩(只裁缓存)。
				// 时间线上插一条系统提示,之后的 usage 会明显回落。
				meta := msg.CompactMetadata
				if msg.Subtype == "microcompact_boundary" {
					meta = msg.MicrocompactMetadata
				}
				p := &CompactionPayload{Micro: msg.Subtype == "microcompact_boundary"}
				if meta != nil {
					p.Trigger = meta.Trigger
					p.PreTokens = meta.PreTokens
					p.TokensSaved = meta.TokensSaved
				}
				d.emit(Event{Kind: KindCompaction, Payload: p})
			} else if msg.Subtype == "status" {
				// 压缩进度(TUI 底部的 Compacting… 就是它):status=compacting
				// 先到(瞬态,只驱动 StatusBar),compact_result=failed 的第二条
				// 报失败;成功不报 —— 边界事件自己会说话。
				switch {
				case msg.Status == "compacting":
					d.emit(Event{Kind: KindCompaction, Payload: &CompactionPayload{Phase: "start"}})
				case msg.CompactResult == "failed":
					d.emit(Event{Kind: KindCompaction, Payload: &CompactionPayload{
						Failed: true,
						Error:  truncate(msg.CompactError, 500),
					}})
				}
			}
		case "control_request":
			// 权限询问:单独 goroutine 处理,不阻塞读循环
			// (claude 在等答复的同时还会继续输出 thinking/progress)。
			go d.handleCanUseTool(msg.RequestID, msg.Request)
		case "assistant":
			d.handleAssistant(msg.Message)
		case "user":
			d.handleUserEcho(msg.Message)
		case "result":
			if msg.IsError {
				d.emit(Event{Kind: KindError, Payload: &ErrorPayload{Message: truncate(msg.Result, 4000)}})
			}
			// result 行的 usage 是回合最后一次 API 调用的计数,是最准的期末值。
			var m string
			json.Unmarshal(msg.Model, &m)
			d.emitUsage(msg.Usage, m)
			d.emit(Event{Kind: KindStatus, Payload: &StatusPayload{State: StatusIdle}})
		case "error":
			d.emit(Event{Kind: KindError, Payload: &ErrorPayload{Message: truncate(line, 4000)}})
		}
	}
}

// handleAssistant assistant 消息:text/thinking 透出,tool_use 生成运行中的工具卡片。
// 消息附带的 usage 也在这里透出(StatusBar 上下文占用,前端取最后一条)。
func (d *claudeDriver) handleAssistant(m *claudeMsg) {
	if m == nil {
		return
	}
	d.emitUsage(m.Usage, m.Model)
	var blocks []contentBlock
	if rawIsString(m.Content) {
		// content 是纯字符串的退化形式:整条当正文。
		var text string
		json.Unmarshal(m.Content, &text)
		if text != "" {
			d.emit(Event{Kind: KindAssistantText, Payload: text})
		}
		return
	}
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				d.emit(Event{Kind: KindAssistantText, Payload: b.Text})
			}
		case "thinking":
			if b.Thinking != "" {
				d.emit(Event{Kind: KindReasoning, Payload: b.Thinking})
			}
		case "tool_use":
			d.toolNames[b.ID] = b.Name
			d.emit(Event{Kind: KindToolCall, Payload: &ToolCallPayload{
				Tool: b.Name,
				Args: truncate(string(b.Input), 16*1024),
				// state=running:结果在下一条 user(tool_result)里,前端按 toolUseID 配对成同一张卡。
				ToolUseID: b.ID,
				State:     "running",
			}})
		}
	}
}

// handleUserEcho user 消息两种来源:我们自己 Send 的回显(REST 路径已落库,跳过)
// 与 tool_result(补全工具卡片结果)。只认后者。
func (d *claudeDriver) handleUserEcho(m *claudeMsg) {
	if m == nil || rawIsString(m.Content) {
		return
	}
	var blocks []contentBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		result := toolResultText(b.Content)
		if b.IsError && result != "" {
			result = "错误: " + result
		}
		// 回填工具名:tool_result 块只有 id,名字从 tool_use 时记下的表里取。
		// 取完即删(结果只来一次),表不随会话膨胀。
		tool := d.toolNames[b.ToolUseID]
		delete(d.toolNames, b.ToolUseID)
		d.emit(Event{Kind: KindToolCall, Payload: &ToolCallPayload{
			Tool:      tool,
			ToolUseID: b.ToolUseID,
			Result:    truncate(result, 16*1024),
			State:     map[bool]string{true: "error", false: "ok"}[b.IsError],
		}})
	}
}

// handleCanUseTool 一个权限询问:落一条 permission_request 事件,挂起等 Resolve。
// 会话规则(本会话允许)命中时直接放行,不打扰用户。
func (d *claudeDriver) handleCanUseTool(reqID string, rawReq json.RawMessage) {
	var req canUseToolRequest
	if err := json.Unmarshal(rawReq, &req); err != nil || req.Subtype != "can_use_tool" {
		return
	}
	if reqID == "" {
		return
	}
	args := truncate(string(req.Input), 8*1024)

	d.mu.Lock()
	if d.sessionRules[reqRuleKey(req.ToolName, args)] {
		d.mu.Unlock()
		d.writeControlResponse(reqID, true)
		return
	}
	ch := make(chan *approveDecision, 1)
	d.pending[reqID] = ch
	d.lastReqTool[reqID] = req.ToolName
	d.lastReqArgs[reqID] = args
	d.mu.Unlock()

	d.emit(Event{Kind: KindPermissionReq, Payload: &PermissionReqPayload{
		ReqID: reqID,
		Tool:  req.ToolName,
		Args:  args,
	}})

	dec := <-ch // Resolve 或进程退出都不会让它永远挂着(退出时 pending 被清,Resolve 返回错)

	d.writeControlResponse(reqID, dec.allow)
}

// writeControlResponse 把审批决定按控制协议写回 stdin。
func (d *claudeDriver) writeControlResponse(reqID string, allow bool) {
	resp := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": reqID,
		},
	}
	if allow {
		resp["response"].(map[string]any)["response"] = map[string]any{
			"behavior":     "allow",
			"updatedInput": map[string]any{},
		}
	} else {
		resp["response"].(map[string]any)["response"] = map[string]any{
			"behavior": "deny",
			"message":  "用户拒绝了此操作",
		}
	}
	b, _ := json.Marshal(resp)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stdin != nil {
		d.stdin.Write(append(b, '\n'))
	}
}

func rawIsString(raw json.RawMessage) bool {
	return len(raw) > 0 && raw[0] == '"'
}

// toolResultText tool_result 的 content:字符串或 [{type:"text",text:…}] 块数组。
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		json.Unmarshal(raw, &s)
		return s
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return string(raw)
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}
