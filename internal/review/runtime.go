package review

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gvs-pr-agent/internal/instance"
	"gvs-pr-agent/internal/llm"
)

var errReviewBusy = errors.New("另一个实例正在审核 PR；本次不领取")

func acquireExecution(directory string) (func(), error) {
	lock, err := instance.Acquire(filepath.Join(directory, "execution.lock"))
	if err != nil {
		if instance.IsBusy(err) {
			return nil, errReviewBusy
		}
		return nil, err
	}
	return func() { _ = lock.Close() }, nil
}

func fingerprint(value any) string {
	raw, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func writeRuntime(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".review-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

type runtimeJob struct {
	Key, Owner, Version, State              string
	Created, LastSeen, LastAttempt, RetryAt time.Time
}

type runtimeQueue struct {
	directory, owner string
	ttl              time.Duration
}

func (q runtimeQueue) path(key string) string {
	return filepath.Join(q.directory, "tasks", fingerprint(key)+".json")
}

func (q runtimeQueue) retireMissing(ctx context.Context, active map[string]bool) error {
	return q.locked(ctx, func() error {
		paths, err := filepath.Glob(filepath.Join(q.directory, "tasks", "*.json"))
		if err != nil {
			return err
		}
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var job runtimeJob
			if err := json.Unmarshal(raw, &job); err != nil {
				return err
			}
			if job.Owner == q.owner && !active[job.Key] {
				job.State = "withdrawn"
				if err := writeRuntime(path, job); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (q runtimeQueue) locked(ctx context.Context, fn func() error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		lock, err := instance.Acquire(filepath.Join(q.directory, "queue.lock"))
		if err == nil {
			defer lock.Close()
			return fn()
		}
		if !instance.IsBusy(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (q runtimeQueue) load(key string) (runtimeJob, error) {
	raw, err := os.ReadFile(q.path(key))
	if os.IsNotExist(err) {
		return runtimeJob{Key: key}, nil
	}
	if err != nil {
		return runtimeJob{}, err
	}
	var job runtimeJob
	if err := json.Unmarshal(raw, &job); err != nil {
		return job, err
	}
	if job.Key != key {
		return job, fmt.Errorf("审核队列身份不一致")
	}
	return job, nil
}

func (q runtimeQueue) register(ctx context.Context, key, version string) error {
	return q.locked(ctx, func() error {
		job, err := q.load(key)
		if err != nil {
			return err
		}
		now := time.Now()
		if job.State == "" || job.State == "withdrawn" || job.Version != version {
			job = runtimeJob{Key: key, Version: version, State: "pending", Created: now}
		}
		job.Owner, job.LastSeen = q.owner, now
		return writeRuntime(q.path(key), job)
	})
}

// Call only while holding execution.lock. A reviewing entry then belongs to a
// crashed worker; the OS lock, rather than a journal flag, determines liveness.
func (q runtimeQueue) next(ctx context.Context) (runtimeJob, error) {
	var selected runtimeJob
	err := q.locked(ctx, func() error {
		paths, err := filepath.Glob(filepath.Join(q.directory, "tasks", "*.json"))
		if err != nil {
			return err
		}
		var ready []runtimeJob
		ttl := q.ttl
		if ttl <= 0 {
			ttl = 3 * time.Minute
		}
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var job runtimeJob
			if err := json.Unmarshal(raw, &job); err != nil {
				return err
			}
			if (job.State != "pending" && job.State != "interrupted" && job.State != "reviewing") || time.Since(job.LastSeen) > ttl || time.Now().Before(job.RetryAt) {
				continue
			}
			ready = append(ready, job)
		}
		sort.Slice(ready, func(i, j int) bool {
			a, b := ready[i], ready[j]
			if !a.LastAttempt.Equal(b.LastAttempt) {
				return a.LastAttempt.Before(b.LastAttempt)
			}
			if !a.Created.Equal(b.Created) {
				return a.Created.Before(b.Created)
			}
			return a.Key < b.Key
		})
		if len(ready) == 0 {
			return nil
		}
		selected = ready[0]
		// Each process can execute only tasks for which it has the repository
		// credentials/config. Leave another live owner's oldest task for its poll.
		if selected.Owner != q.owner {
			return nil
		}
		selected.State, selected.LastAttempt = "reviewing", time.Now()
		return writeRuntime(q.path(selected.Key), selected)
	})
	return selected, err
}

func (q runtimeQueue) finish(ctx context.Context, key string, failure error, expectedVersion ...string) error {
	// Persist a cancelled attempt with a short independent deadline.
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}
	return q.locked(ctx, func() error {
		job, err := q.load(key)
		if err != nil {
			return err
		}
		if len(expectedVersion) > 0 && job.Version != expectedVersion[0] {
			return nil
		}
		job.LastAttempt = time.Now()
		job.State, job.RetryAt = "completed", time.Time{}
		if failure != nil {
			job.State, job.RetryAt = "interrupted", time.Now().Add(time.Minute)
			var quota *llm.QuotaError
			if errors.As(failure, &quota) && quota.RetryAt.After(time.Now()) {
				job.RetryAt = quota.RetryAt
			}
		}
		return writeRuntime(q.path(key), job)
	})
}

type executionContextKey struct{}
type collectionKey struct{}
type pipelineKey struct{}
type pipelineAttempt struct{ started bool }
type reviewCandidate struct {
	key, version string
	execute      func(context.Context) error
}

func (s *Service) queue() runtimeQueue {
	owner := fingerprint(struct {
		GitHub any
		Codeup any
		State  string
	}{s.cfg.GitHub.Repos, s.cfg.Codeup.Repos, s.cfg.Codeup.StatePath})
	ttl := 3 * s.cfg.Scheduler.IntervalDuration()
	if ttl < 3*time.Minute {
		ttl = 3 * time.Minute
	}
	return runtimeQueue{directory: s.cfg.Review.RuntimeDir, owner: owner, ttl: ttl}
}

func (s *Service) runTask(ctx context.Context, key, version string, fn func(context.Context) error) error {
	knownVersion := version != ""
	version += ":" + s.taskSignature()
	if collection, ok := ctx.Value(collectionKey{}).(*[]reviewCandidate); ok {
		*collection = append(*collection, reviewCandidate{key, version, fn})
		return nil
	}
	trackerVersion := version
	if !knownVersion {
		trackerVersion = ""
	}
	return s.tasks.run(ctx, key, trackerVersion, func() error {
		if ctx.Value(executionContextKey{}) != true {
			release, err := acquireExecution(s.cfg.Review.RuntimeDir)
			if err != nil {
				return err
			}
			defer release()
			ctx = context.WithValue(ctx, executionContextKey{}, true)
			if err := s.queue().register(ctx, key, version); err != nil {
				return err
			}
		}
		round, cancel := s.roundContext(ctx)
		defer cancel()
		round = llm.WithReviewSession(round)
		round = llm.WithQuotaDirectory(round, filepath.Join(s.cfg.Review.RuntimeDir, "models"))
		round = context.WithValue(round, pinnedRulesKey{}, s.rulesStore.Current())
		err := fn(round)
		if err == nil {
			err = round.Err()
		}
		if err != nil {
			if unavailable := s.llm.Availability(round); unavailable != nil && round.Err() == nil {
				err = errors.Join(unavailable, err)
			}
		}
		if saveErr := s.queue().finish(ctx, key, err, version); saveErr != nil {
			return errors.Join(err, saveErr)
		}
		return err
	})
}

func (s *Service) executeCollected(ctx context.Context, candidates []reviewCandidate) error {
	q := s.queue()
	active := map[string]bool{}
	for _, c := range candidates {
		active[c.key] = true
		if err := q.register(ctx, c.key, c.version); err != nil {
			return err
		}
	}
	if err := q.retireMissing(ctx, active); err != nil {
		return err
	}
	release, err := acquireExecution(s.cfg.Review.RuntimeDir)
	if err != nil {
		return err
	}
	defer release()
	ctx = context.WithValue(ctx, executionContextKey{}, true)
	attempt := &pipelineAttempt{}
	ctx = context.WithValue(ctx, pipelineKey{}, attempt)
	byKey := map[string]reviewCandidate{}
	for _, c := range candidates {
		byKey[c.key] = c
	}
	for range candidates {
		job, err := q.next(ctx)
		if err != nil {
			return err
		}
		if job.Key == "" || job.Owner != q.owner {
			return nil
		}
		c, ok := byKey[job.Key]
		if !ok {
			return nil
		}
		if c.version != job.Version {
			return fmt.Errorf("任务 %s 已被新扫描更新版本，等待重新发现后审核", job.Key)
		}
		// Polling duplicates are already controlled by the shared execution lock;
		// avoid a process-local completed flag preventing journal completion.
		round, cancel := s.roundContext(ctx)
		round = llm.WithReviewSession(round)
		round = llm.WithQuotaDirectory(round, filepath.Join(s.cfg.Review.RuntimeDir, "models"))
		round = context.WithValue(round, pinnedRulesKey{}, s.rulesStore.Current())
		err = c.execute(round)
		if err == nil {
			err = round.Err()
		}
		if err != nil {
			if unavailable := s.llm.Availability(round); unavailable != nil && round.Err() == nil {
				err = errors.Join(unavailable, err)
			}
		}
		cancel()
		if saveErr := q.finish(ctx, c.key, err, job.Version); saveErr != nil {
			return errors.Join(err, saveErr)
		}
		if attempt.started || err != nil {
			return err
		}
	}
	return nil
}
