package main

import (
	"context"
	"crypto/tls"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"
)

var memoPathRe = regexp.MustCompile(`^/api/memos/([A-Za-z0-9_-]+)(?:/(audio|transcribe|analyze|export))?$`)
var remindDoneRe = regexp.MustCompile(`^/api/reminders/([A-Za-z0-9_-]+)/done$`)

type handler struct {
	st        *Store
	ai        *aiService
	settings  *SettingsStore
	publicDir string
	caFile    string
	https     bool
}

func (h *handler) limits() Limits { return h.settings.Limits() }

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[panic] %s %s: %v", r.Method, r.URL.Path, rec)
			writeError(w, bad(500, "服务器内部错误"))
		}
	}()
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		serveStatic(w, r, h.publicDir)
		return
	}
	if e := h.route(w, r); e != nil {
		if e.Status >= 500 {
			log.Printf("[error] %s %s: %s", r.Method, r.URL.Path, e.Message)
		}
		writeError(w, e)
	}
}

func (h *handler) route(w http.ResponseWriter, r *http.Request) *apiError {
	p := r.URL.Path
	m := r.Method
	q := r.URL.Query()

	switch {
	case p == "/api/health" && m == http.MethodGet:
		sendJSON(w, 200, map[string]any{"ok": true, "time": nowISO()})
		return nil

	case p == "/api/config" && m == http.MethodGet:
		sendJSON(w, 200, h.ai.PublicConfig(h.st))
		return nil

	case p == "/api/settings" && m == http.MethodGet:
		sendJSON(w, 200, h.settings.View(h.st, h.https))
		return nil

	case p == "/api/settings" && m == http.MethodPut:
		body, e := readJSON(r)
		if e != nil {
			return e
		}
		if _, e := h.settings.Update(body); e != nil {
			return e
		}
		sendJSON(w, 200, h.settings.View(h.st, h.https))
		return nil

	case p == "/api/ai/test" && m == http.MethodPost:
		body, e := readJSON(r)
		if e != nil {
			return e
		}
		target := "llm"
		if strVal(body["target"], 20) == "asr" {
			target = "asr"
		}
		status, message := h.ai.Test(target)
		sendJSON(w, 200, map[string]any{"ok": status == 200, "status": status, "message": message})
		return nil

	case p == "/api/ca.crt" && m == http.MethodGet:
		if h.caFile == "" {
			return bad(404, "未启用内置自签证书")
		}
		b, err := os.ReadFile(h.caFile)
		if err != nil {
			return bad(404, "证书文件不可用")
		}
		w.Header().Set("Content-Type", "application/x-x509-ca-cert")
		w.Header().Set("Content-Disposition", `attachment; filename="voice-memo-ca.crt"`)
		w.WriteHeader(200)
		_, _ = w.Write(b)
		return nil

	case p == "/api/memos" && m == http.MethodGet:
		items := h.st.List(ListOptions{
			Q: q.Get("q"), Folder: q.Get("folder"), Tag: q.Get("tag"),
			View: firstNonEmpty(q.Get("view"), "all"),
			Sort: firstNonEmpty(q.Get("sort"), "updated"),
		})
		sendJSON(w, 200, map[string]any{"items": items, "stats": h.st.Stats()})
		return nil

	case p == "/api/memos" && m == http.MethodPost:
		body, e := readJSON(r)
		if e != nil {
			return e
		}
		memo, e := h.st.Create(body)
		if e != nil {
			return e
		}
		sendJSON(w, 201, memo)
		return nil

	case p == "/api/reminders" && m == http.MethodGet:
		sendJSON(w, 200, map[string]any{"items": h.st.DueReminders()})
		return nil

	case p == "/api/export":
		limit := h.limits().MaxExportItems
		var ids []string
		if m == http.MethodGet {
			ids = parseIDs(limit, q.Get("ids"), q.Get("id"))
		} else if m == http.MethodPost {
			body, e := readJSON(r)
			if e != nil {
				return e
			}
			ids = idsFromValue(body["ids"], limit)
			if len(ids) == 0 {
				ids = idsFromValue(body["id"], limit)
			}
		} else {
			return bad(405, "不支持的请求方法")
		}
		return h.export(w, ids)
	}

	if match := remindDoneRe.FindStringSubmatch(p); match != nil && m == http.MethodPost {
		memo, e := h.st.MarkReminded(match[1])
		if e != nil {
			return e
		}
		if memo == nil {
			return bad(404, "备忘录不存在")
		}
		sendJSON(w, 200, memo)
		return nil
	}

	match := memoPathRe.FindStringSubmatch(p)
	if match == nil {
		return bad(404, "接口不存在")
	}
	id, action := match[1], match[2]
	memo := h.st.Get(id)
	if memo == nil {
		return bad(404, "备忘录不存在")
	}

	if action == "" {
		switch m {
		case http.MethodGet:
			sendJSON(w, 200, memo)
			return nil
		case http.MethodPatch:
			body, e := readJSON(r)
			if e != nil {
				return e
			}
			patch := map[string]any{}
			for key, value := range body {
				if publicPatch[key] {
					patch[key] = value
				}
			}
			if _, ok := patch["transcript"]; ok {
				patch["transcriptStatus"] = "manual"
			}
			if _, ok := patch["summary"]; ok {
				patch["aiStatus"] = "manual"
			}
			if _, ok := patch["tags"]; ok {
				patch["aiStatus"] = "manual"
			}
			updated, e := h.st.Update(id, patch, true)
			if e != nil {
				return e
			}
			sendJSON(w, 200, updated)
			return nil
		case http.MethodDelete:
			h.st.Remove(id)
			sendJSON(w, 200, map[string]any{"ok": true})
			return nil
		default:
			return bad(405, "不支持的请求方法")
		}
	}

	switch action {
	case "audio":
		if m == http.MethodGet || m == http.MethodHead {
			file := h.st.AudioPath(memo)
			if file == "" {
				return bad(404, "该备忘录暂无音频")
			}
			mimeType := ""
			if memo.AudioMime != nil {
				mimeType = *memo.AudioMime
			}
			serveAudio(w, r, file, mimeType)
			return nil
		}
		if m == http.MethodPut {
			return h.uploadAudio(w, r, memo)
		}
		return bad(405, "不支持的请求方法")

	case "transcribe":
		if m != http.MethodPost {
			return bad(405, "不支持的请求方法")
		}
		return h.transcribe(w, memo)

	case "analyze":
		if m != http.MethodPost {
			return bad(405, "不支持的请求方法")
		}
		return h.analyze(w, memo)

	case "export":
		if m != http.MethodGet {
			return bad(405, "不支持的请求方法")
		}
		return h.export(w, []string{id})
	}
	return bad(405, "不支持的请求方法")
}

