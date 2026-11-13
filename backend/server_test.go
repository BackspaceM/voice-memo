package main

import (
	"archive/zip"
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type testEnv struct {
	h   *handler
	dir string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	for _, key := range []string{
		"AI_API_KEY", "ASR_API_KEY", "LLM_API_KEY", "AI_BASE_URL", "ASR_BASE_URL", "LLM_BASE_URL",
		"AI_PROVIDER", "ASR_PROVIDER", "LLM_MODEL", "ASR_MODEL", "ASR_LANGUAGE",
		"RECORD_FORMAT", "ENABLE_HTTPS", "TLS_SAN", "TLS_CERT_FILE", "TLS_KEY_FILE",
	} {
		t.Setenv(key, "")
	}
	dir := t.TempDir()
	st, err := NewStore(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	www := filepath.Join(dir, "www")
	if err := os.MkdirAll(www, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(www, "index.html"), []byte("<html>语音备忘录</html>"), 0644); err != nil {
		t.Fatal(err)
	}
	settings := NewSettingsStore(st.DataDir())
	return &testEnv{
		h:   &handler{st: st, ai: newAI(settings), settings: settings, publicDir: www},
		dir: st.DataDir(),
	}
}

func (e *testEnv) request(t *testing.T, method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *testEnv) json(t *testing.T, method, path string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	if headers == nil {
		headers = map[string]string{}
	}
	if raw != nil {
		headers["Content-Type"] = "application/json"
	}
	rec := e.request(t, method, path, raw, headers)
	var data map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &data)
	return rec.Code, data
}

func mustStatus(t *testing.T, got, want int, label string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: 期望 HTTP %d，实际 %d", label, want, got)
	}
}

func TestHealthAndConfig(t *testing.T) {
	e := newTestEnv(t)
	code, data := e.json(t, http.MethodGet, "/api/health", nil, nil)
	mustStatus(t, code, 200, "健康检查")
	if data["ok"] != true {
		t.Fatal("健康检查应返回 ok=true")
	}
	code, cfg := e.json(t, http.MethodGet, "/api/config", nil, nil)
	mustStatus(t, code, 200, "配置")
	if cfg["asr"] != false || cfg["llm"] != false {
		t.Fatal("无密钥时应降级为 false")
	}
	if cfg["dataDir"] == "" {
		t.Fatal("配置应包含数据目录")
	}
}

