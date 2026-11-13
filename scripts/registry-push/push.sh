#!/usr/bin/env bash
# 无需 Docker 守护进程：交叉编译 -> 构造 OCI/Docker 镜像 -> 经 Registry v2 API 推送。
# 凭据只从 DOCKERHUB_TOKEN 环境变量读取，绝不写入文件、不打印、不出现在进程参数中。
set -euo pipefail

TOOL_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$TOOL_DIR/../.." && pwd)"
TAG="${TAG:-0.2.1}"
REPO="${DOCKERHUB_REPO:-liuyunshan613/voice-memo}"
LOGIN_USER="${DOCKERHUB_USERNAME:-liuyunshan613}"
PROXY="${DOCKERHUB_PROXY:-}"
GO_BIN="${GO_BIN:-go}"
DRY_RUN="${DRY_RUN:-0}"
[[ "${1:-}" == "--dry-run" ]] && DRY_RUN=1

command -v "$GO_BIN" >/dev/null 2>&1 || { echo "错误：找不到 Go 工具链，请用 GO_BIN=... 指定" >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "错误：需要 python3" >&2; exit 1; }

CURL=(curl -sS --noproxy '' --max-time 300)
[[ -n "$PROXY" ]] && CURL+=(-x "$PROXY")
REG="https://registry-1.docker.io"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin" "$WORK/image"

echo "==> 交叉编译后端（linux/amd64 + linux/arm64）"
for arch in amd64 arm64; do
  ( cd "$ROOT/backend" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
      "$GO_BIN" build -trimpath -ldflags="-s -w" -o "$WORK/bin/voice-memo-$arch" . )
done

echo "==> 构造镜像层 / config / manifest / 多架构 index"
python3 "$TOOL_DIR/make-image.py" --root "$ROOT" --out "$WORK/image" --tag "$TAG" \
  --amd64 "$WORK/bin/voice-memo-amd64" --arm64 "$WORK/bin/voice-memo-arm64"

if [[ "$DRY_RUN" == "1" ]]; then
  echo "==> DRY-RUN：校验产物结构（不联网、不上传）"
  python3 - "$WORK/image" <<'PY'
import hashlib, json, pathlib, sys, tarfile, gzip
out = pathlib.Path(sys.argv[1])
rows = [l.split("\t") for l in (out / "uploads.tsv").read_text().strip().splitlines()]
assert len(rows) == 7, f"应有 7 个上传对象，实际 {len(rows)}"
for kind, dg, path, ctype, ref in rows:
    data = pathlib.Path(path).read_bytes()
    assert "sha256:" + hashlib.sha256(data).hexdigest() == dg, f"digest 不匹配: {path}"
idx = json.loads((out / "index.json").read_bytes())
assert idx["mediaType"].endswith("manifest.list.v2+json")
assert [m["platform"]["architecture"] for m in idx["manifests"]] == ["amd64", "arm64"]
for arch in ("amd64", "arm64"):
    cfg = json.loads((out / f"config-{arch}.json").read_bytes())
    assert cfg["architecture"] == arch and cfg["os"] == "linux"
    assert cfg["config"].get("User", "") in ("", "0", "0:0"), "镜像应默认以 root 运行"
    env = cfg["config"]["Env"]
    for key in ("PUID=0", "PGID=0", "UMASK=022", "MAX_AUDIO_MB=0", "MAX_EXPORT_MB=0", "MAX_EXPORT_ITEMS=0"):
        assert key in env, "缺少环境变量 " + key
    assert cfg["config"]["Entrypoint"] == ["/app/voice-memo"]
    assert cfg["config"]["Healthcheck"]["Test"] == ["CMD", "/app/voice-memo", "--health"]
    with tarfile.open(out / f"layer-{arch}.tar.gz", "r:gz") as t:
        names = set(t.getnames())
        assert {"app/voice-memo", "app/www/index.html", "app/www/app.js", "app/www/styles.css",
                "etc/ssl/certs/ca-certificates.crt", "data"} <= names, names
        binary = t.extractfile("app/voice-memo").read()
        assert binary[:4] == b"\x7fELF", "二进制不是 ELF"
        machine = int.from_bytes(binary[18:20], "little")
        assert machine == (0x3E if arch == "amd64" else 0xB7), f"{arch} 架构不匹配"
        di = t.getmember("data")
        assert di.uid == 0 and di.isdir(), "数据目录应存在且属主为 root"
        assert t.getmember("tmp").mode == 0o1777, "临时目录应为 1777"
    assert "sha256:" + hashlib.sha256(gzip.decompress((out / f"layer-{arch}.tar.gz").read_bytes())).hexdigest() == cfg["rootfs"]["diff_ids"][0]
print("DRY-RUN 通过：7 个对象 digest 自洽、双架构 ELF 正确、默认 root、权限/上限环境变量齐备、tmp=1777")
PY
  exit 0
fi

: "${DOCKERHUB_TOKEN:?请通过环境变量 DOCKERHUB_TOKEN 提供 Access Token}"
[[ -n "$PROXY" ]] || { echo "提示：未指定 DOCKERHUB_PROXY，将使用系统默认代理" >&2; }

