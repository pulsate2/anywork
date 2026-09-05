# LightRemote — 超轻量远程工作台 设计文档

> 状态:**设计定稿,未编码**
> 日期:2026-09-01

---

## 1. 项目定位

**不是** code-server / VS Code 的降级替代,而是以**终端为核心、文件/Git/AI 配置为辅**的"远程环境操控台"。

- 编辑只是快速改文件,**不做 LSP、调试、重构等重型编码能力**。
- 目标是**低内存、手机适配**;面向当前 vibecoding 工作流:人在手机上,远程服务器在跑 claude/codex/终端。
- 运行时**单进程**(Go),Node 只存在于构建期。

---

## 2. 总体架构

```
┌──────────────────────────── 手机 / 平板 / 桌面浏览器 ────────────────────────────┐
│  Vue 3 + Naive UI SPA (PWA 可安装)   xterm.js 终端   CodeMirror 6 编辑器          │
└──────────────────────────────────┬──────────────────────────────────────────────┘
                                    │ HTTPS / WebSocket
┌──────────────────────────────────▼──────────────────────────────────────────────┐
│                     Go 单二进制 (go:embed 托管前端静态资源)                        │
│  ┌──────────┐ ┌─────────────┐ ┌─────────┐ ┌──────────┐ ┌──────────┐ ┌────────┐ │
│  │ Auth     │ │ Terminal    │ │ FileSvc │ │ GitSvc   │ │ AIConfig │ │Backup  │ │
│  │ 鉴权/限速 │ │ PTY 会话管理 │ │ 文件操作 │ │ git CLI  │ │ 配置档案  │ │调度+快照│ │
│  └──────────┘ └─────────────┘ └─────────┘ └──────────┘ └──────────┘ └────────┘ │
│                              ┌──────────────┐                                   │
│                              │ internal/db/ │ database/sql + embed migrations   │
│                              │ SQLite / PG  │                                   │
│                              └──────────────┘                                   │
└──────────────────────────────────┬──────────────────────────────────────────────┘
                                    ▼
                      Shell / git / claude / codex / 文件系统 / WebDAV
```

---

## 3. 技术选型(定稿)

| 层 | 选型 | 说明 |
|---|---|---|
| 后端语言 | Go 1.22+ | 单二进制;运行时单进程 |
| HTTP 路由 | `chi` | 轻量,stdlib 兼容 |
| WebSocket | `coder/websocket` | 更轻;备选 gorilla |
| PTY | `creack/pty` | |
| 压缩/解压 | stdlib `archive/zip` `archive/tar` + `gzip` | 零第三方 |
| 数据库驱动 | `database/sql` + **`modernc.org/sqlite`**(默认)+ `pgx`(远程 PG) | SQLite 纯 Go 无 CGO |
| cron 调度 | `robfig/cron/v3` | 备份任务调度 |
| 排除规则 | `sabhiram/go-gitignore` | gitignore 风格匹配 |
| WebDAV 客户端 | `golang.org/x/net/webdav.Client` | 官方实现,零重量级依赖 |
| 系统信息 | 直接读 `/proc` | 不用 gopsutil(省内存) |
| 前端框架 | **Vue 3 + TypeScript + Vite** | SPA 静态打包,`go:embed` 进二进制 |
| UI 组件库 | **Naive UI** | 按需引入;表单/弹窗/树/列表直接用它 |
| 终端 | `xterm.js` + `fit` 插件 | 无替代 |
| 编辑器 | **CodeMirror 6** 最小构建 | 不用 Monaco(2~3MB 太重) |
| PWA | manifest + service worker | 添加到主屏幕 + 全屏 |
| 构建 | Vite → `go build -tags embed` | Node 仅在构建期镜像 |

### 成果目标

| 指标 | 目标 |
|---|---|
| 前端首屏 gz | < 600KB |
| 二进制体积 | < 25MB(embed 前端后) |
| 空闲 RSS | < 70MB |
| 运行时进程 | 1 个 |

---

