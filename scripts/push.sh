#!/usr/bin/env bash
# 登录 Docker Hub 并构建/推送 linux/amd64 + linux/arm64 多架构镜像。
# 凭据只经 stdin 传给 docker login，不写入任何文件、不出现在进程参数中。
# 强烈建议使用 Docker Hub Access Token 而不是账户密码。
set -euo pipefail
cd "$(dirname "$0")/.."

NS="${DOCKERHUB_USER:-backspacem}"
LOGIN_USER="${DOCKERHUB_USERNAME:-$NS}"
TAG="${TAG:-0.2.1}"
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"
IMAGE="${NS}/voice-memo"

command -v docker >/dev/null 2>&1 || { echo "错误：未找到 docker" >&2; exit 1; }

if [[ -n "${DOCKERHUB_TOKEN:-}" ]]; then
  TOKEN="$DOCKERHUB_TOKEN"
elif [[ -t 0 ]]; then
  read -r -s -p "Docker Hub Access Token（输入不回显）: " TOKEN
  echo
else
  echo "非交互环境请用环境变量提供令牌，例如：" >&2
  echo "  read -rs TOK && DOCKERHUB_TOKEN=\"\$TOK\" ./scripts/push.sh" >&2
  exit 1
fi

echo "==> 登录 Docker Hub（${LOGIN_USER}）"
printf '%s' "$TOKEN" | docker login -u "$LOGIN_USER" --password-stdin
unset TOKEN

echo "==> 准备 buildx 构建器"
if docker buildx inspect vms-builder >/dev/null 2>&1; then
  docker buildx use vms-builder
else
  docker buildx create --name vms-builder --use
fi
docker buildx inspect --bootstrap >/dev/null

echo "==> 构建并推送 ${IMAGE}:${TAG} 与 :latest（${PLATFORMS}）"
docker buildx build --platform "$PLATFORMS" --push \
  --tag "${IMAGE}:${TAG}" --tag "${IMAGE}:latest" .

echo "==> 校验远端 manifest"
docker buildx imagetools inspect "${IMAGE}:${TAG}"
docker logout >/dev/null 2>&1 || true
echo "==> 完成：docker pull ${IMAGE}:${TAG}"