func parseIDs(limit int, values ...string) []string {
	if limit <= 0 {
		limit = 1 << 30
	}
	out := []string{}
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

func idsFromValue(v any, limit int) []string {
	if limit <= 0 {
		limit = 1 << 30
	}
	switch x := v.(type) {
	case string:
		return parseIDs(limit, x)
	case []any:
		out := []string{}
		for _, item := range x {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
				if len(out) >= limit {
					break
				}
			}
		}
		return out
	}
	return []string{}
}

func (h *handler) uploadAudio(w http.ResponseWriter, r *http.Request, memo *Memo) *apiError {
	raw := firstNonEmpty(r.Header.Get("X-Audio-Mime"), r.Header.Get("Content-Type"), "audio/webm")
	mimeType := strings.ToLower(strings.TrimSpace(strings.Split(raw, ";")[0]))
	ext, ok := audioExt[mimeType]
	if !ok {
		return bad(415, "不支持的音频格式："+mimeType)
	}
	filename := memo.ID + "." + ext
	size, e := saveAudio(r, h.st.audioDir, filename, h.limits().MaxAudioBytes)
	if e != nil {
		return e
	}
	if old := h.st.AudioPath(memo); old != "" && filepath.Base(old) != filename {
		_ = os.Remove(old)
	}
	patch := map[string]any{
		"audioFile": filename,
		"audioMime": mimeType,
		"audioSize": size,
	}
	if d, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get("X-Audio-Duration")), 10, 64); err == nil && d > 0 {
		patch["durationMs"] = d
	}
	updated, e := h.st.Update(memo.ID, patch, true)
	if e != nil {
		return e
	}
	sendJSON(w, 200, updated)
	return nil
}

