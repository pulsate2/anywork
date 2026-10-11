package agent

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// piGateJS 审批 gate 扩展正文(见 pi_gate.mjs);Start 时落盘并以 -e 注入,
// 给 rpc 模式补上原生缺失的工具审批通道。
//
//go:embed pi_gate.mjs
var piGateJS string

// gate 与 pi_gate.mjs 约定的三态令牌(select 的 option / 回带的 value)。
const (
	piGateDeny         = "deny"
	piGateAllowOnce    = "allow-once"
	piGateAllowSession = "allow-session"
)

// piDriver:spawn `pi --mode rpc`,行分隔 JSON(NDJSON)over stdio。
// 协议形状对齐 hapi 的 cli/src/pi/*(无正式文档,以 hapi 实测为准):
//
//	命令(写 stdin): {"type":"prompt","message":…,"streamingBehavior":"followUp"}
//	                 {"type":"get_state"} / {"type":"abort"} / {"type":"set_model",…} …
//	事件(读 stdout):{"type":"agent_start"|"turn_start"|"message_start"|
//	                 "message_update"|"message_end"|"turn_end"|"agent_end"|
//	                 "agent_settled"|"tool_execution_start"|…}
//	响应(读 stdout):{"type":"response","command":…,"success":…,"error"?,"data"?,"id"?}
//
// pi 命令原生不带 id;与 codex 的 JSON-RPC 不同,这里给需要配对响应的命令注入
// id:String(++nextID),pi 会在 response 事件里原样回带(对齐 hapi 的
// PiRpcResolver),据此把响应回填给等待中的 call。
// pi 的审批模型:rpc 模式原生自动放行、没有权限通道,这里靠注入的 gate 扩展
// (pi_gate.mjs,-e 注入)在有副作用的工具执行前经 extension_ui_request(select,
// title 为 sentinel-JSON)发询问,handleExtensionUi 认出 sentinel 后转成
// KindPermissionReq 审批卡挂起等 Resolve,再把三态令牌经 extension_ui_response
// 回带给 gate(allow→放行、deny→gate block)。非 sentinel 的扩展交互仍走
// ask_user 等 Answer。
type piDriver struct {
	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser

	nextID    int
	pending   map[string]chan piResp         // 注入 id → 响应
	asks      map[string]chan *piAskDecision // 扩展交互 reqID → 回答(nil = 取消)
	approvals map[string]piApproval          // 审批 reqID → 工具名/入参(Resolve/PendingRequests 用)
	gatePath  string                         // 落盘的 gate 扩展路径(Close 时删)
	autoAllow bool                           // accept(全放行)模式:审批询问由 driver 直接代答,不出卡

	// 编辑批次:pi 把一回合里连续的编辑折成一个 batch,只在 batch 末尾那条
	// tool_execution_end 给出覆盖整批的合并 patch,前面的成员(last:false)
	// patch 为空(结果文本是 "In batch N (queued)")。先把这些空 patch 的成员卡
	// 攒着,等末尾合并 patch 到手再回填给它们 —— 否则 replace_match 这类排在
	// 批中间的编辑卡永远没有 diff(用户实测)。只在 pump 单协程里读写,无需锁。
	piBatch []*ToolCallPayload

	sessionID    string // pi sessionId(续聊凭据)
	model        string // 当前 modelId(get_state / set_model 回填)
	provider     string // 当前 provider(set_model 需要 {provider, modelId})
	effort       string // 思考档位(anywork Effort → pi thinkingLevel)
	cwd          string
	models       []piModel // get_available_models 缓存:set_model 反查 provider、算上下文窗口
	stderrTail   string
	lastErrorMsg string // 回合级错误去重(message_end 与 turn_end 带同一条)
	sawResult    atomic.Bool

	// 流式累积:按 contentIndex 分段攒,message_end 时按序拼成完整块落库。
	textSeg      map[int]string
	reasoningSeg map[int]string

	// 回合去重:pi 的 agent_start/settled 与 Send 都可能触发 running/idle,
	// 用一个标记保证一回合只翻一次状态。
	running   bool
	turnStart time.Time

	closeOnce sync.Once
	exitErr   error

	events   chan Event
	done     chan struct{}
	doneOnce sync.Once
	evMu     sync.RWMutex
	evClosed bool
}

// piResp 一条 response 事件的归一化结果。
type piResp struct {
	success bool
	errMsg  string
	data    json.RawMessage
}

// piModel get_available_models 里的一条(字段名对齐 pi:id/provider/…)。
type piModel struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Name          string `json:"name"`
	ContextWindow int    `json:"contextWindow"`
}

// piAskDecision 扩展交互的回答;nil = 取消。
type piAskDecision struct {
	answers map[string][]string
}

// piApproval 一条挂起的审批(gate 发来的 sentinel-select):工具名与入参,
// 供审批卡展示与断线重连补发。
type piApproval struct {
	tool string
	args string
}

func newPiDriver() *piDriver {
	return &piDriver{
		pending:      map[string]chan piResp{},
		asks:         map[string]chan *piAskDecision{},
		approvals:    map[string]piApproval{},
		textSeg:      map[int]string{},
		reasoningSeg: map[int]string{},
		events:       make(chan Event, 128),
		done:         make(chan struct{}),
	}
}