## 4. 存储设计:数据库(不用 JSON 文件)

### 4.1 分层

```
启动参数(端口/root/密码/keep_alive) → CLI flags + 环境变量  ← 进程启动前就要,不进 DB
业务数据(工作区/AI档案/备份任务/快照/设置KV) → database/sql → SQLite(默认) | PostgreSQL(远程)
```

### 4.2 双后端策略

| | SQLite(默认) | PostgreSQL(可选) |
|---|---|---|
| 适用 | 单机、容器内、`/data` 卷持久化 | 数据不随容器销毁、多实例共享、已有 PG |
| 切换 | 默认 | 设 `DATABASE_URL=postgres://...` 自动切换 |
| 驱动 | `modernc.org/sqlite`(纯 Go,无 CGO) | `pgx` |
| 内存 | 极小,WAL 模式,单进程一写多读 | 远程才启用,额外连接开销 |

- **不用 GORM**(反射 + 连接池对单用户工具是负担,违背低内存原则)。所有 SQL 抽到 `internal/db/`,双后端共用。
- **不用 `mattn/go-sqlite3`**(需要 CGO,破坏静态单二进制)。选 `modernc.org/sqlite`(纯 Go 移植)。

### 4.3 表结构草案

```sql
-- 迁移文件 embed 进二进制,启动时自动建表/升级
CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL                      -- 当前 AI 档案、默认工作区、主题等 KV
);

CREATE TABLE workspaces (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  path        TEXT NOT NULL UNIQUE,        -- 绑定唯一路径,防同名
  favorite    INTEGER DEFAULT 0,
  sort_order  INTEGER DEFAULT 0,
  created_at  TEXT NOT NULL
);

CREATE TABLE ai_profiles (
  id                 TEXT PRIMARY KEY,
  name               TEXT NOT NULL UNIQUE,
  env_json           TEXT NOT NULL,        -- 注入的环境变量(API key/模型/base_url)
  claude_config_dir  TEXT,                 -- 档案自己的 CLAUDE_CONFIG_DIR 路径
  codex_home         TEXT,                 -- 档案自己的 CODEX_HOME 路径
  created_at         TEXT NOT NULL,
  updated_at         TEXT NOT NULL
);

-- 备份任务只存配置;快照不建表 —— 见 4.5,WebDAV 目录本身即索引
CREATE TABLE backup_jobs (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL,
  source_dir      TEXT NOT NULL,
  webdav_url      TEXT NOT NULL,
  webdav_user     TEXT,
  webdav_password TEXT,                    -- AES-GCM 加密,见 4.4
  schedule_cron   TEXT,                    -- NULL = 仅手动
  excludes_json   TEXT NOT NULL,           -- ["node_modules",".git","*.log"]
  retention       INTEGER DEFAULT 7,
  auto_restore    INTEGER DEFAULT 0,
  enabled         INTEGER DEFAULT 1,
  created_at      TEXT NOT NULL
  -- 运行状态(last_run/next_run/result)不落库,纯内存态,重启重算
);
```

### 4.4 凭据加密

- 主密钥:`LIGHTREMOTE_MASTER_KEY` 环境变量,或首次启动生成存 0600 文件。
- `webdav_password` 用 **AES-GCM + 主密钥** 加密后落库。
- 另提供"纯环境变量注入"选项(`BACKUP_<id>_PASSWORD`),完全不进 DB。

### 4.5 边界

- **终端会话不落库**(仍内存态)——PTY 本就无法跨重启存活,写了也是死数据。
- **快照不建表**:快照文件本体在远程 WebDAV,**WebDAV 目录即唯一事实来源**。每次操作实时 `PROPFIND` 列目录、按文件名时间戳排序即得完整快照列表,DB 无需也不该缓存这份数据(缓存只会在远程被外部修改时失真)。DB 只管任务配置(`backup_jobs`)。

---

## 5. 功能设计

### 5.0 工作区