func (h *handler) transcribe(w http.ResponseWriter, memo *Memo) *apiError {
	path := h.st.AudioPath(memo)
	if path == "" {
		return bad(400, "请先录制或导入音频")
	}
	if _, err := os.Stat(path); err != nil {
		return bad(400, "请先录制或导入音频")
	}
	_, _ = h.st.Update(memo.ID, map[string]any{"transcriptStatus": "pending"}, true)
	mimeType := ""
	if memo.AudioMime != nil {
		mimeType = *memo.AudioMime
	}
	ext := "bin"
	if v, ok := audioExt[mimeType]; ok {
		ext = v
	}
	text, e := h.ai.Transcribe(path, mimeType, memo.ID+"."+ext)
	if e != nil {
		updated, _ := h.st.Update(memo.ID, map[string]any{"transcriptStatus": "failed"}, true)
		if e.Code != "NO_ASR" {
			e.Status = 502
		}
		e.Memo = updated
		return e
	}
	updated, ue := h.st.Update(memo.ID, map[string]any{"transcript": text, "transcriptStatus": "done"}, true)
	if ue != nil {
		return ue
	}
	sendJSON(w, 200, updated)
	return nil
}

func (h *handler) analyze(w http.ResponseWriter, memo *Memo) *apiError {
	_, _ = h.st.Update(memo.ID, map[string]any{"aiStatus": "pending"}, true)
	summary, tags, e := h.ai.Analyze(memo)
	if e != nil {
		updated, _ := h.st.Update(memo.ID, map[string]any{"aiStatus": "failed"}, true)
		e.Memo = updated
		return e
	}
	patch := map[string]any{"summary": summary, "aiStatus": "done"}
	fresh := h.st.Get(memo.ID)
	if fresh != nil && len(fresh.Tags) == 0 {
		patch["tags"] = tags
	}
	updated, ue := h.st.Update(memo.ID, patch, true)
	if ue != nil {
		return ue
	}
	sendJSON(w, 200, updated)
	return nil
}

