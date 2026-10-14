package review

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestTaskSkipsActiveAndCompletedVersions(t *testing.T) {
	var tasks taskTracker
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	go func() {
		defer close(done)
		_ = tasks.run(context.Background(), "github:repo:1", "sha1", func() error {
			calls.Add(1)
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	for _, version := range []string{"sha1", "sha2"} {
		_ = tasks.run(context.Background(), "github:repo:1", version, func() error { calls.Add(1); return nil })
	}
	close(release)
	<-done
	_ = tasks.run(context.Background(), "github:repo:1", "sha1", func() error { calls.Add(1); return nil })
	if calls.Load() != 1 {
		t.Fatal("active/completed task was duplicated")
	}
	_ = tasks.run(context.Background(), "github:repo:1", "sha2", func() error { calls.Add(1); return nil })
	if calls.Load() != 2 {
		t.Fatal("new version was not reviewed")
	}
}

func TestTaskFailureAllowsRetry(t *testing.T) {
	var tasks taskTracker
	errFailed := errors.New("review failed")
	if err := tasks.run(context.Background(), "mr:repo:1", "", func() error { return errFailed }); !errors.Is(err, errFailed) {
		t.Fatal(err)
	}
	calls := 0
	if err := tasks.run(context.Background(), "mr:repo:1", "", func() error { calls++; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("failed task was not retried")
	}
}

func TestQueuedTaskCancellationAllowsRetry(t *testing.T) {
	var tasks taskTracker
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_ = tasks.run(context.Background(), "pr:1", "sha1", func() error { close(started); <-release; return nil })
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := tasks.run(ctx, "pr:2", "sha2", func() error { t.Error("cancelled queued task executed"); return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	close(release)
	<-done
	calls := 0
	_ = tasks.run(context.Background(), "pr:2", "sha2", func() error { calls++; return nil })
	if calls != 1 {
		t.Fatal("cancelled task was not retried")
	}
}

func TestPendingTaskIsDeduplicatedAndReviewsAreSerial(t *testing.T) {
	var tasks taskTracker
	started, release, firstDone, secondDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(firstDone)
		_ = tasks.run(context.Background(), "pr:1", "sha1", func() error { close(started); <-release; return nil })
	}()
	<-started
	var calls atomic.Int32
	go func() {
		defer close(secondDone)
		_ = tasks.run(context.Background(), "pr:2", "sha2", func() error { calls.Add(1); return nil })
	}()
	deadline := time.Now().Add(time.Second)
	for {
		tasks.mu.Lock()
		status := tasks.states["pr:2"].status
		tasks.mu.Unlock()
		if status == taskPending {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("second task did not enter pending state")
		}
		time.Sleep(time.Millisecond)
	}
	_ = tasks.run(context.Background(), "pr:2", "sha3", func() error { calls.Add(1); return nil })
	if calls.Load() != 0 {
		close(release)
		t.Fatal("pending task duplicated or reviews overlapped")
	}
	close(release)
	<-firstDone
	<-secondDone
	if calls.Load() != 1 {
		t.Fatal("queued task did not execute exactly once")
	}
}
