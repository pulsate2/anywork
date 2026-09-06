# LightRemote — Agent 会话远控(Agent Chat)设计文档

> 状态:**设计定稿,未编码**
> 日期:2026-09-06
> 上游文档:DESIGN.md(总体架构以本文档为准的增量)

---

## 1. 背景与定位

现状:人在手机上遥控服务器的唯一结构化途径是终端,`claude`/`codex` 只能在 PTY 里手敲——TUI 在手机上难以操作,权限确认看不清,长输出难回溯,离座时没有任何通知。

目标:像 HAPI(/root/hapi)一样,把 Claude Code / Codex 会话变成**结构化聊天**:消息双向流、工具调用渲染成卡片、权限请求变成审批卡片 + Web Push、会话可恢复。人不在终端前也能推进 agent 工作。

**定位边界**:
- 本功能是"工作台新增一个视图",不是把 anywork 改造成 HAPI。终端仍然保留,而且是逃生舱(agent 会话出任何问题,attach 终端手动接管)。
- 只做 Claude Code / Codex 两个 agent。HAPI 的 ACP 后端(Cursor/Grok/Copilot 等)不做——每个后端都是一份协议适配成本,先用两个主流 agent 验证价值。

---

## 2. 参考实现结论(来自 /root/hapi 源码分析)

HAPI 驱动两个 agent **都不用 SDK、不走 HTTP API,而是 spawn 官方 CLI 子进程 + 进程级 JSON 协议**:

| | Claude Code | Codex |
|---|---|---|
| 驱动方式 | `claude --output-format stream-json --input-format stream-json --permission-prompt-tool stdio`,stdio 双向 JSON 行流 | `codex app-server`,stdio 上的 JSON-RPC |
| 消息注入 | 用户消息 JSON 写 stdin | `turn/start` 请求 |
| 权限回传 | stdout 发 `can_use_tool` control_request → 决定以 `control_response` 写回 stdin | `exec_approval_request` 通知 → 回 RPC 响应 |
| 会话恢复 | `--resume <id>` / `--continue` | `thread/resume` |
| 打断 | stdin 发 `interrupt` control_request | `turn/steer` / abort |

HAPI 架构之所以复杂(CLI ↔ Socket.IO ↔ Hub ↔ SSE ↔ 手机),是因为 **agent CLI 跑在开发机、控制端在公网**。anywork 的 Go 进程本身就跑在目标机上、就持有 WebSocket,所以整层中转塌缩掉:

| HAPI 需要 | anywork 对应 |
|---|---|
| Socket.IO hub + 离线消息队列 + afterSeq 回补 | 直连 WS;消息本来就在本机 SQLite,重连重查即可 |
| JWT / CLI_API_TOKEN 双轨认证 | 已有单密码 cookie 鉴权直接覆盖 |
| tunwg 隧道 | 已有反代 + PWA 方案 |
| 推送通道(FCM/APNs/…) | 已有 Web Push(`internal/push`) |
| runner 守护进程 | 不需要,Go 进程自己管理子进程 |

---

## 3. 架构

```
┌────────────── 浏览器 / 手机 PWA ──────────────┐
│  AgentView:会话列表 + 聊天时间线 + 审批卡片      │
└──────────────────┬───────────────────────────┘
                   │ REST 操作 + WS 推送
┌──────────────────▼───────────────────────────┐
│  internal/agent/                              │
│  ┌─────────────────────────────────────────┐ │
│  │ Manager(会话生命周期 + 订阅广播)        │ │
│  │  sessionID → Session{Driver, state}     │ │
│  ├─────────────────────────────────────────┤ │
│  │ claudeDriver   spawn claude stream-json  │ │
│  │ codexDriver    spawn codex app-server    │ │
│  │      ↓ 统一事件模型(两种 flavor 归一)     │ │
│  ├─────────────────────────────────────────┤ │
│  │ approvals: map[reqID]chan → WS/REST 决定  │ │
│  └────────────────┬────────────────────────┘ │
│                   │ 消息落库 + 事件广播         │
│        internal/db(agent_sessions/messages)   │
│        internal/push(权限请求/回合完成推送)     │
└──────────────────┬───────────────────────────┘
                   ▼
          claude / codex 子进程(cwd = 工作区)
```

