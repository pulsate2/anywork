package agent

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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
	toolNames map[string]string
	external  string // claude session_id(system/init 里返回)
	model     string // 当前模型(system/init 里返回)
	// filesDir 会话附件目录:tool_result 的 image 块落这里(空 = 不落盘)。
	filesDir string
	// cwd 工作目录:转录 tailer 要按它拼 ~/.claude/projects/<slug>/ 路径。
	cwd string
	// notified 最近转发过的通知内容键(transcript tailer 与 stdout user-echo
	// 两条路径可能先后看到同一条,防双发);notifyOrder 是淘汰顺序。
	notified    map[string]struct{}
	notifyOrder []string
	// asks 挂起的提问(reqID → 原始 input + 归一后的问题):AskUserQuestion
	// 走 can_use_tool 协议,回答要回 allow + updatedInput.answers。
	asks       map[string]*askPending
	stderrTail string // 退出原因排查用
	closeOnce  sync.Once
	exitErr    error
	// sawResult 收到过 result 行(resume 失败也发 result + errors 数组,
	// 错误已从那条路透出)。ExitDetail 据此去重:崩溃退出(没 result 行)
	// 才补 stderr 尾部,免得 resume 失败报两张重复的错误卡。
	sawResult atomic.Bool

	events   chan Event
	done     chan struct{}
	doneOnce sync.Once
}

// approveDecision 一次审批的答复。Session=本会话允许;
// answers 是 ask_user 的回答(问题 id → 选项 label),仅 allow 时有意义。
type approveDecision struct {
	allow   bool
	session bool
	answers map[string][]string
}

// askPending 一个挂起的提问:原始 input(回 updatedInput 要原样带上
// questions 数组)+ 归一后的问题列表。
type askPending struct {
	raw       json.RawMessage
	questions []AskQuestion
}

