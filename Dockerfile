# syntax=docker/dockerfile:1

# ---- 阶段 1:构建前端 ----
FROM node:20-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build
# 产物在 web/../cmd/server/dist,即 /web/../cmd/server/dist = /cmd/server/dist
# 因此把 dist 显式复制到一个干净位置,供阶段 2 使用。
RUN mkdir -p /dist && cp -r /web/../cmd/server/dist/* /dist/

# ---- 阶段 2:构建后端(embed 前端) ----
FROM golang:1.24-alpine AS go
WORKDIR /src
# 先用私有镜像源缓存模块(构建机无外网时的优化)
ENV GOPROXY=https://goproxy.cn,direct
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# dist 在阶段 1 已生成;此处把前端产物放回 cmd/server/dist 供 embed。
RUN mkdir -p cmd/server/dist && cp -r /dist/* cmd/server/dist/
# 纯静态编译,无 CGO。
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /lightremote ./cmd/server

# ---- 阶段 3:运行镜像 ----
FROM alpine:3.20
RUN apk add --no-cache git bash ca-certificates tzdata \
 && adduser -D -u 1000 app
# 复制二进制(带 embed 前端)
COPY --from=go /lightremote /usr/local/bin/lightremote
USER app
# 数据目录:SQLite、会话密钥
VOLUME ["/data"]
EXPOSE 8080
ENV LIGHTREMOTE_DATA_DIR=/data \
    LIGHTREMOTE_BIND=0.0.0.0 \
    LIGHTREMOTE_PORT=8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:8080/api/health >/dev/null 2>&1 || exit 1
ENTRYPOINT ["lightremote"]
