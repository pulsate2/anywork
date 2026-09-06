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
	"time"
)

// codexDriver:spawn `codex app-server`,行分隔 JSON-RPC over stdio。
// 协议形状对齐 hapi 的 codexAppServerClient.ts / appServerEventConverter.ts /
// appServerPermissionAdapter.ts(DESIGN-AGENT.md 4.2,无正式文档,以 hapi 实测为准):
//
//	→ {"id":1,"method":"initialize","params":{"clientInfo":{…}}},再通知 initialized
//	→ {"id":2,"method":"thread/start","params":{cwd,approvalPolicy,sandbox}} ← {thread:{id}}
//	→ {"id":3,"method":"turn/start","params":{threadId,input:[{type:"text",text:…}],…}}
//	← 通知 item/started、item/completed(item.type: agentMessage|reasoning|commandExecution|…)
//	← 请求 item/commandExecution/requestApproval 等 → 答复 {"id":…,"result":{"decision":"accept"|"decline"}}
type codexDriver struct {
	mu     sync.Mutex // 保护 stdin 写入与两张 pending map
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	nextID    int
	pending   map[int]chan json.RawMessage // RPC 响应
	approvals map[string]chan int          // reqID(itemId) → 审批结果(0 拒 / 1 允许 / 2 本会话允许)

	threadID   string // codex thread id(续聊凭据)
	permMode   string
	model      string // 当前模型(thread/start 响应回填,运行中可改)
	effort     string // 思考强度,每回合随 turn/start 下发
	cwd        string
	stderrTail string
	// 最近一次 turn/diff/updated 的 unified diff(整回合累计)。变化时发一张
	// CodexDiff 工具卡,前端渲染行级 diff —— ApplyPatch 通知里只有文件名,
	// 真正的改动内容要走这个通知(对齐 hapi 的 DiffProcessor)。
	lastTurnDiff string
	closeOnce    sync.Once
	exitErr      error

	events   chan Event
	done     chan struct{}
	doneOnce sync.Once
}

func newCodexDriver() *codexDriver {
	return &codexDriver{
		pending:   map[int]chan json.RawMessage{},
		approvals: map[string]chan int{},
		events:    make(chan Event, 128),
		done:      make(chan struct{}),
	}
}

// codexBin 解析 codex 可执行文件;LR_CODEX_BIN 可覆盖。
func codexBin() (string, error) {
	if p := os.Getenv("LR_CODEX_BIN"); p != "" {
		return p, nil
	}
	return exec.LookPath("codex")
}

func (d *codexDriver) Start(opts StartOpts) error {
	bin, err := codexBin()
	if err != nil {
		return fmt.Errorf("未找到 codex 可执行文件(可用 LR_CODEX_BIN 指定): %w", err)
	}
	d.permMode = opts.PermissionMode
	if d.permMode == "" {
		d.permMode = PermAsk
	}
	d.model = opts.Model
	d.effort = opts.Effort
	d.cwd = opts.Cwd

	cmd := exec.Command(bin, "app-server")
	cmd.Dir = opts.Cwd
	cmd.Env = os.Environ()
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
		return fmt.Errorf("启动 codex app-server 失败: %w", err)
	}

	d.mu.Lock()
	d.cmd = cmd
	d.stdin = stdin
	d.mu.Unlock()

	go d.readStdout(stdout)
	go d.readStderr(stderr)
	go d.waitExit()

	// 握手同步做:失败即「协议不兼容/没登录」,Create 直接报错,
	// 会话不落库(设计 4.2:不降级为 PTY,明说不支持)。
	if err := d.handshake(opts.ResumeID); err != nil {
		d.Close()
		return err
	}
	return nil
}