// piBin 解析 pi 可执行文件;LR_PI_BIN 可覆盖。
func piBin() (string, error) {
	if p := os.Getenv("LR_PI_BIN"); p != "" {
		return p, nil
	}
	return exec.LookPath("pi")
}

// writeGate 把内嵌的审批 gate 扩展落到临时文件,返回路径(Close 时删)。
// .mjs 后缀让 pi 的加载器按 ESM 处理 default-export 工厂。
func (d *piDriver) writeGate() (string, error) {
	f, err := os.CreateTemp("", "anywork-pi-gate-*.mjs")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(piGateJS); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	d.mu.Lock()
	d.gatePath = f.Name()
	d.mu.Unlock()
	return f.Name(), nil
}

// piThinkingLevel anywork Effort → pi thinkingLevel。anywork 的 7 档正是照
// pi 的档位设的(off/minimal/low/medium/high/xhigh/max),这里一一对应;
// none → off,空 / 不认识返回空(不下发,用 pi 默认)。
func piThinkingLevel(effort string) string {
	switch effort {
	case EffortNone:
		return "off"
	case EffortMinimal:
		return "minimal"
	case EffortLow:
		return "low"
	case EffortMedium:
		return "medium"
	case EffortHigh:
		return "high"
	case EffortXHigh:
		return "xhigh"
	case EffortMax:
		return "max"
	}
	return ""
}

func (d *piDriver) Start(opts StartOpts) error {
	bin, err := piBin()
	if err != nil {
		return fmt.Errorf("未找到 pi 可执行文件(可用 LR_PI_BIN 指定): %w", err)
	}
	d.model = opts.Model
	d.effort = opts.Effort
	d.cwd = opts.Cwd
	d.autoAllow = opts.PermissionMode == PermAccept

	args := []string{"--mode", "rpc"}
	// 审批 gate:落盘失败只是退回“无审批”(不阻断启动)。
	if gate, gerr := d.writeGate(); gerr == nil && gate != "" {
		args = append(args, "-e", gate)
	}
	if opts.ResumeID != "" {
		args = append(args, "--session", opts.ResumeID)
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = opts.Cwd
	cmd.Env = os.Environ()
	if len(opts.Env) > 0 {
		cmd.Env = append(cmd.Env, opts.Env...)
	}
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
		return fmt.Errorf("启动 pi --mode rpc 失败: %w", err)
	}

	d.mu.Lock()
	d.cmd = cmd
	d.stdin = stdin
	d.mu.Unlock()

	go d.readStdout(stdout)
	go d.readStderr(stderr)
	go d.waitExit()

	// 握手同步做:get_state 失败 = 协议不兼容 / --session 无效,Create 直接
	// 报错,会话不落库(对齐 codex,不降级为别的模式)。
	if err := d.handshake(); err != nil {
		d.Close()
		return err
	}
	return nil
}

// handshake 发 get_state(就绪信号 + sessionId + model + thinkingLevel)、
// get_available_models(缓存给 set_model 反查 provider / 算上下文窗口)、
// get_commands(/ 指令提示)。get_state 超时/失败即握手失败。
func (d *piDriver) handshake() error {
	timeout := 30 * time.Second
	resp, err := d.call("get_state", nil, timeout)
	if err != nil {
		return fmt.Errorf("pi get_state 失败(版本不兼容或 --session 无效?): %w", err)
	}
	if !resp.success {
		msg := resp.errMsg
		if msg == "" {
			msg = "get_state 返回失败"
		}
		return fmt.Errorf("pi 会话初始化失败: %s", msg)
	}
	var st struct {
		Model struct {
			ID       string `json:"id"`
			ModelID  string `json:"modelId"`
			Provider string `json:"provider"`
		} `json:"model"`
		SessionID string `json:"sessionId"`
	}
	json.Unmarshal(resp.data, &st)
	d.mu.Lock()
	d.sessionID = st.SessionID
	if m := firstNonEmpty(st.Model.ModelID, st.Model.ID); m != "" {
		d.model = m
	}
	if st.Model.Provider != "" {
		d.provider = st.Model.Provider
	}
	d.mu.Unlock()

	// 可用模型缓存(失败不致命:set_model 时反查不到会直接用当前 provider)。
	if mresp, err := d.call("get_available_models", nil, timeout); err == nil && mresp.success {
		var md struct {
			Models []piModel `json:"models"`
		}
		if json.Unmarshal(mresp.data, &md) == nil {
			d.mu.Lock()
			d.models = md.Models
			d.mu.Unlock()
		}
	}

	// 启动时指定了思考档位就先下发(对齐 hapi:get_state 的 thinkingLevel 是
	// 权威,set_thinking_level 成功后才改用我们的)。
	if lvl := piThinkingLevel(d.effort); lvl != "" {
		d.call("set_thinking_level", map[string]any{"level": lvl}, timeout)
	}

	// / 指令提示表:get_commands 响应里的 name 列表(前端做输入提示)。
	if cresp, err := d.call("get_commands", nil, timeout); err == nil && cresp.success {
		var cd struct {
			Commands []struct {
				Name string `json:"name"`
			} `json:"commands"`
		}
		if json.Unmarshal(cresp.data, &cd) == nil {
			cmds := make([]string, 0, len(cd.Commands))
			for _, c := range cd.Commands {
				if c.Name == "" {
					continue
				}
				name := c.Name
				if !strings.HasPrefix(name, "/") {
					name = "/" + name
				}
				cmds = append(cmds, name)
			}
			if len(cmds) > 0 {
				d.emit(Event{Kind: KindSlashCommands, Payload: cmds})
			}
		}
	}
	return nil
}

