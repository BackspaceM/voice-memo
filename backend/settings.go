package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Provider 是 OpenAI 兼容端点的预设。所有国内厂商的兼容地址都可能随版本调整，
// 用户可在 Web 设置页里直接改 baseUrl / model，不必改代码或容器环境变量。
type Provider struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
	Model   string `json:"model"`
	Note    string `json:"note"`
}

var llmProviders = []Provider{
	{ID: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini", Note: "官方接口，需境外网络"},
	{ID: "deepseek", Name: "DeepSeek 深度求索", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat", Note: "国内直连，无语音转写"},
	{ID: "qwen", Name: "通义千问（阿里云百炼）", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "qwen-plus", Note: "OpenAI 兼容模式"},
	{ID: "siliconflow", Name: "硅基流动 SiliconFlow", BaseURL: "https://api.siliconflow.cn/v1", Model: "Qwen/Qwen2.5-7B-Instruct", Note: "同时提供语音转写模型"},
	{ID: "moonshot", Name: "月之暗面 Kimi", BaseURL: "https://api.moonshot.cn/v1", Model: "moonshot-v1-8k", Note: "国内直连"},
	{ID: "zhipu", Name: "智谱 GLM", BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-4-flash", Note: "有免费模型"},
	{ID: "volcengine", Name: "火山方舟 / 豆包", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Model: "", Note: "模型名填推理接入点 ID（ep-…）"},
	{ID: "hunyuan", Name: "腾讯混元", BaseURL: "https://api.hunyuan.cloud.tencent.com/v1", Model: "hunyuan-lite", Note: "腾讯云 OpenAI 兼容端点"},
	{ID: "qianfan", Name: "百度千帆", BaseURL: "https://qianfan.baidubce.com/v2", Model: "ernie-speed-8k", Note: "千帆 v2 兼容接口"},
	{ID: "minimax", Name: "MiniMax", BaseURL: "https://api.minimaxi.com/v1", Model: "abab6.5s-chat", Note: "国内直连"},
	{ID: "stepfun", Name: "阶跃星辰", BaseURL: "https://api.stepfun.com/v1", Model: "step-1-8k", Note: "国内直连"},
	{ID: "groq", Name: "Groq", BaseURL: "https://api.groq.com/openai/v1", Model: "llama-3.3-70b-versatile", Note: "免费额度，需境外网络"},
	{ID: "ollama", Name: "Ollama（本地）", BaseURL: "http://127.0.0.1:11434/v1", Model: "qwen2.5:7b", Note: "填 NAS/主机可达地址，密钥可留空"},
	{ID: "custom", Name: "自定义 OpenAI 兼容端点", BaseURL: "", Model: "", Note: "任意支持 /chat/completions 的服务"},
}

var asrProviders = []Provider{
	{ID: "openai", Name: "OpenAI Whisper", BaseURL: "https://api.openai.com/v1", Model: "whisper-1", Note: "官方 whisper-1"},
	{ID: "siliconflow", Name: "硅基流动 SenseVoice", BaseURL: "https://api.siliconflow.cn/v1", Model: "FunAudioLLM/SenseVoiceSmall", Note: "国内直连，中文识别好"},
	{ID: "groq", Name: "Groq Whisper", BaseURL: "https://api.groq.com/openai/v1", Model: "whisper-large-v3", Note: "免费额度，需境外网络"},
	{ID: "deepseek", Name: "（无转写，用 LLM 端点）", BaseURL: "https://api.deepseek.com/v1", Model: "whisper-1", Note: "DeepSeek 不提供转写，请另选"},
	{ID: "ollama", Name: "本地 / 自建 Whisper 服务", BaseURL: "http://127.0.0.1:8000/v1", Model: "whisper-1", Note: "如 faster-whisper-server、speaches"},
	{ID: "custom", Name: "自定义 OpenAI 兼容转写", BaseURL: "", Model: "whisper-1", Note: "任意支持 /audio/transcriptions 的服务"},
}

// AIConfig 是每次请求实时解析出来的有效配置。
type AIConfig struct {
	ASRBase, ASRKey, ASRModel, ASRLanguage, ASRPath string
	LLMBase, LLMKey, LLMModel, LLMPath              string
}

// Limits 由 Web 设置或环境变量共同决定，0 表示不限制。
type Limits struct {
	MaxAudioBytes  int64
	MaxExportBytes int64
	MaxExportItems int
}

type SettingsStore struct {
	mu   sync.RWMutex
	path string
	data map[string]any // 仅保存用户在 Web 上显式设置过的值
}

func NewSettingsStore(dataDir string) *SettingsStore {
	s := &SettingsStore{
		path: filepath.Join(dataDir, "settings.json"),
		data: map[string]any{},
	}
	if b, err := os.ReadFile(s.path); err == nil {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil && m != nil {
			s.data = m
		}
	}
	return s
}

func nested(m map[string]any, keys ...string) (any, bool) {
	var cur any = m
	for _, k := range keys {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[k]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func setNested(m map[string]any, value any, keys ...string) {
	cur := m
	for _, k := range keys[:len(keys)-1] {
		next, ok := cur[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[k] = next
		}
		cur = next
	}
	if value == nil {
		delete(cur, keys[len(keys)-1])
	} else {
		cur[keys[len(keys)-1]] = value
	}
}

func (s *SettingsStore) rawStr(keys ...string) (string, bool) {
	v, ok := nested(s.data, keys...)
	if !ok {
		return "", false
	}
	str, ok := v.(string)
	return str, ok
}

func (s *SettingsStore) rawInt(keys ...string) (int64, bool) {
	v, ok := nested(s.data, keys...)
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case int:
		return int64(x), true
	case int64:
		return x, true
	case json.Number:
		n, e := x.Int64()
		return n, e == nil
	case string:
		n, e := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n, e == nil
	}
	return 0, false
}

func (s *SettingsStore) pick(keys []string, fallback string) string {
	v, ok := s.rawStr(keys...)
	if ok {
		return strings.TrimSpace(v)
	}
	return fallback
}

func (s *SettingsStore) pickInt(keys []string, fallback int64) int64 {
	if v, ok := s.rawInt(keys...); ok {
		return v
	}
	return fallback
}

func providerDefault(list []Provider, id, field string) string {
	for _, p := range list {
		if p.ID == id {
			if field == "baseUrl" {
				return p.BaseURL
			}
			return p.Model
		}
	}
	return ""
}

func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return "••••••••"
	}
	return k[:3] + "••••" + k[len(k)-4:]
}

// AI 配置解析顺序：Web 设置 > 容器环境变量 > 厂商预设 > 内置默认值。
func (s *SettingsStore) AIConfig() AIConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	llmProvider := firstNonEmpty(envFirst("AI_PROVIDER"), "openai")
	asrProvider := firstNonEmpty(envFirst("ASR_PROVIDER"), "openai")
	if v, ok := s.rawStr("ai", "provider"); ok && v != "" {
		llmProvider = v
	}
	if v, ok := s.rawStr("ai", "asrProvider"); ok && v != "" {
		asrProvider = v
	}

	llmBaseDefault := firstNonEmpty(
		envFirst("LLM_BASE_URL", "AI_BASE_URL"),
		providerDefault(llmProviders, llmProvider, "baseUrl"),
		"https://api.openai.com/v1",
	)
	asrBaseDefault := firstNonEmpty(
		envFirst("ASR_BASE_URL", "AI_BASE_URL"),
		providerDefault(asrProviders, asrProvider, "baseUrl"),
		"https://api.openai.com/v1",
	)
	llmModelDefault := firstNonEmpty(envFirst("LLM_MODEL"), providerDefault(llmProviders, llmProvider, "model"), "gpt-4o-mini")
	asrModelDefault := firstNonEmpty(envFirst("ASR_MODEL"), providerDefault(asrProviders, asrProvider, "model"), "whisper-1")

	cfg := AIConfig{
		LLMBase:     strings.TrimRight(s.pick([]string{"ai", "baseUrl"}, llmBaseDefault), "/"),
		LLMKey:      s.pick([]string{"ai", "apiKey"}, envFirst("LLM_API_KEY", "AI_API_KEY")),
		LLMModel:    s.pick([]string{"ai", "model"}, llmModelDefault),
		LLMPath:     s.pick([]string{"ai", "chatPath"}, "/chat/completions"),
		ASRBase:     strings.TrimRight(s.pick([]string{"ai", "asrBaseUrl"}, asrBaseDefault), "/"),
		ASRKey:      s.pick([]string{"ai", "asrApiKey"}, envFirst("ASR_API_KEY", "AI_API_KEY")),
		ASRModel:    s.pick([]string{"ai", "asrModel"}, asrModelDefault),
		ASRLanguage: s.pick([]string{"ai", "asrLanguage"}, firstNonEmpty(envFirst("ASR_LANGUAGE"), "zh")),
		ASRPath:     s.pick([]string{"ai", "asrPath"}, "/audio/transcriptions"),
	}
	if !strings.HasPrefix(cfg.LLMPath, "/") {
		cfg.LLMPath = "/" + cfg.LLMPath
	}
	if !strings.HasPrefix(cfg.ASRPath, "/") {
		cfg.ASRPath = "/" + cfg.ASRPath
	}
	return cfg
}

func (s *SettingsStore) Limits() Limits {
	s.mu.RLock()
	defer s.mu.RUnlock()
	audioMB := s.pickInt([]string{"limits", "maxAudioMB"}, int64(parseIntEnv("MAX_AUDIO_MB", 10, 0)))
	exportMB := s.pickInt([]string{"limits", "maxExportMB"}, int64(parseIntEnv("MAX_EXPORT_MB", 10, 0)))
	items := s.pickInt([]string{"limits", "maxExportItems"}, int64(parseIntEnv("MAX_EXPORT_ITEMS", 10, 0)))
	l := Limits{}
	if audioMB > 0 {
		l.MaxAudioBytes = audioMB << 20
	}
	if exportMB > 0 {
		l.MaxExportBytes = exportMB << 20
	}
	if items > 0 {
		l.MaxExportItems = int(items)
	} else {
		l.MaxExportItems = 1 << 30
	}
	return l
}

func (s *SettingsStore) RecordFormat() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := s.pick([]string{"record", "format"}, firstNonEmpty(envFirst("RECORD_FORMAT"), "wav"))
	switch strings.ToLower(v) {
	case "wav", "webm", "mp4", "ogg", "auto":
		return strings.ToLower(v)
	}
	return "wav"
}

