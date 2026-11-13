package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"time"
)

// aiService 走 OpenAI 兼容协议。密钥、端点、模型来自 Web 设置（落盘 settings.json），
// 未显式设置时回退到容器环境变量，再回退到厂商预设。任意支持
// /chat/completions 与 /audio/transcriptions 的厂商都可直接接入。
type aiService struct {
	s          *SettingsStore
	asrClient  *http.Client
	llmClient  *http.Client
	testClient *http.Client
}

func envFirst(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

func newAI(s *SettingsStore) *aiService {
	return &aiService{
		s:          s,
		asrClient:  &http.Client{Timeout: 300 * time.Second},
		llmClient:  &http.Client{Timeout: 180 * time.Second},
		testClient: &http.Client{Timeout: 25 * time.Second},
	}
}

func hostOf(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "自定义服务"
	}
	return u.Host
}

func truncateText(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) > max {
		return string(runes[:max]) + "…"
	}
	return s
}

// PublicConfig 只暴露可用状态与模型名，绝不回传密钥。
func (a *aiService) PublicConfig(st *Store) map[string]any {
	cfg := a.s.AIConfig()
	var warning any
	if w := st.Warning(); w != "" {
		warning = w
	}
	return map[string]any{
		"asr":            cfg.ASRKey != "",
		"llm":            cfg.LLMKey != "",
		"asrModel":       cfg.ASRModel,
		"llmModel":       cfg.LLMModel,
		"asrHost":        hostOf(cfg.ASRBase),
		"llmHost":        hostOf(cfg.LLMBase),
		"dataDir":        st.DataDir(),
		"warning":        warning,
		"recordFormat":   a.s.RecordFormat(),
		"downloadFormat": a.s.DownloadFormat(),
		"managed":        true, // 前端据此显示「设置」入口
	}
}