func (d *piDriver) Send(text string) error {
	d.mu.Lock()
	if d.stdin == nil {
		d.mu.Unlock()
		return fmt.Errorf("会话未运行")
	}
	d.running = true
	if d.turnStart.IsZero() {
		d.turnStart = time.Now()
	}
	// 只在空闲时被 Manager 调(回合中发消息走服务端队列,idle 时放行),所以
	// 这里总是起一个新回合;streamingBehavior=followUp 兜底:万一撞上进行中的
	// 回合,pi 把它当后续排队而不是打断。
	err := d.writeLocked(map[string]any{
		"type":              "prompt",
		"message":           text,
		"streamingBehavior": "followUp",
	})
	d.mu.Unlock()
	if err != nil {
		return err
	}
	d.emit(Event{Kind: KindStatus, Payload: &StatusPayload{State: StatusRunning}})
	return nil
}

// Interrupt 打断当前回合:pi 的 abort 不杀进程,随后照常走 turn_end/agent_end
// /agent_settled 把状态收回 idle。
func (d *piDriver) Interrupt() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stdin == nil {
		return fmt.Errorf("会话未运行")
	}
	return d.writeLocked(map[string]any{"type": "abort"})
}

// ApplySettings 运行中调整:pi 原生支持改思考档位 / 模型,当场下发并等确认
// (对齐 hapi:确认后才提交到本地状态)。
func (d *piDriver) ApplySettings(u SettingsUpdate) error {
	d.mu.Lock()
	running := d.stdin != nil
	d.mu.Unlock()
	if !running {
		return fmt.Errorf("会话未运行")
	}
	// 权限模式当场生效:gate 还在跑,autoAllow 开则后续审批询问由 driver 直接
	// 代答 allow-once(不出卡);关则恢复出卡。无需重启(与 claude 不同)。
	if u.PermissionMode != "" {
		d.mu.Lock()
		d.autoAllow = u.PermissionMode == PermAccept
		d.mu.Unlock()
	}
	if u.Effort != "" {
		if lvl := piThinkingLevel(u.Effort); lvl != "" {
			if resp, err := d.call("set_thinking_level", map[string]any{"level": lvl}, 15*time.Second); err == nil && resp.success {
				d.mu.Lock()
				d.effort = u.Effort
				d.mu.Unlock()
			}
		}
	}
	if u.Model != "" {
		d.mu.Lock()
		provider := d.providerFor(u.Model)
		d.mu.Unlock()
		resp, err := d.call("set_model", map[string]any{"provider": provider, "modelId": u.Model}, 15*time.Second)
		if err != nil {
			return err
		}
		if !resp.success {
			return fmt.Errorf("pi 切换模型失败: %s", firstNonEmpty(resp.errMsg, "未知错误"))
		}
		var sm struct {
			ID       string `json:"id"`
			ModelID  string `json:"modelId"`
			Provider string `json:"provider"`
		}
		json.Unmarshal(resp.data, &sm)
		d.mu.Lock()
		if m := firstNonEmpty(sm.ModelID, sm.ID); m != "" {
			d.model = m
		} else {
			d.model = u.Model
		}
		if p := firstNonEmpty(sm.Provider, provider); p != "" {
			d.provider = p
		}
		d.mu.Unlock()
	}
	return nil
}

// providerFor 反查某 modelId 的 provider;缓存里没有就用当前 provider。
// 必须在持有 mu 时调用。
func (d *piDriver) providerFor(modelID string) string {
	for _, m := range d.models {
		if m.ID == modelID {
			return m.Provider
		}
	}
	return d.provider
}

// Resolve 答复一条审批(gate 的 sentinel-select):把三态映射成 gate 认得的
// value 经 extension_ui_response 回带 —— deny 让 gate block,allow-once/
// allow-session 放行(session 由 gate 侧按工具名记忆,本会话后续不再问)。
func (d *piDriver) Resolve(reqID string, allow, session bool) error {
	d.mu.Lock()
	_, ok := d.approvals[reqID]
	if ok {
		delete(d.approvals, reqID)
	}
	d.mu.Unlock()
	if !ok {
		return fmt.Errorf("权限请求不存在或已答复: %s", reqID)
	}
	value := piGateDeny
	if allow {
		if session {
			value = piGateAllowSession
		} else {
			value = piGateAllowOnce
		}
	}
	d.sendExtResponse(reqID, map[string]any{"value": value})
	return nil
}

// Answer 回答一个扩展交互(extension_ui_request)。answers=nil 表示取消。
func (d *piDriver) Answer(reqID string, answers map[string][]string) error {
	d.mu.Lock()
	ch := d.asks[reqID]
	if ch != nil {
		delete(d.asks, reqID)
	}
	d.mu.Unlock()
	if ch == nil {
		return fmt.Errorf("问题不存在或已回答: %s", reqID)
	}
	if answers == nil {
		ch <- nil
	} else {
		ch <- &piAskDecision{answers: answers}
	}
	return nil
}

