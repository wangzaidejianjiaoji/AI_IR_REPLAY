package review

import (
	"context"
	"log/slog"
	"sync"
)

type taskStatus string

const (
	taskPending   taskStatus = "pending"
	taskReviewing taskStatus = "reviewing"
	taskCompleted taskStatus = "completed"
	taskFailed    taskStatus = "failed"
)

type taskState struct {
	version string
	status  taskStatus
}

// taskTracker claims each PR/MR/branch atomically before waiting for the review
// slot. Scans can overlap, while review execution and mutable baselines remain
// serial. Running state is process-local; Codeup baselines remain persistent.
type taskTracker struct {
	mu     sync.Mutex
	states map[string]taskState
	slot   chan struct{}
	logger *slog.Logger
}

func (t *taskTracker) set(key, version string, status taskStatus) {
	t.states[key] = taskState{version: version, status: status}
	if t.logger != nil {
		t.logger.Info("审核任务状态变更", "task", key, "version", version, "status", status)
	}
}

func (t *taskTracker) run(ctx context.Context, key, version string, review func() error) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	if t.states == nil {
		t.states = make(map[string]taskState)
		t.slot = make(chan struct{}, 1)
	}
	state := t.states[key]
	if state.status == taskPending || state.status == taskReviewing ||
		(version != "" && state.status == taskCompleted && state.version == version) {
		t.mu.Unlock()
		return nil
	}
	t.set(key, version, taskPending)
	t.mu.Unlock()
	completed := false
	defer func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		status := taskFailed
		if completed {
			status = taskCompleted
		}
		t.set(key, version, status)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case t.slot <- struct{}{}:
	}
	defer func() { <-t.slot }()
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	t.set(key, version, taskReviewing)
	t.mu.Unlock()
	err = review()
	if err == nil {
		err = ctx.Err()
	}
	completed = err == nil
	return err
}
