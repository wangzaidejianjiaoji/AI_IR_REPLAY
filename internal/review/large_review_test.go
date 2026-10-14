package review

import (
	"context"
	"encoding/json"
	"fmt"
	"gvs-pr-agent/internal/agent"
	"gvs-pr-agent/internal/analyzer"
	"gvs-pr-agent/internal/coordinator"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMRSeventyFiveFilesPublishesCompleteReport(t *testing.T) {
	cu := newMockCodeup(t)
	calls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "[]"}}}})
	}))
	defer model.Close()
	cfg := testConfig(t, cu.srv.URL, model.URL)
	cfg.Review.MaxFilesPerReview = 100
	cfg.Review.FilePackaging.Enabled = true
	cfg.Review.FilePackaging.MaxPackageSize = 5
	cfg.Codeup.MRReview, cfg.Codeup.MRComment = true, true
	svc := newTestService(t, cfg)
	cu.setBranches(branch("master", "sha-m", true))
	cu.mu.Lock()
	cu.mrs = []map[string]any{{"localId": 8, "title": "fix focus", "state": "UNDER_REVIEW", "sourceBranch": "ws100-1012", "targetBranch": "master"}}
	cu.patchSets = []map[string]any{{"patchSetBizId": "ps-1", "commitId": "mr-sha-1", "versionNo": 1, "relatedMergeItemType": "MERGE_SOURCE"}}
	cu.mu.Unlock()
	var files []map[string]any
	for i := 0; i < 75; i++ {
		path := fmt.Sprintf("gvs/f%02d.go", i)
		files = append(files, map[string]any{"oldPath": path, "newPath": path, "diff": fileDiff(path)})
	}
	cu.setCompareDiffs(files...)
	svc.RunOnce(context.Background())
	_, _, comments := cu.snapshot()
	if len(comments[8]) != 1 || baseline(t, cfg.Codeup.StatePath, "mr:lzvsg/gvs:8") != "mr-sha-1" || calls != 15 {
		t.Fatalf("comments=%v calls=%d", comments, calls)
	}
	content, _ := comments[8][0]["content"].(string)
	if !strings.Contains(content, "75/75") {
		t.Fatalf("missing file coverage: %s", content)
	}
}

func TestLaterConfirmationBatchFailureDoesNotPublishOrAdvanceBaseline(t *testing.T) {
	cu := newMockCodeup(t)
	confirmationCalls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		var content string
		if strings.Contains(req.Messages[0].Content, "最终代码审查统筹确认 Agent") {
			confirmationCalls++
			marker := "## 待确认候选问题\n"
			var candidates []struct {
				ID string `json:"candidate_id"`
			}
			user := req.Messages[1].Content
			json.Unmarshal([]byte(user[strings.LastIndex(user, marker)+len(marker):]), &candidates)
			decisions := []map[string]any{}
			if confirmationCalls == 1 {
				for _, candidate := range candidates {
					decisions = append(decisions, map[string]any{"candidate_id": candidate.ID, "confirmed": true, "reason": "exact evidence"})
				}
			}
			encoded, _ := json.Marshal(map[string]any{"decisions": decisions})
			content = string(encoded)
		} else {
			var candidates []agent.Issue
			for i := 0; i < 45; i++ {
				candidates = append(candidates, agent.Issue{File: "gvs/main.go", Line: 2, Problem: fmt.Sprintf("p%d", i), Severity: "high"})
			}
			encoded, _ := json.Marshal(candidates)
			content = string(encoded)
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": content}}}})
	}))
	defer model.Close()
	cfg := testConfig(t, cu.srv.URL, model.URL)
	cfg.Codeup.MRReview, cfg.Codeup.MRComment = true, true
	svc := newTestService(t, cfg)
	// Keep the real specialised reviewers and add only the final confirmer.
	svc.coord = coordinator.New(agent.BuildEnabled(cfg.Agents, svc.llm, svc.logger), 1, nil, nil, svc.logger, coordinator.NewConfirmer(svc.llm))
	cu.setBranches(branch("master", "sha-m", true))
	cu.mu.Lock()
	cu.mrs = []map[string]any{{"localId": 8, "title": "later confirmation failure", "state": "UNDER_REVIEW", "sourceBranch": "feature", "targetBranch": "master"}}
	cu.patchSets = []map[string]any{{"patchSetBizId": "ps-1", "commitId": "mr-sha-1", "versionNo": 1, "relatedMergeItemType": "MERGE_SOURCE"}}
	cu.mu.Unlock()
	svc.RunOnce(context.Background())
	_, _, comments := cu.snapshot()
	if confirmationCalls != 2 || len(comments) != 0 || baseline(t, cfg.Codeup.StatePath, "mr:lzvsg/gvs:8") != "" {
		t.Fatalf("incomplete confirmation published: calls=%d comments=%v", confirmationCalls, comments)
	}
}