func newClaudeDriver() *claudeDriver {
	return &claudeDriver{
		pending:      map[string]chan *approveDecision{},
		sessionRules: map[string]bool{},
		lastReqTool:  map[string]string{},
		lastReqArgs:  map[string]string{},
		toolNames:    map[string]string{},
		asks:         map[string]*askPending{},
		notified:     map[string]struct{}{},
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
	d.filesDir = opts.FilesDir
	d.cwd = opts.Cwd
	bin, err := claudeBin()
	if err != nil {
		return fmt.Errorf("未找到 claude 可执行文件(可用 LR_CLAUDE_BIN 指定): %w", err)
	}

	args := []string{"--output-format", "stream-json", "--verbose", "--input-format", "stream-json",
		// 流式增量:partial message 事件(text_delta/thinking_delta)随生成
		// 逐段到达,完整消息照旧在后面;不接的话长思考期间远端一直空白。
		"--include-partial-messages"}
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

// ExitDetail 进程异常退出时的可读原因(给 pump 透到时间线):正常退出、
// 被杀(stderr 无输出)或已随 result 行报过错(resume 失败)时为空。
func (d *claudeDriver) ExitDetail() string {
	d.mu.Lock()
	tail, err := d.stderrTail, d.exitErr
	d.mu.Unlock()
	if err == nil || tail == "" || d.sawResult.Load() {
		return ""
	}
	return truncate(err.Error()+": "+tail, 4000)
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
	// 进程死了,挂起的审批/提问永远等不到用户答复,全部按拒绝收尾
	// (claude 已经收不到 control_response,只是清掉 pending/asks map;
	// handleAskUser 见 asks 里没有该项就不再回写)。
	d.pending = map[string]chan *approveDecision{}
	d.asks = map[string]*askPending{}
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
	Status        string `json:"status"`
	CompactResult string `json:"compact_result"`
	CompactError  string `json:"compact_error"`
	// system/task_notification:后台任务完成/Monitor 事件的摘要与状态词。
	Summary string `json:"summary"`
	// system/task_notification:失败的后台命令的输出文件(尾部内容进 Event)。
	OutputFile string `json:"output_file"`
	// system/api_error / turn_duration / away_summary 的载荷。
	Content       json.RawMessage     `json:"content"` // away_summary 的 recap
	RetryAttempt  int                 `json:"retryAttempt"`
	MaxRetries    int                 `json:"maxRetries"`
	APIError      json.RawMessage     `json:"error"`      // 字符串或 {message}
	DurationMs    float64             `json:"durationMs"` // turn_duration 用驼峰
	ResultSummary *claudeRoundSummary `json:"resultSummary"`
	Message       *claudeMsg          `json:"message"`
	// stream_event(--include-partial-messages):event 包着 Anthropic 流式事件;
	// parent_tool_use_id 非空 = 子 agent(Task 工具)自己的流,不透出。
	Event           json.RawMessage `json:"event"`
	ParentToolUseID string          `json:"parent_tool_use_id"`
	// control_request(权限询问)
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
	// result
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
	// resume 失败等执行错误的明细:error_during_execution 的 result 文本
	// 为空,原因全在 errors 数组里 —— 不接它,"No conversation found"
	// 这类失败就被吞掉,远端只看到会话无声死掉。
	Errors   []string     `json:"errors"`
	Duration float64      `json:"duration_ms"`
	CostUSD  float64      `json:"total_cost_usd"`
	Usage    *claudeUsage `json:"usage"`
	// result 行的 modelUsage(键是模型名):带真实上下文窗口,比前端按
	// 模型名猜 200k/1M 准。
	ModelUsage map[string]*claudeModelUsage `json:"modelUsage"`
}

// claudeModelUsage result.modelUsage 的单模型条目(只取我们用的字段)。
type claudeModelUsage struct {
	ContextWindow int `json:"contextWindow"`
}

// claudeCompactMeta compact_boundary 携带的元数据(触发方式与压缩前规模)。
type claudeCompactMeta struct {
	Trigger     string `json:"trigger"`     // auto | manual
	PreTokens   int    `json:"preTokens"`   // 压缩前上下文占用
	TokensSaved int    `json:"tokensSaved"` // 微压缩省下的量(整压不带)
}

// claudeRoundSummary turn_duration 的 resultSummary:回合统计。
type claudeRoundSummary struct {
	TotalCostUSD float64 `json:"total_cost_usd"`
	NumTurns     int     `json:"num_turns"`
	DurationMs   float64 `json:"duration_ms"`
}

// claudeUsage stream-json 的 usage 块(assistant 消息与 result 行都带)。
type claudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// emitUsage 把 usage 块归一成 KindUsage 事件。上下文占用按 claude CLI 自己的
// 口径 = input + cache_read(不计 cache_creation:缓存写入是一次性成本,token
// 并不驻留上下文,计进去会把占用顶过 100%,比如 247k/200k)。全零不发声。
func (d *claudeDriver) emitUsage(u *claudeUsage, model string, window int) {
	if u == nil {
		return
	}
	ctx := u.InputTokens + u.CacheReadInputTokens
	if ctx+u.OutputTokens <= 0 {
		return
	}
	d.emit(Event{Kind: KindUsage, Payload: &UsagePayload{
		Context:   ctx,
		Output:    u.OutputTokens,
		CacheRead: u.CacheReadInputTokens,
		Model:     model,
		Window:    window,
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
	Type      string          `json:"type"` // text | thinking | tool_use | tool_result | image
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`          // tool_use
	Name      string          `json:"name"`        // tool_use
	Input     json.RawMessage `json:"input"`       // tool_use
	ToolUseID string          `json:"tool_use_id"` // tool_result
	Content   json.RawMessage `json:"content"`     // tool_result(字符串或块数组)
	IsError   bool            `json:"is_error"`
	// image 块(用户贴图 / 工具产出图):source.data 是 base64。
	Source *imageSource `json:"source"`
}

// imageSource Anthropic 格式的图片源(base64 编码 + media_type)。
type imageSource struct {
	Type      string `json:"type"`       // base64
	MediaType string `json:"media_type"` // image/png 等
	Data      string `json:"data"`
}

// imgExt media_type → 扩展名(扩展名同时当 REST 端点的 Content-Type 依据)。
func imgExt(mediaType string) string {
	switch strings.ToLower(mediaType) {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	default:
		return "png"
	}
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
					first := d.external == ""
					d.external = sid
					d.mu.Unlock()
					// 转录 tailer 只起一次:补捞 stdout 上看不见的后台通知
					// (回合中入队被 absorbed_mid_turn 吸收的 Monitor 事件等)。
					if first {
						go d.tailTranscript(sid)
					}
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
			} else if msg.Subtype == "task_notification" {
				// 后台任务(Bash run_in_background / Monitor)完成或产出事件:
				// claude 在空闲间隙注入,不透出的话远程端会以为任务还在跑。
				// 与 XML 形态共用 seenNotify 去重(event 为空时键一致)。
				event := ""
				if msg.Status == "failed" && msg.OutputFile != "" {
					event = readNotifyOutput(msg.OutputFile, msg.Status)
				}
				if msg.Summary != "" && !d.seenNotify(msg.Summary+"|"+msg.Status+"|"+event) {
					d.emit(Event{Kind: KindSystemInfo, Payload: &SystemInfoPayload{
						Type:   "task_notification",
						Text:   truncate(msg.Summary, 2000),
						Status: msg.Status,
						Event:  truncate(event, 8000),
					}})
				}
			} else if msg.Subtype == "api_error" {
				// API 错误(过载/限流重试):不接的话用户只看到一直转圈,
				// 分不清是慢还是挂了。error 字符串或 {message} 都认。
				errMsg := ""
				if rawIsString(msg.APIError) {
					json.Unmarshal(msg.APIError, &errMsg)
				} else {
					var e struct {
						Message string `json:"message"`
					}
					json.Unmarshal(msg.APIError, &e)
					errMsg = e.Message
				}
				d.emit(Event{Kind: KindSystemInfo, Payload: &SystemInfoPayload{
					Type:     "api_error",
					Error:    truncate(errMsg, 300),
					Retry:    msg.RetryAttempt,
					MaxRetry: msg.MaxRetries,
				}})
			} else if msg.Subtype == "turn_duration" {
				// 回合结束统计(耗时/轮数/花费,有啥带啥)。
				p := &SystemInfoPayload{Type: "turn_duration", DurationMs: msg.DurationMs}
				if msg.ResultSummary != nil {
					p.Turns = msg.ResultSummary.NumTurns
					p.CostUSD = msg.ResultSummary.TotalCostUSD
					if p.DurationMs == 0 {
						p.DurationMs = msg.ResultSummary.DurationMs
					}
				}
				d.emit(Event{Kind: KindSystemInfo, Payload: p})
			} else if msg.Subtype == "away_summary" {
				// 离开期间的 recap:content 是字符串。
				var recap string
				if rawIsString(msg.Content) {
					json.Unmarshal(msg.Content, &recap)
				}
				if recap != "" {
					d.emit(Event{Kind: KindSystemInfo, Payload: &SystemInfoPayload{
						Type: "away_summary",
						Text: truncate(recap, 2000),
					}})
				}
			}
		case "control_request":
			// 权限询问:单独 goroutine 处理,不阻塞读循环
			// (claude 在等答复的同时还会继续输出 thinking/progress)。
			go d.handleCanUseTool(msg.RequestID, msg.Request)
		case "assistant", "user":
			// parent_tool_use_id 非空 = 子 agent(Task 工具)自己的流:
			// 文本/思考不进主时间线(hapi isSidechain 同款语义),但工具调用
			// 带 parentToolUseId 透出 —— 前端挂到父 Task 卡下当"过程"展示,
			// 否则子 agent 跑半天远程端只看到一张静止的 Agent 卡。usage
			// 仍然挡在 StatusBar 外(子 agent 上下文小得多,放进来会让占用
			// 读数中途塌陷再弹回)。
			if msg.ParentToolUseID != "" {
				d.handleSubagentTools(msg.Message, msg.ParentToolUseID)
				continue
			}
			if msg.Type == "assistant" {
				d.handleAssistant(msg.Message)
			} else {
				d.handleUserEcho(msg.Message)
			}
		case "stream_event":
			d.handleStreamEvent(msg)
		case "result":
			d.sawResult.Store(true)
			// 打断的回合也是 is_error,但没有任何文本:用户自己按的中断,
			// 不报错(报了也是空 message 的错误卡)。真失败带 result 文本;
			// resume 失败(error_during_execution)的文本在 errors 数组里。
			if msg.IsError {
				reason := strings.TrimSpace(msg.Result)
				if reason == "" && len(msg.Errors) > 0 {
					reason = strings.Join(msg.Errors, "; ")
				}
				if reason != "" {
					d.emit(Event{Kind: KindError, Payload: &ErrorPayload{Message: truncate(reason, 4000)}})
				}
			}
			// result 行的 usage 是回合最后一次 API 调用的计数,是最准的期末值。
			var m string
			json.Unmarshal(msg.Model, &m)
			// modelUsage 里挑一个非零 contextWindow 透传(回合里换过模型时
			// 取最后一个;空 map/全零 = 没给,前端回落启发式)。
			window := 0
			for _, mu := range msg.ModelUsage {
				if mu != nil && mu.ContextWindow > 0 {
					window = mu.ContextWindow
				}
			}
			d.emitUsage(msg.Usage, m, window)
			// 回合统计:实测 CLI 不发 subtype=turn_duration 的 system 行,耗时
			// 在 result 行上(duration_ms)—— 前端 StatusBar 完成后显示本次时长、
			// ⏱️ 时间线行都靠这条。total_cost_usd 不透出:对包月/充值用户是
			// 无意义的小数,显示出来只会让人困惑。
			d.emit(Event{Kind: KindSystemInfo, Payload: &SystemInfoPayload{
				Type:       "turn_duration",
				DurationMs: msg.Duration,
			}})
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
	d.emitUsage(m.Usage, m.Model, 0)
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
				// 参数用 truncateJSON:Edit/Write 的 diff 靠前端 parse 这段 JSON,
				// 盲切产生的非法 JSON 会让 diff 整个不渲染。60KB 给 store 的
				// 64KB payloadLimit 留出事件包装的余量。
				Args: truncateJSON(string(b.Input), 60*1024),
				// state=running:结果在下一条 user(tool_result)里,前端按 toolUseID 配对成同一张卡。
				ToolUseID: b.ID,
				State:     "running",
			}})
		}
	}
}

// handleUserEcho user 消息两种来源:我们自己 Send 的回显(REST 路径已落库,跳过)
// 与 tool_result(补全工具卡片结果)。字符串 content 只有回显与后台通知两种
// 可能 —— 后者(旧形态的 task_notification)是空闲间隙注入的 XML,要透出。
func (d *claudeDriver) handleUserEcho(m *claudeMsg) {
	if m == nil {
		return
	}
	if rawIsString(m.Content) {
		var text string
		json.Unmarshal(m.Content, &text)
		d.maybeTaskNotificationXML(text)
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
		images := d.saveResultImages(b.Content, b.ToolUseID)
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
			Images:    images,
			State:     map[bool]string{true: "error", false: "ok"}[b.IsError],
		}})
	}
}

// handleSubagentTools 子 agent 的工具调用 → 挂到父 Task 卡的过程事件。
// 只取 tool_use / tool_result 块:文本/思考不透(子 agent 的推理对主时间线
// 是噪音,前端只要知道它在读哪个文件、跑哪条命令)。与主时间线共用
// tool_call 形态,差异只在 ParentToolUseID 字段。
func (d *claudeDriver) handleSubagentTools(m *claudeMsg, parent string) {
	if m == nil {
		return
	}
	var blocks []contentBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "tool_use":
			d.emit(Event{Kind: KindToolCall, Payload: &ToolCallPayload{
				Tool:            b.Name,
				ToolUseID:       b.ID,
				ParentToolUseID: parent,
				Args:            truncateJSON(string(b.Input), 8*1024),
				State:           "running",
			}})
		case "tool_result":
			result := toolResultText(b.Content)
			if b.IsError && result != "" {
				result = "错误: " + result
			}
			d.emit(Event{Kind: KindToolCall, Payload: &ToolCallPayload{
				ToolUseID:       b.ToolUseID,
				ParentToolUseID: parent,
				Result:          truncate(result, 4*1024),
				State:           map[bool]string{true: "error", false: "ok"}[b.IsError],
			}})
		}
	}
}

// maybeTaskNotificationXML 后台通知:作为 user 消息注入的
// <task-notification><summary>…</summary><status>…</status><event>…</event>
// <output-file>…</output-file></task-notification>(hapi eventParsing 同款解析;
// Monitor 事件带 <event> 具体行、无 <status>)。非此形态(普通回显)静默跳过。
// transcript tailer 与 stdout user-echo 两条路径都走这里,seenNotify 去重。
func (d *claudeDriver) maybeTaskNotificationXML(text string) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "<task-notification>") {
		return
	}
	summary := xmlTagValue(trimmed, "summary")
	if summary == "" {
		return
	}
	status := xmlTagValue(trimmed, "status")
	event := xmlTagValue(trimmed, "event")
	if event == "" {
		event = readNotifyOutput(xmlTagValue(trimmed, "output-file"), status)
	}
	if d.seenNotify(summary + "|" + status + "|" + event) {
		return
	}
	d.emit(Event{Kind: KindSystemInfo, Payload: &SystemInfoPayload{
		Type:   "task_notification",
		Text:   truncate(summary, 2000),
		Status: status,
		Event:  truncate(event, 8000),
	}})
}

// readNotifyOutput 后台命令失败时的输出捞取:通知 XML 里带 <output-file>
// 指向任务的完整输出文件,claude 不经流转发给 stdout —— 不读它,远程端只
// 能看到退出码一行,失败原因(测试输出/编译错误)全在文件里。只对 failed 读:
// completed 的输出常驻磁盘(日志/服务),没必要搬进事件。
func readNotifyOutput(path, status string) string {
	if path == "" || status != "failed" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	out := strings.TrimSpace(string(b))
	if out == "" {
		return ""
	}
	// 只搬尾部 8KB:失败输出常见"跑了一半才挂",头部没诊断价值,
	// 事件 payload 也没地方装几 MB 的日志。
	const tail = 8 * 1024
	if len(out) > tail {
		cut := len(out) - tail
		// 找一个行首落点再对齐 UTF-8 边界,别把半行糊在"已截断"后面。
		for cut < len(out) && out[cut] != '\n' && out[cut]&0xC0 != 0x80 {
			cut++
		}
		for cut < len(out) && out[cut]&0xC0 == 0x80 {
			cut++
		}
		out = "…(已截断)\n" + out[cut:]
	}
	return out
}

// seenNotify 通知去重(双路径防双发):没见过就记下并返回 false。
// 键有界(64 条 FIFO),会话生命周期内够用且不膨胀。
func (d *claudeDriver) seenNotify(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.notified[key]; ok {
		return true
	}
	d.notified[key] = struct{}{}
	d.notifyOrder = append(d.notifyOrder, key)
	if len(d.notifyOrder) > 64 {
		delete(d.notified, d.notifyOrder[0])
		d.notifyOrder = d.notifyOrder[1:]
	}
	return false
}

// tailTranscript 盯 claude 的会话转录文件,补捞 stream-json stdout 上看不见的
// 后台通知:回合进行中入队的 task-notification 会被 absorbed_mid_turn 吸收进
// 上下文(转录里只留下 queue-operation/attachment 条目),stdout 永不发对应
// user 消息 —— Monitor 事件几乎总落在这个窗口,只能从转录侧看见。
// 轮询周期 1 秒:告警延迟可接受,又不用 inotify(容器/跨平台省心)。
func (d *claudeDriver) tailTranscript(sessionID string) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}
	path := filepath.Join(home, ".claude", "projects", projectSlug(d.cwd), sessionID+".jsonl")
	var offset int64
	for {
		select {
		case <-d.done:
			return
		default:
		}
		if f, err := os.Open(path); err == nil {
			if st, err := f.Stat(); err == nil {
				if st.Size() < offset {
					offset = 0 // 文件被重建:从头再来
				}
				if _, err := f.Seek(offset, io.SeekStart); err == nil {
					br := bufio.NewReaderSize(f, 64*1024)
					for {
						line, err := br.ReadBytes('\n')
						if err != nil {
							break // EOF:半行留到下一轮,offset 不动
						}
						offset += int64(len(line))
						d.onTranscriptLine(line)
					}
				}
			}
			f.Close()
		}
		// 文件晚于 init 创建也无所谓:开着一直等。
		select {
		case <-d.done:
			return
		case <-time.After(time.Second):
		}
	}
}

// onTranscriptLine 转录里的一条:只关心 queue-operation 的 enqueue(通知
// 发生的最早信号;attachment/remove 是它的后续,dequeue 后 stdout 会发
// user 消息,由去重兜住)。
func (d *claudeDriver) onTranscriptLine(line []byte) {
	var o struct {
		Type      string `json:"type"`
		Operation string `json:"operation"`
		Content   string `json:"content"`
	}
	if json.Unmarshal(line, &o) != nil || o.Type != "queue-operation" || o.Operation != "enqueue" {
		return
	}
	d.maybeTaskNotificationXML(o.Content)
}

// projectSlug claude 转录目录的路径编码:路径里非字母数字的字符一律换 '-'。
func projectSlug(cwd string) string {
	var b strings.Builder
	b.Grow(len(cwd))
	for _, r := range cwd {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// xmlTagValue 提取 <tag>值</tag>(首个匹配;标签不存在返回空串)。
func xmlTagValue(s, tag string) string {
	open, close := "<"+tag+">", "</"+tag+">"
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	i += len(open)
	j := strings.Index(s[i:], close)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(s[i : i+j])
}

// claudeStreamEvent --include-partial-messages 的 stream_event 行:event 字段
// 包着 Anthropic Messages API 的流式事件,只关心 content_block_delta 的
// 文本与思考增量(工具参数的 input_json_delta 不透出,完整 tool_use 照旧到达)。
type claudeStreamEvent struct {
	Type  string `json:"type"`
	Delta *struct {
		Type     string `json:"type"` // text_delta | thinking_delta | input_json_delta
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"delta"`
}

// handleStreamEvent 流式增量 → 瞬态事件(不落库,只广播;完整消息随后到达)。
// parent_tool_use_id 非空的是子 agent(Task 工具)自己的流:子 agent 的输出
// 不会以顶级 assistant 消息落库,流了也配不上对,跳过。
func (d *claudeDriver) handleStreamEvent(msg claudeLine) {
	if msg.ParentToolUseID != "" || len(msg.Event) == 0 {
		return
	}
	var ev claudeStreamEvent
	if err := json.Unmarshal(msg.Event, &ev); err != nil || ev.Type != "content_block_delta" || ev.Delta == nil {
		return
	}
	switch ev.Delta.Type {
	case "text_delta":
		if ev.Delta.Text != "" {
			d.emit(Event{Kind: KindAssistantDelta, Payload: ev.Delta.Text})
		}
	case "thinking_delta":
		if ev.Delta.Thinking != "" {
			d.emit(Event{Kind: KindReasoningDelta, Payload: ev.Delta.Thinking})
		}
	}
}

// handleCanUseTool 一个权限询问:落一条 permission_request 事件,挂起等 Resolve。
// 会话规则(本会话允许)命中时直接放行,不打扰用户。
// AskUserQuestion 是特例:它不是权限询问而是 agent 向用户提问,分流到
// handleAskUser(回答走 allow + updatedInput.answers,空答案会锁死回合)。
func (d *claudeDriver) handleCanUseTool(reqID string, rawReq json.RawMessage) {
	var req canUseToolRequest
	if err := json.Unmarshal(rawReq, &req); err != nil || req.Subtype != "can_use_tool" {
		return
	}
	if reqID == "" {
		return
	}
	if isAskToolName(req.ToolName) {
		d.handleAskUser(reqID, req.Input)
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

// isAskToolName claude 的提问工具(stream-json 里同样走 can_use_tool 协议)。
func isAskToolName(tool string) bool {
	return tool == "AskUserQuestion" || tool == "ask_user_question"
}

// handleAskUser 一个提问:解析 questions 落 ask_user 事件,挂起等 Answer。
// 形状不认识(没有 questions)时退回普通审批卡 —— 用户至少能看到点什么,
// 拒绝即"不给答案",claude 自己会绕开。
func (d *claudeDriver) handleAskUser(reqID string, rawInput json.RawMessage) {
	var in struct {
		Questions []struct {
			Header      string `json:"header"`
			Question    string `json:"question"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
				Preview     string `json:"preview"`
			} `json:"options"`
		} `json:"questions"`
	}
	json.Unmarshal(rawInput, &in)
	qs := make([]AskQuestion, 0, len(in.Questions))
	for i, q := range in.Questions {
		if strings.TrimSpace(q.Question) == "" {
			continue
		}
		opts := make([]AskOption, 0, len(q.Options))
		for _, o := range q.Options {
			if strings.TrimSpace(o.Label) == "" {
				continue
			}
			opts = append(opts, AskOption{Label: o.Label, Description: o.Description, Preview: o.Preview})
		}
		qs = append(qs, AskQuestion{
			ID:       strconv.Itoa(i), // claude 没有稳定问题 id,用下标
			Header:   q.Header,
			Question: q.Question,
			Multi:    q.MultiSelect,
			Required: true,
			Options:  opts,
		})
	}
	if len(qs) == 0 {
		args := truncate(string(rawInput), 8*1024)
		ch := make(chan *approveDecision, 1)
		d.mu.Lock()
		if d.stdin == nil {
			d.mu.Unlock()
			return
		}
		d.pending[reqID] = ch
		d.lastReqTool[reqID] = "AskUserQuestion"
		d.lastReqArgs[reqID] = args
		d.mu.Unlock()
		d.emit(Event{Kind: KindPermissionReq, Payload: &PermissionReqPayload{
			ReqID: reqID,
			Tool:  "AskUserQuestion",
			Args:  args,
		}})
		dec := <-ch
		d.writeControlResponse(reqID, dec.allow)
		return
	}

	ch := make(chan *approveDecision, 1)
	d.mu.Lock()
	if d.stdin == nil {
		d.mu.Unlock()
		return
	}
	d.pending[reqID] = ch
	d.asks[reqID] = &askPending{raw: rawInput, questions: qs}
	d.mu.Unlock()

	d.emit(Event{Kind: KindAskUser, Payload: &AskUserPayload{ReqID: reqID, Questions: qs}})

	dec := <-ch

	d.mu.Lock()
	ask := d.asks[reqID]
	delete(d.asks, reqID)
	d.mu.Unlock()
	if ask == nil { // 进程退出时被清:claude 已收不到答复
		return
	}
	if !dec.allow || dec.answers == nil {
		d.writeControlResponse(reqID, false)
		return
	}
	// claude 2.x 的 AskUserQuestion 要 answers: {问题文本: 选项}(多选拼
	// 逗号),且 updatedInput 带上原 questions 数组 —— 按问题 id(下标)
	// 反查问题文本。空答案会产出空结果并锁死回合(hapi 踩过),所以提交前
	// 前端强制必选。
	answers := map[string]string{}
	for _, q := range ask.questions {
		if sel := dec.answers[q.ID]; len(sel) > 0 {
			answers[q.Question] = strings.Join(sel, ",")
		}
	}
	var input map[string]any
	if json.Unmarshal(ask.raw, &input) != nil || input == nil {
		input = map[string]any{}
	}
	input["answers"] = answers
	d.writeControlResponseInput(reqID, input)
}

// Answer 回答一个 ask_user 请求(见 Driver 接口)。answers=nil 表示取消。
func (d *claudeDriver) Answer(reqID string, answers map[string][]string) error {
	d.mu.Lock()
	ch := d.pending[reqID]
	if ch != nil {
		delete(d.pending, reqID)
	}
	delete(d.lastReqTool, reqID)
	delete(d.lastReqArgs, reqID)
	d.mu.Unlock()
	if ch == nil {
		return fmt.Errorf("问题不存在或已回答: %s", reqID)
	}
	if answers == nil {
		ch <- &approveDecision{allow: false}
	} else {
		ch <- &approveDecision{allow: true, answers: answers}
	}
	return nil
}

// writeControlResponse 把审批决定按控制协议写回 stdin。
func (d *claudeDriver) writeControlResponse(reqID string, allow bool) {
	if allow {
		d.writeControlResponseInput(reqID, nil)
		return
	}
	d.writeControlResponseRaw(reqID, map[string]any{
		"behavior": "deny",
		"message":  "用户拒绝了此操作",
	})
}

// writeControlResponseInput allow + 带 updatedInput 的答复。updatedInput 为
// nil 时等于普通放行(空 updatedInput);AskUserQuestion 用它把 answers 塞回。
func (d *claudeDriver) writeControlResponseInput(reqID string, updatedInput map[string]any) {
	if updatedInput == nil {
		updatedInput = map[string]any{}
	}
	d.writeControlResponseRaw(reqID, map[string]any{
		"behavior":     "allow",
		"updatedInput": updatedInput,
	})
}

// writeControlResponseRaw control_response 信封 + 行为体,唯一的写 stdin 路径。
func (d *claudeDriver) writeControlResponseRaw(reqID string, behavior map[string]any) {
	resp := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": reqID,
			"response":   behavior,
		},
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

// saveResultImages 把 tool_result content 里的 image 块(base64)落盘到会话
// 附件目录,返回文件名列表。filesDir 为空(测试)或没有 image 块时返回 nil。
// 文件名 = img-<toolUseId>-<序号>.<ext>,同 id 重复落盘直接覆盖,幂等。
func (d *claudeDriver) saveResultImages(raw json.RawMessage, toolUseID string) []string {
	if d.filesDir == "" || len(raw) == 0 || raw[0] == '"' {
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	// toolUseId 只留安全字符做文件名(toolu_01X… 本就安全,防御性过滤)。
	safe := make([]byte, 0, len(toolUseID))
	for _, c := range []byte(toolUseID) {
		if c == '-' || c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			safe = append(safe, c)
		}
	}
	if len(safe) == 0 {
		safe = append(safe, 'x')
	}
	var names []string
	i := 0
	for _, b := range blocks {
		if b.Type != "image" || b.Source == nil || b.Source.Data == "" {
			continue
		}
		if b.Source.Type != "" && b.Source.Type != "base64" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(b.Source.Data)
		if err != nil || len(data) == 0 {
			continue
		}
		name := fmt.Sprintf("img-%s-%d.%s", string(safe), i, imgExt(b.Source.MediaType))
		if err := os.MkdirAll(d.filesDir, 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(d.filesDir, name), data, 0o644); err != nil {
			continue
		}
		names = append(names, name)
		i++
	}
	return names
}

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
