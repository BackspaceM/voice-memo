# 语音备忘录 · Voice Memo Suite

装在绿联 NAS（或任意 Docker 主机）上的私人语音备忘录：录音、回放、语音转文字、AI 摘要与标签、全文检索、文件夹 / 标签 / 收藏 / 提醒、单条或批量导出。数据全部保存在自己的 `/data` 卷里。

- **镜像**：`backspacem/voice-memo:0.2.1`（`:latest` 同步更新），Public，支持 `linux/amd64` + `linux/arm64`
- **端口**：容器内 `21087`，映射到主机的端口可自定义（下文用 `8787`）
- **依赖**：零运行时依赖，静态二进制 + `scratch` 镜像，不需要数据库
- **配置**：网页里就能改，写完立即生效并保存，不用改编排文件、不用 SSH

---

## 1. 快速开始

### 一行命令

```bash
docker run -d --name voice-memo \
  -p 8787:21087 \
  -v voice-memo-data:/data \
  -e TZ=Asia/Shanghai \
  -e PUID=0 -e PGID=0 \
  --restart unless-stopped \
  backspacem/voice-memo:0.2.1
```

### 或用 docker compose

把下面内容存成 `docker-compose.yml`，然后 `docker compose up -d`：

```yaml
services:
  voice-memo:
    image: backspacem/voice-memo:0.2.1
    container_name: voice-memo
    restart: unless-stopped
    ports:
      - "8787:21087"
    volumes:
      - ./data:/data          # 也可改成绿联共享目录的绝对路径
    environment:
      TZ: Asia/Shanghai
      PUID: "0"              # 目录权限报错时改成 NAS 用户的 UID
      PGID: "0"              # 绿联常见为 1000 / 10
      UMASK: "022"
      MAX_AUDIO_MB: "0"      # 0 = 单条音频不限制
      RECORD_FORMAT: wav
      DOWNLOAD_FORMAT: wav
```

### 绿联 NAS 图形界面

「应用中心 → Docker → 镜像 → 拉取」填 `backspacem/voice-memo:0.2.1`；创建容器时端口填 `8787 → 21087`，卷把 `/data` 指到一个共享目录，环境变量按上面填写。

打开 `http://<NAS IP>:8787` 即可使用。

> **升级**：改镜像 tag 后必须「重新创建容器」（`docker compose up -d --force-recreate` 或在面板里重新创建），只点「重启」不会换镜像。**保留 `/data` 卷，数据不会丢。**

---

## 2. 能做什么

| 功能 | 说明 |
|---|---|
| 录音 | 浏览器直接录；「手机录音」调起手机系统录音机；「导入音频」上传录音笔 / 已有文件 |
| 回放 | 网页播放器，支持拖动进度、倍速；WAV 格式全设备通用 |
| 转文字 | 接 ASR 服务自动转写，也可手动录入 / 修改文本 |
| AI 整理 | 一键生成摘要与标签，支持 OpenAI 兼容的国内外多家服务 |
| 检索 | 标题 / 转写全文 / 摘要 / 标签加权搜索 |
| 组织 | 文件夹、标签、收藏、今天、待提醒；提醒在页面打开时轮询提示 |
| 导出 | 单条「下载音频」「转 WAV 下载」；批量导出 ZIP（含 `memo.md`、`manifest.json`） |
| 设置 | 网页设置中心，AI、格式、上限等即时生效并落盘 |

---

## 3. 手机 / 平板使用

- **竖屏顶部操作栏常驻**：开始录音、导入、手机录音、设置、视图切换、文件夹 / 标签筛选都在，不随列表滚动，录完多少条都在。
- **右下角还有一个悬浮录音键**，即使页面布局异常也不会找不到入口；录音中会变红，再点一次就是停止。
- **「手机录音」是手机上最省事的方式**：点它调起系统录音机，录完自动上传成一条备忘录。它不走浏览器麦克风接口，因此纯 HTTP、自签 HTTPS、App 内嵌浏览器、iOS WebView 都能用。
- 系统录音机产出的 M4A / 3GP / AMR / CAF / AIFF / WAV / MP3 / WebM / OGG / FLAC / AAC 等都能保存和下载；个别格式（如 AMR）桌面浏览器不能直接网页播放，用「转 WAV 下载」即可。

若想在网页里**直接点「开始录音」**，见下一节的 HTTPS 说明——这是浏览器的安全限制，不是套件的故障。

---

## 4. 录音格式与下载

默认 **WAV**（16-bit PCM，带完整时长信息），Windows / macOS / iOS / Android / 车载都能直接播放，约 5.8 MB/分钟 @48kHz 单声道。设置页「录音格式」可改：

| 值 | 说明 |
|---|---|
| `wav`（默认） | 全平台通用，体积较大 |
| `webm` | Opus，体积约 1/10，Chrome / Edge / Firefox 原生 |
| `mp4` | AAC / M4A，苹果设备友好 |
| `ogg` | Opus，Firefox |
| `auto` | 不转换，保留浏览器原始容器 |

选项会按当前浏览器实际支持情况自动过滤。WAV 由浏览器转码生成，预估超过约 400MB 时自动回退原始格式并提示。