func (s *SettingsStore) DownloadFormat() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := s.pick([]string{"record", "downloadFormat"}, firstNonEmpty(envFirst("DOWNLOAD_FORMAT"), "wav"))
	switch strings.ToLower(v) {
	case "wav", "original":
		return strings.ToLower(v)
	}
	return "wav"
}

// View 是给 Web 设置面板的完整视图：密钥永不回传，只给是否已设置与尾号提示。
func (s *SettingsStore) View(st *Store, https bool) map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg := s.AIConfigLocked()
	l := s.LimitsLocked()
	tlsMode := "self-signed"
	if envFirst("TLS_CERT_FILE") != "" && envFirst("TLS_KEY_FILE") != "" {
		tlsMode = "custom"
	}
	return map[string]any{
		"ai": map[string]any{
			"provider":      s.pick([]string{"ai", "provider"}, firstNonEmpty(envFirst("AI_PROVIDER"), "openai")),
			"baseUrl":       cfg.LLMBase,
			"model":         cfg.LLMModel,
			"chatPath":      cfg.LLMPath,
			"apiKeySet":     cfg.LLMKey != "",
			"apiKeyHint":    maskKey(cfg.LLMKey),
			"asrProvider":   s.pick([]string{"ai", "asrProvider"}, firstNonEmpty(envFirst("ASR_PROVIDER"), "openai")),
			"asrBaseUrl":    cfg.ASRBase,
			"asrModel":      cfg.ASRModel,
			"asrPath":       cfg.ASRPath,
			"asrLanguage":   cfg.ASRLanguage,
			"asrApiKeySet":  cfg.ASRKey != "",
			"asrApiKeyHint": maskKey(cfg.ASRKey),
		},
		"limits": map[string]any{
			"maxAudioMB":  l.MaxAudioBytes >> 20,
			"maxExportMB": l.MaxExportBytes >> 20,
			"maxExportItems": func() int64 {
				if l.MaxExportItems >= 1<<30 {
					return 0
				}
				return int64(l.MaxExportItems)
			}(),
		},
		"record": map[string]any{
			"format":         s.pick([]string{"record", "format"}, firstNonEmpty(envFirst("RECORD_FORMAT"), "wav")),
			"downloadFormat": s.pick([]string{"record", "downloadFormat"}, firstNonEmpty(envFirst("DOWNLOAD_FORMAT"), "wav")),
		},
		"runtime": map[string]any{
			"puid":         parseIntEnv("PUID", 10, 0),
			"pgid":         parseIntEnv("PGID", 10, 0),
			"umask":        fmt.Sprintf("%04o", parseIntEnv("UMASK", 8, 022)),
			"dataDir":      st.DataDir(),
			"https":        https,
			"httpsAuto":    envFirst("ENABLE_HTTPS") == "1",
			"tlsSan":       firstNonEmpty(envFirst("TLS_SAN"), "localhost"),
			"tlsMode":      tlsMode,
			"identityHint": "PUID / PGID / UMASK / HTTPS 由容器启动参数决定，需在 compose 中修改并重建容器后生效",
		},
		"providers": map[string]any{"llm": llmProviders, "asr": asrProviders},
	}
}

