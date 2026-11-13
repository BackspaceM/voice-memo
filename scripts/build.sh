#!/usr/bin/env bash
# 本地构建单架构镜像。ARCH=amd64|arm64 可指定目标平台（需 BuildKit）。
set -euo pipefail
cd "$(dirname "$0")/.."

NS="${DOCKERHUB_USER:-liuyunshan613}"
TAG="${TAG:-0.2.1}"
IMAGE="${NS}/voice-memo"

command -v docker >/dev/null 2>&1 || { echo "错误：未找到 docker，请先安装 Docker Desktop / Docker Engine" >&2; exit 1; }

args=()
if [[ -n "${ARCH:-}" ]]; then
  args+=(--platform "linux/${ARCH}")
fi

echo "==> 构建 ${IMAGE}:${TAG}"
docker build "${args[@]}" -t "${IMAGE}:${TAG}" -t "${IMAGE}:latest" .
echo "==> 完成"
echo "    运行: docker run -d --name voice-memo -p 8787:21087 -v voice-memo-data:/data ${IMAGE}:${TAG}"