// PendingRequests 挂起的审批(断线重连补发)。pi 的审批带得到工具名与入参,
// 比 claude/codex 更全。按 reqID 排序保证顺序稳定。
func (d *piDriver) PendingRequests() []PermissionReqPayload {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.approvals) == 0 {
		return nil
	}
	ids := make([]string, 0, len(d.approvals))
	for id := range d.approvals {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]PermissionReqPayload, 0, len(ids))
	for _, id := range ids {
		a := d.approvals[id]
		out = append(out, PermissionReqPayload{ReqID: id, Tool: a.tool, Args: a.args})
	}
	return out
}

func (d *piDriver) ExternalID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sessionID
}

func (d *piDriver) CurrentModel() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.model
}

func (d *piDriver) Events() <-chan Event  { return d.events }
func (d *piDriver) Done() <-chan struct{} { return d.done }

func (d *piDriver) ExitErr() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.exitErr
}

func (d *piDriver) ExitDetail() string {
	d.mu.Lock()
	tail, err := d.stderrTail, d.exitErr
	d.mu.Unlock()
	if err == nil || tail == "" || d.sawResult.Load() {
		return ""
	}
	return truncate(err.Error()+": "+tail, 4000)
}

func (d *piDriver) Close() error {
	d.closeOnce.Do(func() {
		if d.cmd != nil && d.cmd.Process != nil {
			killProcessGroup(d.cmd)
		}
		d.mu.Lock()
		gp := d.gatePath
		d.gatePath = ""
		d.mu.Unlock()
		if gp != "" {
			os.Remove(gp)
		}
	})
	return nil
}

// turnEnd 回合耗时统计:发 turn_duration(与 claude/codex 同一事件形态)。
func (d *piDriver) turnEnd() {
	d.mu.Lock()
	start := d.turnStart
	d.turnStart = time.Time{}
	d.mu.Unlock()
	if start.IsZero() {
		return
	}
	d.emit(Event{Kind: KindSystemInfo, Payload: &SystemInfoPayload{
		Type:       "turn_duration",
		DurationMs: float64(time.Since(start).Milliseconds()),
	}})
}

// ---- 事件通道与进程生命周期 ----

// emit 投递一条事件。与 close(d.events) 用 evMu 互斥(同 codexDriver.emit:
// 进程退出那一刻读循环手里还压着没消化的行,关流后再裸发会 panic 带走进程)。
func (d *piDriver) emit(ev Event) {
	d.evMu.RLock()
	defer d.evMu.RUnlock()
	if d.evClosed {
		return
	}
	select {
	case d.events <- ev:
	default:
		// 消费停滞时丢消息优于卡死。
	}
}

func (d *piDriver) waitExit() {
	err := d.cmd.Wait()
	d.mu.Lock()
	d.exitErr = err
	d.stdin = nil
	// 进程死了:等待中的 RPC 全部失败,挂起的扩展交互按取消收尾。
	for id, ch := range d.pending {
		select {
		case ch <- piResp{success: false, errMsg: "会话已结束"}:
		default:
		}
		delete(d.pending, id)
	}
	for id, ch := range d.asks {
		select {
		case ch <- nil:
		default:
		}
		delete(d.asks, id)
	}
	// 挂起的审批无等待通道(Resolve 直接回 RPC),进程已死直接清空。
	for id := range d.approvals {
		delete(d.approvals, id)
	}
	d.mu.Unlock()
	d.doneOnce.Do(func() { close(d.done) })
	// 关流必须与 emit 互斥(见 emit)。
	d.evMu.Lock()
	d.evClosed = true
	close(d.events)
	d.evMu.Unlock()
}

func (d *piDriver) readStderr(r io.Reader) {
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

// ---- RPC 底座(注入 id 配对) ----

// call 发一条带注入 id 的命令并同步等响应:锁内注册+写出,锁外等 ——
// 等待期间 readStdout 的 handleResponse 要拿同一把锁回填通道。
func (d *piDriver) call(cmdType string, extra map[string]any, timeout time.Duration) (piResp, error) {
	d.mu.Lock()
	if d.stdin == nil {
		d.mu.Unlock()
		return piResp{}, fmt.Errorf("会话未运行")
	}
	d.nextID++
	id := strconv.Itoa(d.nextID)
	ch := make(chan piResp, 1)
	d.pending[id] = ch
	msg := map[string]any{"type": cmdType, "id": id}
	for k, v := range extra {
		msg[k] = v
	}
	if err := d.writeLocked(msg); err != nil {
		delete(d.pending, id)
		d.mu.Unlock()
		return piResp{}, err
	}
	d.mu.Unlock()

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(timeout):
		d.mu.Lock()
		delete(d.pending, id)
		d.mu.Unlock()
		return piResp{}, fmt.Errorf("%s 超时", cmdType)
	case <-d.done:
		return piResp{}, fmt.Errorf("会话已结束")
	}
}

