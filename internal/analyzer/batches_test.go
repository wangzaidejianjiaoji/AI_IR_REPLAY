package analyzer

import (
	"context"
	"gvs-pr-agent/internal/llm"
	"strings"
	"testing"
)

type batchAnalysisModel struct {
	calls            int
	tailSeen, merged bool
}

func (m *batchAnalysisModel) Chat(_ context.Context, _ string, messages []llm.Message) (string, llm.Usage, error) {
	m.calls++
	user := messages[1].Content
	if strings.Contains(user, "TAIL_CHANGE") {
		m.tailSeen = true
	}
	if strings.Contains(user, "批次分析结果") {
		m.merged = true
	}
	return `{"summary":"batch fact","intent":"shared intent","affected_modules":["api"],"behavior_constraints":["preserve compatibility"],"comment_logic_mismatches":[]}`, llm.Usage{TotalTokens: 1}, nil
}
func TestAnalyzeBatchesReadsTailAndBuildsSharedContext(t *testing.T) {
	m := &batchAnalysisModel{}
	result, usage, err := New(m).AnalyzeBatches(context.Background(), Input{Model: "test"}, []string{"FIRST_CHANGE", "TAIL_CHANGE"}, 1000)
	if err != nil || !m.tailSeen || !m.merged || m.calls != 3 || usage.TotalTokens != 3 || result.Intent != "shared intent" {
		t.Fatalf("result=%+v usage=%+v model=%+v error=%v", result, usage, m, err)
	}
}
