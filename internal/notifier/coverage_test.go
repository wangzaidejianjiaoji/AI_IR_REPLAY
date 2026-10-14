package notifier

import (
	"gvs-pr-agent/internal/coordinator"
	"strings"
	"testing"
)

func TestAnalysisTruncationCannotReportPass(t *testing.T) {
	body := BuildComment(coordinator.Summary{AnalysisTotalBytes: 100, AnalysisReviewedBytes: 20}, Options{MaxIssuesDisplay: 10})
	if strings.Contains(body, "审查结果：通过") || !strings.Contains(body, "审查不完整") {
		t.Fatalf("truncated analysis appeared complete:\n%s", body)
	}
}

func TestReportDisclosesStageBudgets(t *testing.T) {
	body := BuildComment(coordinator.Summary{AnalysisTotalBytes: 100, AnalysisReviewedBytes: 20, TruncatedPackages: []string{"infer"}, ConfirmationTotalBytes: 100, ConfirmationReviewedBytes: 50, UnconfirmedCount: 2}, Options{MaxIssuesDisplay: 10})
	for _, want := range []string{"20/100", "50/100", "infer", "未确认候选：2"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s", want)
		}
	}
}