// AIConfigLocked / LimitsLocked 供已经持锁的 View 使用。
func (s *SettingsStore) AIConfigLocked() AIConfig {
	llmProvider := s.pick([]string{"ai", "provider"}, firstNonEmpty(envFirst("AI_PROVIDER"), "openai"))
	asrProvider := s.pick([]string{"ai", "asrProvider"}, firstNonEmpty(envFirst("ASR_PROVIDER"), "openai"))
	llmBase := firstNonEmpty(envFirst("LLM_BASE_URL", "AI_BASE_URL"), providerDefault(llmProviders, llmProvider, "baseUrl"), "https://api.openai.com/v1")
	asrBase := firstNonEmpty(envFirst("ASR_BASE_URL", "AI_BASE_URL"), providerDefault(asrProviders, asrProvider, "baseUrl"), "https://api.openai.com/v1")
	return AIConfig{
		LLMBase:     strings.TrimRight(s.pick([]string{"ai", "baseUrl"}, llmBase), "/"),
		LLMKey:      s.pick([]string{"ai", "apiKey"}, envFirst("LLM_API_KEY", "AI_API_KEY")),
		LLMModel:    s.pick([]string{"ai", "model"}, firstNonEmpty(envFirst("LLM_MODEL"), providerDefault(llmProviders, llmProvider, "model"), "gpt-4o-mini")),
		LLMPath:     s.pick([]string{"ai", "chatPath"}, "/chat/completions"),
		ASRBase:     strings.TrimRight(s.pick([]string{"ai", "asrBaseUrl"}, asrBase), "/"),
		ASRKey:      s.pick([]string{"ai", "asrApiKey"}, envFirst("ASR_API_KEY", "AI_API_KEY")),
		ASRModel:    s.pick([]string{"ai", "asrModel"}, firstNonEmpty(envFirst("ASR_MODEL"), providerDefault(asrProviders, asrProvider, "model"), "whisper-1")),
		ASRLanguage: s.pick([]string{"ai", "asrLanguage"}, firstNonEmpty(envFirst("ASR_LANGUAGE"), "zh")),
		ASRPath:     s.pick([]string{"ai", "asrPath"}, "/audio/transcriptions"),
	}
}

