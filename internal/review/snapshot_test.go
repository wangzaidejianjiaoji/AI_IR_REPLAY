package review

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMRComparePinnedToVersionCommits(t *testing.T) {
	cu := newMockCodeup(t)
	llmSrv, _ := newMockLLM(t)
	cfg := testConfig(t, cu.srv.URL, llmSrv.URL)
	cu.setBranches(branch("master", "target-sha", true))
	cu.patchSets = []map[string]any{{"patchSetBizId": "ps-1", "commitId": "source-sha", "versionNo": 1, "relatedMergeItemType": "MERGE_SOURCE"}}
	svc := newTestService(t, cfg)
	cu.mrs = []map[string]any{{"localId": 8, "title": "fix", "sourceBranch": "feature", "targetBranch": "master"}}
	cfg.Codeup.MRReview = true
	svc.RunOnce(context.Background())
	compares, _, _ := cu.snapshot()
	if len(compares) != 1 || compares[0] != "target-sha..source-sha" {
		t.Fatalf("diff used mutable refs: %v", compares)
	}
}

func TestMRNewVersionDuringReviewDoesNotPublish(t *testing.T) {
	cu := newMockCodeup(t)
	cu.setBranches(branch("master", "target-sha", true))
	cu.patchSets = []map[string]any{{"patchSetBizId": "ps-1", "commitId": "source-sha", "versionNo": 1, "relatedMergeItemType": "MERGE_SOURCE"}}
	cu.mrs = []map[string]any{{"localId": 8, "title": "fix", "sourceBranch": "feature", "targetBranch": "master"}}
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cu.mu.Lock()
		cu.patchSets = []map[string]any{{"patchSetBizId": "ps-2", "commitId": "new-source", "versionNo": 2, "relatedMergeItemType": "MERGE_SOURCE"}}
		cu.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": `[{"severity":"high","file":"gvs/main.go","line":2,"problem":"p","detail":"key"}]`}}}})
	}))
	defer llm.Close()
	cfg := testConfig(t, cu.srv.URL, llm.URL)
	cfg.Codeup.MRReview = true
	cfg.Codeup.MRComment = true
	svc := newTestService(t, cfg)
	svc.RunOnce(context.Background())
	_, _, comments := cu.snapshot()
	if len(comments[8]) != 0 {
		t.Fatal("published stale review to changed MR")
	}
	if b := baseline(t, cfg.Codeup.StatePath, "mr:lzvsg/gvs:8"); strings.TrimSpace(b) != "" {
		t.Fatalf("stale MR baseline advanced: %s", b)
	}
}
