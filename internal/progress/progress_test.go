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
	endSecurity()
	finish("interrupted")
	tracker.Publish()
	if err := Print(path, os.Getpid(), &logs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "interrupted") || !strings.Contains(logs.String(), "Update streaming lifetime") || !strings.Contains(logs.String(), "100") {
		t.Fatal(logs.String())
	}
	logs.Reset()
	if err := Print(path, os.Getpid()+1, &logs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "stale") {
		t.Fatal("stale process state presented as live")
	}
}

func TestOldAttemptCannotMutateNextReview(t *testing.T) {
	tracker := New(filepath.Join(t.TempDir(), "status.json"), slog.New(slog.NewTextHandler(io.Discard, nil)))
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
}