func TestCRUDSearchAndReminders(t *testing.T) {
	e := newTestEnv(t)
	code, a := e.json(t, http.MethodPost, "/api/memos", map[string]any{
		"title": "项目周会要点", "tags": []string{"工作", "会议"}, "folder": "工作", "durationMs": 90000,
	}, nil)
	mustStatus(t, code, 201, "创建 a")
	id := a["id"].(string)
	if a["tags"] == nil || a["remindAt"] != nil {
		t.Fatal("tags 应为数组，remindAt 应为 null")
	}

	_, b := e.json(t, http.MethodPost, "/api/memos", map[string]any{
		"title": "买菜清单", "note": "周末记得买牛奶", "tags": []string{"生活"},
	}, nil)
	_, c := e.json(t, http.MethodPost, "/api/memos", map[string]any{
		"title": "项目复盘", "transcript": "会议讨论了项目排期与交付节奏",
	}, nil)
	if c["transcriptStatus"] != "manual" {
		t.Fatal("初始转录文本应保留并标记 manual")
	}

	code, list := e.json(t, http.MethodGet, "/api/memos?q="+urlEncode("项目"), nil, nil)
	mustStatus(t, code, 200, "单词检索")
	if len(list["items"].([]any)) != 2 {
		t.Fatal("单词检索应命中 2 条")
	}

	code, list = e.json(t, http.MethodGet, "/api/memos?q="+urlEncode("项目 会议"), nil, nil)
	mustStatus(t, code, 200, "多词 AND 检索")
	hits := list["items"].([]any)
	if len(hits) != 2 {
		t.Fatalf("多词 AND 应命中 2 条，实际 %d", len(hits))
	}
	if hits[0].(map[string]any)["id"] != id {
		t.Fatal("标签权重高于转录，排序不正确")
	}

	code, list = e.json(t, http.MethodGet, "/api/memos?q="+urlEncode("项目 交付"), nil, nil)
	if len(list["items"].([]any)) != 1 {
		t.Fatal("AND 语义应排除不含全部关键词的条目")
	}

	code, list = e.json(t, http.MethodGet, "/api/memos?folder="+urlEncode("工作"), nil, nil)
	if len(list["items"].([]any)) != 1 {
		t.Fatal("文件夹过滤失败")
	}
	code, list = e.json(t, http.MethodGet, "/api/memos?tag="+urlEncode("生活"), nil, nil)
	if hits := list["items"].([]any); len(hits) != 1 || hits[0].(map[string]any)["id"] != b["id"] {
		t.Fatal("标签过滤失败")
	}

	code, updated := e.json(t, http.MethodPatch, "/api/memos/"+id, map[string]any{
		"title": "项目周会要点(已整理)", "starred": true,
		"remindAt": "2020-01-01T00:00:00.000Z", "transcript": "手动录入的文本",
	}, nil)
	mustStatus(t, code, 200, "更新")
	if updated["starred"] != true || updated["transcriptStatus"] != "manual" {
		t.Fatal("更新未生效")
	}

	code, rems := e.json(t, http.MethodGet, "/api/reminders", nil, nil)
	mustStatus(t, code, 200, "提醒列表")
	if len(rems["items"].([]any)) != 1 {
		t.Fatal("应有 1 条到期提醒")
	}
	code, _ = e.json(t, http.MethodPost, "/api/reminders/"+id+"/done", nil, nil)
	mustStatus(t, code, 200, "标记提醒完成")
	code, rems = e.json(t, http.MethodGet, "/api/reminders", nil, nil)
	if len(rems["items"].([]any)) != 0 {
		t.Fatal("提醒去重失败")
	}

	code, _ = e.json(t, http.MethodDelete, "/api/memos/"+b["id"].(string), nil, nil)
	mustStatus(t, code, 200, "删除")
	code, _ = e.json(t, http.MethodGet, "/api/memos/"+b["id"].(string), nil, nil)
	mustStatus(t, code, 404, "删除后查询")
}

func TestAudioRangeAndExport(t *testing.T) {
	e := newTestEnv(t)
	_, memo := e.json(t, http.MethodPost, "/api/memos", map[string]any{"title": "中文 测试录音"}, nil)
	id := memo["id"].(string)

	audio := []byte("fake-webm-audio-bytes-0123456789")
	rec := e.request(t, http.MethodPut, "/api/memos/"+id+"/audio", audio, map[string]string{
		"Content-Type": "audio/webm", "X-Audio-Mime": "audio/webm", "X-Audio-Duration": "65000",
	})
	mustStatus(t, rec.Code, 200, "音频上传")
	var uploaded map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &uploaded)
	if uploaded["audioSize"].(float64) != float64(len(audio)) || uploaded["durationMs"].(float64) != 65000 {
		t.Fatal("音频元数据不正确")
	}

	rec = e.request(t, http.MethodGet, "/api/memos/"+id+"/audio", nil, map[string]string{"Range": "bytes=0-9"})
	mustStatus(t, rec.Code, 206, "Range 回放")
	if rec.Header().Get("Content-Range") != "bytes 0-9/"+itoa(len(audio)) {
		t.Fatal("Content-Range 不正确: " + rec.Header().Get("Content-Range"))
	}
	if rec.Body.String() != string(audio[:10]) {
		t.Fatal("Range 内容不正确")
	}

	rec = e.request(t, http.MethodGet, "/api/memos/"+id+"/audio", nil, map[string]string{"Range": "bytes=99999-"})
	mustStatus(t, rec.Code, 416, "非法 Range")

	rec = e.request(t, http.MethodGet, "/api/export?ids="+id, nil, nil)
	mustStatus(t, rec.Code, 200, "ZIP 导出")
	if rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatal("导出 MIME 不正确")
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal("ZIP 结构无效:", err)
	}
	names := []string{}
	for _, f := range zr.File {
		names = append(names, f.Name)
		if strings.HasSuffix(f.Name, "/memo.md") {
			if !strings.Contains(f.Name, "中文") {
				t.Fatal("导出目录应保留中文标题: " + f.Name)
			}
			if f.NonUTF8 {
				t.Fatal("中文文件名应设置 UTF-8 标志: " + f.Name)
			}
		}
	}
	joined := strings.Join(names, "|")
	if !strings.Contains(joined, "memo.md") || !strings.Contains(joined, "audio.webm") || !strings.Contains(joined, "manifest.json") {
		t.Fatal("导出内容缺失: " + joined)
	}
}

