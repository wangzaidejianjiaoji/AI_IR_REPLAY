package progress

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStatusPersistsActiveReviewUsageAndTerminalFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	var logs bytes.Buffer
	tracker := New(path, slog.New(slog.NewTextHandler(&logs, nil)))
	ctx := WithTracker(context.Background(), tracker)
	finish := Begin(ctx, "codeup:infer:7", "revision")
	Stage(ctx, "understanding", "infer", "Update streaming lifetime")
	endSecurity := Job(ctx, "security:streamer")
	endArchitect := Job(ctx, "architect:core")
	endArchitect()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); Usage(ctx, 7, 3, 10, true) }()
	}
	wg.Wait()
	Cached(ctx)
	tracker.Publish()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Snapshot
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Task != "codeup:infer:7" || got.Stage != "understanding" || got.TotalTokens != 100 || got.PromptTokens != 70 || got.CompletionTokens != 30 || got.CacheHits != 1 || got.StartedAt.IsZero() || got.PID != os.Getpid() || len(got.ActiveJobs) != 1 || got.ActiveJobs[0] != "security:streamer" {
		t.Fatalf("status=%+v", got)
	}
	// 进行中的审核：当前行带表头展示，历史为空时不输出历史行；
	// 运行中的角色/包不进表格，仅在日志"运行状态"行输出。
	logs.Reset()
	if err := Print(path, os.Getpid(), &logs); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"审核对象", "infer Update streaming lifetime", "understanding", "100 (70+30)"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("live row missing %q:\n%s", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), "运行中的角色/包") || strings.Contains(logs.String(), "security:streamer") {
		t.Fatalf("active jobs should stay out of the table:\n%s", logs.String())
	}
	endSecurity()
	finish("interrupted")
	tracker.Publish()
	// 已结束：当前行不再展示，历史行记录结果与用量。
	logs.Reset()
	if err := Print(path, os.Getpid(), &logs); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"审核对象", "中断·understanding", "infer Update streaming lifetime", "100 (70+30)"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("history row missing %q:\n%s", want, logs.String())
		}
	}
	// 状态文件不属于发现进程：仍罗列历史，不展示当前行。
	logs.Reset()
	if err := Print(path, os.Getpid()+1, &logs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "中断·understanding") {
		t.Fatal("history not listed for mismatched pid")
	}
	// 状态文件声称审核中但无法核实归属：提示 stale 且不展示当前行。
	stale, err := json.Marshal(Snapshot{PID: os.Getpid() + 1, State: "reviewing", Task: "codeup:infer:9", Stage: "analysis", StartedAt: time.Now(), UpdatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, stale, 0600); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	if err := Print(path, os.Getpid(), &logs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "stale") || strings.Contains(logs.String(), "codeup:infer:9") {
		t.Fatalf("stale state presented as live:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "中断·understanding") {
		t.Fatalf("history missing alongside stale note:\n%s", logs.String())
	}
}

func TestHistoryAggregatesUnchangedReviewObject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gvs.status.json")
	base := time.Date(2026, 10, 26, 17, 48, 25, 0, time.Local)
	// 历史按时间正序追加：另一对象（单版本，已完成）+ 同一 MR 的两个版本各两次中断尝试。
	entries := []Snapshot{{
		Task: "mr:lizheng/lzvsg/gvs:541", Version: "aaa1111",
		State: "completed", Stage: "analysis",
		Repository: "lzvsg/gvs", Title: "MR !541 fix(qatool)",
		StartedAt: base.Add(-time.Hour), FinishedAt: base.Add(-time.Hour + time.Minute),
		ElapsedSeconds: 60, PromptTokens: 400, CompletionTokens: 100, TotalTokens: 500,
	}}
	for _, e := range []struct {
		version    string
		delay      time.Duration
		elapsed    float64
		prompt     int
		completion int
	}{
		{"cc46d2b1", 0, 1, 20, 5},
		{"cc46d2b1", 2 * time.Minute, 2, 10, 5},
		{"ffcf85f6", 10 * time.Minute, 1, 70, 10},
		{"ffcf85f6", 11 * time.Minute, 3, 10, 10},
	} {
		start := base.Add(e.delay)
		entries = append(entries, Snapshot{
			Task: "mr:lizheng/lzvsg/gvs:556", Version: e.version,
			State: "interrupted", Stage: "analysis",
			Repository: "lzvsg/gvs", Title: "MR !556 fix focus",
			StartedAt: start, FinishedAt: start.Add(time.Duration(e.elapsed * float64(time.Second))),
			ElapsedSeconds: e.elapsed, PromptTokens: e.prompt, CompletionTokens: e.completion,
			TotalTokens: e.prompt + e.completion,
		})
	}
	var buf bytes.Buffer
	for _, e := range entries {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(raw)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "gvs.history.jsonl"), buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	// pid<=0：只罗列历史；对象未变的多次尝试合并为一行。
	var logs bytes.Buffer
	if err := Print(path, 0, &logs); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	for _, want := range []string{
		"首次开始", "最近结束", "累计耗时", "尝试",
		"MR !556 fix focus @ffcf85f", "17:58:25", "17:59:28", "4s", "100 (80+20)",
		"MR !556 fix focus @cc46d2b", "17:48:25", "17:50:27", "3s", "40 (30+10)",
		"MR !541 fix(qatool)", "完成", "1m0s", "500 (400+100)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("aggregated row missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "MR !541 fix(qatool) @") {
		t.Fatalf("single-version object should not carry version suffix:\n%s", out)
	}
	// 组间按最近结束时间倒序：新版本在前，旧版本次之，另一对象最后。
	if strings.Index(out, "@ffcf85f") > strings.Index(out, "@cc46d2b") ||
		strings.Index(out, "@cc46d2b") > strings.Index(out, "MR !541") {
		t.Fatalf("groups not ordered by latest end desc:\n%s", out)
	}
}

