package review

import (
	"context"
	"testing"
)

func TestRoundBudgetIncludesAllBranchReviews(t *testing.T) {
	cu := newMockCodeup(t)
	srv, calls := newMockLLM(t)
	cfg := testConfig(t, cu.srv.URL, srv.URL)
	cfg.Codeup.ReviewFirstPush = true
	cfg.Review.MaxLLMCalls = 1
	cu.setBranches(branch("master", "master-sha", true), branch("qatool-one", "one", false), branch("qatool-two", "two", false))
	svc := newTestService(t, cfg)
	svc.RunOnce(context.Background())
	if *calls != 1 {
		t.Fatalf("round budget ignored across reviews: requests=%d", *calls)
	}
	if got := baseline(t, cfg.Codeup.StatePath, "lzvsg/gvs:qatool-two"); got != "" {
		t.Fatalf("budget exhausted but baseline advanced: %s", got)
	}
}
