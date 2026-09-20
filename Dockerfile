# ---------------------------------------------------------------------------
# 3328 单容器镜像：Go 后端 + Vite 前端静态产物，一个二进制一个端口。
#   - Go 二进制（CGO=0 静态编译）跑 API + 机器人，启动时自带 DB 迁移
#   - web/dist 由 Go 的 ServeStatic 托管（DY_WEB_DIR）
# 相比旧 Next.js 镜像（1.18GB），本镜像约 50MB。
#
# 本机原生构建（在盒子上）:
#   docker build --platform linux/arm64 -t douyin-data-manager:arm64 .
# GitHub Actions（ubuntu-24.04-arm 原生 runner）自动构建 → artifact tar → 盒子 docker load
# ---------------------------------------------------------------------------

FROM golang:1.27-alpine AS build-go
WORKDIR /src
ENV GOTOOLCHAIN=auto \
    CGO_ENABLED=0
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ .
RUN go build -trimpath -ldflags "-s -w" -o /out/douyin-server ./cmd/server

FROM node:22-alpine AS build-web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ .
RUN npm run build

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata curl
WORKDIR /app
COPY --from=build-go /out/douyin-server ./douyin-server
COPY --from=build-web /web/dist ./web
ENV DY_WEB_DIR=/app/web \
    DY_ADDR=:3000 \
    TZ=Asia/Shanghai
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD curl -fsS "http://127.0.0.1:3000/healthz" || exit 1
CMD ["./douyin-server"]