func TestLargeGlobalAnalysisReadsEveryPackageAndSharesMergedIntent(t *testing.T) {
	analysisCalls, reviewCalls := 0, 0
	tailSeen, mergeSeen := false, false
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		content := "[]"
		if strings.Contains(req.Messages[0].Content, "代码变更意图分析器") {
			analysisCalls++
			if strings.Contains(req.Messages[1].Content, "gvs/f74.go") {
				tailSeen = true
			}
			if strings.Contains(req.Messages[1].Content, "批次分析结果") {
				mergeSeen = true
			}
			content = `{"summary":"complete facts","intent":"shared intent","affected_modules":["gvs"],"behavior_constraints":["compatibility"],"comment_logic_mismatches":[]}`
		} else {
			reviewCalls++
			if !strings.Contains(req.Messages[1].Content, "shared intent") {
				t.Error("reviewer missing merged global intent")
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": content}}}})
	}))
	defer model.Close()
	cfg := testConfig(t, "http://127.0.0.1:1", model.URL)
	cfg.Review.MaxFilesPerReview = 100
	cfg.Review.MaxDiffChars = 1000
	cfg.Review.FilePackaging.Enabled = true
	cfg.Review.FilePackaging.MaxPackageSize = 5
	svc := newTestService(t, cfg)
	svc.analyzer = analyzer.New(svc.llm)
	var raw strings.Builder
	for i := 0; i < 75; i++ {
		path := fmt.Sprintf("gvs/f%02d.go", i)
		fmt.Fprintf(&raw, "diff --git a/%s b/%s\n%s", path, path, fileDiff(path))
	}
	summary, err := svc.reviewDiff(context.Background(), raw.String(), "gvs", "large", "", svc.logger)
	if err != nil || summary == nil {
		t.Fatal(err)
	}
	if !tailSeen || !mergeSeen || reviewCalls != 15 || analysisCalls <= 15 || summary.AnalysisTotalBytes != summary.AnalysisReviewedBytes || summary.Reviewed != 75 {
		t.Fatalf("tail=%v merge=%v analysis=%d review=%d summary=%+v", tailSeen, mergeSeen, analysisCalls, reviewCalls, summary)
	}
}

func TestOversizedFileFailsBeforeAnyModelCall(t *testing.T) {
	model, calls := newMockLLM(t)
	cfg := testConfig(t, "http://127.0.0.1:1", model.URL)
	cfg.Review.FilePackaging.Enabled = true
	cfg.Review.MaxDiffChars = 100
	svc := newTestService(t, cfg)
	patch := "diff --git a/huge.go b/huge.go\n--- a/huge.go\n+++ b/huge.go\n@@ -1 +1 @@\n-old\n+" + strings.Repeat("x", 200) + "\n"
	_, err := svc.ReviewLocalDiff(context.Background(), patch, "oversized")
	if err == nil || !strings.Contains(err.Error(), "拒绝截断") || *calls != 0 {
		t.Fatalf("error=%v calls=%d", err, *calls)
	}
}

func TestMRPartialJobFailureDoesNotPublishOrAdvanceBaseline(t *testing.T) {
	cu := newMockCodeup(t)
	calls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		content := "[]"
		if calls > 1 {
			content = "invalid review output"
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": content}}}})
	}))
	defer model.Close()
	cfg := testConfig(t, cu.srv.URL, model.URL)
	cfg.Codeup.MRReview, cfg.Codeup.MRComment = true, true
	cfg.Review.FilePackaging.Enabled = true
	cfg.Review.FilePackaging.MaxPackageSize = 1
	svc := newTestService(t, cfg)
	cu.setBranches(branch("master", "sha-m", true))
	cu.mu.Lock()
	cu.mrs = []map[string]any{{"localId": 8, "title": "partial", "state": "UNDER_REVIEW", "sourceBranch": "feature", "targetBranch": "master"}}
	cu.patchSets = []map[string]any{{"patchSetBizId": "ps-1", "commitId": "mr-sha-1", "versionNo": 1, "relatedMergeItemType": "MERGE_SOURCE"}}
	cu.mu.Unlock()
	cu.setCompareDiffs(map[string]any{"oldPath": "gvs/a.go", "newPath": "gvs/a.go", "diff": fileDiff("gvs/a.go")}, map[string]any{"oldPath": "gvs/b.go", "newPath": "gvs/b.go", "diff": fileDiff("gvs/b.go")})
	svc.RunOnce(context.Background())
	_, _, comments := cu.snapshot()
	if len(comments) != 0 || baseline(t, cfg.Codeup.StatePath, "mr:lzvsg/gvs:8") != "" || calls != 3 {
		t.Fatalf("partial result published: comments=%v calls=%d", comments, calls)
	}
}