echo "==> 认证 Docker Hub（${LOGIN_USER} -> ${REPO}）"
AUTH="$(printf '%s:%s' "$LOGIN_USER" "$DOCKERHUB_TOKEN" | base64 | tr -d '\n')"
RESP="$("${CURL[@]}" -H "Authorization: Basic $AUTH" \
  "https://auth.docker.io/token?service=registry.docker.io&scope=repository:${REPO}:pull,push")"
BEARER="$(printf '%s' "$RESP" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get("token") or d.get("access_token") or "")' 2>/dev/null || true)"
[[ -n "$BEARER" ]] || { echo "认证失败：用户名或 Token 无效（响应未包含 token）" >&2; exit 1; }
unset AUTH RESP
echo "    认证成功，已取得 push 权限令牌"

upload_blob() {
  local file="$1" dg="$2" code hdr loc sep
  code="$("${CURL[@]}" -o /dev/null -w '%{http_code}' -I \
    -H "Authorization: Bearer $BEARER" "$REG/v2/$REPO/blobs/$dg")" || code="000"
  if [[ "$code" == "200" ]]; then echo "    blob 已存在 ${dg:0:19}…"; return 0; fi
  hdr="$("${CURL[@]}" -D - -o /dev/null -X POST \
    -H "Authorization: Bearer $BEARER" "$REG/v2/$REPO/blobs/uploads/")"
  loc="$(printf '%s' "$hdr" | tr -d '\r' | awk 'tolower($1)=="location:" {print $2}' | tail -1)"
  [[ -n "$loc" ]] || { echo "    无法取得上传 URL (POST 返回异常)" >&2; return 1; }
  case "$loc" in http*) ;; *) loc="$REG$loc" ;; esac
  sep='?'; [[ "$loc" == *'?'* ]] && sep='&'
  code="$("${CURL[@]}" -o /dev/null -w '%{http_code}' -X PUT \
    -H "Authorization: Bearer $BEARER" -H 'Content-Type: application/octet-stream' \
    --data-binary "@$file" "${loc}${sep}digest=${dg}")"
  case "$code" in
    201|202) echo "    blob 上传完成 ${dg:0:19}… (HTTP $code)" ;;
    *) echo "    blob 上传失败 $dg (HTTP $code)" >&2; return 1 ;;
  esac
}

put_manifest() {
  local file="$1" ctype="$2" ref="$3"
  "${CURL[@]}" -o /dev/null -w '%{http_code}' -X PUT \
    -H "Authorization: Bearer $BEARER" -H "Content-Type: $ctype" \
    --data-binary "@$file" "$REG/v2/$REPO/manifests/$ref"
}

echo "==> 上传 blob 与 manifest"
while IFS=$'\t' read -r kind dg path ctype ref; do
  [[ -z "$kind" ]] && continue
  case "$kind" in
    blob) upload_blob "$path" "$dg" ;;
    manifest|index)
      code="$(put_manifest "$path" "$ctype" "$ref")"
      case "$code" in
        201|202) echo "    $kind -> $ref (HTTP $code)" ;;
        *) echo "    $kind 上传失败 $ref (HTTP $code)" >&2; exit 1 ;;
      esac ;;
  esac
done < "$WORK/image/uploads.tsv"

echo "==> 同步 latest 标签"
code="$(put_manifest "$WORK/image/index.json" \
  'application/vnd.docker.distribution.manifest.list.v2+json' latest)"
[[ "$code" == "201" || "$code" == "202" ]] || { echo "latest 上传失败 (HTTP $code)" >&2; exit 1; }

echo "==> 远端校验"
ACCEPT='application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json'
BODY="$("${CURL[@]}" -H "Authorization: Bearer $BEARER" -H "Accept: $ACCEPT" \
  "$REG/v2/$REPO/manifests/$TAG")"
printf '%s' "$BODY" | python3 -c '
import sys, json
d = json.load(sys.stdin)
ms = d.get("manifests") or []
print("    mediaType:", d.get("mediaType"))
for m in ms:
    p = m["platform"]
    print("    -", p["os"] + "/" + p["architecture"], m["digest"][:19] + "…")
assert len(ms) == 2, "双架构 manifest 不完整"
assert {m["platform"]["architecture"] for m in ms} == {"amd64", "arm64"}
'
latest_code="$("${CURL[@]}" -o /dev/null -w '%{http_code}' -I \
  -H "Authorization: Bearer $BEARER" -H "Accept: $ACCEPT" \
  "$REG/v2/$REPO/manifests/latest")"
echo "    latest: HTTP $latest_code"

HUB="$("${CURL[@]}" --max-time 20 "https://hub.docker.com/v2/repositories/$REPO/" 2>/dev/null || true)"
printf '%s' "$HUB" | python3 -c '
import sys, json
try:
    d = json.load(sys.stdin)
    print("    Hub 仓库:", d.get("name"), "| private =", d.get("is_private"))
    if d.get("is_private"):
        print("    提示：仓库为私有，如需公开请在 Docker Hub 网页 Settings 中改为 Public")
except Exception:
    print("    Hub 仓库信息暂不可用（新建仓库可能有同步延迟）")
' 2>/dev/null || true

echo "==> 完成：docker pull ${REPO}:${TAG}"
