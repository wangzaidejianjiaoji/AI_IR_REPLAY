// Package progress records observable work without storing prompts or credentials.
package progress

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Snapshot struct {
	PID              int       `json:"pid"`
	State            string    `json:"state"`
	Task             string    `json:"task"`
	Version          string    `json:"version"`
	Repository       string    `json:"repository"`
	Title            string    `json:"title"`
	Stage            string    `json:"stage"`
	StartedAt        time.Time `json:"started_at"`
	FinishedAt       time.Time `json:"finished_at,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
	ElapsedSeconds   float64   `json:"elapsed_seconds"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	Requests         int       `json:"requests"`
	UsageMissing     int       `json:"usage_missing"`
	CacheHits        int       `json:"cache_hits"`
	ActiveJobs       []string  `json:"active_jobs,omitempty"`
}

const (
	historyKeep  = 100 // 历史文件保留的已结束审核条数上限
	historyShown = 10  // status 表格最多展示的历史行数
)

var tableHeader = []string{"审核对象", "阶段", "运行中的角色/包", "开始时间", "耗时", "Token"}

type Tracker struct {
	mu           sync.Mutex
	snapshot     Snapshot
	generation   uint64
	path         string
	historyPath  string
	logger       *slog.Logger
}

type trackerKey struct{}
type generationKey struct{}

func historyPathFor(path string) string {
	if strings.HasSuffix(path, ".status.json") {
		return strings.TrimSuffix(path, ".status.json") + ".history.jsonl"
	}
	return strings.TrimSuffix(path, filepath.Ext(path)) + ".history.jsonl"
}

func New(path string, logger *slog.Logger) *Tracker {
	return &Tracker{path: path, historyPath: historyPathFor(path), logger: logger, snapshot: Snapshot{PID: os.Getpid(), State: "idle", Stage: "waiting"}}
}

func WithTracker(ctx context.Context, t *Tracker) context.Context {
	return context.WithValue(ctx, trackerKey{}, t)
}

// ReviewContext pins callbacks to the active attempt; late work cannot affect the next one.
func ReviewContext(ctx context.Context) context.Context {
	if t, ok := ctx.Value(trackerKey{}).(*Tracker); ok {
		t.mu.Lock()
		generation := t.generation
		t.mu.Unlock()
		return context.WithValue(ctx, generationKey{}, generation)
	}
	return ctx
}

func change(ctx context.Context, fn func(*Snapshot)) {
	if t, ok := ctx.Value(trackerKey{}).(*Tracker); ok {
		t.mu.Lock()
		defer t.mu.Unlock()
		if generation, pinned := ctx.Value(generationKey{}).(uint64); pinned && generation != t.generation {
			return
		}
		fn(&t.snapshot)
	}
}

func Begin(ctx context.Context, task, version string) func(string) {
	t, ok := ctx.Value(trackerKey{}).(*Tracker)
	if !ok {
		return func(string) {}
	}
	t.mu.Lock()
	t.generation++
	generation := t.generation
	t.snapshot = Snapshot{PID: os.Getpid(), State: "reviewing", Task: task, Version: version, Stage: "fetching", StartedAt: time.Now()}
	t.mu.Unlock()
	t.Publish()
	return func(state string) {
		t.mu.Lock()
		var finished Snapshot
		terminal := false
		if generation == t.generation {
			t.snapshot.State = state
			t.snapshot.FinishedAt = time.Now()
			if !t.snapshot.StartedAt.IsZero() {
				t.snapshot.ElapsedSeconds = t.snapshot.FinishedAt.Sub(t.snapshot.StartedAt).Seconds()
			}
			finished = t.snapshot
			terminal = true
		}
		t.mu.Unlock()
		t.Publish()
		if terminal {
			if err := appendHistory(t.historyPath, finished); err != nil && t.logger != nil {
				t.logger.Warn("保存审核历史失败", "error", err)
			}
		}
	}
}