// handshake initialize → thread/start|resume。超时 30s。
func (d *codexDriver) handshake(resumeID string) error {
	initTimeout := 30 * time.Second
	if _, err := d.call("initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "lightremote", "version": "1.0"},
		"capabilities": nil,
	}, initTimeout); err != nil {
		return fmt.Errorf("codex 协议握手失败(版本不兼容?): %w", err)
	}
	d.notify("initialized", nil)

	params := map[string]any{
		"cwd":            d.cwd,
		"approvalPolicy": codexApprovalPolicy(d.permMode),
		"sandbox":        codexSandboxMode(d.permMode),
	}
	if d.model != "" {
		params["model"] = d.model
	}
	method := "thread/start"
	if resumeID != "" {
		method = "thread/resume"
		params["threadId"] = resumeID
	}
	var resp struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model string `json:"model"`
	}
	raw, err := d.call(method, params, initTimeout)
	if err != nil {
		return fmt.Errorf("codex %s 失败(未登录或版本不兼容?): %w", method, err)
	}
	if err := json.Unmarshal(raw, &resp); err != nil || resp.Thread.ID == "" {
		return fmt.Errorf("codex %s 返回里没有 thread id(协议不兼容)", method)
	}
	d.mu.Lock()
	d.threadID = resp.Thread.ID
	// 响应里的 model 是 codex 实际采用的(指定不合法时会回落),以它为准展示。
	if resp.Model != "" {
		d.model = resp.Model
	}
	d.mu.Unlock()

	// / 指令提示:codex 没有可查询的指令清单端点,用内置表
	// (hapi BUILTIN_SLASH_COMMANDS.codex 同款)。发给前端做输入提示;
	// app-server 会把不认识的 /xx 当普通文本,无害。
	d.emit(Event{Kind: KindSlashCommands, Payload: codexBuiltinCommands})
	return nil
}

// codexBuiltinCommands codex 的 / 指令提示表(hapi BUILTIN_SLASH_COMMANDS
// 同款,排除 app-server 不支持的终端型指令)。
var codexBuiltinCommands = []string{
	"clear", "compact", "status", "model", "reasoning", "effort",
}

// codexApprovalPolicy / codexSandboxMode / codexSandboxPolicy:
// our 权限模式 → codex 三件套,映射表对齐 hapi permissionModeConfig.ts。
func codexApprovalPolicy(mode string) string {
	switch mode {
	case PermAccept:
		return "never"
	case PermEdits:
		return "on-failure"
	case PermPlan:
		return "untrusted"
	default: // ask
		return "on-request"
	}
}

func codexSandboxMode(mode string) string {
	switch mode {
	case PermAccept:
		return "danger-full-access"
	case PermPlan:
		return "read-only"
	default:
		return "workspace-write"
	}
}

func codexSandboxPolicy(mode string) map[string]any {
	switch mode {
	case PermAccept:
		return map[string]any{"type": "dangerFullAccess"}
	case PermPlan:
		return map[string]any{"type": "readOnly"}
	default:
		return map[string]any{"type": "workspaceWrite"}
	}
}

// Send 发一轮:turn/start 是长请求(响应等回合结束才来),不能阻塞在这里 ——
// 发出去后由 readStdout 异步收响应,出错以 error 事件透出。
func (d *codexDriver) Send(text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stdin == nil || d.threadID == "" {
		return fmt.Errorf("会话未运行")
	}
	d.emit(Event{Kind: KindStatus, Payload: &StatusPayload{State: StatusRunning}})
	params := map[string]any{
		"threadId":       d.threadID,
		"cwd":            d.cwd,
		"input":          []any{map[string]any{"type": "text", "text": text}},
		"approvalPolicy": codexApprovalPolicy(d.permMode),
		"sandboxPolicy":  codexSandboxPolicy(d.permMode),
	}
	// model/effort 每回合都带:运行中改设置即"下一回合生效"。
	if d.model != "" {
		params["model"] = d.model
	}
	if d.effort != "" && d.effort != EffortNone {
		params["effort"] = d.effort
	}
	return d.asyncCall("turn/start", params)
}

// ApplySettings 运行中调整:存进 driver,下一回合 turn/start 生效(原生支持)。
func (d *codexDriver) ApplySettings(u SettingsUpdate) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stdin == nil {
		return fmt.Errorf("会话未运行")
	}
	if u.Model != "" {
		d.model = u.Model
	}
	if u.Effort != "" {
		d.effort = u.Effort
	}
	if u.PermissionMode != "" {
		d.permMode = u.PermissionMode
	}
	return nil
}

