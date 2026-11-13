# 语音备忘录 · Docker 版（v0.2.0）

跑在绿联 NAS（或任意 Docker 主机）上的私人语音备忘录：浏览器录音、回放、语音转文字、AI 摘要/标签、全文检索、文件夹/标签/提醒、单条或批量导出。

**镜像**：`liuyunshan613/voice-memo:0.2.0` / `:latest`（`linux/amd64` + `linux/arm64`，Public）
**源码**：本目录，通过 GitHub Actions 自动构建推送
**依赖**：零运行时依赖（Go 静态二进制 + scratch 镜像），数据全部落在 `/data`

---

## 1. 快速开始

```bash
docker run -d --name voice-memo \
  -p 8787:21087 \
  -v voice-memo-data:/data \
  -e TZ=Asia/Shanghai \
  -e PUID=0 -e PGID=0 \
  --restart unless-stopped \
  liuyunshan613/voice-memo:0.2.0
```

或用仓库里的编排文件：

```bash
cp .env.example .env
docker compose up -d
```

打开 `http://<NAS IP>:8787`。绿联 NAS 也可在「应用中心 → Docker → 镜像 → 拉取」填 `liuyunshan613/voice-memo:0.2.0`，端口 `8787 → 21087`，卷 `/data` 指向共享目录。

> **升级注意**：从 0.1.x 升级必须「重新创建容器」（`docker compose up -d --force-recreate`），只点重启不会刷新镜像配置。

---

## 2. Web 设置中心（v0.2.0 新增）

左侧栏底部「⚙ 设置」打开设置面板，**所有配置即时生效并写入 `/data/settings.json`**，重启容器不丢失，不用再改 compose、不用 SSH。

| 分区 | 可改内容 |
|---|---|
| AI 服务 | LLM / ASR 服务商预设、Base URL、模型名、API 密钥、对话路径、识别语言；每个服务都有「测试连接」 |
| 录音与下载 | 新录音保存格式、下载默认格式 |
| 上限与运行 | 单条音频 / 导出 / 条数上限；只读展示 PUID、PGID、UMASK、数据目录、HTTPS 状态 |
| 连接与安全 | 当前协议与安全上下文、内置 HTTPS 状态、CA 证书下载、Chrome/Edge 启动参数 |

**优先级**：Web 设置 > 容器环境变量 > 厂商预设 > 内置默认值。
密钥只存服务端，`GET /api/settings` 从不回传明文，只返回「是否已设置 + 尾号提示」；留空表示不修改，点「清除密钥」才删除。

### 对应 API

```
GET  /api/settings       读取（密钥脱敏）
PUT  /api/settings       保存（密钥传 "__KEEP__" 不改，传 "" 清除）
POST /api/ai/test        {"target":"llm"|"asr"} 连通性探测
GET  /api/ca.crt         下载内置自签 CA（仅在启用内置 HTTPS 时可用）
```

---

## 3. 录音格式与「首次播放没声音」

浏览器 `MediaRecorder` 生成的 WebM/M4A 常缺 **EBML Duration 元数据**：第一次播放时浏览器算不出时长，表现为静音；播过一次后浏览器缓存了真实长度才正常。v0.2.0 从三处一起修：

1. 播放器 `<audio preload="auto">`，换 `src` 后显式调用 `load()`；
2. 检测到 `duration === Infinity` 时触发一次 seek 强制浏览器计算时长（对已有 WebM 录音也生效）；
3. **默认录音格式改为 WAV**：录完后用 Web Audio 解码、编码成带完整 RIFF 头的 16-bit PCM WAV，天生自带时长。

设置页「录音格式」可选：

| 值 | 说明 |
|---|---|
| `wav`（默认） | 全平台通用（Windows / macOS / iOS / Android / 车载），约 5.8 MB/分钟 @48kHz 单声道 |
| `webm` | Opus，体积小约 1/10，Chrome/Edge/Firefox 原生 |
| `mp4` | AAC / M4A，苹果设备友好 |
| `ogg` | Opus，Firefox |
| `auto` | 不转换，保留浏览器原始容器 |

选项按当前浏览器 `MediaRecorder.isTypeSupported()` 动态过滤。WAV 由浏览器转码生成，超过约 400MB 预估大小时自动回退到原始格式并提示。

