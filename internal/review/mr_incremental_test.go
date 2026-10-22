package review

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMRNextVersionReviewsOnlyDeltaFromSuccessfulVersion(t *testing.T) {
	cu, cfg, _ := focusedFixture(t)
	var prompts []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		var prompt strings.Builder
		for _, m := range req.Messages {
			prompt.WriteString(m.Content)
		}
		prompts = append(prompts, prompt.String())
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "[]"}}}})
	}))
	defer model.Close()
	cfg.LLM.BaseURL = model.URL
	cu.setCompareDiffs(map[string]any{"oldPath": "gvs/main.go", "newPath": "gvs/main.go", "diff": strings.ReplaceAll(sampleDiff, "hardcoded-secret", "already-reviewed-feature")})
	if err := focusedCall(t, newTestService(t, cfg), "lzvsg/gvs", 8); err != nil {
		t.Fatal(err)
	}
	cu.mu.Lock()
	cu.patchSets[0]["commitId"] = "version-six"
	cu.patchSets[0]["versionNo"] = 6
	cu.mu.Unlock()
	cu.setCompareDiffs(map[string]any{"oldPath": "gvs/main.go", "newPath": "gvs/main.go", "diff": strings.ReplaceAll(sampleDiff, "hardcoded-secret", "new-version-only")})
	if err := focusedCall(t, newTestService(t, cfg), "lzvsg/gvs", 8); err != nil {
		t.Fatal(err)
	}
	compares, _, comments := cu.snapshot()
	if len(compares) != 2 || compares[1] != "full-source-sha..version-six" {
		t.Fatalf("re-reviewed full MR: %v", compares)
	}
	if len(prompts) != 2 || !strings.Contains(prompts[1], "new-version-only") || strings.Contains(prompts[1], "already-reviewed-feature") {
		t.Fatalf("incremental prompt did not isolate new code: %v", prompts)
	}
	cu.mu.Lock()
	mode := cu.compareModes[1]
	cu.mu.Unlock()
	if mode != "true" {
		t.Fatalf("version delta used merge-base instead of direct comparison: %q", mode)
	}
	if len(comments[8]) != 2 {
		t.Fatalf("reports=%d", len(comments[8]))
	}
	report, _ := comments[8][1]["content"].(string)
	if !strings.Contains(report, "增量审核") || !strings.Contains(report, "full-source-sha") || !strings.Contains(report, "version-six") {
		t.Fatalf("report hides review scope: %s", report)
	}
}

func TestMRFailedVersionDoesNotBecomeIncrementalBase(t *testing.T) {
	cu, cfg, _ := focusedFixture(t)
	var fail atomic.Bool
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "temporary model failure", 503)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "[]"}}}})
	}))
	defer model.Close()
	cfg.LLM.BaseURL = model.URL
	if err := focusedCall(t, newTestService(t, cfg), "lzvsg/gvs", 8); err != nil {
		t.Fatal(err)
	}
	cu.mu.Lock()
	cu.patchSets[0]["commitId"] = "version-six"
	cu.mu.Unlock()
	fail.Store(true)
	if err := focusedCall(t, newTestService(t, cfg), "lzvsg/gvs", 8); err == nil {
		t.Fatal("failed review accepted")
	}
	if got := baseline(t, cfg.Codeup.StatePath, "mr:lzvsg/gvs:8"); got != "full-source-sha" {
		t.Fatalf("failed version became baseline: %s", got)
	}
	cu.mu.Lock()
	cu.patchSets[0]["commitId"] = "version-seven"
	cu.mu.Unlock()
	fail.Store(false)
	if err := focusedCall(t, newTestService(t, cfg), "lzvsg/gvs", 8); err != nil {
		t.Fatal(err)
	}
	compares, _, comments := cu.snapshot()
	if len(compares) != 3 || compares[2] != "full-source-sha..version-seven" || len(comments[8]) != 2 {
		t.Fatalf("failed version's changes lost: compares=%v reports=%d", compares, len(comments[8]))
	}
}

func TestMREmptyVersionDeltaSkipsModelAndAdvancesCodeBaseline(t *testing.T) {
	cu, cfg, _ := focusedFixture(t)
	model, calls := newMockLLM(t)
	cfg.LLM.BaseURL = model.URL
	if err := focusedCall(t, newTestService(t, cfg), "lzvsg/gvs", 8); err != nil {
		t.Fatal(err)
	}
	cu.mu.Lock()
	cu.patchSets[0]["commitId"] = "same-code-new-commit"
	cu.compareDiffs = []map[string]any{}
	cu.mu.Unlock()
	if err := focusedCall(t, newTestService(t, cfg), "lzvsg/gvs", 8); err != nil {
		t.Fatal(err)
	}
	_, _, comments := cu.snapshot()
	if *calls != 1 || len(comments[8]) != 1 || baseline(t, cfg.Codeup.StatePath, "mr:lzvsg/gvs:8") != "same-code-new-commit" {
		t.Fatal("empty code delta invoked model/published or failed to record version")
	}
}

func TestMRMetadataChangeWhileReviewingDoesNotDiscardCodeReview(t *testing.T) {
	cu, cfg, _ := focusedFixture(t)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cu.mu.Lock()
		cu.mrs[1]["title"] = "new wording"
		cu.mrs[1]["description"] = "new description"
		cu.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "[]"}}}})
	}))
	defer model.Close()
	cfg.LLM.BaseURL = model.URL
	if err := focusedCall(t, newTestService(t, cfg), "lzvsg/gvs", 8); err != nil {
		t.Fatal(err)
	}
	if baseline(t, cfg.Codeup.StatePath, "mr:lzvsg/gvs:8") != "full-source-sha" {
		t.Fatal("metadata change discarded valid review")
	}
}

func TestMRMetadataOnlyChangeDoesNotRequeueCompletedTask(t *testing.T) {
	cu, cfg, _ := focusedFixture(t)
	cfg.Codeup.Repos = cfg.Codeup.Repos[:1]
	cfg.Codeup.Repos[0].BranchFilter.Include = []string{"feature"}
	cu.mu.Lock()
	cu.mrs = cu.mrs[1:]
	cu.mu.Unlock()
	model, calls := newMockLLM(t)
	cfg.LLM.BaseURL = model.URL
	s := newTestService(t, cfg)
	s.RunOnce(context.Background())
	before, err := s.queue().load("mr:lzvsg/gvs:8")
	if err != nil {
		t.Fatal(err)
	}
	cu.mu.Lock()
	cu.mrs[0]["title"] = "description-only"
	cu.mrs[0]["description"] = "reworded"
	cu.mu.Unlock()
	s.RunOnce(context.Background())
	after, err := s.queue().load("mr:lzvsg/gvs:8")
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 || after.Version != before.Version || !after.LastAttempt.Equal(before.LastAttempt) {
		t.Fatalf("metadata requeued completed task: before=%+v after=%+v calls=%d", before, after, *calls)
	}
}
