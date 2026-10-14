package scheduler

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestSchedulerTriggersWhileTaskIsRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := New(time.Hour, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	started := make(chan struct{})
	release := make(chan struct{})
	extraRun := make(chan struct{}, 1)
	done := make(chan struct{})
	var calls atomic.Int32

	go func() {
		s.Run(ctx, func(context.Context) {
			if calls.Add(1) == 1 {
				close(started)
				<-release
				return
			}
			select {
			case extraRun <- struct{}{}:
			default:
			}
		})
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("startup task did not begin")
	}

	s.Trigger()

	select {
	case <-extraRun:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("new scan did not run while the previous round was active")
	}
	close(release)
	if got := calls.Load(); got != 2 {
		t.Fatalf("task calls = %d, want 2", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
}

func TestSchedulerTicksWhilePreviousRoundIsRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(10*time.Millisecond, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	started, release, scanned, done := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1), make(chan struct{})
	var calls atomic.Int32
	go func() {
		defer close(done)
		s.Run(ctx, func(context.Context) {
			if calls.Add(1) == 1 {
				close(started)
				<-release
				return
			}
			select {
			case scanned <- struct{}{}:
			default:
			}
		})
	}()
	<-started
	select {
	case <-scanned:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("periodic scan skipped while previous round was active")
	}
	cancel()
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not wait/stop")
	}
}
