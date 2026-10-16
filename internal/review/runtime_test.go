package review

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Simulate the next eligible polling interval without sleeping a minute.
func expireRuntimeRetries(t *testing.T, s *Service) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(s.cfg.Review.RuntimeDir, "tasks", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var job runtimeJob
		if err := json.Unmarshal(raw, &job); err != nil {
			t.Fatal(err)
		}
		job.RetryAt = time.Time{}
		if err := writeRuntime(path, job); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSharedExecutionSlotAcrossProcesses(t *testing.T) {
	if directory := os.Getenv("REVIEW_SLOT_TEST"); directory != "" {
		release, err := acquireExecution(directory)
		if err == nil {
			release()
			os.Exit(2)
		}
		os.Exit(0)
	}
	directory := t.TempDir()
	release, err := acquireExecution(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for i := 0; i < 2; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSharedExecutionSlotAcrossProcesses$")
		cmd.Env = append(os.Environ(), "REVIEW_SLOT_TEST="+directory)
		if err := cmd.Run(); err != nil {
			t.Fatal("another process entered occupied slot:", err)
		}
	}
}

func TestDurableQueueFairnessAndRecovery(t *testing.T) {
	directory := t.TempDir()
	q := runtimeQueue{directory: directory, owner: "gvs"}
	q2 := runtimeQueue{directory: directory, owner: "infer"}
	ctx := context.Background()
	if err := q.register(ctx, "mr:gvs:1", "source:target"); err != nil {
		t.Fatal(err)
	}
	if err := q2.register(ctx, "mr:infer:2", "source:target"); err != nil {
		t.Fatal(err)
	}
	first, err := q.next(ctx)
	if err != nil || first.Key != "mr:gvs:1" {
		t.Fatalf("%+v %v", first, err)
	}
	if err := q.finish(ctx, first.Key, context.DeadlineExceeded); err != nil {
		t.Fatal(err)
	}
	next, err := q2.next(ctx)
	if err != nil || next.Key != "mr:infer:2" {
		t.Fatalf("cooling job starved next: %+v %v", next, err)
	}
	if err := q2.finish(ctx, next.Key, nil); err != nil {
		t.Fatal(err)
	}
	// A restart preserves completed versions and makes a changed target eligible.
	q2 = runtimeQueue{directory: directory, owner: "infer"}
	if err := q2.register(ctx, next.Key, "source:new-target"); err != nil {
		t.Fatal(err)
	}
	next, err = q2.next(ctx)
	if err != nil || next.Version != "source:new-target" {
		t.Fatalf("target change lost: %+v %v", next, err)
	}
	// Reconstruct the queue as if the owner crashed while reviewing; the OS
	// execution lock, not a stale journal state, is authoritative for liveness.
	q2 = runtimeQueue{directory: directory, owner: "infer"}
	if err := q2.register(ctx, next.Key, next.Version); err != nil {
		t.Fatal(err)
	}
	got, err := q2.next(ctx)
	if err != nil || got.Key != next.Key {
		t.Fatalf("crashed task wedged: %+v %v", got, err)
	}
}

func TestPollingRunsOneReviewAndResumesOnNextRound(t *testing.T) {
	cu := newMockCodeup(t)
	model, calls := newMockLLM(t)
	cfg := testConfig(t, cu.srv.URL, model.URL)
	cfg.Codeup.ReviewFirstPush = true
	cu.setBranches(branch("master", "base", true), branch("one", "sha-one", false), branch("two", "sha-two", false))
	s := newTestService(t, cfg)
	s.RunOnce(context.Background())
	if *calls != 1 {
		t.Fatalf("one round ran %d model calls for two PR tasks", *calls)
	}
	s.RunOnce(context.Background())
	if *calls != 2 {
		t.Fatalf("next round did not advance next task: %d", *calls)
	}
}

func TestMRTargetOnlyChangeIsReviewed(t *testing.T) {
	cu, cfg, _ := focusedFixture(t)
	model, calls := newMockLLM(t)
	cfg.LLM.BaseURL = model.URL
	if err := focusedCall(t, newTestService(t, cfg), "lzvsg/gvs", 8); err != nil {
		t.Fatal(err)
	}
	cu.mu.Lock()
	cu.patchSets[1]["commitId"] = "new-target"
	cu.mu.Unlock()
	if err := focusedCall(t, newTestService(t, cfg), "lzvsg/gvs", 8); err != nil {
		t.Fatal(err)
	}
	if *calls != 2 {
		t.Fatalf("target-only update skipped: %d", *calls)
	}
}

func TestFocusedMRNewVersionInSameService(t *testing.T) {
	cu, cfg, _ := focusedFixture(t)
	model, calls := newMockLLM(t)
	cfg.LLM.BaseURL = model.URL
	s := newTestService(t, cfg)
	if err := focusedCall(t, s, "lzvsg/gvs", 8); err != nil {
		t.Fatal(err)
	}
	cu.mu.Lock()
	cu.patchSets[1]["commitId"] = "new-target"
	cu.mu.Unlock()
	if err := focusedCall(t, s, "lzvsg/gvs", 8); err != nil {
		t.Fatal(err)
	}
	if *calls != 2 {
		t.Fatalf("same service hid target change: %d", *calls)
	}
}

func TestMRMetadataOnlyChangeIsReviewed(t *testing.T) {
	cu, cfg, _ := focusedFixture(t)
	model, calls := newMockLLM(t)
	cfg.LLM.BaseURL = model.URL
	if err := focusedCall(t, newTestService(t, cfg), "lzvsg/gvs", 8); err != nil {
		t.Fatal(err)
	}
	cu.mu.Lock()
	cu.mrs[1]["description"] = "new requirement"
	cu.mu.Unlock()
	if err := focusedCall(t, newTestService(t, cfg), "lzvsg/gvs", 8); err != nil {
		t.Fatal(err)
	}
	if *calls != 2 {
		t.Fatalf("metadata changed but baseline matched: %d", *calls)
	}
}

func TestFinishingOldAttemptCannotCompleteNewVersion(t *testing.T) {
	ctx := context.Background()
	q := runtimeQueue{directory: t.TempDir(), owner: "gvs"}
	if err := q.register(ctx, "pr:1", "old"); err != nil {
		t.Fatal(err)
	}
	if _, err := q.next(ctx); err != nil {
		t.Fatal(err)
	}
	if err := q.register(ctx, "pr:1", "new"); err != nil {
		t.Fatal(err)
	}
	if err := q.finish(ctx, "pr:1", nil, "old"); err != nil {
		t.Fatal(err)
	}
	job, err := q.next(ctx)
	if err != nil || job.Version != "new" {
		t.Fatalf("new version lost: %+v %v", job, err)
	}
}

func TestDueFailureCannotStarveUnattemptedJob(t *testing.T) {
	ctx := context.Background()
	q := runtimeQueue{directory: t.TempDir(), owner: "gvs"}
	_ = q.register(ctx, "bad", "v")
	_, _ = q.next(ctx)
	_ = q.finish(ctx, "bad", context.DeadlineExceeded)
	job, _ := q.load("bad")
	job.RetryAt = time.Time{}
	_ = writeRuntime(q.path("bad"), job)
	_ = q.register(ctx, "next", "v")
	next, err := q.next(ctx)
	if err != nil || next.Key != "next" {
		t.Fatalf("poison job starved next: %+v %v", next, err)
	}
}

func TestQueuedSnapshotChangePreventsStaleClosure(t *testing.T) {
	cfg := testConfig(t, "", "")
	s := newTestService(t, cfg)
	ctx := context.Background()
	ranStale := false
	candidates := []reviewCandidate{
		{key: "first", version: "v1", execute: func(ctx context.Context) error { return s.queue().register(ctx, "second", "new-version") }},
		{key: "second", version: "old-version", execute: func(context.Context) error { ranStale = true; return nil }},
	}
	if err := s.executeCollected(ctx, candidates); err == nil {
		t.Fatal("stale queue closure accepted")
	}
	if ranStale {
		t.Fatal("old snapshot executed for new queued version")
	}
	job, err := s.queue().load("second")
	if err != nil || job.Version != "new-version" || job.State == "completed" {
		t.Fatalf("new version marked complete: %+v %v", job, err)
	}
}
