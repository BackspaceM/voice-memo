package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func renderMarkdown(m *Memo) string {
	tags := "无"
	if len(m.Tags) > 0 {
		tags = strings.Join(m.Tags, "、")
	}
	lines := []string{
		"# " + firstNonEmpty(m.Title, "未命名备忘录"),
		"",
		"- 创建时间：" + fmtTime(&m.CreatedAt),
		"- 更新时间：" + fmtTime(&m.UpdatedAt),
		"- 时长：" + fmtDuration(m.DurationMs),
		"- 文件夹：" + firstNonEmpty(m.Folder, "默认"),
		"- 标签：" + tags,
		"- 提醒时间：" + fmtTime(m.RemindAt),
		"- 转写状态：" + m.TranscriptStatus,
		"",
		"## AI 摘要",
		"",
		firstNonEmpty(m.Summary, "（暂无摘要）"),
		"",
		"## 语音转写",
		"",
		firstNonEmpty(m.Transcript, "（暂无转写文本）"),
		"",
		"## 备注",
		"",
		firstNonEmpty(m.Note, "（无）"),
		"",
	}
	return strings.Join(lines, "\n")
}

func shortID(id string) string {
	runes := []rune(id)
	if len(runes) > 2 {
		runes = runes[2:]
	}
	if len(runes) > 6 {
		runes = runes[:6]
	}
	if len(runes) == 0 {
		return "memo"
	}
	return string(runes)
}

func writeZipEntry(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

type manifestMemo struct {
	*Memo
	AudioIncluded bool `json:"audioIncluded"`
}

// writeExport 流式生成 ZIP：音频直接从文件 copy 到 zip entry，不整块读进内存。
func writeExport(st *Store, memos []*Memo, out io.Writer, limit int64) *apiError {
	zw := zip.NewWriter(out)
	manifest := []manifestMemo{}
	var totalBytes int64

	for _, m := range memos {
		dir := fmt.Sprintf("%s-%s", safeName(m.Title), shortID(m.ID))
		if err := writeZipEntry(zw, dir+"/memo.md", []byte(renderMarkdown(m))); err != nil {
			_ = zw.Close()
			return bad(500, "生成导出包失败")
		}
		included := false
		if p := st.AudioPath(m); p != "" {
			f, err := os.Open(p)
			if err == nil {
				ext := "bin"
				if m.AudioMime != nil {
					if v, ok := audioExt[*m.AudioMime]; ok {
						ext = v
					}
				}
				if info, statErr := f.Stat(); statErr == nil && limit > 0 && totalBytes+info.Size() > limit {
					_ = f.Close()
					_ = zw.Close()
					return bad(413, fmt.Sprintf("音频总量超过 %dMB，请分批导出或调大 MAX_EXPORT_MB", limit>>20))
				}
				if entry, createErr := zw.Create(dir + "/audio." + ext); createErr == nil {
					n, copyErr := io.Copy(entry, f)
					totalBytes += n
					included = copyErr == nil
				}
				_ = f.Close()
			}
		}
		manifest = append(manifest, manifestMemo{cloneMemo(m), included})
	}

	manifestJSON, err := json.MarshalIndent(map[string]any{
		"app":        "voice-memo",
		"version":    1,
		"exportedAt": time.Now().UTC().Format(time.RFC3339),
		"count":      len(memos),
		"memos":      manifest,
	}, "", "  ")
	if err != nil {
		_ = zw.Close()
		return bad(500, "生成导出包失败")
	}
	if err := writeZipEntry(zw, "manifest.json", manifestJSON); err != nil {
		_ = zw.Close()
		return bad(500, "生成导出包失败")
	}
	if err := zw.Close(); err != nil {
		return bad(500, "生成导出包失败")
	}
	return nil
}

func exportFilename(memos []*Memo) string {
	if len(memos) == 1 {
		return safeName(memos[0].Title) + ".zip"
	}
	stamp := time.Now().UTC().Format("200601021504")
	return fmt.Sprintf("voice-memos-%s-%d条.zip", stamp, len(memos))
}
