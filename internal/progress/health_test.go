package progress

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTrackerAutomaticallyWritesHealthWithoutDoubleCounting(t *testing.T) {
	root := t.TempDir()
	tracker := New(filepath.Join(root, "runtime", "gvs.status.json"), nil)
	ctx := WithTracker(context.Background(), tracker)
	finish := Begin(ctx, "mr:7", "sha")
	Stage(ctx, "reviewing", "repo", "<script>alert(1)</script>")
	Attempt(ctx)
	Usage(ctx, 70, 30, 100, true)
	Cached(ctx)
	tracker.Publish()
	tracker.Publish()
	finish("interrupted")
	tracker.Publish()
	raw, err := os.ReadFile(filepath.Join(root, "health", "gvs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ledger healthLedger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		t.Fatal(err)
	}
	d := ledger.Days[healthDate(time.Now())]
	if d.TotalTokens != 100 || d.PromptTokens != 70 || d.CompletionTokens != 30 || d.Requests != 1 || d.CacheHits != 1 || d.Attempts != 1 || d.Interrupted != 1 {
		t.Fatalf("duplicate or lost usage: %+v", d)
	}
	if len(d.Objects) != 1 {
		t.Fatalf("metadata changes split an attempt: %+v", d.Objects)
	}
	page, err := os.ReadFile(filepath.Join(root, "health", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(page), "<script>alert(1)</script>") || !strings.Contains(string(page), "&lt;script&gt;") || !strings.Contains(string(page), "中断") {
		t.Fatalf("unsafe or missing report: %s", page)
	}
}

func TestHealthMonotonicCountersSurviveOldSnapshotAndIdleRestart(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	s := Snapshot{PID: 1, Task: "mr", Version: "a", StartedAt: now, State: "completed", FinishedAt: now, TotalTokens: 100}
	if err := recordHealth(dir, "gvs", s, now, true); err != nil {
		t.Fatal(err)
	}
	old := s
	old.TotalTokens, old.FinishedAt, old.State = 10, time.Time{}, "reviewing"
	for _, sample := range []Snapshot{old, s, {PID: 2, State: "idle"}, s} {
		if err := recordHealth(dir, "gvs", sample, now, true); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "gvs.json"))
	var ledger healthLedger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		t.Fatal(err)
	}
	d := ledger.Days[healthDate(now)]
	if d.TotalTokens != 100 || d.Completed != 1 || d.Attempts != 1 {
		t.Fatalf("replay counted twice: %+v", d)
	}
}

func TestHealthFollowsTrackerStartAndStop(t *testing.T) {
	root := t.TempDir()
	tracker := New(filepath.Join(root, "runtime", "gvs.status.json"), nil)
	stop := tracker.Run(context.Background(), time.Hour)
	path := filepath.Join(root, "health", "gvs.json")
	read := func() healthLedger {
		t.Helper()
		var ledger healthLedger
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &ledger); err != nil {
			t.Fatal(err)
		}
		return ledger
	}
	if !read().Running {
		t.Fatal("monitor not started with tracker")
	}
	stop()
	if read().Running {
		t.Fatal("monitor did not stop with tracker")
	}
}

func TestHealthCrossDayRestartAndMultipleInstances(t *testing.T) {
	dir := t.TempDir()
	before := time.Date(2026, 10, 27, 15, 59, 0, 0, time.UTC)
	s := Snapshot{PID: 10, Task: "mr:1", Version: "a", State: "reviewing", StartedAt: before, TotalTokens: 100, PromptTokens: 80, CompletionTokens: 20, Requests: 1, ElapsedSeconds: 60}
	if err := recordHealth(dir, "gvs", s, before, true); err != nil {
		t.Fatal(err)
	}
	after := before.Add(2 * time.Minute)
	s.TotalTokens, s.PromptTokens, s.Requests, s.ElapsedSeconds = 150, 130, 2, 180
	s.State, s.FinishedAt = "completed", after
	for i := 0; i < 2; i++ {
		if err := recordHealth(dir, "gvs", s, after, true); err != nil {
			t.Fatal(err)
		}
	}
	s.PID, s.StartedAt, s.State, s.FinishedAt = 11, after, "reviewing", time.Time{}
	s.TotalTokens, s.PromptTokens, s.CompletionTokens, s.Requests, s.ElapsedSeconds = 20, 10, 10, 1, 10
	if err := recordHealth(dir, "gvs", s, after, true); err != nil {
		t.Fatal(err)
	}
	if err := recordHealth(dir, "gvs-infer", s, after, true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "gvs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ledger healthLedger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		t.Fatal(err)
	}
	old, today := ledger.Days["2026-10-27"], ledger.Days["2026-10-28"]
	if old.TotalTokens != 100 || today.TotalTokens != 70 || today.Completed != 1 || today.Attempts != 1 || today.ElapsedSeconds != 130 {
		t.Fatalf("wrong daily delta: old=%+v new=%+v", old, today)
	}
	for _, name := range []string{"index.html", "2026-10-27.html", "2026-10-28.html"} {
		page, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "index.html" && (!strings.Contains(string(page), "gvs-infer") || !strings.Contains(string(page), "gvs")) {
			t.Fatal("missing instance")
		}
	}
}

func TestHealthRejectsCorruptLedgerWithoutOverwriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gvs.json")
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := recordHealth(dir, "gvs", Snapshot{}, time.Now(), true); err == nil {
		t.Fatal("corruption silently reset")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "broken" {
		t.Fatal("ledger overwritten")
	}
}

func TestHealthFailureDoesNotPreventReviewStatusPublication(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "health")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gvs.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	path := filepath.Join(root, "runtime", "gvs.status.json")
	tracker := New(path, slog.New(slog.NewTextHandler(&logs, nil)))
	ctx := WithTracker(context.Background(), tracker)
	finish := Begin(ctx, "mr", "sha")
	Usage(ctx, 4, 6, 10, true)
	finish("completed")
	var status Snapshot
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	if status.TotalTokens != 10 || status.State != "completed" || !strings.Contains(logs.String(), "项目健康度记录失败") {
		t.Fatalf("health failure affected review or was hidden: %+v %s", status, logs.String())
	}
}

func TestHealthRecommendationsHaveEvidence(t *testing.T) {
	d := healthDay{Attempts: 3, Interrupted: 1, Requests: 10, UsageMissing: 2, TotalTokens: 200000, PromptTokens: 190000, CompletionTokens: 10000,
		Objects: map[string]*healthObject{"mr": {Attempts: 3, TotalTokens: 200000}}}
	items := healthAdvice(d, healthDay{TotalTokens: 100000})
	joined := strings.Join(items, " ")
	for _, want := range []string{"usage", "中断", "重复", "缓存", "输入", "前日"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing evidence %q: %v", want, items)
		}
	}
}
