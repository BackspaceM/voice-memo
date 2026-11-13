package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxJSONBytes = 2 << 20

// 上限来自 Web 设置（settings.json），未设置时回退到容器环境变量，0 表示不限制。
// 具体解析在 settings.go 的 SettingsStore.Limits() 中完成。

var audioExt = map[string]string{
	"audio/webm":               "webm",
	"audio/mp4":                "m4a",
	"audio/x-m4a":              "m4a",
	"audio/ogg":                "ogg",
	"audio/wav":                "wav",
	"audio/x-wav":              "wav",
	"audio/mpeg":               "mp3",
	"audio/aac":                "aac",
	"audio/flac":               "flac",
	"application/octet-stream": "bin",
}

var staticMIME = map[string]string{
	".html":  "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".ico":   "image/x-icon",
	".woff2": "font/woff2",
}

// apiError 同时承载 HTTP 状态、业务错误码与可选的备忘录快照，
// 用于转写/摘要降级时把更新后的状态一并回传前端。
type apiError struct {
	Status  int
	Message string
	Code    string
	Memo    *Memo
}

func (e *apiError) Error() string { return e.Message }

func bad(status int, msg string) *apiError {
	return &apiError{Status: status, Message: msg}
}

func sendJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		body = []byte(`{"error":"序列化响应失败"}`)
		status = http.StatusInternalServerError
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeError(w http.ResponseWriter, e *apiError) {
	if e == nil {
		return
	}
	body := map[string]any{"error": e.Message}
	if e.Code != "" {
		body["code"] = e.Code
	}
	if e.Memo != nil {
		body["memo"] = e.Memo
	}
	sendJSON(w, e.Status, body)
}

func readBody(r *http.Request, limit int64) ([]byte, *apiError) {
	if r.Body == nil {
		return []byte{}, nil
	}
	defer r.Body.Close()
	buf, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, bad(http.StatusBadRequest, "请求读取失败")
	}
	if int64(len(buf)) > limit {
		return nil, bad(http.StatusRequestEntityTooLarge,
			fmt.Sprintf("请求体超过 %dMB 限制", limit>>20))
	}
	return buf, nil
}

// saveAudio 流式把请求体写入临时文件再原子替换：单条音频默认不限制大小，
// 且不会把整个文件读进内存（原实现用 io.ReadAll，大文件会 OOM）。
func saveAudio(r *http.Request, dir, filename string, limit int64) (int64, *apiError) {
	if r.Body == nil {
		return 0, bad(http.StatusBadRequest, "音频内容为空")
	}
	defer r.Body.Close()
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return 0, bad(500, "保存音频失败："+err.Error())
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	defer tmp.Close()

	var src io.Reader = r.Body
	if limit > 0 {
		src = io.LimitReader(r.Body, limit+1)
	}
	n, err := io.Copy(tmp, src)
	if err != nil {
		return 0, bad(400, "音频读取失败")
	}
	if limit > 0 && n > limit {
		return 0, bad(http.StatusRequestEntityTooLarge,
			fmt.Sprintf("音频超过 %dMB 限制（可用 MAX_AUDIO_MB 调整）", limit>>20))
	}
	if n == 0 {
		return 0, bad(400, "音频内容为空")
	}
	if err := tmp.Sync(); err != nil {
		return 0, bad(500, "保存音频失败")
	}
	if err := tmp.Close(); err != nil {
		return 0, bad(500, "保存音频失败")
	}
	if err := os.Rename(tmpName, filepath.Join(dir, filename)); err != nil {
		return 0, bad(500, "保存音频失败")
	}
	return n, nil
}

func sendStream(w http.ResponseWriter, f *os.File, size int64, contentType, filename string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"export.zip\"; filename*=UTF-8''%s", uriEncode(filename)))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func readJSON(r *http.Request) (map[string]any, *apiError) {
	buf, e := readBody(r, maxJSONBytes)
	if e != nil {
		return nil, e
	}
	if len(bytes.TrimSpace(buf)) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(buf))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, bad(http.StatusBadRequest, "JSON 格式错误")
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, bad(http.StatusBadRequest, "请求体必须是 JSON 对象")
	}
	return obj, nil
}

func strVal(v any, max int) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) > max {
		s = string(runes[:max])
	}
	return s
}

func numVal(v any) int64 {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return int64(f)
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	case int32:
		return int64(x)
	case uint64:
		return int64(x)
	case uint:
		return int64(x)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return int64(f)
	case bool:
		if x {
			return 1
		}
	}
	return 0
}

func boolVal(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1"
	case json.Number:
		return numVal(v) != 0
	case float64:
		return x != 0
	}
	return false
}

func tagsVal(v any) []string {
	out := []string{}
	arr, ok := v.([]any)
	if !ok {
		return out
	}
	seen := map[string]bool{}
	for _, raw := range arr {
		t, ok := raw.(string)
		if !ok {
			continue
		}
		t = strings.TrimSpace(t)
		runes := []rune(t)
		if len(runes) > 30 {
			t = string(runes[:30])
		}
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) >= 20 {
			break
		}
	}
	return out
}

