// Package progress records observable work without storing prompts or credentials.
package progress

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
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

type Tracker struct {
	mu         sync.Mutex
	snapshot   Snapshot
	generation uint64
	path       string
	logger     *slog.Logger
}

type trackerKey struct{}
type generationKey struct{}

func New(path string, logger *slog.Logger) *Tracker {
	return &Tracker{path: path, logger: logger, snapshot: Snapshot{PID: os.Getpid(), State: "idle", Stage: "waiting"}}
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
		if generation == t.generation {
			t.snapshot.State = state
			t.snapshot.FinishedAt = time.Now()
		}
		t.mu.Unlock()
		t.Publish()
	}
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

// Print displays only state belonging to the discovered live agent PID.
func Print(path string, pid int, output io.Writer) error {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		fmt.Fprintln(output, "  review status unavailable (older agent or not started)")
		return nil
	}
	if err != nil {
		return err
	}
	var s Snapshot
	if err = json.Unmarshal(raw, &s); err != nil {
		return err
	}
	if s.PID != pid || time.Since(s.UpdatedAt) > time.Minute {
		fmt.Fprintln(output, "  review status stale; inspect agent logs")
		return nil
	}
	elapsed := s.ElapsedSeconds
	if s.State == "reviewing" && !s.StartedAt.IsZero() {
		elapsed = time.Since(s.StartedAt).Seconds()
	}
	started := "n/a"
	if !s.StartedAt.IsZero() {
		started = s.StartedAt.Format(time.RFC3339)
	}
	fmt.Fprintf(output, "  state=%s task=%q repo=%q title=%q stage=%s jobs=%q\n  started=%s elapsed=%.0fs tokens=%d (input=%d output=%d) requests=%d usage_missing=%d cache_hits=%d\n", s.State, s.Task, s.Repository, s.Title, s.Stage, s.ActiveJobs, started, elapsed, s.TotalTokens, s.PromptTokens, s.CompletionTokens, s.Requests, s.UsageMissing, s.CacheHits)
	return nil
}
