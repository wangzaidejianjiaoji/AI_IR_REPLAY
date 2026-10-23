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
	// 进行中的审核：当前行带表头展示，历史为空时不输出历史行。
	logs.Reset()
	if err := Print(path, os.Getpid(), &logs); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"审核对象", "运行中的角色/包", "codeup:infer:7 Update streaming lifetime", "understanding", "security:streamer", "100 (70+30)"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("live row missing %q:\n%s", want, logs.String())
		}
	}
	endSecurity()
	finish("interrupted")
	tracker.Publish()
	// 已结束：当前行不再展示，历史行记录结果与用量。
	logs.Reset()
	if err := Print(path, os.Getpid(), &logs); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"审核对象", "中断·understanding", "codeup:infer:7 Update streaming lifetime", "100 (70+30)"} {
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