**下载**：详情面板新增「下载音频」和「转 WAV 下载」。设置里选「WAV（自动转码）」时，点击下载会把非 WAV 音频解码重编码成 WAV；选「原始格式」则直接下载。批量导出的 ZIP 始终按原始格式打包，文件名带正确扩展名（`.wav/.webm/.m4a/.ogg/...`），并附 `memo.md` 与 `manifest.json`。

---

## 4. HTTP 还是 HTTPS？

**结论：不录音的话，纯 HTTP 完全够用；要在浏览器里点「开始录音」，必须有安全上下文。**

这是浏览器对 `getUserMedia` 的硬性策略 —— 只有 `https://` 或 `localhost` 才被视为安全上下文，服务端无法绕过。所以从手机/另一台电脑访问 `http://10.0.0.5:8787` 时，麦克风 API 会直接被浏览器移除。三条可选路径：

### A. 纯 HTTP（不改任何配置）
用「导入音频」上传手机、录音笔或电脑本地录好的文件。转写、AI 摘要、检索、分类、提醒、导出全部正常，只是不能在本页面直接按录音键。

### B. 桌面浏览器把该地址标记为可信（仍是 HTTP）
Chrome / Edge 用**独立的 user-data-dir** 启动（必须新目录，否则参数不生效）：

```bash
# macOS
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
  --unsafely-treat-insecure-origin-as-secure=http://10.0.0.5:8787 \
  --user-data-dir=/tmp/voice-memo-chrome

# Windows（把命令追加到快捷方式「目标」后面）
chrome.exe --unsafely-treat-insecure-origin-as-secure=http://10.0.0.5:8787 --user-data-dir=%TEMP%\voice-memo-chrome
```

Firefox 在 `about:config` 打开 `media.devices.insecure.enabled` 与 `media.getusermedia.insecure.enabled`。
设置页「连接与安全」会按当前访问地址自动生成并一键复制这条命令。

### C. 内置 HTTPS（推荐，无需外部域名/反代）
在 compose 的 `environment` 里加：

```yaml
ENABLE_HTTPS: 1
TLS_SAN: 10.0.0.5,nas.local   # 填 NAS 的 IP 与/或域名
```

重建容器后访问 `https://10.0.0.5:8787`。服务会自动在 `/data/tls/` 生成自签 CA + 服务器证书（SAN 含 localhost、127.0.0.1、主机名和 `TLS_SAN`）。首次打开会有证书告警，在「设置 → 连接与安全」下载 `voice-memo-ca.crt` 并导入系统信任库后，地址栏不再告警、麦克风可用。Android/iOS 也可安装该 CA 描述文件。

也可以自带正式证书：`TLS_CERT_FILE` / `TLS_KEY_FILE` 指向容器内 PEM 路径。设 `HTTPS_REDIRECT_PORT` 可另开一个 HTTP 端口做 301 跳转。

> 若使用 NAS 自带反向代理（绿联 UGLink 域名、nginx 等），把 HTTPS 站点反代到 `http://127.0.0.1:21087` 即可；nginx 需挂根路径并加 `client_max_body_size 0; proxy_request_buffering off;` 以配合大音频上传。

---

## 5. AI 服务：不止 OpenAI

设置页内置预设，**LLM 与 ASR 可分别选不同厂商**，端点、模型名都能手动改：

| 类型 | 预设 |
|---|---|
| 对话 / 摘要 | OpenAI、DeepSeek、通义千问（百炼兼容模式）、硅基流动、月之暗面 Kimi、智谱 GLM、火山方舟/豆包、腾讯混元、百度千帆、MiniMax、阶跃星辰、Groq、Ollama 本地、自定义 |
| 语音转写 | OpenAI Whisper、硅基流动 SenseVoiceSmall、Groq Whisper、本地/自建 Whisper 服务、自定义 |

