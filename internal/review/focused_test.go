package review

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"gvs-pr-agent/internal/config"
)

func TestFocusedMRCheckpointSurvivesRestartAndUsesFullVersions(t *testing.T) {
	cu, cfg, _ := focusedFixture(t)
	server, calls := newMockLLM(t)
	cfg.LLM.BaseURL = server.URL
	cfg.Review.CheckpointDir = t.TempDir()
	cfg.Review.DryRun = true
	for i := 0; i < 4; i++ {
		if i == 2 {
			cu.patchSets[0]["commitId"] = "full-source-sha-new"
		}
		if i == 3 {
			cu.patchSets[1]["commitId"] = "full-target-sha-new"
		}
		cfg.Codeup.StatePath = filepath.Join(t.TempDir(), "state.json")
		if err := focusedCall(t, newTestService(t, cfg), "lzvsg/gvs", 8); err != nil {
			t.Fatal(err)
		}
		want := []int{1, 1, 2, 3}[i]
		if *calls != want {
			t.Fatalf("restart %d: LLM calls=%d want %d", i, *calls, want)
		}
	}
}

func TestFocusedMRVersionChangeReturnsFailure(t *testing.T) {
	cu, cfg, _ := focusedFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cu.mu.Lock()
		cu.patchSets[0]["commitId"] = "changed-source"
		cu.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "[]"}}}})
	}))
	defer server.Close()
	cfg.LLM.BaseURL = server.URL
	if err := focusedCall(t, newTestService(t, cfg), "lzvsg/gvs", 8); err == nil {
		t.Fatal("version change silently succeeded")
	}
	_, _, comments := cu.snapshot()
	if len(comments) != 0 || baseline(t, cfg.Codeup.StatePath, "mr:lzvsg/gvs:8") != "" {
		t.Fatal("stale review published or advanced baseline")
	}
}

func focusedCall(t *testing.T, s *Service, repository string, id int) error {
	t.Helper()
	return s.ReviewCodeupMR(context.Background(), repository, id)
}

func focusedFixture(t *testing.T) (*mockCodeup, *config.Config, *Service) {
	t.Helper()
	cu := newMockCodeup(t)
	server, _ := newMockLLM(t)
	cfg := testConfig(t, cu.srv.URL, server.URL)
	cfg.Codeup.MRReview, cfg.Codeup.MRComment = true, true
	cfg.Codeup.Repos[0].BranchFilter.Include = []string{"stale-branch"}
	cfg.Codeup.Repos = append(cfg.Codeup.Repos, config.CodeupRepoConfig{RepositoryID: "other/repo"})
	cu.setBranches(branch("stale-branch", "branch-head", false))
	cu.mrs = []map[string]any{
		{"localId": 9, "title": "other", "sourceBranch": "feature", "targetBranch": "master"},
		{"localId": 8, "title": "fix", "sourceBranch": "feature", "targetBranch": "master"},
	}
	cu.patchSets = []map[string]any{
		{"patchSetBizId": "source-version", "commitId": "full-source-sha", "versionNo": 1, "relatedMergeItemType": "MERGE_SOURCE"},
		{"patchSetBizId": "target-version", "commitId": "full-target-sha", "versionNo": 1, "relatedMergeItemType": "MERGE_TARGET"},
	}
	return cu, cfg, newTestService(t, cfg)
}

func TestFocusedMRSelectsExactConfiguredMR(t *testing.T) {
	cu, cfg, svc := focusedFixture(t)
	if err := focusedCall(t, svc, "lzvsg/gvs", 8); err != nil {
		t.Fatal(err)
	}
	compares, commits, comments := cu.snapshot()
	if len(compares) != 1 || compares[0] != "full-target-sha..full-source-sha" {
		t.Fatalf("unexpected review scope: %v", compares)
	}
	if len(commits) != 0 || len(comments[8]) != 1 {
		t.Fatalf("unexpected publication: commits=%v MR=%v", commits, comments)
	}
	if got := baseline(t, cfg.Codeup.StatePath, "branch:lzvsg/gvs:stale-branch"); got != "" {
		t.Fatalf("branch scan advanced baseline: %s", got)
	}
	if err := focusedCall(t, svc, "lzvsg/gvs", 8); err != nil {
		t.Fatal(err)
	}
	compares, _, comments = cu.snapshot()
	if len(compares) != 1 || len(comments[8]) != 1 {
		t.Fatal("published version was reviewed again")
	}
}

func TestFocusedMRFailuresDoNotAdvanceOrPublish(t *testing.T) {
	for _, name := range []string{"unknown-repository", "duplicate-repository", "invalid-id", "missing", "wip", "under-dev", "conflict", "closed", "missing-source", "invalid-source", "filtered", "disabled"} {
		t.Run(name, func(t *testing.T) {
			cu, cfg, svc := focusedFixture(t)
			repository, id := "lzvsg/gvs", 8
			switch name {
			case "unknown-repository":
				repository = "unconfigured/repo"
			case "duplicate-repository":
				cfg.Codeup.Repos = append(cfg.Codeup.Repos, cfg.Codeup.Repos[0])
			case "invalid-id":
				id = 0
			case "missing":
				id = 77
			case "wip":
				cu.mrs[1]["title"] = "WIP: fix"
			case "under-dev":
				cu.mrs[1]["state"] = "UNDER_DEV"
			case "conflict":
				cu.mrs[1]["mergeStatus"] = "conflict"
			case "closed":
				cu.mrs[1]["state"] = "CLOSED"
			case "missing-source":
				cu.patchSets = cu.patchSets[1:]
			case "invalid-source":
				cu.patchSets[0]["commitId"] = ""
			case "filtered":
				cfg.Review.IncludePaths = []string{"missing/**"}
			case "disabled":
				cfg.Codeup.MRReview = false
			}
			if err := focusedCall(t, svc, repository, id); err == nil {
				t.Fatal("focused request silently succeeded")
			}
			_, commits, comments := cu.snapshot()
			if len(commits) != 0 || len(comments) != 0 {
				t.Fatalf("failed request published: %v %v", commits, comments)
			}
			if got := baseline(t, cfg.Codeup.StatePath, "mr:lzvsg/gvs:8"); got != "" {
				t.Fatalf("failed request advanced baseline: %s", got)
			}
		})
	}
}

func TestFocusedMRPublicationGates(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		t.Run(map[bool]string{true: "dry-run", false: "comment-disabled"}[dryRun], func(t *testing.T) {
			cu, cfg, svc := focusedFixture(t)
			cfg.Review.DryRun = dryRun
			cfg.Codeup.MRComment = dryRun
			if err := focusedCall(t, svc, "lzvsg/gvs", 8); err != nil {
				t.Fatal(err)
			}
			_, _, comments := cu.snapshot()
			if len(comments) != 0 {
				t.Fatalf("gate allowed publication: %v", comments)
			}
			if got := baseline(t, cfg.Codeup.StatePath, "mr:lzvsg/gvs:8"); got != "full-source-sha" {
				t.Fatalf("baseline semantics changed: %s", got)
			}
		})
	}
}