// appendHistory 追加一条已结束审核并按 historyKeep 截断，避免历史文件无限增长。
func appendHistory(path string, s Snapshot) error {
	entries := append(readHistory(path), s)
	if excess := len(entries) - historyKeep; excess > 0 {
		entries = entries[excess:]
	}
	var buf bytes.Buffer
	for _, entry := range entries {
		raw, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		buf.Write(raw)
		buf.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".history-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// readHistory 尽力读取历史条目；损坏行跳过，读取失败视为无历史。
func readHistory(path string) []Snapshot {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var entries []Snapshot
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var s Snapshot
		if json.Unmarshal([]byte(line), &s) == nil && s.Task != "" {
			entries = append(entries, s)
		}
	}
	return entries
}

func Stage(ctx context.Context, stage, repository, title string) {
	change(ctx, func(s *Snapshot) {
		s.Stage = stage
		if repository != "" {
			s.Repository = repository
		}
		if title != "" {
			s.Title = title
		}
	})
}

func Attempt(ctx context.Context) { change(ctx, func(s *Snapshot) { s.Requests++ }) }
func Cached(ctx context.Context)  { change(ctx, func(s *Snapshot) { s.CacheHits++ }) }

func Job(ctx context.Context, name string) func() {
	change(ctx, func(s *Snapshot) { s.ActiveJobs = append(s.ActiveJobs, name); sort.Strings(s.ActiveJobs) })
	return func() {
		change(ctx, func(s *Snapshot) {
			for i, job := range s.ActiveJobs {
				if job == name {
					s.ActiveJobs = append(s.ActiveJobs[:i], s.ActiveJobs[i+1:]...)
					break
				}
			}
		})
	}
}
func Usage(ctx context.Context, prompt, completion, total int, reported bool) {
	change(ctx, func(s *Snapshot) {
		if !reported {
			s.UsageMissing++
			return
		}
		s.PromptTokens += prompt
		s.CompletionTokens += completion
		if total == 0 {
			total = prompt + completion
		}
		s.TotalTokens += total
	})
}

// Publish serialises both writes and snapshots so an old heartbeat cannot overwrite completion.
func (t *Tracker) Publish() {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := &t.snapshot
	s.UpdatedAt = time.Now()
	if !s.StartedAt.IsZero() {
		end := s.FinishedAt
		if end.IsZero() {
			end = s.UpdatedAt
		}
		s.ElapsedSeconds = end.Sub(s.StartedAt).Seconds()
	}
	err := save(t.path, *s)
	if t.logger != nil {
		if err != nil {
			t.logger.Warn("保存运行状态失败", "error", err)
		}
		t.logger.Info("运行状态", "state", s.State, "task", s.Task, "repo", s.Repository, "title", s.Title, "stage", s.Stage, "active_jobs", s.ActiveJobs, "started_at", s.StartedAt, "elapsed_seconds", int(s.ElapsedSeconds), "prompt_tokens", s.PromptTokens, "completion_tokens", s.CompletionTokens, "total_tokens", s.TotalTokens, "requests", s.Requests, "usage_missing", s.UsageMissing, "cache_hits", s.CacheHits)
	}
}

func save(path string, s Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".status-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = json.NewEncoder(f).Encode(s); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (t *Tracker) Run(ctx context.Context, interval time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	t.Publish()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				t.Publish()
			}
		}
	}()
	return func() { cancel(); <-done; t.Publish() }
}

// Print 以表格展示审核进度：仅当 pid 对应的存活审核进程正在审核时展示当前行，
// 否则只罗列历史（倒序，最多 historyShown 条）；pid<=0 表示实例已停止，只罗列历史。
func Print(path string, pid int, output io.Writer) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var s Snapshot
	haveStatus := err == nil
	if haveStatus {
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
	}
	live := pid > 0 && haveStatus && s.PID == pid && time.Since(s.UpdatedAt) <= time.Minute
	history := readHistory(historyPathFor(path))
	if live && s.State == "reviewing" {
		renderTable(output, append([][]string{snapshotRow(&s, true)}, historyRows(history)...))
		return nil
	}
	if haveStatus && pid > 0 && s.State == "reviewing" {
		fmt.Fprintln(output, "  review status stale; inspect agent logs")
	}
	rows := historyRows(history)
	if len(rows) == 0 {
		if !haveStatus && pid > 0 {
			fmt.Fprintln(output, "  review status unavailable (older agent or not started)")
		}
		return nil
	}
	renderTable(output, rows)
	return nil
}