// writeLocked 写一行命令。必须在持有 mu 时调用。
func (d *piDriver) writeLocked(msg map[string]any) error {
	if d.stdin == nil {
		return fmt.Errorf("会话未运行")
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := d.stdin.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// handleResponse 把 response 事件回填给等待中的 call(按注入的 id 配对)。
func (d *piDriver) handleResponse(line []byte) {
	var r struct {
		Success bool            `json:"success"`
		Error   string          `json:"error"`
		Data    json.RawMessage `json:"data"`
		ID      string          `json:"id"`
	}
	if json.Unmarshal(line, &r) != nil || r.ID == "" {
		return
	}
	d.mu.Lock()
	ch := d.pending[r.ID]
	delete(d.pending, r.ID)
	d.mu.Unlock()
	if ch == nil {
		return
	}
	ch <- piResp{success: r.Success, errMsg: r.Error, data: r.Data}
}

func (d *piDriver) readStdout(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &head) != nil {
			continue
		}
		// 至少一行合法 JSON:进程是活的,退出时 stderr 尾部多半是噪音
		// (见 ExitDetail)。
		d.sawResult.Store(true)
		// scanner 复用底层缓冲:拷一份再交给 handler,扩展交互的异步 goroutine
		// 不会读到下一行覆盖后的内容。
		raw := make([]byte, len(line))
		copy(raw, line)
		if head.Type == "response" {
			d.handleResponse(raw)
			continue
		}
		d.handleEvent(head.Type, raw)
	}
}

// ---- 事件归一化 ----

func (d *piDriver) handleEvent(typ string, raw []byte) {
	switch typ {
	case "agent_start":
		d.mu.Lock()
		already := d.running
		d.running = true
		d.lastErrorMsg = ""
		if d.turnStart.IsZero() {
			d.turnStart = time.Now()
		}
		d.mu.Unlock()
		if !already {
			d.emit(Event{Kind: KindStatus, Payload: &StatusPayload{State: StatusRunning}})
		}

	case "turn_start":
		d.mu.Lock()
		d.lastErrorMsg = ""
		d.mu.Unlock()

	case "message_start":
		// 新一条 assistant 消息:上一条若没收尾先 flush,再清分段缓冲。
		d.flushMessage()

	case "message_update":
		d.handleMessageUpdate(raw)

	case "message_end":
		d.flushMessage()
		d.checkTurnError(raw)

	case "turn_end":
		d.flushMessage()
		d.checkTurnError(raw)
		d.handleTurnUsage(raw)

	case "agent_end":
		var e struct {
			WillRetry bool `json:"willRetry"`
		}
		json.Unmarshal(raw, &e)
		d.flushMessage()
		if !e.WillRetry {
			d.settle()
		}

	case "agent_settled":
		d.settle()

	case "tool_execution_start":
		d.handleToolStart(raw)
	case "tool_execution_end":
		d.handleToolEnd(raw)
	case "tool_execution_update":
		// 中间进度 anywork 不单列:start 已出卡,end 收卡。

	case "compaction_start":
		d.emit(Event{Kind: KindCompaction, Payload: &CompactionPayload{Phase: "start"}})
	case "compaction_end":
		var e struct {
			ErrorMessage string `json:"errorMessage"`
		}
		json.Unmarshal(raw, &e)
		p := &CompactionPayload{Trigger: "auto"}
		if e.ErrorMessage != "" {
			p.Failed = true
			p.Error = truncate(e.ErrorMessage, 4000)
		}
		d.emit(Event{Kind: KindCompaction, Payload: p})

	case "extension_ui_request":
		d.handleExtensionUi(raw)

	default:
		// keep_alive / entry_appended / session_info_changed / auto_retry_* 等:忽略。
	}
}

// settle 回合收尾:发回合时长 + 翻回 idle,一回合只翻一次(agent_end 与
// agent_settled 哪个先到都行,后到者空转)。
func (d *piDriver) settle() {
	d.mu.Lock()
	wasRunning := d.running
	d.running = false
	d.mu.Unlock()
	if !wasRunning {
		return
	}
	d.turnEnd()
	d.emit(Event{Kind: KindStatus, Payload: &StatusPayload{State: StatusIdle}})
}