要点：
- 协议统一为 OpenAI 兼容的 `/chat/completions` 与 `/audio/transcriptions`；只要厂商提供兼容端点（含各类中转、本地 LLM 服务），在「自定义」里填 Base URL 即可。
- 火山方舟的模型名填推理接入点 ID（`ep-…`）；Ollama 填 NAS 可达地址，密钥可留空。
- 国内厂商若不接受 `language` 字段，把「识别语言」改成 `auto`，后端会跳过该字段。
- 「测试连接」先试 `GET /models`，不支持时退回一条极简 chat 请求；转写端点不支持 `/models` 时会提示用一条录音实测。
- 不填密钥时转写/摘要自动降级为手动录入，其余功能不受影响。

---

## 6. 环境变量（都可被 Web 设置覆盖）

| 变量 | 默认 | 说明 |
|---|---|---|
| `PUID` / `PGID` / `UMASK` | `0` / `0` / `022` | 运行身份。非 0 时自动 `chown /data` 并降权；兼容面板的 `UID`/`GID` |
| `MAX_AUDIO_MB` / `MAX_EXPORT_MB` / `MAX_EXPORT_ITEMS` | `0` | `0` = 不限制。上传与导出均为流式写盘 |
| `RECORD_FORMAT` / `DOWNLOAD_FORMAT` | `wav` / `wav` | 录音格式、下载默认格式 |
| `ENABLE_HTTPS` / `TLS_SAN` / `HTTPS_REDIRECT_PORT` | `0` / `localhost` / `0` | 内置 HTTPS |
| `AI_API_KEY` / `AI_BASE_URL` | 空 | 通用密钥与端点 |
| `ASR_API_KEY` / `ASR_BASE_URL` / `ASR_MODEL` / `ASR_LANGUAGE` | 空 / 空 / `whisper-1` / `zh` | 转写 |
| `LLM_API_KEY` / `LLM_BASE_URL` / `LLM_MODEL` | 空 / 空 / `gpt-4o-mini` | 摘要与标签 |

`PUID/PGID/UMASK/ENABLE_HTTPS/TLS_*` 在进程启动时读取，**需重建容器**；其余都能在设置页即时改。

---

## 7. GitHub → Actions → Docker Hub

仓库已配置 `.github/workflows/docker-publish.yml`：

- 触发：`push` 到 `main`、打 `v*` tag、或在 Actions 页手动 `Run workflow`（可指定镜像 tag）。
- 步骤：Go 单元测试 → QEMU + buildx → 登录 Docker Hub → 构建 `linux/amd64` + `linux/arm64` → 推送指定 tag 与 `latest`。
- 需要仓库 Secrets：`DOCKERHUB_USERNAME`、`DOCKERHUB_TOKEN`（Docker Hub Access Token，不要填账户密码）。

```bash
# 本地开发校验
cd backend && go test ./...
bash -n scripts/registry-push/push.sh
docker compose config
docker compose up -d --build && curl -s localhost:8787/api/health
```

若本机没有 Docker daemon 但要直推 Registry，可用 `scripts/registry-push/push.sh`（Python 构造 OCI 层 + curl 调 Registry v2 API，支持 `--dry-run`），Token 只经环境变量传入。

---

## 8. 绿联 NAS 部署要点

- 镜像默认 **root** 运行：绿联共享目录常为 `1000:10 / drwxrwx---`，非 root 的 uid 10001 会 `mkdir /data/audio: permission denied`。想让文件归 `Backspace`，把 `PUID=1000`、`PGID=10` 填进 compose 并重建容器（已实测可用）。
- 用共享目录做 bind mount 时，先确认目录属主与 `PUID/PGID` 一致（或保持 `0`）。
- 容器是 `scratch`，没有 shell / wget / curl；健康检查由二进制自带 `--health` 完成，排障看 `docker logs voice-memo`。
- 提醒靠页面打开时 30 秒轮询，不是系统级推送。

## 9. 备份与已知限制

- 数据全在 `/data`：`memos.json`、`audio/`、`settings.json`、`tls/`（若启用 HTTPS）。用界面「导出 ZIP」或整目录打包备份。
- **卸载/删除容器时若删除了 volume，数据会一起没**；重要内容先导出。
- 浏览器端 WAV 转码受内存限制，超长录音（预计 >400MB WAV）会自动回退原始格式。
- `settings.json` 含 AI 密钥明文（权限 0600），请妥善保管数据卷，不要把它提交到公开仓库。
- 录音需要麦克风授权；ASR/LLM 走外部 API 时会把音频/文本发送给对应厂商。