---

## 4. 协议细节(定稿)

### 4.1 Claude Code(stream-json)

启动参数(参照 hapi `cli/src/claude/sdk/query.ts` 的成熟组合):

```
claude --output-format stream-json --verbose --input-format stream-json \
       --permission-prompt-tool stdio \
       [--resume <sessionID>] [--continue] \
       [--append-system-prompt <s>]
```

- stdio 全 pipe;stdout 逐行 `JSON.parse`,消息类型:`system`(init,含 session_id)/`assistant`(content 块:text / tool_use / thinking)/`user`(tool_result)/`result`(回合结束,含 cost、duration)。
- 用户消息:`{"type":"user","content":"…"}` + `\n` 写 stdin。
- 权限:子进程 stdout 发 `{"type":"control_request","subtype":"can_use_tool",…}` → 挂起等审批 → `{"type":"control_response","subtype":"success","upgraded":false}` 写回 stdin。
- 打断:stdin 发 `{"type":"control_request","subtype":"interrupt"}`。
- env 注入:`DISABLE_AUTOUPDATER=1`;**剥离 `CLAUDE_CODE_ENTRYPOINT`**(不剥的话 hapi 会话不能被原生 `claude --resume` 看到)。

### 4.2 Codex(app-server JSON-RPC,Phase 2)

- `codex app-server` 子进程,行分隔 JSON-RPC over stdio。
- 方法:`initialize` / `thread/start` / `thread/resume` / `turn/start` / `turn/steer`;通知:`agent_message` / `agent_reasoning_delta` / `exec_approval_request` / `patch_apply_*` / `mcp_tool_call_*` / `turn_diff` / `task_complete`。
- 权限模式映射为 `approvalPolicy` + `sandbox`(参照 hapi `appServerConfig.ts` 的映射表)。
- **风险与降级**:app-server 是 codex 自家 VS Code 插件在用的内部协议,无正式文档,版本间可能变。driver 初始化握手失败即标记该会话"协议不兼容",前端提示降级为 PTY 模式(见 4.4)。

### 4.3 统一事件模型(两种 flavor 归一后对 WS 只有一种)

```jsonc
// 一次事件 = 一条可落库的消息
{ "kind": "user" | "assistant_text" | "reasoning" | "tool_call"
      | "permission_request" | "permission_result" | "status" | "error",
  "seq": 123,                       // 会话内单调递增,重连回放的游标
  "payload": { … } }
```

`tool_call.payload = { tool, args(截断), result(截断), state: running|ok|error }`;
`permission_request.payload = { tool, args, reqID }`。

### 4.4 PTY 降级(保留,不做主路线)

复用现有终端设施把 agent 跑在 PTY 里 + `PreToolUse` hook 桥接审批,是协议不兼容时的兜底,不是第一优先级。**理由**:旁路抓取 + hook 注入比直连协议更绕,体验上限低。

---

## 5. 后端设计(internal/agent/)

### 5.1 目录

```
internal/agent/
├── manager.go      # Session 管理:生命周期、订阅广播、pending approvals
├── session.go      # 一个会话:Driver 实例 + 消息管道 + pending approvals
├── driver.go       # Driver 接口定义
├── claude.go       # claudeDriver:spawn + stream-json 解析
├── codex.go        # codexDriver:spawn + JSON-RPC(Phase 2)
├── handler.go      # WS 推送 handler(单向广播,无操作语义)
└── store.go        # agent_sessions / agent_messages 仓储
```

### 5.2 Driver 接口

