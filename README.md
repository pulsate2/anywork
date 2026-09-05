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

## 安全

- 单密码认证,登录限速(5 次/分钟),内存 HMAC 签名 Cookie。
- 根目录允许 `/`,**持密码者 = 完全控制此目录边界内机器**。
- 公网访问务必通过 HTTPS 反向代理(Caddy/nginx)并设强密码。
- 可选 `--readonly`、IP 白名单(待实现)。

## 里程碑进度

- [x] **骨架**:Go server + 单密码鉴权 + Vue3/NaiveUI 壳 + PWA(Dockerfile + SQLite 接入)
- [x] **终端**:PTY + 多窗口 + 移动键盘层 + 服务端环形缓冲持久化
- [x] **文件管理**:列表/读写/上传下载/搜索/压缩解压
- [x] **Git**:状态/diff/提交/推送/分支/提交树/stash/worktree
- [x] **AI 配置档案**:预设 + 克隆 + 导入导出 + 当前档案徽标 + 新终端注入 env
- [x] **备份**:WebDAV 数据层 + cron 调度 + tar.gz 流式快照 + 启动自动恢复 + 排除 + 轮转保留
- [x] **打磨**:系统面板(CPU/内存/Swap/磁盘)、只读模式、**Web Push 通知**(VAPID + RFC 8291 aes128gcm 加密 + 终端命令完成推送)
- [ ] **待办**:IP 白名单、更细粒度的性能与安全加固