一个"工作区" = 一个目录 + 名称 + 收藏。终端在这里打开、Git 针对这个仓库、文件管理器以此为根。移动端少敲路径。首次进入是工作区列表。

### 5.1 终端(核心难点在移动端)

**多窗口与持久化**
- 服务端 `SessionManager`:`sessionID → {PTY, 输出环形缓冲(2000~5000行), cwd, env, 创建时间}`。
- 滚动缓冲在**服务端**:断线/换设备重连,先回放历史再续实时流 = "内存持久化"。
- 客户端断开**进程不杀**(可配 `keep_alive`);窗口列表显示运行中会话,点击重连。
- 同一会话允许多客户端同时 attach(广播,类 tmux)。
- 服务重启后 PTY 必然存活不了,列表标记"已结束",可一键重建。

**WebSocket 协议(二进制帧)**
```
客户端 → {type:attach|create|input|resize|kill, sessionId, data, cols, rows}
服务端 → {type:output, data} | {type:sessionList, ...} | {type:exit, code} | {type:resized}
```

**移动键盘层**(手机虚拟键盘不送 Ctrl/Alt/Esc/Tab/方向键)
- 底部固定工具条:⌨️ 唤系统键盘、Esc、Tab、**Ctrl/Alt 粘滞键**、方向四键、退格、回车、符号切换。
- **Ctrl 粘滞**:点 Ctrl → 点 `c` → 发 `^C`;顶部有当前粘滞修饰符指示灯;长按 Alt。
- 顶部"粘贴"按钮:手机剪贴板 → 终端中转(远程读不到手机剪贴板)。
- 单指滚动滚回缓冲、双指/按钮缩放字体。
- 物理键盘走正常 keydown,全功能映射。

### 5.2 文件管理

REST API,全部**流式**,大文件不整读进内存:
- 列表:`GET /api/fs/list?path=`,目录优先排序。
- 读/写:读前探测二进制(NUL 字节),二进制禁止进编辑器;写用"临时文件 + rename"原子替换并保留权限。
- 上传:multipart 流式写临时文件后原子改名;二期加分块断点续传。
- 下载:流式 + `Range` 断点续传;多选 → 服务端实时 zip 流下载。
- 操作:新建/重命名/复制/移动/删除(可选回收站)。
- 属性:类型/大小/权限/属主/mtime/软链目标。
- 搜索:优先 `rg`,无则 `grep -rniE`,遵循 `.gitignore`,结果可点击直达,可取消。
- 压缩/解压:zip、tar.gz;**zip-slip 防护**(拒绝路径逃逸)。

### 5.3 Git(走 git CLI,不用 go-git)

- **状态**:`git status --porcelain=v1 -b`,分组(已暂存/未暂存/未跟踪)。
- **Diff**:`git diff` / `--cached` / `HEAD`,unified 原文 + 前端轻量着色。
- **提交/推送**:逐文件 add → 提交信息 → commit → push(复用系统凭据/ssh-agent)。
- **提交树**:`git log --graph --oneline --all` 解析成节点,移动端折叠式树。
- **worktree**:`add / list / remove`。
- 补充:分支切换/新建/删除、stash。

### 5.4 AI 配置档案(Claude / Codex 切换)

核心原则:**不污染现有 `~/.claude`、`~/.codex`**,用"配置目录覆盖"实现:
- 档案 `profiles/<name>/`:`env.json` + `claude/`(作为该档案 `CLAUDE_CONFIG_DIR`)+ `codex/`(作为 `CODEX_HOME`)。
- "切换" = 更新 active 记录;**新开终端会话继承当前档案 env**。
- 内置"直连 / 中转代理"预设;支持从当前 `~/.claude`、`~/.codex` **克隆成新档案**;支持导入/导出。
- UI 提示:顶栏显示当前生效档案徽标;终端标题栏显示当前会话配置。**已运行进程不受切换影响,新会话生效**(页面明确标注)。

### 5.5 WebDAV 目录备份

**语义**:本地目录 → 定时打包上传到远程 WebDAV(备份);启动时按配置从 WebDAV 拉回最新快照还原(恢复)。