```go
type Driver interface {
    Start(opts StartOpts) error          // spawn;opts: App/Cwd/Resume/PermissionMode/Env
    Send(text string) error              // 注入用户消息
    Interrupt() error                    // 打断当前回合
    Resolve(reqID string, allow bool) error  // 审批决定回传
    Events() <-chan Event                // 统一事件流(4.3),driver 内部完成归一
    Done() <-chan struct{}               // 进程退出
    ExitErr() error
    Close() error                        // kill 进程树(sysmon.Kill 同款护栏)
}
```

新增 driver = 新增一个文件 + `App` 常量注册,后续接 ACP 类 agent 时不动上层。

### 5.3 API 形态(REST 操作 + WS 单向推送,普通聊天风格)

不仿终端的 attach 模型。终端是"把键盘接进 PTY",操作和字节流必须同一条连接;聊天不是——**每个操作是一次独立请求,消息是推送**。这也是 hapi web 侧的形态(REST + SSE)。好处:弱网下每个操作独立重试,不存在"连接断了会话就失控"的耦合;审批推送通知点进来直接打 REST,不用先建立会话连接。

**REST(所有操作)**
```
GET    /api/agent/sessions                        # 列表:运行中 + 可恢复的历史
POST   /api/agent/sessions                        # 建会话 {app, workspaceID, resume?, permissionMode?}
GET    /api/agent/sessions/:id/messages           # 历史/增量:?afterSeq=&limit=(首次 afterSeq=0)
POST   /api/agent/sessions/:id/messages           # 发消息 {text};回合进行中 = 插话
POST   /api/agent/sessions/:id/interrupt          # 打断当前回合
POST   /api/agent/sessions/:id/approve            # 审批 {reqID, allow}
DELETE /api/agent/sessions/:id                    # 结束并归档
```

**WS `/api/agent`(仅服务端→客户端推送)**
```
客户端 → {type:"subscribe"|"unsubscribe", sessionID}   # 仅订阅管理
服务端 → {type:"message", sessionID, seq, kind, payload}
         {type:"state", sessionID, running|idle|dead}
         {type:"exit",  sessionID, code}
```

- 同一会话多个订阅者广播(多标签页/换设备)。
- **断线恢复不靠 WS 回放**:重连 → 重新 subscribe → `GET messages?afterSeq=本地最大seq` 补差。与 hapi 的 afterSeq 游标同思路,但库就在本机,不需要它的三层兜底。
- 消息全量落库,历史加载就是普通分页查询,不引入环形缓冲(见 5.4 边界)。

### 5.4 存储

```sql
CREATE TABLE agent_sessions (
  id            TEXT PRIMARY KEY,
  app           TEXT NOT NULL,              -- claude | codex
  workspace     TEXT NOT NULL,              -- cwd 绝对路径
  title         TEXT,                       -- 首条用户消息截断 / agent 可改
  external_id   TEXT,                       -- claude session_id / codex thread id
  permission_mode TEXT NOT NULL DEFAULT 'ask',
  status        TEXT NOT NULL,              -- running | idle | dead
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);

CREATE TABLE agent_messages (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id  TEXT NOT NULL,
  seq         INTEGER NOT NULL,             -- 会话内单调递增,回放游标
  kind        TEXT NOT NULL,
  payload     TEXT NOT NULL,                -- JSON
  created_at  TEXT NOT NULL,
  UNIQUE(session_id, seq)
);
```

边界(与 DESIGN.md 4.5 同一逻辑):
- **会话进程不落库**——重启后 Go 进程里的 session 必然没了,DB 里的会话标 `dead`,列表可一键「续聊」(`--resume external_id` 拉起新进程,对话连续性不丢)。
- `permission_request` 已决的也落库(审批历史可回看),pending 态只在内存——进程死了 pending 自然作废,不该假装它还能答复。

### 5.5 生命周期与进程管理

