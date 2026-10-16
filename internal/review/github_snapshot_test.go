package review

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gvs-pr-agent/internal/config"
	"gvs-pr-agent/internal/github"
	"gvs-pr-agent/internal/llm"
)

func TestGitHubChangedVersionCannotPublish(t *testing.T) {
	var changed atomic.Bool
	var posts atomic.Int32
	pr := &github.PullRequest{Number: 1, Title: "fix", State: "open"}
	pr.Head.SHA, pr.Base.SHA = "source", "target"
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/compare/"):
			if !strings.HasSuffix(r.URL.Path, "target...source") {
				t.Error("moving refs used for diff")
			}
			_, _ = w.Write([]byte("diff --git a/gvs/main.go b/gvs/main.go\n" + sampleDiff))
		case strings.HasSuffix(r.URL.Path, "/comments"):
			if r.Method == http.MethodPost {
				posts.Add(1)
				w.WriteHeader(201)
				return
			}
			_, _ = w.Write([]byte("[]"))
		default:
			fresh := *pr
			if changed.Load() {
				fresh.Head.SHA = "new-source"
			}
			_ = json.NewEncoder(w).Encode(fresh)
		}
	}))
	defer platform.Close()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		changed.Store(true)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": llm.Message{Content: "[]"}}}})
	}))
	defer model.Close()
	cfg := testConfig(t, "", model.URL)
	cfg.Codeup.Enabled = false
	cfg.GitHub.APIBase = platform.URL
	cfg.Notification.PRComment = true
	s := newTestService(t, cfg)
	if err := s.reviewPR(context.Background(), config.RepoConfig{Owner: "org", Repo: "repo"}, pr); err == nil || posts.Load() != 0 {
		t.Fatalf("stale review accepted: posts=%d error=%v", posts.Load(), err)
	}
}
