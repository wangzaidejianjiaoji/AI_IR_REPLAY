package coordinator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gvs-pr-agent/internal/agent"
	"gvs-pr-agent/internal/llm"
)

type confirmModel struct{ calls int }

func (m *confirmModel) Chat(_ context.Context, _ string, messages []llm.Message) (string, llm.Usage, error) {
	m.calls++
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