func (d *codexDriver) Interrupt() error {
	d.mu.Lock()
	running := d.stdin != nil && d.threadID != ""
	threadID := d.threadID
	d.mu.Unlock()
	if !running {
		return fmt.Errorf("会话未运行")
	}
	_, err := d.call("turn/interrupt", map[string]any{"threadId": threadID}, 30*time.Second)
	return err
}

// Resolve 答复审批:结果送进等待中的审批 handler。session=true 时对 codex
// 答复 acceptForSession(官方语义:本会话同类请求自动放行)。
func (d *codexDriver) Resolve(reqID string, allow, session bool) error {
	d.mu.Lock()
	ch := d.approvals[reqID]
	if ch != nil {
		delete(d.approvals, reqID)
	}
	d.mu.Unlock()
	if ch == nil {
		return fmt.Errorf("权限请求不存在或已答复: %s", reqID)
	}
	ch <- approveCode(allow, session)
	return nil
}

// approveCode 审批答复三态:0 拒绝 / 1 允许 / 2 本会话允许。
func approveCode(allow, session bool) int {
	if !allow {
		return 0
	}
	if session {
		return 2
	}
	return 1
}

func (d *codexDriver) PendingRequests() []PermissionReqPayload {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]PermissionReqPayload, 0, len(d.approvals))
	for id := range d.approvals {
		// args 在审批 handler 里;这里只有 id,补全靠历史消息(与 claude 同)。
		out = append(out, PermissionReqPayload{ReqID: id})
	}
	return out
}

func (d *codexDriver) ExternalID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.threadID
}

func (d *codexDriver) CurrentModel() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.model
}

func (d *codexDriver) Events() <-chan Event  { return d.events }
func (d *codexDriver) Done() <-chan struct{} { return d.done }
func (d *codexDriver) ExitErr() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.exitErr
}

func (d *codexDriver) Close() error {
	d.closeOnce.Do(func() {
		if d.cmd != nil && d.cmd.Process != nil {
			killProcessGroup(d.cmd)
		}
	})
	return nil
}

func (d *codexDriver) emit(ev Event) {
	select {
	case d.events <- ev:
	default:
		// 同 claudeDriver:消费停滞时丢消息优于卡死。
	}
}

func (d *codexDriver) waitExit() {
	err := d.cmd.Wait()
	d.mu.Lock()
	d.exitErr = err
	d.stdin = nil
	// 进程死了:RPC 全部失败,挂起的审批按拒绝收尾。
	for _, ch := range d.approvals {
		select {
		case ch <- 0:
		default:
		}
	}
	d.approvals = map[string]chan int{}
	d.mu.Unlock()
	d.doneOnce.Do(func() { close(d.done) })
	close(d.events)
}

func (d *codexDriver) readStderr(r io.Reader) {
	var tail []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for sc.Scan() {
		tail = append(tail, sc.Text())
		if len(tail) > 20 {
			tail = tail[len(tail)-20:]
		}
	}
	if len(tail) > 0 {
		d.mu.Lock()
		d.stderrTail = strings.Join(tail, "\n")
		d.mu.Unlock()
	}
}

// ---- RPC 底座 ----

// jsonrpcLine stdout 一行的三种身份:响应(有 id 无 method)、
// 服务端请求(有 id 有 method)、通知(无 id)。
type jsonrpcLine struct {
	ID     *int            `json:"id"` // nil = 通知
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
	Params json.RawMessage `json:"params"`
}

// call 同步请求:锁内注册+写出,锁外等响应 —— 等待期间 readStdout 的
// handleRPCResponse 还要拿同一把锁回填通道,持锁等会把自己锁死到超时。
func (d *codexDriver) call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	d.mu.Lock()
	id, ch, err := d.sendLocked(method, params)
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case raw := <-ch:
		// 出错响应由 handleRPCResponse 转成 error 事件后回填 nil;nil 即失败。
		if raw == nil {
			return nil, fmt.Errorf("%s 失败", method)
		}
		return raw, nil
	case <-time.After(timeout):
		d.mu.Lock()
		delete(d.pending, id)
		d.mu.Unlock()
		return nil, fmt.Errorf("%s 超时", method)
	case <-d.done:
		return nil, fmt.Errorf("会话已结束")
	}
}