func (a *aiService) Transcribe(path, mimeType, filename string) (string, *apiError) {
	cfg := a.s.AIConfig()
	if cfg.ASRKey == "" {
		return "", &apiError{Status: 503, Message: "未配置语音转写服务，可在「设置」里接入或手动录入文本", Code: "NO_ASR"}
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	audio, err := os.ReadFile(path)
	if err != nil {
		return "", &apiError{Status: 500, Message: "读取音频失败", Code: "ASR_IO"}
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	header.Set("Content-Type", mimeType)
	part, err := mw.CreatePart(header)
	if err == nil {
		_, err = part.Write(audio)
	}
	if err == nil {
		err = mw.WriteField("model", cfg.ASRModel)
	}
	// 部分厂商（如 SenseVoice）不接受 language/response_format，留空即可跳过。
	if err == nil && cfg.ASRLanguage != "" && cfg.ASRLanguage != "auto" {
		err = mw.WriteField("language", cfg.ASRLanguage)
	}
	if err == nil {
		err = mw.Close()
	}
	if err != nil {
		return "", &apiError{Status: 500, Message: "构造转写请求失败", Code: "ASR_IO"}
	}

	req, err := http.NewRequest(http.MethodPost, cfg.ASRBase+cfg.ASRPath, &buf)
	if err != nil {
		return "", &apiError{Status: 502, Message: "转写服务地址无效", Code: "ASR_NETWORK"}
	}
	req.Header.Set("Authorization", "Bearer "+cfg.ASRKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := a.asrClient.Do(req)
	if err != nil {
		return "", &apiError{Status: 502, Message: "无法连接转写服务：" + truncateText(err.Error(), 120), Code: "ASR_NETWORK"}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &apiError{Status: 502, Message: fmt.Sprintf("转写服务返回 %d：%s", resp.StatusCode, truncateText(string(raw), 220)), Code: "ASR_FAILED"}
	}
	var data struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", &apiError{Status: 502, Message: "转写服务返回格式异常：" + truncateText(string(raw), 120), Code: "ASR_FAILED"}
	}
	text := strings.TrimSpace(data.Text)
	if text == "" {
		return "", &apiError{Status: 502, Message: "转写结果为空，请确认音频中包含人声", Code: "ASR_EMPTY"}
	}
	return text, nil
}

func parseJSONLoose(text string) (struct {
	Summary string   `json:"summary"`
	Tags    []string `json:"tags"`
}, error) {
	var result struct {
		Summary string   `json:"summary"`
		Tags    []string `json:"tags"`
	}
	s := strings.TrimSpace(text)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return result, fmt.Errorf("no json object")
	}
	err := json.Unmarshal([]byte(s[start:end+1]), &result)
	return result, err
}

func (a *aiService) Analyze(m *Memo) (string, []string, *apiError) {
	cfg := a.s.AIConfig()
	if cfg.LLMKey == "" {
		return "", nil, &apiError{Status: 503, Message: "未配置 AI 服务，可在「设置」里接入或手动填写摘要与标签", Code: "NO_LLM"}
	}
	transcript := strings.TrimSpace(m.Transcript)
	if transcript == "" {
		return "", nil, &apiError{Status: 503, Message: "请先完成转写或手动录入文本", Code: "NO_TRANSCRIPT"}
	}
	runes := []rune(transcript)
	if len(runes) > 12000 {
		transcript = string(runes[:12000])
	}
	system := "你是语音备忘录整理助手。只输出 JSON 对象，不要输出任何解释文字。"
	user := strings.Join([]string{
		"请根据下面这条语音备忘录的内容，生成中文摘要与标签。",
		"要求：summary 一句话、不超过 80 字、突出结论与待办；tags 为 2-5 个简短中文标签，不要带 # 号。",
		`返回格式：{"summary":"...","tags":["...","..."]}`,
		"",
		"标题：" + m.Title,
		"已有备注：" + firstNonEmpty(m.Note, "无"),
		"转写文本：" + transcript,
	}, "\n")

	payload := map[string]any{
		"model":       cfg.LLMModel,
		"temperature": 0.3,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, cfg.LLMBase+cfg.LLMPath, bytes.NewReader(body))
	if err != nil {
		return "", nil, &apiError{Status: 502, Message: "AI 服务地址无效", Code: "LLM_NETWORK"}
	}
	req.Header.Set("Authorization", "Bearer "+cfg.LLMKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.llmClient.Do(req)
	if err != nil {
		return "", nil, &apiError{Status: 502, Message: "无法连接 AI 服务：" + truncateText(err.Error(), 120), Code: "LLM_NETWORK"}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, &apiError{Status: 502, Message: fmt.Sprintf("AI 服务返回 %d：%s", resp.StatusCode, truncateText(string(raw), 220)), Code: "LLM_FAILED"}
	}
	var data struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &data); err != nil || len(data.Choices) == 0 || data.Choices[0].Message.Content == "" {
		return "", nil, &apiError{Status: 502, Message: "AI 服务未返回有效内容：" + truncateText(string(raw), 120), Code: "LLM_FAILED"}
	}
	parsed, err := parseJSONLoose(data.Choices[0].Message.Content)
	if err != nil {
		return "", nil, &apiError{Status: 502, Message: "AI 返回内容不是有效 JSON", Code: "LLM_PARSE"}
	}
	summary := strings.TrimSpace(parsed.Summary)
	if r := []rune(summary); len(r) > 400 {
		summary = string(r[:400])
	}
	tags := []string{}
	for _, t := range parsed.Tags {
		t = strings.TrimPrefix(strings.TrimSpace(t), "#")
		if t != "" {
			tags = append(tags, t)
		}
		if len(tags) >= 5 {
			break
		}
	}
	if summary == "" && len(tags) == 0 {
		return "", nil, &apiError{Status: 502, Message: "AI 未生成有效摘要或标签", Code: "LLM_PARSE"}
	}
	return summary, tags, nil
}

// Test 用当前配置做一次低成本连通性探测：先试 GET /models，
// 不支持的厂商（如部分国内兼容端点）再退回一条极简 chat 请求。
func (a *aiService) Test(target string) (int, string) {
	cfg := a.s.AIConfig()
	base, key, model, path := cfg.LLMBase, cfg.LLMKey, cfg.LLMModel, cfg.LLMPath
	if target == "asr" {
		base, key, model, path = cfg.ASRBase, cfg.ASRKey, cfg.ASRModel, cfg.ASRPath
	}
	if base == "" {
		return 400, "服务地址为空"
	}
	if key == "" {
		return 400, "尚未填写 API 密钥"
	}

	req, err := http.NewRequest(http.MethodGet, base+"/models", nil)
	if err != nil {
		return 400, "服务地址无效：" + err.Error()
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := a.testClient.Do(req)
	if err == nil {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return 200, fmt.Sprintf("连接成功（%s /models，HTTP %d，返回 %d 字节）", hostOf(base), resp.StatusCode, len(raw))
		}
		if resp.StatusCode != 404 && resp.StatusCode != 405 && resp.StatusCode != 501 {
			return resp.StatusCode, fmt.Sprintf("服务返回 HTTP %d：%s", resp.StatusCode, truncateText(string(raw), 160))
		}
		// 落到下面的 chat 探测
	} else if target == "asr" {
		return 502, "无法连接：" + truncateText(err.Error(), 140)
	}

	if target == "asr" {
		return 502, "转写端点不支持 /models，请在保存后直接用一条录音试转写"
	}
	payload := map[string]any{
		"model":      model,
		"max_tokens": 1,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	}
	body, _ := json.Marshal(payload)
	req2, err := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return 400, "服务地址无效"
	}
	req2.Header.Set("Authorization", "Bearer "+key)
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := a.testClient.Do(req2)
	if err != nil {
		return 502, "无法连接：" + truncateText(err.Error(), 140)
	}
	defer resp2.Body.Close()
	raw2, _ := io.ReadAll(io.LimitReader(resp2.Body, 1<<16))
	if resp2.StatusCode < 200 || resp2.StatusCode >= 300 {
		return resp2.StatusCode, fmt.Sprintf("服务返回 HTTP %d：%s", resp2.StatusCode, truncateText(string(raw2), 180))
	}
	return 200, fmt.Sprintf("连接成功（%s，模型 %s，HTTP %d）", hostOf(base), model, resp2.StatusCode)
}