func (s *SettingsStore) LimitsLocked() Limits {
	audioMB := s.pickInt([]string{"limits", "maxAudioMB"}, int64(parseIntEnv("MAX_AUDIO_MB", 10, 0)))
	exportMB := s.pickInt([]string{"limits", "maxExportMB"}, int64(parseIntEnv("MAX_EXPORT_MB", 10, 0)))
	items := s.pickInt([]string{"limits", "maxExportItems"}, int64(parseIntEnv("MAX_EXPORT_ITEMS", 10, 0)))
	l := Limits{MaxExportItems: 1 << 30}
	if audioMB > 0 {
		l.MaxAudioBytes = audioMB << 20
	}
	if exportMB > 0 {
		l.MaxExportBytes = exportMB << 20
	}
	if items > 0 {
		l.MaxExportItems = int(items)
	}
	return l
}

var allowedSettings = map[string]bool{
	"ai.provider": true, "ai.baseUrl": true, "ai.apiKey": true, "ai.model": true, "ai.chatPath": true,
	"ai.asrProvider": true, "ai.asrBaseUrl": true, "ai.asrApiKey": true, "ai.asrModel": true,
	"ai.asrPath": true, "ai.asrLanguage": true,
	"limits.maxAudioMB": true, "limits.maxExportMB": true, "limits.maxExportItems": true,
	"record.format": true, "record.downloadFormat": true,
}

