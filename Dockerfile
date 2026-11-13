# syntax=docker/dockerfile:1.7
# 多阶段构建：Go 静态二进制 + 原生前端 -> scratch 运行镜像（约 9MB，无 Node/Python/系统库）
# 默认以 root 运行：绿联 NAS 的共享目录常为 1000:10 / 770，非 root 用户无法创建 /data/audio。
ARG GO_VERSION=1.27

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS builder
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
COPY backend/go.mod ./
COPY backend/*.go ./
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/voice-memo . \
 && apk add --no-cache ca-certificates \
 && mkdir -p /out/data /out/tmp \
 && chmod 1777 /out/tmp \
 && : > /out/data/.keep

FROM scratch
LABEL org.opencontainers.image.title="Voice Memo Suite" \
      org.opencontainers.image.description="录音、回放、转写、摘要、检索、提醒与导出的语音备忘录" \
      org.opencontainers.image.version="0.2.0" \
      org.opencontainers.image.licenses="MIT"

# 时区数据已通过 _ "time/tzdata" 编进二进制；CA 供 ASR/LLM 的 HTTPS 调用
COPY --from=builder /out/voice-memo /app/voice-memo
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/data/ /data/
COPY --from=builder /out/tmp/ /tmp/
COPY www/ /app/www/

ENV PORT=21087 \
    HOST=0.0.0.0 \
    DATA_DIR=/data \
    PUBLIC_DIR=/app/www \
    TZ=Asia/Shanghai \
    PUID=0 \
    PGID=0 \
    UMASK=022 \
    MAX_AUDIO_MB=0 \
    MAX_EXPORT_MB=0 \
    MAX_EXPORT_ITEMS=0 \
    RECORD_FORMAT=wav \
    DOWNLOAD_FORMAT=wav \
    ENABLE_HTTPS=0 \
    TLS_SAN=localhost \
    HTTPS_REDIRECT_PORT=0

# 不设 USER：默认 root，保证能在任意 NAS 共享目录下创建 /data/audio。
# 需要非 root 时设置 PUID/PGID（兼容 UID/GID），程序会自动 chown 数据目录并降权。
VOLUME ["/data"]
EXPOSE 21087
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/app/voice-memo", "--health"]
ENTRYPOINT ["/app/voice-memo"]
