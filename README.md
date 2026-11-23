# LightRemote

超轻量远程工作台:以终端为核心,文件/Git/AI 配置为辅,手机适配、低内存。设计见 [DESIGN.md](DESIGN.md)。

## 技术栈

- **后端** Go 1.24(单二进制,embed 前端;运行时无 Node)
- **前端** Vue 3 + Naive UI + Vite(SPA 静态打包)
- **存储** SQLite(默认,纯 Go 无 CGO);设 `DATABASE_URL` 切远程 PostgreSQL

## 快速开始

### 本地开发

```bash
# 终端 1:起后端(密码走环境变量)
LIGHTREMOTE_PASSWORD=secret go run ./cmd/server

# 终端 2:起前端 dev server(代理到 :8080)
cd web && npm install && npm run dev
# 打开 http://localhost:5173
```

### 直接跑二进制

```bash
LIGHTREMOTE_PASSWORD=secret go build -o lightremote ./cmd/server
./lightremote --port 8080
```

### Termux(Android ARM64)

GitHub Release 提供 `lightremote-<版本>-termux-arm64.tar.gz`,适用于 `uname -m` 输出为 `aarch64` 的 Termux。请使用这个包,而不是 `linux-arm64` 包。

在 Termux 中下载并将安装包放到 `$HOME` 后:

```bash
pkg install bash git
cd "$HOME"
VERSION=v1.0.0 # 改成下载的版本号
tar -xzf "lightremote-${VERSION}-termux-arm64.tar.gz"
cd "lightremote-${VERSION}-termux-arm64"
chmod +x lightremote
SHELL="$PREFIX/bin/bash" LIGHTREMOTE_PASSWORD='请换成强密码' \
  ./lightremote --root "$HOME" --port 8080
# 浏览器打开 http://127.0.0.1:8080
```

二进制已内嵌前端,运行时不需要 Go 或 Node。请在 Termux 私有目录(如 `$HOME`)运行,不要直接从共享存储(`/sdcard`)执行。目前仅提供 ARM64 版本;非 root Android 的系统监控和终端资源限制受系统权限约束。

Android 10+ 部分 Termux 版本会限制直接执行应用目录内的程序。Android 构建统一为终端、Agent(Claude/Codex/Pi)和 Git 准备启动命令:动态 ELF 经 `/system/bin/linker64` 加载;npm 脚本的 `#!/usr/bin/env node` 会解析为 Termux 的 Node 解释器。兼容准备先于 Agent 的 stdio 管道创建,不依赖失败后重试已经关闭的管道。不会修改可执行文件权限,也不会把 Linux/glibc 的 Agent 二进制当作 Android 程序启动。

Termux 版启用 CGO 并链接 Android bionic,使用系统 DNS(含系统 Private DNS/VPN 配置),不要设置 `GODEBUG=netdns=go` 强制退回纯 Go DNS。HTTPS 保持证书校验,自动读取 Termux CA 包和 Android 系统证书(含 Android 14+ 的 Conscrypt APEX);用户已设置的 `SSL_CERT_FILE`、`SSL_CERT_DIR`、`NODE_EXTRA_CA_CERTS` 不会被覆盖。请保留 Termux 的 `PREFIX`、`PATH`、`LD_PRELOAD` 和所需的代理环境变量。需要代理时,在启动服务前设置 `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`;各 Agent 是否支持代理及哪些变量仍由 Agent 自身决定。

### Docker

```bash
docker build -t lightremote .
docker run -d \
  --name lightremote \
  -p 8080:8080 \
  -e LIGHTREMOTE_PASSWORD=secret \
  -v lightremote-data:/data \
  lightremote
```

> 容器内的根目录 = 容器内的 `/`。要管理宿主机文件,挂载目录并设 `--root`:
> `-v /:/host -e LIGHTREMOTE_ROOT=/host`
> 或直接在宿主机跑二进制。

## 配置

工作区页提供固定的 **Home** 文件浏览入口;新建终端的目录选项始终保留 **Home**,与自定义工作区无关。Home 指服务端用户的主目录,受 `--root` 边界约束,越界时显示但不可选。

Git 的“克隆远程仓库”直接克隆到**当前选中的工作区根目录**(`git clone <url> .`),不跟随文件页浏览的子目录,也不额外新建仓库名子目录。克隆窗口显示完整目标路径;打开后切换工作区必须重新确认。目标目录必须为空,非空时拒绝操作,不会覆盖已有文件或自动改用子目录。“初始化 Git 仓库”则是在工作区根目录创建空仓库的 `.git` 元数据,不是克隆远程仓库。