// Update 只接受白名单字段；密钥传 "__KEEP__" 表示不改，传 "" 表示清除。
func (s *SettingsStore) Update(patch map[string]any) (map[string]any, *apiError) {
	s.mu.Lock()
	defer s.mu.Unlock()

	apply := func(group string, src any) *apiError {
		obj, ok := src.(map[string]any)
		if !ok {
			if src != nil {
				return bad(400, group+" 必须是对象")
			}
			return nil
		}
		for k, v := range obj {
			path := group + "." + k
			if !allowedSettings[path] {
				return bad(400, "不支持的设置项："+path)
			}
			if k == "format" || k == "downloadFormat" {
				str, _ := v.(string)
				if str != "" && !map[string]bool{"wav": true, "webm": true, "mp4": true, "ogg": true, "auto": true, "original": true}[str] {
					return bad(400, "不支持的音频格式："+str)
				}
			}
			if strings.Contains(k, "MB") || k == "maxExportItems" {
				n := numVal(v)
				if n < 0 || n > 1<<20 {
					return bad(400, path+" 取值超出范围")
				}
				setNested(s.data, n, group, k)
				continue
			}
			str, ok := v.(string)
			if !ok {
				return bad(400, path+" 必须是字符串")
			}
			str = strings.TrimSpace(str)
			if str == "__KEEP__" {
				continue
			}
			if strings.HasSuffix(k, "ApiKey") || k == "apiKey" || k == "asrApiKey" {
				if str == "" {
					setNested(s.data, nil, group, k)
				} else {
					setNested(s.data, str, group, k)
				}
				continue
			}
			if len(str) > 500 {
				str = str[:500]
			}
			if str == "" {
				setNested(s.data, nil, group, k)
			} else {
				setNested(s.data, str, group, k)
			}
		}
		return nil
	}

	for _, group := range []string{"ai", "limits", "record"} {
		if e := apply(group, patch[group]); e != nil {
			return nil, e
		}
	}

	buf, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return nil, bad(500, "序列化设置失败")
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".settings-*")
	if err != nil {
		return nil, bad(500, "写入设置失败")
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(buf)
	}
	if err == nil {
		err = tmp.Close()
	} else {
		_ = tmp.Close()
	}
	if err == nil {
		err = os.Rename(name, s.path)
	}
	if err != nil {
		return nil, bad(500, "保存设置失败："+err.Error())
	}
	return s.data, nil
}