func (d *piDriver) handleMessageUpdate(raw []byte) {
	var m struct {
		AssistantMessageEvent struct {
			Type         string `json:"type"`
			Delta        string `json:"delta"`
			ContentIndex *int   `json:"contentIndex"`
		} `json:"assistantMessageEvent"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	ame := m.AssistantMessageEvent
	if ame.Delta == "" {
		return
	}
	idx := 0
	if ame.ContentIndex != nil {
		idx = *ame.ContentIndex
	}
	switch ame.Type {
	case "text_delta":
		d.mu.Lock()
		d.textSeg[idx] += ame.Delta
		d.mu.Unlock()
		d.emit(Event{Kind: KindAssistantDelta, Payload: ame.Delta})
	case "thinking_delta":
		d.mu.Lock()
		d.reasoningSeg[idx] += ame.Delta
		d.mu.Unlock()
		d.emit(Event{Kind: KindReasoningDelta, Payload: ame.Delta})
	}
}

// flushMessage message_end/turn_end:把攒着的分段按 contentIndex 升序拼成完整
// reasoning / assistant_text 落库(reasoning 在前,与 hapi 快照顺序一致),随后
// 清空。无内容不发。幂等:连续调用第二次什么都不发。
func (d *piDriver) flushMessage() {
	d.mu.Lock()
	reasoning := joinSegments(d.reasoningSeg)
	text := joinSegments(d.textSeg)
	d.textSeg = map[int]string{}
	d.reasoningSeg = map[int]string{}
	d.mu.Unlock()
	if strings.TrimSpace(reasoning) != "" {
		d.emit(Event{Kind: KindReasoning, Payload: reasoning})
	}
	if strings.TrimSpace(text) != "" {
		d.emit(Event{Kind: KindAssistantText, Payload: text})
	}
}

// joinSegments 按 index 升序拼接分段内容。
func joinSegments(seg map[int]string) string {
	if len(seg) == 0 {
		return ""
	}
	idxs := make([]int, 0, len(seg))
	for i := range seg {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	var b strings.Builder
	for _, i := range idxs {
		b.WriteString(seg[i])
	}
	return b.String()
}

// checkTurnError pi 在 message_end / turn_end 的 message 上报回合级失败
// (stopReason==="error" 带 errorMessage;aborted 是用户主动打断,不算错)。
// 两处带同一条,用 lastErrorMsg 去重只发一次。
func (d *piDriver) checkTurnError(raw []byte) {
	var m struct {
		Message struct {
			StopReason   string `json:"stopReason"`
			ErrorMessage string `json:"errorMessage"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &m) != nil || m.Message.StopReason != "error" {
		return
	}
	msg := firstNonEmpty(m.Message.ErrorMessage, "pi 回合出错")
	d.mu.Lock()
	dup := d.lastErrorMsg == msg
	d.lastErrorMsg = msg
	d.mu.Unlock()
	if dup {
		return
	}
	d.emit(Event{Kind: KindError, Payload: &ErrorPayload{Message: truncate(msg, 4000)}})
}

