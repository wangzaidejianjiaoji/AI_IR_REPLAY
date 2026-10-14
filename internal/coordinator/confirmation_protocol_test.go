package coordinator

import (
	"context"
	"gvs-pr-agent/internal/agent"
	"gvs-pr-agent/internal/llm"
	"strings"
	"testing"
)

type protocolModel struct{ content string }

func (m protocolModel) Chat(context.Context, string, []llm.Message) (string, llm.Usage, error) {
	return m.content, llm.Usage{}, nil
}

func TestConfirmationCannotRewriteCandidateAtAllowedPosition(t *testing.T) {
	c := NewConfirmer(protocolModel{content: `{"issues":[{"file":"a.go","line":1,"problem":"invented","detail":"fabricated","severity":"critical"}]}`})
	_, _, err := c.Confirm(context.Background(), ConfirmationInput{
		Diff:       "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n",
		Candidates: []agent.Issue{{File: "a.go", Line: 1, Problem: "original", Severity: "low"}},
	})
	if err == nil {
		t.Fatal("same-position invented finding was accepted without a candidate ID")
	}
}

func TestConfirmationDisclosesUnprocessedCandidates(t *testing.T) {
	issues := make([]agent.Issue, 41)
	for i := range issues {
		issues[i] = agent.Issue{File: "a.go", Line: 1, Problem: "different", Severity: "high"}
	}
	_, _, err := NewConfirmer(protocolModel{content: `{"decisions":[]}`}).Confirm(context.Background(), ConfirmationInput{Diff: "+new", Candidates: issues})
	// Missing identities and evidence must not become an apparent complete review.
	if err == nil || !strings.Contains(err.Error(), "确认") {
		t.Fatalf("incomplete confirmation protocol accepted: %v", err)
	}
}