func TestAIDegradation(t *testing.T) {
	e := newTestEnv(t)
	_, memo := e.json(t, http.MethodPost, "/api/memos", map[string]any{"title": "无密钥测试"}, nil)
	id := memo["id"].(string)

	code, data := e.json(t, http.MethodPost, "/api/memos/"+id+"/analyze", nil, nil)
	mustStatus(t, code, 503, "无密钥摘要降级")
	if data["code"] != "NO_LLM" {
		t.Fatal("应返回 NO_LLM 错误码")
	}
	if data["memo"] == nil {
		t.Fatal("降级响应应回传更新后的备忘录")
	}

	audio := []byte("wavdata")
	e.request(t, http.MethodPut, "/api/memos/"+id+"/audio", audio, map[string]string{"Content-Type": "audio/wav"})
	code, data = e.json(t, http.MethodPost, "/api/memos/"+id+"/transcribe", nil, nil)
	mustStatus(t, code, 503, "无密钥转写降级")
	if data["code"] != "NO_ASR" {
		t.Fatal("应返回 NO_ASR 错误码")
	}
}

func TestStaticAndSecurity(t *testing.T) {
	e := newTestEnv(t)
	rec := e.request(t, http.MethodGet, "/index.html", nil, nil)
	mustStatus(t, rec.Code, 200, "静态首页")
	if !strings.Contains(rec.Body.String(), "语音备忘录") {
		t.Fatal("静态内容不正确")
	}
	rec = e.request(t, http.MethodGet, "/../package.json", nil, nil)
	mustStatus(t, rec.Code, 200, "路径穿越应回落到 SPA 首页")

	code, _ := e.json(t, http.MethodGet, "/api/nope", nil, nil)
	mustStatus(t, code, 404, "未知接口")
	code, _ = e.json(t, http.MethodPost, "/api/memos", []string{"not", "an", "object"}, nil)
	mustStatus(t, code, 400, "非法 JSON 主体")
}

func TestPersistenceAcrossRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, e := st.Create(map[string]any{"title": "持久化验证"}); e != nil {
		t.Fatal(e)
	}
	st2, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	items := st2.List(ListOptions{})
	if len(items) != 1 || items[0].Title != "持久化验证" {
		t.Fatal("跨实例持久化失败")
	}
}

