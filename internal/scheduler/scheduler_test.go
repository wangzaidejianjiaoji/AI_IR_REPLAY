package scheduler

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestSchedulerSkipsTriggerWhileTaskIsRunning(t *testing.T) {
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
	close(release)

	select {
	case <-extraRun:
		t.Fatal("trigger received during an active review must be skipped")
	case <-time.After(100 * time.Millisecond):
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("task calls = %d, want 1", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
}