func TestLiveAttemptMergesPriorRetriesOfSameVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gvs.status.json")
	base := time.Date(2026, 10, 26, 17, 48, 25, 0, time.Local)
	// 同一审核对象（任务+版本未变）的两次中断尝试先进入历史。
	entries := []Snapshot{
		{
			Task: "mr:lizheng/lzvsg/gvs:556", Version: "ffcf85f6",
			State: "interrupted", Stage: "analysis",
			Repository: "lzvsg/gvs", Title: "MR !556 fix focus",
			StartedAt: base, FinishedAt: base.Add(time.Second), ElapsedSeconds: 1,
			PromptTokens: 20, CompletionTokens: 5, TotalTokens: 25,
		},
		{
			Task: "mr:lizheng/lzvsg/gvs:556", Version: "ffcf85f6",
			State: "interrupted", Stage: "analysis",
			Repository: "lzvsg/gvs", Title: "MR !556 fix focus",
			StartedAt: base.Add(2 * time.Minute), FinishedAt: base.Add(2*time.Minute + 2*time.Second),
			ElapsedSeconds: 2, PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
		},
	}
	var buf bytes.Buffer
	for _, e := range entries {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(raw)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "gvs.history.jsonl"), buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	// 进行中的第三次尝试与历史同版本合并为首行：最早开始沿用首次尝试，token 三次累计。
	live, err := json.Marshal(Snapshot{
		PID: os.Getpid(), State: "reviewing", Task: "mr:lizheng/lzvsg/gvs:556", Version: "ffcf85f6",
		Stage: "analysis", Repository: "lzvsg/gvs", Title: "MR !556 fix focus",
		StartedAt: time.Now().Add(-5 * time.Second), UpdatedAt: time.Now(),
		PromptTokens: 10, TotalTokens: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, live, 0600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	if err := Print(path, os.Getpid(), &logs); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	for _, want := range []string{"进行中", "17:48:25", "50 (40+10)", "analysis"} {
		if !strings.Contains(out, want) {
			t.Fatalf("live merged row missing %q:\n%s", want, out)
		}
	}
	if got := strings.Count(out, "MR !556"); got != 1 {
		t.Fatalf("same-version retries should collapse into one row, got %d:\n%s", got, out)
	}
}

func TestOldAttemptCannotMutateNextReview(t *testing.T) {
	statusPath := filepath.Join(t.TempDir(), "status.json")
	tracker := New(statusPath, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := WithTracker(context.Background(), tracker)
	endOld := Begin(ctx, "old", "v1")
	oldCtx := ReviewContext(ctx)
	endOld("completed")
	finish := Begin(ctx, "new", "v2")
	Usage(oldCtx, 1, 1, 2, true)
	tracker.Publish()
	if tracker.snapshot.TotalTokens != 0 {
		t.Fatal("late result charged to next review")
	}
	finish("completed")
	stop := tracker.Run(context.Background(), time.Millisecond)
	stop()
	history := readHistory(historyPathFor(statusPath))
	if len(history) != 2 || history[0].Task != "old" || history[1].Task != "new" || history[1].TotalTokens != 0 || history[1].State != "completed" {
		t.Fatalf("history=%+v", history)
	}
}

func TestBranchReviewsAreNotRecordedOrDisplayed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	var logs bytes.Buffer
	tracker := New(path, slog.New(slog.NewTextHandler(&logs, nil)))
	ctx := WithTracker(context.Background(), tracker)
	finishBranch := Begin(ctx, "branch:lizheng/lzvsg/gvs:testCali", "v1")
	Stage(ctx, "analysis", "lzvsg/gvs", "分支 testCali 新推送")
	finishBranch("completed")
	finishMR := Begin(ctx, "mr:lizheng/lzvsg/gvs:541", "v2")
	Stage(ctx, "analysis", "lzvsg/gvs", "MR !541 fix(qatool)")
	finishMR("completed")
	history := readHistory(historyPathFor(path))
	if len(history) != 1 || history[0].Task != "mr:lizheng/lzvsg/gvs:541" {
		t.Fatalf("branch review leaked into history: %+v", history)
	}
	logs.Reset()
	if err := Print(path, os.Getpid(), &logs); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "testCali") || !strings.Contains(logs.String(), "lzvsg/gvs MR !541 fix(qatool)") {
		t.Fatalf("unexpected table:\n%s", logs.String())
	}
}