// historyRows 将历史条目转为倒序表格行。
func historyRows(history []Snapshot) [][]string {
	rows := make([][]string, 0, historyShown)
	for i := len(history) - 1; i >= 0 && len(rows) < historyShown; i-- {
		rows = append(rows, snapshotRow(&history[i], false))
	}
	return rows
}

// snapshotRow 把一次审核快照转成表格行；live 时耗时按当前时刻实时计算。
func snapshotRow(s *Snapshot, live bool) []string {
	elapsed := s.ElapsedSeconds
	if live && !s.StartedAt.IsZero() {
		elapsed = time.Since(s.StartedAt).Seconds()
	}
	stage := s.Stage
	if stage == "" {
		stage = s.State
	} else if !live {
		switch s.State {
		case "completed":
			stage = "完成"
		case "interrupted":
			stage = "中断·" + stage
		}
	}
	return []string{objectLabel(s), stage, jobsLabel(s.ActiveJobs), startedLabel(s.StartedAt), formatDuration(elapsed), tokenLabel(s)}
}

// objectLabel 汇总审核对象：任务标识 + 标题。
func objectLabel(s *Snapshot) string {
	object := s.Task
	if object == "" {
		object = s.Repository
	}
	if s.Title != "" {
		object = strings.TrimSpace(object + " " + s.Title)
	}
	if object == "" {
		object = "-"
	}
	return truncate(object, 60)
}

func jobsLabel(jobs []string) string {
	if len(jobs) == 0 {
		return "-"
	}
	return truncate(strings.Join(jobs, ","), 40)
}

func startedLabel(started time.Time) string {
	if started.IsZero() {
		return "-"
	}
	return started.Local().Format("2006-01-02 15:04:05")
}

func formatDuration(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	d := time.Duration(seconds * float64(time.Second)).Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm%ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// tokenLabel 展示合计 token 及输入/输出拆分。
func tokenLabel(s *Snapshot) string {
	if s.TotalTokens == 0 && s.PromptTokens == 0 && s.CompletionTokens == 0 {
		return "0"
	}
	return fmt.Sprintf("%d (%d+%d)", s.TotalTokens, s.PromptTokens, s.CompletionTokens)
}

// truncate 按显示宽度截断并在末尾补省略号。
func truncate(s string, max int) string {
	if displayWidth(s) <= max {
		return s
	}
	var b strings.Builder
	width := 0
	for _, r := range s {
		w := runeWidth(r)
		if width+w > max-1 {
			break
		}
		b.WriteRune(r)
		width += w
	}
	return b.String() + "…"
}

// runeWidth 估算终端显示宽度：CJK 等宽字符按 2 列计，其余按 1 列。
func runeWidth(r rune) int {
	switch {
	case r >= 0x1100 && r <= 0x115F, // 谚文
		r >= 0x2E80 && r <= 0x303E, // CJK 部首与符号
		r >= 0x3041 && r <= 0x33FF, // 假名与 CJK 兼容
		r >= 0x3400 && r <= 0x4DBF,
		r >= 0x4E00 && r <= 0x9FFF, // CJK 统一表意
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFF00 && r <= 0xFF60, // 全角形式
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x20000 && r <= 0x2FFFD,
		r >= 0x30000 && r <= 0x3FFFD:
		return 2
	}
	return 1
}

func displayWidth(s string) int {
	width := 0
	for _, r := range s {
		width += runeWidth(r)
	}
	return width
}

// renderTable 按终端显示宽度对齐输出表格。
func renderTable(output io.Writer, rows [][]string) {
	const gap = 2
	widths := make([]int, len(tableHeader))
	for _, row := range append([][]string{tableHeader}, rows...) {
		for i, cell := range row {
			if i < len(widths) {
				if w := displayWidth(cell); w > widths[i] {
					widths[i] = w
				}
			}
		}
	}
	writeRow := func(row []string) {
		line := "  "
		for i, cell := range row {
			if i == len(row)-1 {
				line += cell
				break
			}
			line += cell + strings.Repeat(" ", widths[i]-displayWidth(cell)+gap)
		}
		fmt.Fprintln(output, line)
	}
	writeRow(tableHeader)
	total := gap * (len(tableHeader) - 1)
	for _, w := range widths {
		total += w
	}
	fmt.Fprintln(output, "  "+strings.Repeat("-", total))
	for _, row := range rows {
		writeRow(row)
	}
}