**调度**
- `robfig/cron/v3`,标准 5 段 + 秒段;每 Job 一个 cron 项。
- 同一 Job 前一次未跑完则不启动下一次(互斥);全部 Job 共享全局并发闸门(默认同时 1 个在跑)。
- 调度状态(上次/下次运行、上次结果)挂 Job 上,WS 推前端。

**备份流程(流式,O(1) 内存)**
```
预扫描(进度%)→ 按 excludes 走目录 → tar.gz 边打边流式 PUT 到 WebDAV
                                          ↓ 失败
                   删半成品 → 指数退避重试(≤3 次)
```
- 一次**全量**快照,不做增量(简单可靠,契合超轻量)。
- 排除:gitignore 风格(目录名 / `*.log` / 后缀 `/`),用 `go-gitignore`,备份时即不打包依赖。
- WebDAV 目录布局(每个快照 = 一个 tar.gz + 一个**同名** .json 元数据,成对出现,文件名时间戳即版本号):
  ```
  <webdav_root>/<job_id>/
    backup-YYYYMMDD-HHMMSS.tar.gz     # 快照本体
    backup-YYYYMMDD-HHMMSS.json       # 元数据:source、excludes、mtime、字节数、sha256
  ```
- WebDAV 无原生可续传 PUT:失败删半成品整体重试(弱网才值得上分块,留二期)。

**启动自动恢复**
- 启动顺序:加载配置 → 启动 HTTP → **异步**执行 `autoRestoreOnStart` 任务(不阻塞监听)。
- 流程:`PROPFIND` 列远程目录按文件名时间戳取最新 → 流式 GET 到临时文件(校验 sha256,来自同名 .json)→ 解压覆盖到 `sourceDir`。
- **恢复是覆盖还原**,被 excludes 掉的文件本地不动 → 天然不破坏 node_modules。
- 用临时目录解压再整体挪,降低半目录风险。
- UI 上恢复是**危险操作必弹确认**;支持从快照历史任选一份(不只最新)。

**保留轮转(超上限删旧)**
- 每次成功备份后(以及手动触发时)做一次轮转检查:`PROPFIND` 列表 → 超过 `retention` 份 → 删除最旧的,`.tar.gz` + 同名 `.json` 一起删。
- 轮转失败不影响快照本体,记入 `lastError` 提示即可。

**UI(设置页新增「备份」Tab)**
- 任务列表(卡片式):名称/来源/下次运行/上次结果/开关 + 操作(编辑/立即备份/恢复/删除)。
- 新建/编辑表单:来源目录 / WebDAV URL 账号密码 / 调度预设(每小时·每日·每周 + 自定义 cron)/ 排除列表(带"常见排除"一键填充)/ 保留份数 / 启动自动恢复。
- 快照历史:实时 `PROPFIND` 远程目录渲染(无 DB 查询),时间/大小/下载/恢复/删除。
- 实时进度区:当前任务预扫描总量 → 已传字节。

**错误与安全**
- 网络失败退避重试 3 次 → `lastError` → WS 红点;不打断其他功能。
- config/DB 文件 0600;推荐环境变量注入凭据;文档明示"权限等同 SSH key"。
- 恢复前不自动清空目标目录,只覆盖(可预期、不误删)。
- 备份期间目录被编辑:软快照,不保证强一致(文档写明)。

### 5.6 系统信息面板(P2)

CPU/内存/Swap/磁盘/网络,直读 `/proc`,成本极低,手机上顺手看服务器状态。

### 5.7 其他补充功能(按价值)

| 优先级 | 功能 |
|---|---|
| P1 | 粘贴中转按钮(剪贴板→终端) |
| P1 | 文件书签/快速跳转 |
| P2 | 命令完成推送(Web Push) |
| P2 | 回收站 / 软链创建 |
| P3 | 图片/Markdown 预览 |
| P3 | WebDAV 服务端暴露(手机原生文件管理器直连)—— 与备份的 WebDAV 客户端是两个方向,可共存 |