func TestUnlimitedAudioAndStreamingExport(t *testing.T) {
	t.Setenv("MAX_AUDIO_MB", "0")
	t.Setenv("MAX_EXPORT_MB", "0")
	t.Setenv("MAX_EXPORT_ITEMS", "0")
	e := newTestEnv(t)
	_, memo := e.json(t, http.MethodPost, "/api/memos", map[string]any{"title": "大音频验证"}, nil)
	id := memo["id"].(string)

	audio := bytes.Repeat([]byte{0x41, 0x42, 0x43, 0x44}, 2<<20) // 8 MiB
	rec := e.request(t, http.MethodPut, "/api/memos/"+id+"/audio", audio, map[string]string{
		"Content-Type": "audio/webm",
	})
	mustStatus(t, rec.Code, 200, "大音频上传（无上限）")
	var uploaded map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &uploaded)
	if uploaded["audioSize"].(float64) != float64(len(audio)) {
		t.Fatalf("音频大小不正确: %v", uploaded["audioSize"])
	}

	stored, err := os.ReadFile(filepath.Join(e.dir, "audio", id+".webm"))
	if err != nil || len(stored) != len(audio) || !bytes.Equal(stored[:16], audio[:16]) {
		t.Fatal("音频未正确流式落盘")
	}
	rec = e.request(t, http.MethodGet, "/api/memos/"+id+"/audio", nil, map[string]string{"Range": "bytes=-8"})
	mustStatus(t, rec.Code, 206, "后缀 Range 回放")
	if !bytes.Equal(rec.Body.Bytes(), audio[len(audio)-8:]) {
		t.Fatal("后缀 Range 内容不正确")
	}

	rec = e.request(t, http.MethodGet, "/api/export?ids="+id, nil, nil)
	mustStatus(t, rec.Code, 200, "大文件流式导出")
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal("导出 ZIP 无效:", err)
	}
	found := false
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, "/audio.webm") {
			continue
		}
		found = true
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil || len(data) != len(audio) || !bytes.Equal(data[:16], audio[:16]) {
			t.Fatal("导出包内音频内容不正确")
		}
	}
	if !found {
		t.Fatal("导出包缺少音频")
	}
	entries, _ := os.ReadDir(e.dir)
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), ".export-") || strings.HasPrefix(en.Name(), ".upload-") {
			t.Fatal("临时文件未清理: " + en.Name())
		}
	}
}

func TestOptionalSizeLimits(t *testing.T) {
	t.Setenv("MAX_AUDIO_MB", "1")
	e := newTestEnv(t)
	_, memo := e.json(t, http.MethodPost, "/api/memos", map[string]any{"title": "上限验证"}, nil)
	id := memo["id"].(string)
	rec := e.request(t, http.MethodPut, "/api/memos/"+id+"/audio",
		bytes.Repeat([]byte{0x41}, 2<<20), map[string]string{"Content-Type": "audio/webm"})
	mustStatus(t, rec.Code, 413, "MAX_AUDIO_MB=1 应拒绝 2MiB 音频")
	if _, err := os.Stat(filepath.Join(e.dir, "audio", id+".webm")); !os.IsNotExist(err) {
		t.Fatal("超限请求不应留下音频文件")
	}
	entries, _ := os.ReadDir(filepath.Join(e.dir, "audio"))
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), ".upload-") {
			t.Fatal("超限请求未清理临时文件: " + en.Name())
		}
	}
}

func TestIdentityEnvParsing(t *testing.T) {
	t.Setenv("PUID", "1000")
	t.Setenv("PGID", "10")
	t.Setenv("UMASK", "002")
	id := readIdentity()
	if id.UID != 1000 || id.GID != 10 || id.Umask != 2 {
		t.Fatalf("PUID/PGID/UMASK 解析错误: %+v", id)
	}
	t.Setenv("PUID", "")
	t.Setenv("PGID", "")
	t.Setenv("UID", "1001")
	t.Setenv("GID", "11")
	id = readIdentity()
	if id.UID != 1001 || id.GID != 11 {
		t.Fatalf("UID/GID 回退解析错误: %+v", id)
	}
}