// sendLocked 注册请求并写 stdin。必须在持有 mu 时调用。
func (d *codexDriver) sendLocked(method string, params any) (int, chan json.RawMessage, error) {
	if d.stdin == nil {
		return 0, nil, fmt.Errorf("会话未运行")
	}
	d.nextID++
	id := d.nextID
	ch := make(chan json.RawMessage, 1)
	d.pending[id] = ch

	b, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		delete(d.pending, id)
		return 0, nil, err
	}
	if _, err := d.stdin.Write(append(b, '\n')); err != nil {
		delete(d.pending, id)
		return 0, nil, err
	}
	return id, ch, nil
}

// asyncCall 发了不等响应的长请求(turn/start 的响应等回合结束才来);
// 响应/错误由 handleRPCResponse 异步处理。必须在持有 mu 时调用。
func (d *codexDriver) asyncCall(method string, params any) error {
	_, _, err := d.sendLocked(method, params)
	return err
}

func (d *codexDriver) notify(method string, params any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stdin == nil {
		return
	}
	b, _ := json.Marshal(map[string]any{"method": method, "params": params})
	d.stdin.Write(append(b, '\n'))
}

// respond 给服务端请求回结果。失败静默(进程已死时无计可施)。
func (d *codexDriver) respond(id *int, result any) {
	if id == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stdin == nil {
		return
	}
	b, _ := json.Marshal(map[string]any{"id": *id, "result": result})
	d.stdin.Write(append(b, '\n'))
}

func (d *codexDriver) readStdout(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg jsonrpcLine
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		switch {
		case msg.Method == "":
			d.handleRPCResponse(&msg)
		case msg.ID != nil:
			go d.handleServerRequest(&msg)
		default:
			d.handleNotification(msg.Method, msg.Params)
		}
	}
}

// handleRPCResponse 把响应回填给等待中的 call;错误响应顺手转成 error 事件
// (asyncCall 的失败没有别的透出渠道)。
func (d *codexDriver) handleRPCResponse(msg *jsonrpcLine) {
	d.mu.Lock()
	ch := d.pending[*msg.ID]
	delete(d.pending, *msg.ID)
	d.mu.Unlock()
	if ch == nil {
		return
	}
	if msg.Error != nil {
		d.emit(Event{Kind: KindError, Payload: &ErrorPayload{Message: truncate(msg.Error.Message, 4000)}})
		ch <- nil
		return
	}
	ch <- msg.Result
}

// ---- 服务端请求(审批桥) ----

// codexApprovalParams 各 requestApproval 的公共形状(hapi appServerPermissionAdapter)。
type codexApprovalParams struct {
	ItemID     string          `json:"itemId"`
	Reason     string          `json:"reason"`
	Command    json.RawMessage `json:"command"` // 字符串或数组(commandExecution)
	Cwd        string          `json:"cwd"`
	GrantRoot  string          `json:"grantRoot"`   // fileChange
	ToolName   string          `json:"toolName"`    // tool
	Input      json.RawMessage `json:"input"`       // tool
	Permission json.RawMessage `json:"permissions"` // permissions
}

func (d *codexDriver) handleServerRequest(msg *jsonrpcLine) {
	switch msg.Method {
	case "item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
		"item/tool/requestApproval",
		"item/permissions/requestApproval":
		d.handleApproval(msg)
	case "item/tool/requestUserInput", "mcpServer/elicitation/request":
		// 结构化用户输入是 Phase 3;先一律取消,agent 会自己绕开。
		if msg.Method == "item/tool/requestUserInput" {
			d.respond(msg.ID, map[string]any{"decision": "cancel"})
		} else {
			d.respond(msg.ID, map[string]any{"action": "cancel", "content": nil, "_meta": nil})
		}
	default:
		d.respond(msg.ID, map[string]any{"error": map[string]any{"code": -32601, "message": "method not found: " + msg.Method}})
	}
}

