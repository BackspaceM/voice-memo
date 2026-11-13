package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Memo struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Note             string   `json:"note"`
	AudioFile        *string  `json:"audioFile"`
	AudioMime        *string  `json:"audioMime"`
	AudioSize        int64    `json:"audioSize"`
	DurationMs       int64    `json:"durationMs"`
	Transcript       string   `json:"transcript"`
	TranscriptStatus string   `json:"transcriptStatus"`
	Summary          string   `json:"summary"`
	AIStatus         string   `json:"aiStatus"`
	Tags             []string `json:"tags"`
	Folder           string   `json:"folder"`
	Starred          bool     `json:"starred"`
	RemindAt         *string  `json:"remindAt"`
	RemindedAt       *string  `json:"remindedAt"`
	CreatedAt        string   `json:"createdAt"`
	UpdatedAt        string   `json:"updatedAt"`
}

type dbState struct {
	Version int     `json:"version"`
	Memos   []*Memo `json:"memos"`
}

type StatItem struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Stats struct {
	Total   int        `json:"total"`
	Starred int        `json:"starred"`
	Due     int        `json:"due"`
	Today   int        `json:"today"`
	Folders []StatItem `json:"folders"`
	Tags    []StatItem `json:"tags"`
}

type ListOptions struct {
	Q      string
	Folder string
	Tag    string
	View   string
	Sort   string
}

type Store struct {
	mu       sync.RWMutex
	dir      string
	audioDir string
	dbFile   string
	memos    []*Memo
	warning  string
}

func cloneMemo(m *Memo) *Memo {
	if m == nil {
		return nil
	}
	c := *m
	c.Tags = append([]string{}, m.Tags...)
	if m.AudioFile != nil {
		v := *m.AudioFile
		c.AudioFile = &v
	}
	if m.AudioMime != nil {
		v := *m.AudioMime
		c.AudioMime = &v
	}
	if m.RemindAt != nil {
		v := *m.RemindAt
		c.RemindAt = &v
	}
	if m.RemindedAt != nil {
		v := *m.RemindedAt
		c.RemindedAt = &v
	}
	return &c
}

func normalizeMemo(m *Memo) {
	if m.Tags == nil {
		m.Tags = []string{}
	}
	if m.Folder == "" {
		m.Folder = "默认"
	}
	if m.TranscriptStatus == "" {
		m.TranscriptStatus = "none"
	}
	if m.AIStatus == "" {
		m.AIStatus = "none"
	}
	if m.Title == "" {
		m.Title = "未命名备忘录"
	}
}

func NewStore(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{
		dir:      abs,
		audioDir: filepath.Join(abs, "audio"),
		dbFile:   filepath.Join(abs, "memos.json"),
		memos:    []*Memo{},
	}
	if err := os.MkdirAll(s.audioDir, 0755); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	buf, err := os.ReadFile(s.dbFile)
	if os.IsNotExist(err) {
		return s.persistLocked()
	}
	if err != nil {
		return err
	}
	var state dbState
	if err := json.Unmarshal(buf, &state); err != nil {
		backup := fmt.Sprintf("%s.corrupt-%d", s.dbFile, time.Now().UnixMilli())
		_ = os.WriteFile(backup, buf, 0600)
		s.warning = fmt.Sprintf("memos.json 解析失败，原文件已备份为 %s", filepath.Base(backup))
		s.memos = []*Memo{}
		return s.persistLocked()
	}
	if state.Memos == nil {
		state.Memos = []*Memo{}
	}
	for _, m := range state.Memos {
		normalizeMemo(m)
	}
	s.memos = state.Memos
	return nil
}