---

## 6. 前端结构(Vue 3 + Naive UI)

```
web/
├── src/
│   ├── main.ts / App.vue          # Naive UI ConfigProvider + 深浅主题
│   ├── api/                       # REST + WS 封装(终端长连接单独一个 client)
│   ├── views/                     # Terminal / Files / Git / AIConfig / Backup / Settings
│   ├── mobile/                    # ★ 自研移动端壳(组件库覆盖不到的部分)
│   │   ├── BottomNav.vue          #   底部 4 Tab:终端/文件/Git/设置
│   │   ├── MobileActions.vue      #   右键 → 底部 Drawer 动作表
│   │   ├── TerminalKeyboard.vue   #   粘滞键 Ctrl/Alt + Esc/Tab/方向键 工具条
│   │   └── splitter.ts            #   分屏拖拽(平板横屏:左文件右终端)
│   ├── components/                # 极少数自定义(如虚拟滚动列表)
│   └── assets/tokens.css          # 设计令牌:颜色/圆角/间距/断点/触控尺寸
└── vite.config.ts                 # 按需打包,首屏 <600KB gz 预算
```

**Naive UI 是桌面优先,移动端适配策略**(唯一需要手写的地方):
1. 壳自研:底部 Tab、工作区选择器、顶部状态徽标(当前 AI 档案),约 300 行。
2. 交互改写:`<768px` 时右键 → `NDrawer`(底部弹出)+ `NList` 动作表;桌面用 `NDropdown`。同一套操作定义,两种呈现。
3. 表格降级:文件/Git 列表移动端 `NList` 卡片式,桌面 `NDataTable`,共用数据源。
4. 触控目标:ConfigProvider `theme-overrides` 统一放大 ≥44px;深浅主题 + 高对比也走这里。
5. 断点:`<768px` 单栏 + 底部 Tab;`≥768px` 侧栏 + Splitter 分屏。
6. 表单/弹窗/Toast/树/下拉/上传进度直接用 Naive UI。

**结论**:70% 的表单/弹窗/树用 Naive UI(观感统一),30% 的移动壳 + 终端键盘层自研(组件库覆盖不到,正是产品差异化)。手写量远小于纯自研方案。

---

## 7. 安全设计(根目录允许 `/`)

明确接受:**持密码者 = 完全控制这台机器**(终端 + 文件 + git 全权限)。

- **认证**:单密码 + 内存滑动窗口限速(5 次/分钟锁定);Cookie HttpOnly + SameSite=Strict,短期过期。
- **传输**:默认绑定 `127.0.0.1`;公网必须走 Caddy/nginx 反代 HTTPS(**内置强制提示**)。
- **路径**:归一化 + 防穿越;解压 zip-slip 防护照做。
- **可选开关**:`--readonly`(禁终端/禁写)、IP 白名单、`--session-timeout`。
- **部署边界**:裸二进制跑宿主机 = 管理整个 `/`;Docker 内 = 只能看到容器内的 `/`(除非挂载宿主机目录)——天然边界,文档写明。

---

## 8. Docker 部署

### 8.1 Dockerfile(多阶段)

```dockerfile
# --- 阶段1:构建前端 ---
FROM node:20-alpine AS web
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build                # 产物 web/dist

# --- 阶段2:构建后端(embed 前端) ---
FROM golang:1.22-alpine AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -tags embed -ldflags="-s -w" -o /lightremote ./cmd/server

# --- 阶段3:运行镜像 ---
FROM alpine:3.20
RUN apk add --no-cache git bash && adduser -D -u 1000 app
COPY --from=go /lightremote /usr/local/bin/lightremote
USER app
EXPOSE 8080
VOLUME ["/data"]                  # DB + 配置持久化
HEALTHCHECK CMD wget -qO- http://127.0.0.1:8080/api/health || exit 1
ENTRYPOINT ["lightremote"]
```

运行时只需要 `git`,不需要 node。