**旧版本「录完第一次播放没声音」已修复**：原因是浏览器生成的 WebM/M4A 缺少时长元数据，首次播放算不出长度。现在默认录成 WAV，并对已有录音用 `preload="auto"` + 强制计算时长处理，第一次播放就有声音。

下载：详情面板有「下载音频」和「转 WAV 下载」；设置里选「WAV（自动转码）」会把非 WAV 音频转成 WAV 再下载。批量 ZIP 按原始格式打包，文件名带正确扩展名。

---

## 5. 麦克风、HTTP 与 HTTPS

**结论：只导入 / 用手机系统录音机录，纯 HTTP 完全够用；想在网页里直接按「开始录音」，必须是「证书被设备信任」的 HTTPS 或 localhost。**

浏览器规定 `getUserMedia` 只在安全上下文开放，服务端无法绕过。特别注意 **HTTPS ≠ 一定可信**：自签证书没导入设备信任库时，即使点了「高级 → 继续前往」，浏览器仍会隐藏麦克风接口——这与浏览器版本无关，最新版 Edge / Chrome 也一样。可选路径：

### A. 什么都不改（纯 HTTP）
用「导入音频」或「手机录音」。转写、AI 摘要、检索、分类、提醒、导出全部正常。

### B. 桌面浏览器把该地址标记为可信（仍是 HTTP）
Chrome / Edge 必须用**独立的 user-data-dir** 启动，否则参数不生效：

```bash
# macOS
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
  --unsafely-treat-insecure-origin-as-secure=http://10.0.0.5:8787 \
  --user-data-dir=/tmp/voice-memo-chrome

# Windows（追加到快捷方式「目标」后面）
chrome.exe --unsafely-treat-insecure-origin-as-secure=http://10.0.0.5:8787 --user-data-dir=%TEMP%\voice-memo-chrome
```

Firefox 在 `about:config` 打开 `media.devices.insecure.enabled` 与 `media.getusermedia.insecure.enabled`。设置页「连接与安全」会自动生成并一键复制这条命令，还会显示当前麦克风接口的实测状态。

### C. 内置 HTTPS（不依赖域名 / 反向代理）
在 compose 的 `environment` 里加：

```yaml
ENABLE_HTTPS: 1
TLS_SAN: 10.0.0.5,nas.local   # 把实际会用到的 IP / 域名都填上，逗号分隔
```

重新创建容器后访问 `https://10.0.0.5:8787`，**地址必须与 `TLS_SAN` 中某一项完全一致**。服务会在 `/data/tls/` 生成自签 CA 和服务器证书。

⚠️ **只点「继续前往」不够**，必须把 CA 装进设备信任库，浏览器才开放麦克风：

| 平台 | 操作 |
|---|---|
| Windows | 下载 .crt → 双击 → 安装证书 → 本地计算机 → 受信任的根证书颁发机构 → 完全关闭并重启浏览器 |
| macOS | 双击 .crt 导入钥匙串 → 找到 `Voice Memo Suite Local CA` → 信任设为「始终信任」→ 重启浏览器 |
| Android | 下载 .crt → 设置 → 安全 → 加密与凭据 → 从存储设备安装 → CA 证书 → 重开页面 |
| iOS / iPadOS | 用 **Safari** 下载 `.mobileconfig` → 设置 → 通用 → VPN 与设备管理 → 安装描述文件 → 再到 设置 → 通用 → 关于本机 → **证书信任设置** 开启完全信任 → 重启 Safari |

证书和描述文件的下载入口在页面诊断条与「设置 → 连接与安全」，也可直接访问 `https://<地址>/api/ca.crt` 和 `/api/ca.mobileconfig`。装好后地址栏不应再有证书告警，设置页会显示「麦克风接口：可用」。

也可以自带正式证书（`TLS_CERT_FILE` / `TLS_KEY_FILE` 指向容器内 PEM）；用 NAS 自带反向代理时，把 HTTPS 站点反代到 `http://127.0.0.1:21087` 即可，nginx 建议加 `client_max_body_size 0; proxy_request_buffering off;`。反代侧的证书同样必须被设备信任。

---

## 6. AI 服务：不止 OpenAI

设置页内置预设，**转写（ASR）和摘要（LLM）可以分别选不同厂商**，端点、模型名都能手填：

| 类型 | 预设 |
|---|---|
| 摘要 / 标签 | OpenAI、DeepSeek、通义千问、硅基流动、Kimi、智谱 GLM、豆包 / 火山方舟、腾讯混元、百度千帆、MiniMax、阶跃星辰、Groq、Ollama 本地、自定义 |
| 语音转写 | OpenAI Whisper、硅基流动 SenseVoiceSmall、Groq Whisper、本地 / 自建 Whisper 服务、自定义 |

