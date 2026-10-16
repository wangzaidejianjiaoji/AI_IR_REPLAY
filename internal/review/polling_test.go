package review

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gvs-pr-agent/internal/config"
)

// Block the model request so the next poll observes a real in-flight review.
func TestPollingDoesNotDuplicateActiveReview(t *testing.T) {
	for _, platform := range []string{"github", "codeup-mr", "codeup-branch"} {
		t.Run(platform, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var calls atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					close(started)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "[]"}}}})
			}))
			defer model.Close()
			defer unblock()
			cu := newMockCodeup(t)
			cfg := testConfig(t, cu.srv.URL, model.URL)
			cfg.Review.DryRun = true
			cfg.Codeup.ReviewFirstPush = true
			switch platform {
			case "github":
				gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case strings.HasSuffix(r.URL.Path, "/pulls"):
						_, _ = w.Write([]byte(`[{"number":1,"title":"fix","head":{"sha":"sha1"},"base":{"sha":"base1","ref":"main"}}]`))
					case strings.HasSuffix(r.URL.Path, "/pulls/1"):
						_, _ = w.Write([]byte(`{"number":1,"title":"fix","state":"open","head":{"sha":"sha1"},"base":{"sha":"base1","ref":"main"}}`))
					case strings.HasSuffix(r.URL.Path, "/comments"):
						_, _ = w.Write([]byte("[]"))
					default:
						_, _ = w.Write([]byte("diff --git a/gvs/main.go b/gvs/main.go\n" + sampleDiff))
					}
				}))
				defer gh.Close()
				cfg.Codeup.Enabled = false
				cfg.GitHub = config.GitHubConfig{APIBase: gh.URL, Repos: []config.RepoConfig{{Owner: "org", Repo: "repo"}}}
			case "codeup-mr":
				cu.setBranches(branch("master", "target-sha", true))
				cu.mrs = []map[string]any{{"localId": 8, "title": "fix", "sourceBranch": "feature", "targetBranch": "master"}}
				cu.patchSets = []map[string]any{{"patchSetBizId": "ps-1", "commitId": "source-sha", "versionNo": 1, "relatedMergeItemType": "MERGE_SOURCE"}}
				cfg.Codeup.MRReview = true
			case "codeup-branch":
				cu.setBranches(branch("master", "target-sha", true), branch("feature", "source-sha", false))
			}
			svc := newTestService(t, cfg)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			firstDone := make(chan struct{})
			go func() { defer close(firstDone); svc.RunOnce(ctx) }()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("review did not start")
			}
			secondDone := make(chan struct{})
			go func() { defer close(secondDone); svc.RunOnce(ctx) }()
			select {
			case <-secondDone:
			case <-time.After(2 * time.Second):
				t.Fatal("poll waited for/duplicated active task")
			}
			unblock()
			select {
			case <-firstDone:
			case <-time.After(2 * time.Second):
				t.Fatal("review did not complete")
			}
			svc.RunOnce(ctx)
			if got := calls.Load(); got != 1 {
				t.Fatalf("active/completed version reviewed %d times", got)
			}
		})
	}
}

func TestPollingRetriesPRWhenDiffConflictResolves(t *testing.T) {
	var conflict atomic.Bool
	conflict.Store(true)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			_, _ = w.Write([]byte(`[{"number":1,"title":"fix","head":{"sha":"sha1"},"base":{"sha":"base1","ref":"main"}}]`))
		case strings.HasSuffix(r.URL.Path, "/pulls/1"):
			_, _ = w.Write([]byte(`{"number":1,"title":"fix","state":"open","head":{"sha":"sha1"},"base":{"sha":"base1","ref":"main"}}`))
		case strings.HasSuffix(r.URL.Path, "/comments"):
			_, _ = w.Write([]byte("[]"))
		default:
			if conflict.Load() {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			_, _ = w.Write([]byte("diff --git a/gvs/main.go b/gvs/main.go\n" + sampleDiff))
		}
	}))
	defer gh.Close()
	model, calls := newMockLLM(t)
	cfg := testConfig(t, "", model.URL)
	cfg.Codeup.Enabled = false
	cfg.Review.DryRun = true
	cfg.GitHub = config.GitHubConfig{APIBase: gh.URL, Repos: []config.RepoConfig{{Owner: "org", Repo: "repo"}}}
	svc := newTestService(t, cfg)
	svc.RunOnce(context.Background())
	conflict.Store(false)
	expireRuntimeRetries(t, svc)
	svc.RunOnce(context.Background())
	if *calls != 1 {
		t.Fatalf("resolved conflict did not retry same version: model calls=%d", *calls)
	}
}