全部支持 CLI flag 或环境变量(flag 优先):

| Flag | 环境变量 | 默认 | 说明 |
|---|---|---|---|
| `--bind` | `LIGHTREMOTE_BIND` | `127.0.0.1` | 监听地址 |
| `--port` | `LIGHTREMOTE_PORT` | `8080` | 监听端口 |
| `--root` | `LIGHTREMOTE_ROOT` | 当前盘根目录(Unix `/`,Windows 如 `D:\`) | 根目录,允许 `/` |
| `--password` | `LIGHTREMOTE_PASSWORD` | — | 登录密码(**必填**) |
| `--data-dir` | `LIGHTREMOTE_DATA_DIR` | `~/.config/lightremote` | 数据目录(SQLite/密钥) |
| `--database-url` | `DATABASE_URL` | — | 远程 PostgreSQL;空则 SQLite |
| `--readonly` | `LIGHTREMOTE_READONLY` | `false` | 只读模式 |
| `--session-ttl` | `LIGHTREMOTE_SESSION_TTL` | `24` | 会话有效期(小时) |
| `--vapid-private` | `LIGHTREMOTE_VAPID_PRIVATE` | — | Web Push VAPID 私钥;空则用 `data-dir/vapid.key` |
| `--vapid-subject` | `LIGHTREMOTE_VAPID_SUBJECT` | `mailto:admin@localhost` | VAPID subject |
| `--push-idle` | `LIGHTREMOTE_PUSH_IDLE` | `10` | 终端安静秒数后发"命令完成"推送;`0` 禁用 |

## Agent 图片与文件

Agent 的 `view_image`/`viewimage` 调用显示图片缩略图,点击可放大,工具详情和子代理过程也能预览。Codex 正文中的 `sandbox:`、`file:` 和本地文件链接会转为登录后可访问的下载地址,相对路径以该会话工作区为基准;Markdown 中的本地图片也可预览。文件必须真实存在,读取受 `--root` 或当前会话附件目录限制,不允许路径/符号链接越界,HTML 等非图片文件只下载,不在应用同源中执行。

## AI 配置转换

AI 配置卡片的“转换”可生成 Claude Code、Codex 或 Pi 配置草稿,迁移接口地址、API Key 和模型。Pi 可选 Responses、Chat Completions 或 Anthropic Messages。CC 转 Responses 会在末尾补 `/v1`(不重复添加),反向转 CC 去掉末尾 `/v1`。

生成后可编辑再保存为新配置,原配置保持不变;保存不自动生效,需另点“设为当前”。OAuth 登录态和客户端专属参数不跨客户端复制。转换仅改变配置格式,供应商仍需支持目标协议,专用端点与模型名请自行确认。

## 安全

- 单密码认证,登录限速(5 次/分钟),内存 HMAC 签名 Cookie。
- 根目录允许 `/`,**持密码者 = 完全控制此目录边界内机器**。
- 公网访问务必通过 HTTPS 反向代理(Caddy/nginx)并设强密码。
- 可选 `--readonly`、IP 白名单(待实现)。

## 里程碑进度

- [x] **骨架**:Go server + 单密码鉴权 + Vue3/NaiveUI 壳 + PWA(Dockerfile + SQLite 接入)
- [x] **终端**:PTY + 多窗口 + 移动键盘层(可显隐)+ 终端全屏(返回键 / 长按 Esc 退出)+ 服务端环形缓冲持久化
- [x] **文件管理**:列表/读写/上传下载/搜索/压缩解压
- [x] **Git**:初始化仓库(非仓库时空状态里一键 `git init`)/状态/diff/提交/推送/分支/提交树/stash/worktree + 提交身份弹窗(只写当前仓库)
- [x] **AI 配置档案**:预设 + 克隆 + 导入导出 + 当前档案徽标 + 新终端注入 env
- [x] **备份**:WebDAV 数据层 + cron 调度 + tar.gz 流式快照 + 启动自动恢复 + 排除 + 轮转保留
- [x] **打磨**:系统面板(CPU/内存/Swap/磁盘 + 进程表结束进程)、只读模式、**Web Push 通知**(VAPID + RFC 8291 aes128gcm 加密 + 终端命令完成推送)
- [ ] **待办**:IP 白名单、更细粒度的性能与安全加固