// handleApproval 落一条 permission_request,挂起等 Resolve,再回 accept/decline。
func (d *codexDriver) handleApproval(msg *jsonrpcLine) {
	var p codexApprovalParams
	json.Unmarshal(msg.Params, &p)

	reqID := p.ItemID
	if reqID == "" {
		reqID = fmt.Sprintf("codex-%d", time.Now().UnixNano())
	}

	// 摘要参数:command > input > permissions > grantRoot,给审批卡看一行关键的。
	brief := ""
	switch {
	case len(p.Command) > 0:
		brief = rawStringOrJoin(p.Command)
	case len(p.Input) > 0:
		brief = string(p.Input)
	case len(p.Permission) > 0:
		brief = string(p.Permission)
	case p.GrantRoot != "":
		brief = p.GrantRoot
	}

	tool := msg.Method
	switch msg.Method {
	case "item/commandExecution/requestApproval":
		tool = "Bash"
	case "item/fileChange/requestApproval":
		tool = "ApplyPatch"
	case "item/tool/requestApproval":
		if p.ToolName != "" {
			tool = p.ToolName
		}
	case "item/permissions/requestApproval":
		tool = "Sandbox"
	}

	ch := make(chan int, 1)
	d.mu.Lock()
	if d.stdin == nil { // 进程已死,直接拒绝
		d.mu.Unlock()
		d.respond(msg.ID, map[string]any{"decision": "decline"})
		return
	}
	d.approvals[reqID] = ch
	d.mu.Unlock()

	d.emit(Event{Kind: KindPermissionReq, Payload: &PermissionReqPayload{
		ReqID: reqID,
		Tool:  tool,
		Args:  truncate(brief, 8*1024),
	}})

	code := <-ch // 0 拒 / 1 允许 / 2 本会话允许

	var result any
	if msg.Method == "item/permissions/requestApproval" {
		// permissions 审批的答复形状不同(hapi mapPermissionGrant)。
		if code != 0 {
			result = map[string]any{"permissions": json.RawMessage(p.Permission), "scope": "turn"}
		} else {
			result = map[string]any{"permissions": map[string]any{"network": nil, "fileSystem": nil}, "scope": "turn"}
		}
	} else if code == 0 {
		result = map[string]any{"decision": "decline"}
	} else if code == 2 {
		// acceptForSession:codex 官方语义,同类请求本会话自动放行。
		result = map[string]any{"decision": "acceptForSession"}
	} else {
		result = map[string]any{"decision": "accept"}
	}
	d.respond(msg.ID, result)
}

// rawStringOrJoin command 字段可能是字符串或字符串数组。
func rawStringOrJoin(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		json.Unmarshal(raw, &s)
		return s
	}
	var parts []string
	if err := json.Unmarshal(raw, &parts); err == nil {
		return strings.Join(parts, " ")
	}
	return string(raw)
}

// ---- 通知 → 归一事件 ----

// codexItem item/started|completed 里的 item。
type codexItem struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Text             string          `json:"text"`
	Message          string          `json:"message"`
	SummaryText      []string        `json:"summary_text"`
	Content          json.RawMessage `json:"content"`
	Command          json.RawMessage `json:"command"`
	Cwd              string          `json:"cwd"`
	AggregatedOutput string          `json:"aggregatedOutput"`
	Output           string          `json:"output"`
	Stderr           string          `json:"stderr"`
	Error            string          `json:"error"`
	ExitCode         *int            `json:"exitCode"`
	Status           string          `json:"status"`
	Server           string          `json:"server"`
	Tool             string          `json:"tool"`
	Arguments        json.RawMessage `json:"arguments"`
	Changes          json.RawMessage `json:"changes"`
	Stdout           string          `json:"stdout"`
	Success          *bool           `json:"success"`
}

// codexItemParams item/started|completed 通知参数。itemId 顶层可能没有
// (实测 item/completed 只给 item.id),两级都找,对齐 hapi extractItemId。
type codexItemParams struct {
	ItemID string     `json:"itemId"`
	Item   *codexItem `json:"item"`
}

func (p codexItemParams) id() string {
	if p.ItemID != "" {
		return p.ItemID
	}
	if p.Item != nil {
		return p.Item.ID
	}
	return ""
}