- 协议统一为 OpenAI 兼容的 `/chat/completions` 与 `/audio/transcriptions`；任何提供兼容端点的服务（含中转、本地 LLM）在「自定义」里填 Base URL 即可。
- 火山方舟的模型名填推理接入点 ID（`ep-…`）；Ollama 填 NAS 可达地址，密钥可留空。
- 国内厂商若不接受 `language` 字段，把「识别语言」改成 `auto`，后端会跳过该字段。
- 每个服务都有「测试连接」；不填密钥时转写 / 摘要会降级为手动录入，其余功能不受影响。

---

## 7. 网页设置中心

左侧栏（手机竖屏在顶部操作栏）的「⚙ 设置」，所有改动**立即生效并写入 `/data/settings.json`**，重启容器不丢失。

| 分区 | 可改内容 |
|---|---|
| AI 服务 | 服务商预设、Base URL、模型名、API 密钥、对话路径、识别语言、测试连接 |
| 录音与下载 | 新录音保存格式、下载默认格式 |
| 上限与运行 | 单条音频 / 导出 / 条数上限（0 = 不限）；只读展示 PUID、PGID、UMASK、数据目录、HTTPS 状态 |
| 连接与安全 | 安全上下文 / 麦克风接口 / MediaRecorder 实测状态、证书 SAN、CA 与 iOS 描述文件下载、分平台安装步骤 |

优先级：**网页设置 > 容器环境变量 > 厂商预设 > 默认值**。密钥只存服务端，读取时从不回传明文（只显示「是否已设置 + 尾号」）；留空表示不修改，点「清除密钥」才删除。

---

## 8. 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `PUID` / `PGID` / `UMASK` | `0` / `0` / `022` | 运行身份。非 0 时自动 `chown /data` 并降权；兼容面板的 `UID` / `GID` |
| `MAX_AUDIO_MB` / `MAX_EXPORT_MB` / `MAX_EXPORT_ITEMS` | `0` | `0` = 不限制，上传与导出均为流式写盘 |
| `RECORD_FORMAT` / `DOWNLOAD_FORMAT` | `wav` / `wav` | 录音格式、下载默认格式 |
| `ENABLE_HTTPS` / `TLS_SAN` / `HTTPS_REDIRECT_PORT` | `0` / `localhost` / `0` | 内置 HTTPS；SAN 必须包含实际访问地址，装好 CA 后麦克风才可用 |
| `TLS_CERT_FILE` / `TLS_KEY_FILE` | 空 | 自带正式证书（容器内 PEM 路径），设置后不再生成自签 CA |
| `AI_API_KEY` / `AI_BASE_URL` | 空 | 通用密钥与端点 |
| `ASR_API_KEY` / `ASR_BASE_URL` / `ASR_MODEL` / `ASR_LANGUAGE` | 空 / 空 / `whisper-1` / `zh` | 语音转写 |
| `LLM_API_KEY` / `LLM_BASE_URL` / `LLM_MODEL` | 空 / 空 / `gpt-4o-mini` | 摘要与标签 |

`PUID/PGID/UMASK/ENABLE_HTTPS/TLS_*` 在进程启动时读取，**需要重新创建容器**；其余都能在设置页即时修改。

---

## 9. 绿联 NAS 部署要点

- 镜像默认 **root** 运行：绿联共享目录常为 `1000:10 / drwxrwx---`，非 root 的 uid 10001 会报 `mkdir /data/audio: permission denied`。想让文件归 NAS 用户，把 `PUID=1000`、`PGID=10` 填进编排文件并重新创建容器（已实测可用）。
- 用共享目录做 bind mount 时，先确认目录属主与 `PUID/PGID` 一致，或保持 `0`。
- 容器是 `scratch`，没有 shell / curl；健康检查由程序自带，排障看 `docker logs voice-memo`。
- 提醒靠页面打开时 30 秒轮询，不是系统级推送通知。

---

## 10. 数据、备份与已知限制

- 数据全在 `/data`：`memos.json`、`audio/`、`settings.json`、`tls/`（启用 HTTPS 时）。用界面「导出 ZIP」或整目录打包备份。
- **删除容器时若一并删除了 volume，数据会一起没**；重要内容先导出。
- `settings.json` 含 AI 密钥明文（权限 0600），请保管好数据卷，不要提交到公开仓库。
- 录音需要麦克风授权；转写 / 摘要走外部 API 时会把音频 / 文本发送给对应厂商。
- 网页内直接录音始终受浏览器安全上下文限制；自签 HTTPS 必须安装 CA，最通用的替代方案是「手机录音」调起系统录音机后导入。
- 浏览器端 WAV 转码受内存限制，超长录音（预计 >400MB）会自动回退原始格式。

---

## 更新记录

- **0.2.1**：手机竖屏新增常驻操作栏 + 悬浮录音键，录完任何条数后都能继续录音；精确区分「HTTP / 证书未信任 / 浏览器不支持」三类录音诊断，新增 iOS CA 描述文件与分平台安装引导；新增「手机录音」系统录音机兜底，兼容手机录音常见格式。
- **0.2.0**：修复首次播放无声、默认 WAV、网页设置中心、格式自定义、取消单条音频上限、国内 AI 服务兼容、内置 HTTPS。
- **0.1.1**：修复绿联 NAS 目录权限、支持 PUID/PGID/UMASK。