func (h *handler) export(w http.ResponseWriter, ids []string) *apiError {
	memos := []*Memo{}
	for _, id := range ids {
		if memo := h.st.Get(id); memo != nil {
			memos = append(memos, memo)
		}
	}
	if len(memos) == 0 {
		return bad(404, "没有可导出的备忘录")
	}
	// 写到数据目录下的临时文件再回传：ZIP 边生成边落盘，大音频不会占满内存。
	tmp, err := os.CreateTemp(h.st.dir, ".export-*.zip")
	if err != nil {
		return bad(500, "生成导出包失败")
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if e := writeExport(h.st, memos, tmp, h.limits().MaxExportBytes); e != nil {
		return e
	}
	info, err := tmp.Stat()
	if err != nil {
		return bad(500, "生成导出包失败")
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return bad(500, "生成导出包失败")
	}
	sendStream(w, tmp, info.Size(), "application/zip", exportFilename(memos))
	return nil
}

// healthCheck 供容器 HEALTHCHECK 使用：scratch 镜像没有 wget/curl，由服务自身完成探测。
func healthCheck(port int) int {
	client := &http.Client{Timeout: 3 * time.Second}
	scheme := "http"
	if httpsEnabled() {
		scheme = "https"
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec 本机自签证书健康检查
	}
	url := scheme + "://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + "/api/health"
	resp, err := client.Get(url)
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func main() {
	port := flag.Int("port", 21087, "HTTP 服务端口")
	host := flag.String("host", "0.0.0.0", "监听地址")
	health := flag.Bool("health", false, "容器健康检查：请求本机 /api/health 后退出")
	flag.Parse()
	if *health {
		p := *port
		if v := strings.TrimSpace(os.Getenv("PORT")); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				p = n
			}
		}
		os.Exit(healthCheck(p))
	}
	if v := strings.TrimSpace(os.Getenv("PORT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*port = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("HOST")); v != "" {
		*host = v
	}

	dataDir := firstNonEmpty(os.Getenv("UGAPP_DATA_DIR"), os.Getenv("DATA_DIR"), "./data")
	publicDir := firstNonEmpty(os.Getenv("PUBLIC_DIR"), filepath.Join(os.Getenv("UGAPP_INSTALL_DIR"), "www"), "./www")

	applyIdentity(dataDir)

	st, err := NewStore(dataDir)
	if err != nil {
		log.Printf("诊断：uid=%d gid=%d 数据目录=%s", os.Geteuid(), os.Getegid(), dataDir)
		if info, statErr := os.Stat(dataDir); statErr == nil {
			log.Printf("诊断：目录权限=%s", info.Mode())
		} else {
			log.Printf("诊断：目录不可访问=%v", statErr)
		}
		log.Fatalf("初始化数据目录失败: %v（NAS 共享目录请设置 PUID/PGID 为其属主，或保持默认 root）", err)
	}
	settings := NewSettingsStore(dataDir)
	ai := newAI(settings)
	h := &handler{
		st: st, ai: ai, settings: settings, publicDir: publicDir,
		https: httpsEnabled(),
	}

	srv := &http.Server{
		Addr:              net.JoinHostPort(*host, strconv.Itoa(*port)),
		Handler:           h,
		ReadHeaderTimeout: 15 * time.Second,
	}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		log.Println("收到停止信号，正在优雅退出…")
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	cfg := ai.PublicConfig(st)
	aic := settings.AIConfig()
	scheme := "http"
	if h.https {
		scheme = "https"
	}
	log.Printf("语音备忘录服务已启动 → %s://%s:%d", scheme, *host, *port)
	log.Printf("数据目录：%s", st.DataDir())
	log.Printf("录音格式：%s（Web 设置页可改）", settings.RecordFormat())
	log.Printf("语音转写：%v", map[bool]string{true: aic.ASRModel + " @ " + hostOf(aic.ASRBase), false: "未配置（自动降级为手动录入）"}[aic.ASRKey != ""])
	log.Printf("AI 摘要：%v", map[bool]string{true: aic.LLMModel + " @ " + hostOf(aic.LLMBase), false: "未配置（自动降级为手动填写）"}[aic.LLMKey != ""])
	if cfg["warning"] != nil {
		log.Printf("警告：%v", cfg["warning"])
	}

	var serveErr error
	if h.https {
		cert, key, ca, err := ensureTLS(dataDir)
		if err != nil {
			log.Fatalf("初始化 HTTPS 证书失败: %v", err)
		}
		h.caFile = ca
		srv.TLSConfig = tlsMinConfig()
		if rp := parseIntEnv("HTTPS_REDIRECT_PORT", 10, 0); rp > 0 && rp != *port {
			startRedirect(rp, *port)
		}
		serveErr = srv.ListenAndServeTLS(cert, key)
	} else {
		serveErr = srv.ListenAndServe()
	}
	if serveErr != nil && serveErr != http.ErrServerClosed {
		log.Fatalf("服务启动失败: %v", serveErr)
	}
	log.Println("服务已停止")
}