- 可执行文件解析:PATH 上的 `claude` / `codex`,支持 env `LR_CLAUDE_BIN` / `LR_CODEX_BIN` 覆盖;不存在时 create 回 409,前端提示先装 CLI(aiprofile 页已有两个 app 的概念,复用其 App 常量)。
- 进程退出:非正常退出码 → 会话标 `dead` + 落一条 `error` 消息;**不做自动重生**(hapi 的重生循环是为"远端一条消息触发 spawn"设计的,anywork 里用户就在界面上,点一下「续聊」比重生循环可预期)。
- kill 走进程树终止(杀 claude 不杀它起的 bash 等于没杀),pid 复用护栏与 sysmon.Kill 一致。
- cgroup 限流:复用 `terminal/limits_linux.go` 的 wrap 机制,agent 子进程与其 spawn 的工具进程一并入组——**限制值单独放宽**(agent 回合内存峰值远超普通 shell,沿用终端默认值会误杀)。
- AI 档案:会话继承当前生效档案(aiprofile 已把配置写进真实 `~/.claude`、`~/.codex`),driver 不额外注入供应商 env。已运行会话不受切换影响,新会话生效——与终端行为一致。

### 5.6 推送

复用 `internal/push` + IdleWatcher 模式,两个触发点:
- **permission_request** 到达且页面不可见 →「Claude 想执行 `npm install`,点开审批」;
- 回合结束(`result` / `task_complete`)→「回合完成」(取代现在只盯终端的命令完成推送,两者并存)。

### 5.7 会话清理(手动保底 + 可选自动,自动默认关闭)

会话消息库会持续膨胀,清理提供两种模式,**删除逻辑是同一个函数**:

**公共规则**
- **阈值**:N 天(默认 30),存 settings KV(`agent.cleanup.days`)。
- **范围**:`status = dead` 且 `updated_at < now − N天` 的会话。**运行中(idle/running)会话无论多旧都不删**——正在跑的回合不能因为过期被抽走地板。
- **级联**:删会话同时删其 `agent_messages`,返回 `{会话数, 消息数}`。
- **只清自己的库**:`external_id` 指向的原生转录(`~/.claude/projects/*.jsonl`、codex thread 文件)是 CLI 自己的地盘,anywork 不碰。副作用是「续聊」记录被清后无法再 resume,所有涉及清理的界面都明示。

**手动清理(始终可用)**
- 清理界面选天数 → dryRun 显示将删数量 → 确认执行。
- 不依赖自动开关,自动清关闭时这是唯一入口。

**自动清理(可选开关,默认关)**
- settings KV `agent.cleanup.auto`(默认 `false`)。**默认关闭**:会话是对话与审批历史,用户可能认为重要,未经同意定时删数据是错误默认。
- 开启后每日跑一次(复用 backup 的调度器,同一套 cron 设施),按当前阈值执行同一删除函数。
- 最近一次自动清理结果(时间/会话数/消息数)落 settings KV,清理界面展示——自动删了什么必须可追溯,不能静默。
- 开启开关的界面文案明示「将每天自动删除 N 天前已结束的会话,清理后无法再续聊」。

---

## 6. 前端设计

### 6.1 结构

```
web/src/views/AgentView.vue        # 会话列表 + 聊天(或拆 List/Chat 两视图)
web/src/api/agent.ts               # REST 封装 + WS 推送订阅(重连后 afterSeq 补差)
web/src/components/agent/
  ├── MessageList.vue              # 时间线(长会话虚拟滚动)
  ├── ToolCallCard.vue             # 工具调用折叠卡片(默认折叠,点开看 args/result)
  ├── ApprovalCard.vue             # 审批:工具名 + 命令/路径明示 + 批准/拒绝
  └── Composer.vue                 # 输入框 + 发送/打断切换(回合中发送 = 插话)
```

路由懒加载,markdown 渲染复用 markdown-it(注意既有坑:scoped CSS 不作用 v-html,hljs 令牌色放非 scoped 块)。