### 8.2 部署形态

- 裸二进制 + systemd:直接管理宿主机整个 `/`。
- Docker:默认 SQLite + `/data` 卷;**容器内备份 = 容器内目录,挂载宿主机目录才备份宿主机**(文档写明)。
- 远程 PG:设 `DATABASE_URL`,数据不随容器销毁。

---

## 9. 低内存策略汇总

- 单二进制 + embed 前端,无 Node 进程、无数据库服务进程(SQLite 是文件不是进程)。
- 全链路流式 I/O + 终端环形缓冲限长 + 分页列表,杜绝整文件/整目录进内存。
- 前端按需动态 import(xterm、CodeMirror 各自懒加载,首屏只加载核心壳)。
- 系统信息用 `/proc` 而非 gopsutil。
- `modernc.org/sqlite`(无 CGO,静态编译;SQLite WAL 本身几乎不占内存)。

---

## 10. 目录结构(草案)

```
/root/anywork
├── cmd/server/main.go          # 入口:flags → DB 迁移 → 各 Svc → HTTP 启动
├── internal/
│   ├── db/                     # database/sql + embed 迁移 + 各表 Repo
│   ├── auth/                   # 单密码登录、HMAC Cookie、限速
│   ├── terminal/               # SessionManager、PTY、环形缓冲、WS 协议
│   ├── fs/                     # 文件操作、流式上传下载、搜索、压缩
│   ├── git/                    # git CLI 封装、状态/diff/树解析
│   ├── ai/                     # 档案管理、env/CLAUDE_CONFIG_DIR/CODEX_HOME
│   ├── backup/                 # cron 调度、WebDAV 客户端、快照/恢复
│   └── sysinfo/                # /proc 读取
├── web/                        # Vue 3 + Naive UI + Vite
├── migrations/                 # *.sql 迁移(embed)
├── Dockerfile
├── go.mod / go.sum
└── DESIGN.md
```

---

## 11. API 概览

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/login` / `/api/logout` | 认证 |
| GET | `/api/health` | 健康检查 |
| GET/POST/PUT/DELETE | `/api/workspaces` | 工作区 CRUD |
| WS | `/api/term` | 终端长连接(协议见 5.1) |
| GET/POST/PUT/DELETE | `/api/fs/...` | 文件操作(流式) |
| GET | `/api/fs/search` | 搜索 |
| GET/POST | `/api/fs/archive` | 压缩/解压 |
| GET/POST | `/api/git/...` | 状态/diff/提交/推送/树/worktree |
| GET/POST/PUT/DELETE | `/api/ai/profiles` | AI 档案 CRUD、克隆、导入导出、active |
| GET/POST/PUT/DELETE | `/api/backup/jobs` | 备份任务 CRUD |
| POST | `/api/backup/jobs/:id/run` | 立即备份 |
| POST | `/api/backup/jobs/:id/restore` | 恢复(危险,确认) |
| GET/DELETE | `/api/backup/jobs/:id/snapshots` | 快照历史 / 删除(实时 PROPFIND 远程目录,无 DB 查询) |
| GET | `/api/sysinfo` | 系统信息 |
| WS | `/api/events` | 全局事件推送(调度状态、任务完成) |

---

## 12. 里程碑

1. **骨架**:Go server + 单密码鉴权 + Vue3/NaiveUI 壳 + PWA + Dockerfile + SQLite 接入
2. **终端**:PTY + 多窗口 + **移动键盘层** + 服务端环形缓冲持久化 ← 核心难点,先打透
3. **文件管理**:列表/读写/上传下载/搜索/压缩解压
4. **Git**:状态/diff/提交/推送/提交树/worktree
5. **AI 配置档案**:预设 + 克隆 + 导入导出 + 徽标提示
6. **备份**:数据层 + cron 调度 + tar.gz 流式快照 + 启动自动恢复 + 排除 + 快照历史/轮转
7. **打磨**:系统面板、Web Push 通知、性能与安全加固