// persistLocked 必须在持有写锁时调用：先写临时文件再原子重命名。
func (s *Store) persistLocked() error {
	if err := os.MkdirAll(s.audioDir, 0755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(dbState{Version: 1, Memos: s.memos}, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp-%d", s.dbFile, os.Getpid())
	if err := os.WriteFile(tmp, body, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.dbFile)
}

func newID() string {
	buf := make([]byte, 9)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("m_%d", time.Now().UnixNano())
	}
	return "m_" + base64.RawURLEncoding.EncodeToString(buf)
}

func nowISO() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func defaultTitle(now string) string {
	t, err := time.Parse(time.RFC3339Nano, now)
	if err != nil {
		t = time.Now()
	}
	return "语音备忘录 " + t.In(time.Local).Format("2006-01-02 15:04")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func (s *Store) Create(input map[string]any) (*Memo, *apiError) {
	now := nowISO()
	transcript := strVal(input["transcript"], 100000)
	summary := strVal(input["summary"], 4000)
	m := &Memo{
		ID:               newID(),
		Title:            firstNonEmpty(strVal(input["title"], 200), defaultTitle(now)),
		Note:             strVal(input["note"], 10000),
		AudioSize:        0,
		DurationMs:       max(0, numVal(input["durationMs"])),
		Transcript:       transcript,
		TranscriptStatus: "none",
		Summary:          summary,
		AIStatus:         "none",
		Tags:             tagsVal(input["tags"]),
		Folder:           firstNonEmpty(strVal(input["folder"], 60), "默认"),
		Starred:          boolVal(input["starred"]),
		RemindAt:         dateVal(input["remindAt"]),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if transcript != "" {
		m.TranscriptStatus = "manual"
	}
	if summary != "" {
		m.AIStatus = "manual"
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.memos = append([]*Memo{m}, s.memos...)
	if err := s.persistLocked(); err != nil {
		return nil, bad(500, "保存备忘录失败")
	}
	return cloneMemo(m), nil
}

func (s *Store) Get(id string) *Memo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.memos {
		if m.ID == id {
			return cloneMemo(m)
		}
	}
	return nil
}

var publicPatch = map[string]bool{
	"title": true, "note": true, "transcript": true, "summary": true,
	"tags": true, "folder": true, "starred": true, "remindAt": true,
	"durationMs": true,
}

var internalPatch = map[string]bool{
	"audioFile": true, "audioMime": true, "audioSize": true,
	"transcriptStatus": true, "aiStatus": true, "remindedAt": true,
}

func (s *Store) Update(id string, patch map[string]any, internal bool) (*Memo, *apiError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var target *Memo
	for _, m := range s.memos {
		if m.ID == id {
			target = m
			break
		}
	}
	if target == nil {
		return nil, nil
	}
	for key, value := range patch {
		if !publicPatch[key] && !(internal && internalPatch[key]) {
			continue
		}
		switch key {
		case "title":
			target.Title = firstNonEmpty(strVal(value, 200), target.Title)
		case "note":
			target.Note = strVal(value, 10000)
		case "transcript":
			target.Transcript = strVal(value, 100000)
		case "summary":
			target.Summary = strVal(value, 4000)
		case "tags":
			target.Tags = tagsVal(value)
		case "folder":
			target.Folder = firstNonEmpty(strVal(value, 60), "默认")
		case "starred":
			target.Starred = boolVal(value)
		case "remindAt":
			target.RemindAt = dateVal(value)
			target.RemindedAt = nil
		case "durationMs":
			target.DurationMs = max(0, numVal(value))
		case "audioFile":
			if v := strVal(value, 200); v != "" {
				target.AudioFile = &v
			} else {
				target.AudioFile = nil
			}
		case "audioMime":
			if v := strVal(value, 100); v != "" {
				target.AudioMime = &v
			} else {
				target.AudioMime = nil
			}
		case "audioSize":
			target.AudioSize = max(0, numVal(value))
		case "transcriptStatus":
			target.TranscriptStatus = strVal(value, 40)
		case "aiStatus":
			target.AIStatus = strVal(value, 40)
		case "remindedAt":
			target.RemindedAt = dateVal(value)
		}
	}
	target.UpdatedAt = nowISO()
	if err := s.persistLocked(); err != nil {
		return nil, bad(500, "保存备忘录失败")
	}
	return cloneMemo(target), nil
}

func (s *Store) Remove(id string) bool {
	s.mu.Lock()
	idx := -1
	var removed *Memo
	for i, m := range s.memos {
		if m.ID == id {
			idx = i
			removed = cloneMemo(m)
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return false
	}
	s.memos = append(s.memos[:idx], s.memos[idx+1:]...)
	err := s.persistLocked()
	s.mu.Unlock()
	if removed != nil && removed.AudioFile != nil {
		if p := s.AudioPath(removed); p != "" {
			_ = os.Remove(p)
		}
	}
	return err == nil
}

func (s *Store) AudioPath(m *Memo) string {
	if m == nil || m.AudioFile == nil {
		return ""
	}
	name := *m.AudioFile
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return ""
	}
	return filepath.Join(s.audioDir, name)
}

func isTodayISO(iso string) bool {
	t, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		return false
	}
	a := t.In(time.Local)
	b := time.Now().In(time.Local)
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}

func parseISOMillis(iso *string) (int64, bool) {
	if iso == nil || *iso == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339Nano, *iso)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func scoreMemo(m *Memo, query string) (int, bool) {
	tokens := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	if len(tokens) > 8 {
		tokens = tokens[:8]
	}
	type field struct {
		text   string
		weight int
	}
	fields := []field{
		{strings.ToLower(m.Title), 8},
		{strings.ToLower(strings.Join(m.Tags, " ")), 5},
		{strings.ToLower(m.Summary), 3},
		{strings.ToLower(m.Transcript), 2},
		{strings.ToLower(m.Note), 2},
		{strings.ToLower(m.Folder), 1},
	}
	score := 0
	for _, token := range tokens {
		hit := 0
		for _, f := range fields {
			if f.text == "" {
				continue
			}
			count := 0
			from := 0
			for count < 3 && from < len(f.text) {
				at := strings.Index(f.text[from:], token)
				if at < 0 {
					break
				}
				count++
				from += at + len(token)
			}
			hit += f.weight * count
		}
		if hit == 0 {
			return 0, false
		}
		score += hit
	}
	return score, true
}

func (s *Store) List(o ListOptions) []*Memo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := []*Memo{}
	for _, m := range s.memos {
		if o.View == "starred" && !m.Starred {
			continue
		}
		if o.View == "today" && !isTodayISO(m.CreatedAt) {
			continue
		}
		if o.View == "reminders" && m.RemindAt == nil {
			continue
		}
		if o.Folder != "" && m.Folder != o.Folder {
			continue
		}
		if o.Tag != "" && !containsStr(m.Tags, o.Tag) {
			continue
		}
		items = append(items, m)
	}

	out := []*Memo{}
	if strings.TrimSpace(o.Q) != "" {
		type scored struct {
			memo  *Memo
			score int
		}
		ranked := []scored{}
		for _, m := range items {
			if score, ok := scoreMemo(m, o.Q); ok {
				ranked = append(ranked, scored{m, score})
			}
		}
		sort.SliceStable(ranked, func(i, j int) bool {
			if ranked[i].score != ranked[j].score {
				return ranked[i].score > ranked[j].score
			}
			return ranked[i].memo.UpdatedAt > ranked[j].memo.UpdatedAt
		})
		for _, x := range ranked {
			out = append(out, cloneMemo(x.memo))
		}
		return out
	}

	sort.SliceStable(items, func(i, j int) bool {
		switch o.Sort {
		case "created":
			return items[i].CreatedAt > items[j].CreatedAt
		case "duration":
			return items[i].DurationMs > items[j].DurationMs
		default:
			return items[i].UpdatedAt > items[j].UpdatedAt
		}
	})
	for _, m := range items {
		out = append(out, cloneMemo(m))
	}
	return out
}

func (s *Store) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Stats{Folders: []StatItem{}, Tags: []StatItem{}}
	folderMap := map[string]int{}
	tagMap := map[string]int{}
	now := time.Now().UnixMilli()
	for _, m := range s.memos {
		st.Total++
		folderMap[m.Folder]++
		for _, t := range m.Tags {
			tagMap[t]++
		}
		if m.Starred {
			st.Starred++
		}
		if isTodayISO(m.CreatedAt) {
			st.Today++
		}
		if m.RemindAt != nil && m.RemindedAt == nil {
			if ms, ok := parseISOMillis(m.RemindAt); ok && ms <= now {
				st.Due++
			}
		}
	}
	for name, count := range folderMap {
		st.Folders = append(st.Folders, StatItem{name, count})
	}
	for name, count := range tagMap {
		st.Tags = append(st.Tags, StatItem{name, count})
	}
	sort.Slice(st.Folders, func(i, j int) bool { return st.Folders[i].Name < st.Folders[j].Name })
	sort.Slice(st.Tags, func(i, j int) bool {
		if st.Tags[i].Count != st.Tags[j].Count {
			return st.Tags[i].Count > st.Tags[j].Count
		}
		return st.Tags[i].Name < st.Tags[j].Name
	})
	return st
}

func (s *Store) DueReminders() []*Memo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UnixMilli()
	items := []*Memo{}
	for _, m := range s.memos {
		if m.RemindAt == nil || m.RemindedAt != nil {
			continue
		}
		if ms, ok := parseISOMillis(m.RemindAt); ok && ms <= now {
			items = append(items, m)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return *items[i].RemindAt < *items[j].RemindAt
	})
	out := make([]*Memo, 0, len(items))
	for _, m := range items {
		out = append(out, cloneMemo(m))
	}
	return out
}

func (s *Store) MarkReminded(id string) (*Memo, *apiError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.memos {
		if m.ID != id {
			continue
		}
		v := nowISO()
		m.RemindedAt = &v
		if err := s.persistLocked(); err != nil {
			return nil, bad(500, "保存备忘录失败")
		}
		return cloneMemo(m), nil
	}
	return nil, nil
}

func (s *Store) Warning() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.warning
}

func (s *Store) DataDir() string { return s.dir }