func TestSettingsAPI(t *testing.T) {
	e := newTestEnv(t)
	status, view := e.json(t, http.MethodGet, "/api/settings", nil, nil)
	mustStatus(t, status, 200, "读取设置")
	ai, _ := view["ai"].(map[string]any)
	providers, _ := view["providers"].(map[string]any)
	llmList, _ := providers["llm"].([]any)
	if ai == nil || ai["apiKeySet"] != false || len(llmList) < 10 {
		t.Fatalf("设置视图缺少预设或密钥状态异常: %+v", view)
	}
	if _, leaked := ai["apiKey"]; leaked {
		t.Fatal("设置视图不应回传 apiKey 字段")
	}

	status, view = e.json(t, http.MethodPut, "/api/settings", map[string]any{
		"ai": map[string]any{
			"provider":    "deepseek",
			"apiKey":      "sk-unit-test-12345678",
			"asrProvider": "siliconflow",
			"asrApiKey":   "sk-asr-test-8888",
		},
		"record": map[string]any{"format": "mp4", "downloadFormat": "wav"},
		"limits": map[string]any{"maxAudioMB": float64(512), "maxExportItems": float64(100)},
	}, nil)
	mustStatus(t, status, 200, "保存设置")
	if strings.Contains(fmt.Sprint(view), "sk-unit-test") {
		t.Fatal("保存响应泄漏了 API 密钥")
	}
	ai, _ = view["ai"].(map[string]any)
	if ai["apiKeySet"] != true || ai["apiKeyHint"] == "" {
		t.Fatalf("密钥应设置且仅返回提示: %+v", ai)
	}

	_, cfg := e.json(t, http.MethodGet, "/api/config", nil, nil)
	if cfg["llm"] != true || cfg["asr"] != true {
		t.Fatalf("设置后 AI 能力应可用: %+v", cfg)
	}
	if !strings.Contains(fmt.Sprint(cfg["llmHost"]), "deepseek") {
		t.Fatalf("厂商预设未生效: %v", cfg["llmHost"])
	}
	if cfg["recordFormat"] != "mp4" || e.h.settings.RecordFormat() != "mp4" {
		t.Fatal("录音格式设置未生效")
	}
	if l := e.h.limits(); l.MaxAudioBytes != 512<<20 || l.MaxExportItems != 100 {
		t.Fatalf("上限设置未生效: %+v", l)
	}

	// 落盘 + 跨实例重载
	raw, err := os.ReadFile(filepath.Join(e.dir, "settings.json"))
	if err != nil || !strings.Contains(string(raw), "sk-unit-test-12345678") {
		t.Fatal("settings.json 未持久化密钥")
	}
	fresh := NewSettingsStore(e.dir)
	if c := fresh.AIConfig(); c.LLMKey != "sk-unit-test-12345678" || !strings.Contains(c.ASRBase, "siliconflow") {
		t.Fatalf("重载后配置丢失: %+v", c)
	}

	// "__KEEP__" 保留、空串清除、未知字段拒绝
	status, _ = e.json(t, http.MethodPut, "/api/settings", map[string]any{
		"ai": map[string]any{"apiKey": "__KEEP__", "asrApiKey": ""},
	}, nil)
	mustStatus(t, status, 200, "密钥占位保留")
	if c := e.h.settings.AIConfig(); c.LLMKey != "sk-unit-test-12345678" || c.ASRKey != "" {
		t.Fatalf("密钥保留/清除逻辑错误: %+v", c)
	}
	status, _ = e.json(t, http.MethodPut, "/api/settings", map[string]any{
		"ai": map[string]any{"evilField": "x"},
	}, nil)
	mustStatus(t, status, 400, "未知设置项应被拒绝")

	// 无密钥时连接测试给出明确反馈，不应 panic
	status, probe := e.json(t, http.MethodPost, "/api/ai/test", map[string]any{"target": "asr"}, nil)
	mustStatus(t, status, 200, "连接测试")
	if probe["ok"] != false {
		t.Fatalf("无密钥应返回失败: %+v", probe)
	}
}

func TestProviderPresetsApply(t *testing.T) {
	e := newTestEnv(t)
	for _, tc := range []struct{ id, host string }{
		{"qwen", "dashscope.aliyuncs.com"},
		{"zhipu", "open.bigmodel.cn"},
		{"volcengine", "ark.cn-beijing.volces.com"},
		{"ollama", "11434"},
	} {
		status, _ := e.json(t, http.MethodPut, "/api/settings", map[string]any{
			"ai": map[string]any{"provider": tc.id},
		}, nil)
		mustStatus(t, status, 200, "切换服务商 "+tc.id)
		if c := e.h.settings.AIConfig(); !strings.Contains(c.LLMBase, tc.host) {
			t.Fatalf("%s 预设地址错误: %s", tc.id, c.LLMBase)
		}
	}
}