func dateVal(v any) *string {
	if v == nil {
		return nil
	}
	var t time.Time
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return nil
		}
		layouts := []string{
			time.RFC3339Nano,
			"2006-01-02T15:04",
			"2006-01-02 15:04:05",
			"2006-01-02",
		}
		for _, layout := range layouts {
			if parsed, err := time.ParseInLocation(layout, s, time.Local); err == nil {
				t = parsed
				break
			}
		}
		if t.IsZero() {
			return nil
		}
	case json.Number:
		f, _ := x.Float64()
		t = time.UnixMilli(int64(f))
	case float64:
		t = time.UnixMilli(int64(x))
	default:
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}

func fmtDuration(ms int64) string {
	total := ms / 1000
	if total < 0 {
		total = 0
	}
	return fmt.Sprintf("%02d:%02d", total/60, total%60)
}

func fmtTime(iso *string) string {
	if iso == nil || *iso == "" {
		return "—"
	}
	t, err := time.Parse(time.RFC3339Nano, *iso)
	if err != nil {
		return "—"
	}
	return t.In(time.Local).Format("2006-01-02 15:04:05")
}

var unsafeNameRe = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]`)
var spacesRe = regexp.MustCompile(`\s+`)

func safeName(title string) string {
	s := unsafeNameRe.ReplaceAllString(title, "")
	s = spacesRe.ReplaceAllString(s, "-")
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) > 60 {
		s = string(runes[:60])
	}
	if s == "" {
		return "memo"
	}
	return s
}

func uriEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func serveStatic(w http.ResponseWriter, r *http.Request, root string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, bad(http.StatusMethodNotAllowed, "不支持的请求方法"))
		return
	}
	rel := strings.TrimPrefix(filepath.ToSlash(filepath.Clean("/"+strings.TrimPrefix(r.URL.Path, "/"))), "/")
	if rel == "" || rel == "." {
		rel = "index.html"
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		writeError(w, bad(http.StatusInternalServerError, "服务器内部错误"))
		return
	}
	file := filepath.Join(absRoot, filepath.FromSlash(rel))
	absFile, err := filepath.Abs(file)
	if err != nil || (absFile != absRoot && !strings.HasPrefix(absFile, absRoot+string(os.PathSeparator))) {
		writeError(w, bad(http.StatusForbidden, "禁止访问"))
		return
	}
	if info, err := os.Stat(absFile); err == nil && info.IsDir() {
		absFile = filepath.Join(absFile, "index.html")
	}
	if info, err := os.Stat(absFile); err != nil || info.IsDir() {
		absFile = filepath.Join(absRoot, "index.html") // SPA fallback
	}
	body, err := os.ReadFile(absFile)
	if err != nil {
		writeError(w, bad(http.StatusNotFound, "页面不存在"))
		return
	}
	h := w.Header()
	h.Set("Content-Type", staticMIME[strings.ToLower(filepath.Ext(absFile))])
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", "application/octet-stream")
	}
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

var rangeRe = regexp.MustCompile(`^bytes=(\d*)-(\d*)$`)

func serveAudio(w http.ResponseWriter, r *http.Request, file, mimeType string) {
	info, err := os.Stat(file)
	if err != nil {
		writeError(w, bad(http.StatusNotFound, "该备忘录暂无音频"))
		return
	}
	size := info.Size()
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Accept-Ranges", "bytes")
	h.Set("Content-Type", mimeType)
	h.Set("X-Content-Type-Options", "nosniff")

	raw := strings.TrimSpace(r.Header.Get("Range"))
	if raw == "" {
		h.Set("Content-Length", strconv.FormatInt(size, 10))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		f, err := os.Open(file)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = io.Copy(w, f)
		return
	}

	match := rangeRe.FindStringSubmatch(raw)
	if match == nil {
		h.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	var start, end int64
	if match[1] == "" && match[2] != "" {
		suffix, _ := strconv.ParseInt(match[2], 10, 64)
		start = size - suffix
		if start < 0 {
			start = 0
		}
		end = size - 1
	} else {
		start, _ = strconv.ParseInt(match[1], 10, 64)
		end = size - 1
		if match[2] != "" {
			if v, err := strconv.ParseInt(match[2], 10, 64); err == nil && v < end {
				end = v
			}
		}
	}
	if start > end || start >= size || size == 0 {
		h.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
	h.Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	w.WriteHeader(http.StatusPartialContent)
	if r.Method == http.MethodHead {
		return
	}
	f, err := os.Open(file)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Seek(start, io.SeekStart)
	_, _ = io.CopyN(w, f, end-start+1)
}

func sendBuffer(w http.ResponseWriter, buf []byte, contentType, filename string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.Itoa(len(buf)))
	h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"export.zip\"; filename*=UTF-8''%s", uriEncode(filename)))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
}
