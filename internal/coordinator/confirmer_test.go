package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gvs-pr-agent/internal/agent"
	"gvs-pr-agent/internal/llm"
)

type confirmModel struct {
	calls           int
	maxRequestBytes int
}

func (m *confirmModel) Chat(_ context.Context, _ string, messages []llm.Message) (string, llm.Usage, error) {
	m.calls++
	size := 0
	for _, message := range messages {
		size += len(message.Content)
	}
	if size > m.maxRequestBytes {
		m.maxRequestBytes = size
	}
	const marker = "## 待确认候选问题\n"
	user := messages[1].Content
	var input []confirmationCandidate
	if err := json.Unmarshal([]byte(user[strings.LastIndex(user, marker)+len(marker):]), &input); err != nil {
		return "", llm.Usage{}, err
	}
	decisions := []map[string]any{}
	for _, is := range input {
		decisions = append(decisions, map[string]any{"candidate_id": is.ID, "confirmed": true, "reason": "实际diff证据一致"})
	}
	b, _ := json.Marshal(map[string]any{"overview": "核对候选", "decisions": decisions})
	return string(b), llm.Usage{TotalTokens: 30}, nil
}

func TestConfirmationBatchesByEncodedEvidenceBytes(t *testing.T) {
	model := &confirmModel{}
	patch := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+" + strings.Repeat("x", maxConfirmationBytes*3/8) + "\n"
	var candidates []agent.Issue
	for i := 0; i < 5; i++ {
		candidates = append(candidates, agent.Issue{File: "a.go", Line: 1, Problem: fmt.Sprintf("p%d", i)})
	}
	got, _, err := NewConfirmer(model).Confirm(context.Background(), ConfirmationInput{Diff: patch, Candidates: candidates})
	if err != nil || len(got.Issues) != 5 || model.calls != 3 || model.maxRequestBytes > maxConfirmationBytes {
		t.Fatalf("issues=%d calls=%d bytes=%d error=%v", len(got.Issues), model.calls, model.maxRequestBytes, err)
	}
}

func TestConfirmationAcceptsFileNear120KBWithoutTruncation(t *testing.T) {
	model := &confirmModel{}
	patch := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+" + strings.Repeat("x", 119000) + "\n"
	got, _, err := NewConfirmer(model).Confirm(context.Background(), ConfirmationInput{Diff: patch, Candidates: []agent.Issue{{File: "a.go", Line: 1, Problem: "candidate"}}})
	if err != nil || len(got.Issues) != 1 || model.calls != 1 || got.DiffReviewedBytes != len(patch) {
		t.Fatalf("large evidence not fully confirmed: result=%+v calls=%d error=%v", got, model.calls, err)
	}
}

func TestConfirmationRejectsEscapedEvidenceOverBudgetWithoutModelCall(t *testing.T) {
	model := &confirmModel{}
	patch := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+" + strings.Repeat("\\", maxConfirmationBytes/2+5000) + "\n"
	_, _, err := NewConfirmer(model).Confirm(context.Background(), ConfirmationInput{Diff: patch, Candidates: []agent.Issue{{File: "a.go", Line: 1, Problem: "candidate"}}})
	if err == nil || model.calls != 0 {
		t.Fatalf("escaped evidence silently truncated: calls=%d error=%v", model.calls, err)
	}
}

func TestConfirmerProcessesAllCandidatesBeyondFortyAndDiffPrefix(t *testing.T) {
	model := &confirmModel{}
	patch := "diff --git a/tail.go b/tail.go\n--- a/tail.go\n+++ b/tail.go\n@@ -1 +1 @@\n-old\n+new\n"
	prefix := "diff --git a/large.go b/large.go\n--- a/large.go\n+++ b/large.go\n@@ -1 +1 @@\n-old\n+" + strings.Repeat("x", 90000) + "\n"
	var candidates []agent.Issue
	for i := 0; i < 45; i++ {
		candidates = append(candidates, agent.Issue{File: "tail.go", Line: 1, Severity: "high", Problem: fmt.Sprintf("candidate-%d", i)})
	}
	got, usage, err := NewConfirmer(model).Confirm(context.Background(), ConfirmationInput{Diff: prefix + patch, Candidates: candidates})
	if err != nil || len(got.Issues) != 45 || got.UnconfirmedCount != 0 || model.calls != 2 || usage.TotalTokens != 60 {
		t.Fatalf("issues=%d unconfirmed=%d calls=%d usage=%+v error=%v", len(got.Issues), got.UnconfirmedCount, model.calls, usage, err)
	}
}

func TestConfirmerRejectsMissingDecision(t *testing.T) {
	patch := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"
	got, _, err := NewConfirmer(protocolModel{content: `{"decisions":[]}`}).Confirm(context.Background(), ConfirmationInput{Diff: patch, Candidates: []agent.Issue{{File: "a.go", Line: 1, Problem: "original"}}})
	if err == nil || got.UnconfirmedCount != 1 {
		t.Fatalf("missing decision accepted: %+v %v", got, err)
	}
}

func TestConfirmerReturnsOnlyConfirmedIssuesSortedByRisk(t *testing.T) {
	model := &confirmModel{}
	c := NewConfirmer(model)
	got, usage, err := c.Confirm(context.Background(), ConfirmationInput{
		ChangeContext: "意图：收紧输入校验",
		Diff:          "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -3 +3 @@\n-old\n+items[index]\n" + "diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -9 +9 @@\n-old\n+return err\n",
		Candidates: []agent.Issue{
			{Severity: "medium", File: "b.go", Line: 9, Problem: "候选一"},
			{Severity: "critical", File: "a.go", Line: 3, Problem: "候选二"},
		},
		Model: "test-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 || usage.TotalTokens != 30 || got.RejectedCount != 0 || len(got.Issues) != 2 {
		t.Fatalf("result=%+v usage=%+v calls=%d", got, usage, model.calls)
	}
	if got.Issues[0].Severity != "critical" || got.Issues[0].AgentName != "统筹确认 Agent" {
		t.Fatalf("issues not sorted/attributed: %+v", got.Issues)
	}
}