func TestSelfSignedCert(t *testing.T) {
	t.Setenv("TLS_SAN", "10.0.0.5,nas.local")
	dir := t.TempDir()
	cert, key, ca, err := ensureTLS(dir)
	if err != nil {
		t.Fatalf("生成证书失败: %v", err)
	}
	for _, f := range []string{cert, key, ca} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("证书文件缺失 %s: %v", f, err)
		}
	}
	b, _ := os.ReadFile(cert)
	block, _ := pem.Decode(b)
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !containsStr(c.DNSNames, "nas.local") {
		t.Fatalf("SAN 域名缺失: %v", c.DNSNames)
	}
	foundIP := false
	for _, ip := range c.IPAddresses {
		if ip.String() == "10.0.0.5" {
			foundIP = true
		}
	}
	if !foundIP {
		t.Fatalf("SAN IP 缺失: %v", c.IPAddresses)
	}
	cert2, _, _, err := ensureTLS(dir)
	if err != nil || cert2 != cert {
		t.Fatal("证书应可复用")
	}
}

func TestCAEndpoints(t *testing.T) {
	e := newTestEnv(t)
	for _, p := range []string{"/api/ca.crt", "/api/ca.mobileconfig"} {
		code, _ := e.json(t, http.MethodGet, p, nil, nil)
		mustStatus(t, code, 404, "未启用内置 HTTPS 时 "+p)
	}
	dir := t.TempDir()
	_, _, ca, err := ensureTLS(dir)
	if err != nil {
		t.Fatalf("生成证书失败: %v", err)
	}
	e.h.caFile = ca
	caPEM, _ := os.ReadFile(ca)

	rec := e.request(t, http.MethodGet, "/api/ca.crt", nil, nil)
	mustStatus(t, rec.Code, 200, "ca.crt")
	if ct := rec.Header().Get("Content-Type"); ct != "application/x-x509-ca-cert" {
		t.Fatalf("ca.crt Content-Type 错误: %s", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), caPEM) {
		t.Fatal("ca.crt 应与 CA 证书内容完全一致")
	}

	rec = e.request(t, http.MethodGet, "/api/ca.mobileconfig", nil, nil)
	mustStatus(t, rec.Code, 200, "ca.mobileconfig")
	if ct := rec.Header().Get("Content-Type"); ct != "application/x-apple-aspen-config" {
		t.Fatalf("mobileconfig Content-Type 错误: %s", ct)
	}
	for _, want := range []string{"<plist", "com.apple.security.pem", "BEGIN CERTIFICATE", "PayloadDisplayName"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("mobileconfig 缺少 %q", want)
		}
	}
	dec := xml.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("mobileconfig XML 语法无效: %v", err)
		}
	}
}

func TestMobileCaptureAudioFormats(t *testing.T) {
	e := newTestEnv(t)
	_, memo := e.json(t, http.MethodPost, "/api/memos", map[string]any{"title": "手机系统录音"}, nil)
	id := memo["id"].(string)
	for _, tc := range []struct{ mime, ext string }{
		{"audio/3gpp", "3gp"}, {"audio/amr", "amr"},
		{"audio/mp4", "m4a"}, {"audio/x-caf", "caf"},
		{"application/octet-stream", "bin"},
	} {
		rec := e.request(t, http.MethodPut, "/api/memos/"+id+"/audio", []byte("fake-mobile-audio"), map[string]string{
			"Content-Type": tc.mime, "X-Audio-Mime": tc.mime, "X-Audio-Duration": "3000",
		})
		mustStatus(t, rec.Code, 200, "上传 "+tc.mime)
		var data map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &data)
		if !strings.HasSuffix(fmt.Sprint(data["audioFile"]), "."+tc.ext) {
			t.Fatalf("%s 应保存为 .%s，实际 %v", tc.mime, tc.ext, data["audioFile"])
		}
	}
}

func urlEncode(s string) string { return url.QueryEscape(s) }

func itoa(n int) string { return strconv.Itoa(n) }