### 6.2 交互要点(移动优先)

- **普通聊天风格**:消息时间线(用户右侧/agent 左侧)、工具调用与审批都是卡片,不做任何终端隐喻——不渲染 ANSI、不嵌 xterm。终端是另一个视图的事,这里就是聊天软件。
- 底部导航从 4 Tab 扩到 5(终端/Agent/文件/Git/设置);`<768px` 单栏聊天视图,会话列表是抽屉。
- 审批卡片是**最高优先级元素**:出现在输入框正上方,不只埋在时间线里——手机上错过审批 = agent 干等。
- Composer 软键盘适配沿用软键盘双信号方案(遮挡量/是否弹出分开测)。
- 工具卡片内容截断展示(diff 只渲染前 N 行 + 展开按钮),防长输出把手机滚穿。
- 会话列表顶部「清理」入口:手动清理(选天数 → dryRun 显示「将清理 X 个已结束会话,清理后无法再续聊」→ 确认)+ 自动清理开关(默认关,开启文案见 5.7)+ 最近一次自动清理结果。

---

## 7. 安全

- 攻击面不变:审批批准 = 允许 agent 执行命令,而 anywork 本来就有终端 + 文件全权限,没有新增能力面。变化在于**门槛变低**(手机上点一下比开终端敲命令快),所以:
  - 权限模式默认 `ask`;审批卡片必须明示工具名 + 关键参数(命令行原文/文件路径),不允许只有「批准」两个字的裸按钮。
  - 「本回合不再询问 / 全部允许」类快捷键**不做**(hapi 有,但它是多用户产品形态;anywork 单用户,省一次点击不值得引入放行惯性)。
- 会话 cwd 锁定在 workspace 路径下(与终端同权限,不额外收紧也不放宽)。
- 消息 payload 落库前对 args/result 截断上限(如 64KB),防一次超大 tool result 撑爆 DB 与前端。

---

## 8. 分期里程碑

1. **Phase 1 — Claude Code**
   `internal/agent` 骨架 + claudeDriver + REST/WS 推送 API + 两张表 + 前端聊天视图(文本/工具卡片/审批卡片)+ permission_request 与回合完成推送 + 会话清理(5.7,手动 + 可选自动,删除逻辑就一个函数加个按钮/开关,放一期防止消息库从第一天起无人管)。← 本期做完即达到「手机遥控 Claude Code」
2. **Phase 2 — Codex**
   app-server JSON-RPC driver + 事件映射;初始化握手失败 → 会话降级为 PTY(4.4)。
3. **Phase 3 — 增强**
   AskUserQuestion 选项卡片、历史会话列表与一键续聊、多会话并行、worktree 隔离会话(复用 git worktree)、附件上传(复用 fs,`--add-dir` 指向 blob 目录)。

工作量预估:claudeDriver 中等;codexDriver 较大(协议映射表);WS/DB 管道小(照 terminal 模板);**最大头在前端聊天视图**。

---

## 9. API 概览(增量)

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/agent/sessions` | 会话列表(运行中 + 可恢复) |
| POST | `/api/agent/sessions` | 建会话(app/workspaceID/resume/permissionMode);CLI 不存在回 409 |
| GET | `/api/agent/sessions/:id/messages` | 历史/增量(`afterSeq`+`limit` 游标分页) |
| POST | `/api/agent/sessions/:id/messages` | 发消息;回合进行中 = 插话 |
| POST | `/api/agent/sessions/:id/interrupt` | 打断当前回合 |
| POST | `/api/agent/sessions/:id/approve` | 审批(REST 直达,推送通知点进来即可用) |
| DELETE | `/api/agent/sessions/:id` | 结束并归档 |
| WS | `/api/agent` | 推送通道(服务端单向,协议见 5.3) |
| POST | `/api/agent/cleanup` | 清理 N 天前的已结束会话(见 5.7);`{days, dryRun?}`;dryRun 只返回数量不删 |