// handleTurnUsage turn_end 带 message.usage;上下文占用以 get_session_stats 的
// contextUsage 为权威(异步查,拿不到就回落 usage.totalTokens),窗口回落到
// 缓存模型表里的 contextWindow。
func (d *piDriver) handleTurnUsage(raw []byte) {
	var m struct {
		Message struct {
			Usage *struct {
				Input       int `json:"input"`
				Output      int `json:"output"`
				CacheRead   int `json:"cacheRead"`
				CacheWrite  int `json:"cacheWrite"`
				TotalTokens int `json:"totalTokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &m) != nil || m.Message.Usage == nil || m.Message.Usage.TotalTokens <= 0 {
		return
	}
	u := m.Message.Usage
	go func() {
		context := u.TotalTokens
		window := 0
		if resp, err := d.call("get_session_stats", nil, 3*time.Second); err == nil && resp.success {
			var st struct {
				ContextUsage struct {
					Tokens        *int `json:"tokens"`
					ContextWindow *int `json:"contextWindow"`
				} `json:"contextUsage"`
			}
			if json.Unmarshal(resp.data, &st) == nil {
				if st.ContextUsage.Tokens != nil {
					context = *st.ContextUsage.Tokens
				}
				if st.ContextUsage.ContextWindow != nil {
					window = *st.ContextUsage.ContextWindow
				}
			}
		}
		d.mu.Lock()
		model := d.model
		if window == 0 {
			window = d.contextWindowFor(model)
		}
		d.mu.Unlock()
		d.emit(Event{Kind: KindUsage, Payload: &UsagePayload{
			Context:   context,
			Output:    u.Output,
			CacheRead: u.CacheRead,
			Model:     model,
			Window:    window,
		}})
	}()
}

// contextWindowFor 从缓存模型表取上下文窗口;必须在持有 mu 时调用。
func (d *piDriver) contextWindowFor(modelID string) int {
	for _, m := range d.models {
		if m.ID == modelID {
			return m.ContextWindow
		}
	}
	return 0
}

func (d *piDriver) handleToolStart(raw []byte) {
	var e struct {
		ToolCallID string          `json:"toolCallId"`
		ToolName   string          `json:"toolName"`
		Args       json.RawMessage `json:"args"`
	}
	if json.Unmarshal(raw, &e) != nil || e.ToolCallID == "" {
		return
	}
	args := ""
	if len(e.Args) > 0 {
		args = truncateJSON(string(e.Args), 16*1024)
	}
	d.emit(Event{Kind: KindToolCall, Payload: &ToolCallPayload{
		Tool:      firstNonEmpty(e.ToolName, "tool"),
		ToolUseID: e.ToolCallID,
		Args:      args,
		State:     "running",
	}})
}

func (d *piDriver) handleToolEnd(raw []byte) {
	var e struct {
		ToolCallID string          `json:"toolCallId"`
		ToolName   string          `json:"toolName"`
		Result     json.RawMessage `json:"result"`
		IsError    bool            `json:"isError"`
	}
	if json.Unmarshal(raw, &e) != nil || e.ToolCallID == "" {
		return
	}
	state := "ok"
	if e.IsError {
		state = "error"
	}
	p := &ToolCallPayload{
		ToolUseID: e.ToolCallID,
		Result:    truncate(piResultText(e.Result), 16*1024),
		State:     state,
	}
	// pi 的 todo 工具(@juicesharp/rpiv-todo):结果 details.tasks 是全量任务
	// 快照。把它翻成前端 TodoWrite 同款 {todos:[…]} 塞进 Args,顶部任务面板
	// 按全量重建;带上 Tool 名,前端据此把这张卡汇进面板而非时间线。
	if e.ToolName == "todo" && !e.IsError {
		if todos := piTodoSnapshot(e.Result); todos != "" {
			p.Tool = "todo"
			p.Args = todos
		}
	}
	// 编辑类工具(edit/replace/replace_match/insert/copy/move):结果 details.patch
	// 是标准 unified diff,挂到 Diff 上让前端渲染改动(write 无 patch,前端按
	// start 的 content 参数呈现)。pi 会把连续编辑折成 batch:只有末尾那条带
	// 覆盖整批的合并 patch,中间成员 patch 为空 —— 把空成员攒起来,末尾 patch
	// 到手时回填给它们,保证每张编辑卡都看得到改动。
	if !e.IsError && piEditTool(e.ToolName) {
		diff := piEditDiff(e.Result)
		inBatch, last := piEditBatch(e.Result)
		if diff != "" {
			p.Diff = diff
		}
		switch {
		case inBatch && !last:
			// 批中间成员:patch 还没来,先出卡(结果文本已是 queued 提示),
			// 攒进 piBatch 等末尾回填。
			d.emit(Event{Kind: KindToolCall, Payload: p})
			d.piBatch = append(d.piBatch, p)
			return
		case inBatch && last:
			// 批末尾:带整批合并 patch。先出末尾卡,再把合并 patch 回填给
			// 先前攒下的空成员卡(diff 为空则无可回填,仍清空缓冲避免串批)。
			d.emit(Event{Kind: KindToolCall, Payload: p})
			if diff != "" {
				for _, q := range d.piBatch {
					q.Diff = diff
					d.emit(Event{Kind: KindToolCall, Payload: q})
				}
			}
			d.piBatch = nil
			return
		}
	}
	// 前端按 ToolUseID 把 start 的卡与这条结果合并成同一张(对齐 codex)。
	d.emit(Event{Kind: KindToolCall, Payload: p})
}

// piEditTool 认带 details.patch 的编辑类工具(diff 来自结果而非入参)。
func piEditTool(name string) bool {
	switch name {
	case "edit", "replace", "replace_match", "insert", "copy", "move":
		return true
	}
	return false
}

// piEditDiff 从编辑类工具结果抠出标准 unified diff:优先 details.patch
// (标准 ---/+++/@@ 格式),没有再回落 details.diff。认不出返回空串。
func piEditDiff(raw json.RawMessage) string {
	if len(raw) == 0 || raw[0] != '{' {
		return ""
	}
	var o struct {
		Details struct {
			Patch string `json:"patch"`
			Diff  string `json:"diff"`
		} `json:"details"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return ""
	}
	if o.Details.Patch != "" {
		return truncate(o.Details.Patch, 64*1024)
	}
	return truncate(o.Details.Diff, 64*1024)
}

// piEditBatch 读结果里的 details.batch:inBatch=是否属于某个编辑批次,
// last=是否批末尾(带整批合并 patch 的那条)。无 batch 字段返回 (false,false)。
func piEditBatch(raw json.RawMessage) (inBatch, last bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return false, false
	}
	var o struct {
		Details struct {
			Batch *struct {
				Last bool `json:"last"`
			} `json:"batch"`
		} `json:"details"`
	}
	if json.Unmarshal(raw, &o) != nil || o.Details.Batch == nil {
		return false, false
	}
	return true, o.Details.Batch.Last
}

// piTodoSnapshot 从 todo 工具结果里抠出 details.tasks 全量快照,归一成前端
// TodoWrite 同款 {"todos":[{id,content,status}]}(跳过 deleted 墓碑)。认不出
// 返回空串。
func piTodoSnapshot(raw json.RawMessage) string {
	if len(raw) == 0 || raw[0] != '{' {
		return ""
	}
	var o struct {
		Details struct {
			Tasks []struct {
				ID      int    `json:"id"`
				Subject string `json:"subject"`
				Status  string `json:"status"`
			} `json:"tasks"`
		} `json:"details"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return ""
	}
	type todoItem struct {
		ID      int    `json:"id"`
		Content string `json:"content"`
		Status  string `json:"status"`
	}
	todos := make([]todoItem, 0, len(o.Details.Tasks))
	for _, t := range o.Details.Tasks {
		if t.Status == "deleted" {
			continue
		}
		todos = append(todos, todoItem{ID: t.ID, Content: t.Subject, Status: t.Status})
	}
	b, err := json.Marshal(map[string]any{"todos": todos})
	if err != nil {
		return ""
	}
	return string(b)
}

// piResultText tool result 文本化。pi 的结果形如
// {"content":[{"type":"text","text":"…"}],"details":{…}} —— 取 content 里的文本块,
// 跟 claude 的 tool_result 共用 toolResultText 归一。字符串结果直取;认不出的形态
// (比如只有 image 块、或根本不是这套结构)才回退整串 JSON。
func piResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// 字符串结果:直接就是文本。
	if raw[0] == '"' {
		var s string
		json.Unmarshal(raw, &s)
		return s
	}
	// 对象结果:钻进 content 数组。
	if raw[0] == '{' {
		var o struct {
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &o) == nil && len(o.Content) > 0 {
			if t := toolResultText(o.Content); t != "" {
				return t
			}
		}
		return string(raw)
	}
	// 直接就是块数组。
	if t := toolResultText(raw); t != "" {
		return t
	}
	return string(raw)
}

// parsePiApprove 认 gate 发来的 sentinel-select 标题:JSON
// {"__anyworkApprove":1,"tool":…,"input":…}。认出返回工具名与归一化入参。
func parsePiApprove(title string) (tool, args string, ok bool) {
	if !strings.Contains(title, "__anyworkApprove") {
		return "", "", false
	}
	var s struct {
		Flag  int             `json:"__anyworkApprove"`
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal([]byte(title), &s) != nil || s.Flag == 0 || s.Tool == "" {
		return "", "", false
	}
	if len(s.Input) > 0 {
		args = truncateJSON(string(s.Input), 16*1024)
	}
	return s.Tool, args, true
}

// handleExtensionUi 扩展交互。gate 发来的 sentinel-select 先被认出转成审批卡
// (见 parsePiApprove);其余 confirm/select/input/editor 归一成 ask_user 挂起
// 等 Answer,再回 extension_ui_response;notify/setTitle/setStatus/setWidget/
// set_editor_text 是 anywork 不暴露的 UI 面,忽略。
func (d *piDriver) handleExtensionUi(raw []byte) {
	var e struct {
		ID          string   `json:"id"`
		Method      string   `json:"method"`
		Title       string   `json:"title"`
		Message     string   `json:"message"`
		Options     []string `json:"options"`
		Placeholder string   `json:"placeholder"`
		Prefill     string   `json:"prefill"`
	}
	if json.Unmarshal(raw, &e) != nil || e.ID == "" {
		return
	}
	// gate 的 sentinel-select:转成审批卡挂起等 Resolve,不走 ask_user。
	if e.Method == "select" {
		if tool, args, ok := parsePiApprove(e.Title); ok {
			d.mu.Lock()
			if d.stdin == nil {
				d.mu.Unlock()
				return
			}
			// accept 模式:直接代答放行(allow-once,不记进 gate 的本会话集合,
			// 切回 ask 后仍会重新询问),不出卡。
			if d.autoAllow {
				d.mu.Unlock()
				d.sendExtResponse(e.ID, map[string]any{"value": piGateAllowOnce})
				return
			}
			d.approvals[e.ID] = piApproval{tool: tool, args: args}
			d.mu.Unlock()
			d.emit(Event{Kind: KindPermissionReq, Payload: &PermissionReqPayload{
				ReqID: e.ID, Tool: tool, Args: args,
			}})
			return
		}
	}
	var q AskQuestion
	switch e.Method {
	case "confirm":
		q = AskQuestion{
			ID:       e.ID,
			Header:   e.Title,
			Question: firstNonEmpty(e.Message, e.Title),
			Required: true,
			Options:  []AskOption{{Label: "确认"}, {Label: "取消"}},
		}
	case "select":
		opts := make([]AskOption, 0, len(e.Options))
		for _, o := range e.Options {
			if strings.TrimSpace(o) == "" {
				continue
			}
			opts = append(opts, AskOption{Label: o})
		}
		if len(opts) == 0 {
			d.sendExtResponse(e.ID, map[string]any{"cancelled": true})
			return
		}
		q = AskQuestion{ID: e.ID, Header: e.Title, Question: e.Title, Required: true, Options: opts}
	case "input":
		q = AskQuestion{ID: e.ID, Header: e.Title, Question: e.Title, Required: true, Placeholder: e.Placeholder}
	case "editor":
		q = AskQuestion{ID: e.ID, Header: e.Title, Question: e.Title, Required: true, Placeholder: e.Prefill}
	default:
		return
	}

	ch := make(chan *piAskDecision, 1)
	d.mu.Lock()
	if d.stdin == nil {
		d.mu.Unlock()
		return
	}
	// 同一 reqID 又来一条:取消旧的(pi 要求挂起对话 id 唯一)。
	if old := d.asks[e.ID]; old != nil {
		select {
		case old <- nil:
		default:
		}
	}
	d.asks[e.ID] = ch
	d.mu.Unlock()

	d.emit(Event{Kind: KindAskUser, Payload: &AskUserPayload{ReqID: e.ID, Questions: []AskQuestion{q}}})

	go func(method, reqID string) {
		dec := <-ch
		d.mu.Lock()
		delete(d.asks, reqID)
		d.mu.Unlock()
		if dec == nil {
			d.sendExtResponse(reqID, map[string]any{"cancelled": true})
			return
		}
		answer := ""
		if sel := dec.answers[reqID]; len(sel) > 0 {
			answer = sel[0]
		}
		if method == "confirm" {
			d.sendExtResponse(reqID, map[string]any{"confirmed": answer == "确认"})
			return
		}
		if answer == "" {
			d.sendExtResponse(reqID, map[string]any{"cancelled": true})
			return
		}
		d.sendExtResponse(reqID, map[string]any{"value": answer})
	}(e.Method, e.ID)
}

// sendExtResponse 回一条 extension_ui_response。
func (d *piDriver) sendExtResponse(id string, fields map[string]any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stdin == nil {
		return
	}
	msg := map[string]any{"type": "extension_ui_response", "id": id}
	for k, v := range fields {
		msg[k] = v
	}
	d.writeLocked(msg)
}
