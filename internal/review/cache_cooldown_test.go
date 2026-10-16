package review

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"gvs-pr-agent/internal/llm"
)

func TestCachedReviewCanCompleteWhileModelIsCooling(t *testing.T) {
	var cooling atomic.Bool
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if cooling.Load() {
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(429)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": llm.Message{Content: "[]"}}}})
	}))
	defer model.Close()
	cfg := testConfig(t, "", model.URL)
	cfg.Codeup.Enabled = false
	s := newTestService(t, cfg)
	patch := "diff --git a/gvs/main.go b/gvs/main.go\n" + sampleDiff
	if _, err := s.ReviewLocalDiff(context.Background(), patch, "cached"); err != nil {
		t.Fatal(err)
	}
	cooling.Store(true)
	ctx := llm.WithQuotaDirectory(context.Background(), filepath.Join(cfg.Review.RuntimeDir, "models"))
	if _, _, err := s.llm.Chat(ctx, "m-small", []llm.Message{{Role: "user", Content: "force quota response"}}); err == nil {
		t.Fatal("quota simulation failed")
	}
	// A fresh process-equivalent service can regenerate the complete report
	// exclusively from saved responses; known quota cannot block cache replay.
	report, err := newTestService(t, cfg).ReviewLocalDiff(context.Background(), patch, "cached")
	if err != nil || report == "" || calls.Load() != 2 {
		t.Fatalf("report=%t calls=%d error=%v", report != "", calls.Load(), err)
	}
}