func (d *codexDriver) handleNotification(method string, params json.RawMessage) {
	// 新版 app-server 把原生事件包在 codex/event/* 里(msg 字段),
	// 老版直接发 item/*、turn/*。两套都认,归一后再分发。
	if strings.HasPrefix(method, "codex/event/") {
		var wrapped struct {
			Msg json.RawMessage `json:"msg"`
		}
		if json.Unmarshal(params, &wrapped) != nil {
			return
		}
		var m struct {
			Type      string     `json:"type"`
			Item      *codexItem `json:"item"`
			ItemID    string     `json:"item_id"`
			TurnID    string     `json:"turn_id"`
			Error     string     `json:"error"`
			WillRetry bool       `json:"will_retry"`
		}
		if json.Unmarshal(wrapped.Msg, &m) != nil {
			return
		}
		// 包装层同样:item_id 可能没有,兜底 item.id。
		itemID := m.ItemID
		if itemID == "" && m.Item != nil {
			itemID = m.Item.ID
		}
		switch m.Type {
		case "item_started", "item_completed":
			verb := "started"
			if m.Type == "item_completed" {
				verb = "completed"
			}
			d.handleItem(verb, itemID, m.Item)
		case "task_started":
			d.emit(Event{Kind: KindStatus, Payload: &StatusPayload{State: StatusRunning}})
		case "task_complete", "turn_aborted":
			d.emit(Event{Kind: KindStatus, Payload: &StatusPayload{State: StatusIdle}})
		case "context_compacted":
			// 上下文压缩完成(新版包装事件):不带 token 细节。
			d.emit(Event{Kind: KindCompaction, Payload: &CompactionPayload{}})
		case "task_failed":
			if !m.WillRetry {
				msg := m.Error
				if msg == "" {
					msg = "codex 任务失败"
				}
				d.emit(Event{Kind: KindError, Payload: &ErrorPayload{Message: truncate(msg, 4000)}})
			}
			d.emit(Event{Kind: KindStatus, Payload: &StatusPayload{State: StatusIdle}})
		case "error":
			if !m.WillRetry && m.Error != "" {
				d.emit(Event{Kind: KindError, Payload: &ErrorPayload{Message: truncate(m.Error, 4000)}})
			}
		}
		return
	}

	switch method {
	case "item/started", "item/completed":
		var p codexItemParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		verb := "started"
		if method == "item/completed" {
			verb = "completed"
		}
		d.handleItem(verb, p.id(), p.Item)

	case "turn/started":
		d.mu.Lock()
		d.lastTurnDiff = "" // 回合开始:diff 计数归零
		d.mu.Unlock()
		d.emit(Event{Kind: KindStatus, Payload: &StatusPayload{State: StatusRunning}})

	case "turn/diff/updated":
		// params.diff / unified_diff / unifiedDiff 都认(hapi 同款)。
		var p struct {
			Diff         string `json:"diff"`
			UnifiedDiff  string `json:"unified_diff"`
			UnifiedDiff2 string `json:"unifiedDiff"`
		}
		json.Unmarshal(params, &p)
		diff := firstNonEmpty(p.Diff, p.UnifiedDiff, p.UnifiedDiff2)
		if diff == "" {
			return
		}
		d.mu.Lock()
		changed := d.lastTurnDiff != diff
		d.lastTurnDiff = diff
		d.mu.Unlock()
		// 内容有变才发卡:同一 diff 反复推送不刷屏。发出即完成(没有结果事件)。
		if changed {
			args, _ := json.Marshal(map[string]string{"unified_diff": truncate(diff, 60*1024)})
			d.emit(Event{Kind: KindToolCall, Payload: &ToolCallPayload{
				Tool:  "CodexDiff",
				Args:  string(args),
				State: "ok",
			}})
		}

	case "turn/completed":
		// 实测:状态在 params.turn.status(顶层 status 不存在),error 在 turn.error。
		var p struct {
			Status string `json:"status"`
			Error  string `json:"error"`
			Turn   struct {
				Status string `json:"status"`
				Error  struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		json.Unmarshal(params, &p)
		status := strings.ToLower(firstNonEmpty(p.Status, p.Turn.Status))
		errMsg := firstNonEmpty(p.Error, p.Turn.Error.Message)
		switch status {
		case "interrupted", "cancelled", "canceled":
			// 打断不算失败:安静回到 idle。
		case "failed", "error":
			msg := errMsg
			if msg == "" {
				msg = "codex 回合失败"
			}
			d.emit(Event{Kind: KindError, Payload: &ErrorPayload{Message: truncate(msg, 4000)}})
		}
		d.emit(Event{Kind: KindStatus, Payload: &StatusPayload{State: StatusIdle}})

	case "thread/compacted":
		// 上下文压缩完成(老版通知):不带 token 细节,与新版包装事件同一归一。
		d.emit(Event{Kind: KindCompaction, Payload: &CompactionPayload{}})

	case "tokenCount", "token_count", "tokenUsage", "token_usage":
		// 累计 token 用量(StatusBar 上下文占用)。params.info.total_token_usage
		// 是整线程累计,codex 的 input 本就含缓存 —— 直接当上下文数用。
		// 字段名 snake/camel 都认(app-server 版本间有差异)。
		var p struct {
			Info struct {
				Total json.RawMessage `json:"total_token_usage"`
			} `json:"info"`
			Total json.RawMessage `json:"total_token_usage"`
		}
		if json.Unmarshal(params, &p) != nil {
			return
		}
		raw := firstNonEmpty(string(p.Info.Total), string(p.Total))
		if raw == "" {
			return
		}
		var t struct {
			Input   int `json:"input_tokens"`
			Input2  int `json:"inputTokens"`
			Cached  int `json:"cached_input_tokens"`
			Cached2 int `json:"cachedInputTokens"`
			Output  int `json:"output_tokens"`
			Output2 int `json:"outputTokens"`
		}
		if json.Unmarshal([]byte(raw), &t) != nil {
			return
		}
		in := firstNonZero(t.Input, t.Input2)
		cached := firstNonZero(t.Cached, t.Cached2)
		out := firstNonZero(t.Output, t.Output2)
		if in+cached+out <= 0 {
			return
		}
		d.emit(Event{Kind: KindUsage, Payload: &UsagePayload{
			Context:   in, // codex 的 input 含缓存,不再加 cached
			Output:    out,
			CacheRead: cached,
		}})

	case "error":
		var p struct {
			Message   string `json:"message"`
			WillRetry bool   `json:"will_retry"`
		}
		json.Unmarshal(params, &p)
		if !p.WillRetry && p.Message != "" {
			d.emit(Event{Kind: KindError, Payload: &ErrorPayload{Message: truncate(p.Message, 4000)}})
		}
	}
	// 其余通知(plan 等)Phase 3 再说。
}

// handleItem item 生命周期 → 事件。工具项 started 给运行中的卡,completed 补结果。
// fileChangeArgs 把 filechange 的 changes 原文转成前端好解析的形状:
// {"changes":[{path,kind,diff}]}。changes 可能是数组(实测形态)或
// {路径: 改动}(hapi extractChanges 兜的另一种),后者原样透传,
// 前端两种都认。解析不动就退回原文字符串,不丢数据。
func (d *codexDriver) fileChangeArgs(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var arr []struct {
		Path string `json:"path"`
		Kind struct {
			Type string `json:"type"`
		} `json:"kind"`
		Diff string `json:"diff"`
	}
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		b, _ := json.Marshal(map[string]any{"changes": arr})
		return string(b)
	}
	// 非数组(对象形态或截断的非法 JSON):包一层让前端走 fallback。
	b, _ := json.Marshal(map[string]any{"changes": json.RawMessage(raw)})
	return string(b)
}

func (d *codexDriver) handleItem(verb, itemID string, item *codexItem) {
	if item == nil || itemID == "" {
		return
	}
	switch normalizeCodexItemType(item.Type) {
	case "agentmessage":
		if verb == "completed" {
			if text := codexItemText(item); text != "" {
				d.emit(Event{Kind: KindAssistantText, Payload: text})
			}
		}

	case "reasoning":
		if verb == "completed" {
			if text := codexReasoningText(item); text != "" {
				d.emit(Event{Kind: KindReasoning, Payload: text})
			}
		}

	case "commandexecution":
		if verb == "started" {
			args := map[string]any{"command": rawStringOrJoin(item.Command)}
			if item.Cwd != "" {
				args["cwd"] = item.Cwd
			}
			b, _ := json.Marshal(args)
			d.emit(Event{Kind: KindToolCall, Payload: &ToolCallPayload{
				Tool:      "Bash",
				ToolUseID: itemID,
				Args:      truncate(string(b), 16*1024),
				State:     "running",
			}})
		} else {
			state := "ok"
			result := firstNonEmpty(item.AggregatedOutput, item.Output, item.Stdout)
			if item.ExitCode != nil && *item.ExitCode != 0 {
				state = "error"
				result = fmt.Sprintf("exit %d\n%s", *item.ExitCode, result)
			}
			if item.Error != "" {
				state = "error"
				result = item.Error + "\n" + result
			}
			if firstNonEmpty(item.Stderr, "") != "" {
				result += "\n[stderr] " + item.Stderr
			}
			d.emit(Event{Kind: KindToolCall, Payload: &ToolCallPayload{
				ToolUseID: itemID,
				Result:    truncate(result, 16*1024),
				State:     state,
			}})
		}

	case "filechange":
		if verb == "started" {
			// changes 结构化转发(实测形态:数组 [{path, kind:{type:add|update|delete},
			// diff}],diff 是改动内容文本)。整段塞字符串前端解析不动,这里按
			// 对象重新包一层,前端按结构出 per-file diff。
			args := d.fileChangeArgs(item.Changes)
			d.emit(Event{Kind: KindToolCall, Payload: &ToolCallPayload{
				Tool:      "ApplyPatch",
				ToolUseID: itemID,
				Args:      truncate(args, 60*1024),
				State:     "running",
			}})
		} else {
			state := "ok"
			if item.Success != nil && !*item.Success {
				state = "error"
			}
			result := firstNonEmpty(item.Stdout, item.Output)
			if item.Stderr != "" {
				result += "\n[stderr] " + item.Stderr
			}
			d.emit(Event{Kind: KindToolCall, Payload: &ToolCallPayload{
				ToolUseID: itemID,
				Result:    truncate(result, 16*1024),
				State:     state,
			}})
		}

	case "mcptoolcall":
		tool := item.Tool
		if tool == "" {
			tool = item.Server
		}
		if tool == "" {
			tool = "MCP"
		}
		if verb == "started" {
			d.emit(Event{Kind: KindToolCall, Payload: &ToolCallPayload{
				Tool:      tool,
				ToolUseID: itemID,
				Args:      truncate(string(item.Arguments), 16*1024),
				State:     "running",
			}})
		} else {
			state := "ok"
			result := string(item.Arguments) // 占位;result 字段不在公共形状里,能拿到什么给什么
			if item.Error != "" {
				state = "error"
				result = item.Error
			}
			d.emit(Event{Kind: KindToolCall, Payload: &ToolCallPayload{
				ToolUseID: itemID,
				Result:    truncate(result, 16*1024),
				State:     state,
			}})
		}
	}
}

func normalizeCodexItemType(t string) string {
	return strings.ToLower(strings.NewReplacer(" ", "", "_", "", "-", "").Replace(t))
}

// codexItemText agentMessage 正文:text/message/content(字符串或块数组)。
func codexItemText(item *codexItem) string {
	if item.Text != "" {
		return item.Text
	}
	if item.Message != "" {
		return item.Message
	}
	return textFromContent(item.Content)
}

// codexReasoningText reasoning 正文:直接文本或 summary_text 段落。
func codexReasoningText(item *codexItem) string {
	if t := codexItemText(item); t != "" {
		return t
	}
	if len(item.SummaryText) > 0 {
		return strings.Join(item.SummaryText, "\n")
	}
	return ""
}

func textFromContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		json.Unmarshal(raw, &s)
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// firstNonZero 第一个非零值(字段名 snake/camel 两套都解,取有值的那个)。
func firstNonZero(vals ...int) int {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}
