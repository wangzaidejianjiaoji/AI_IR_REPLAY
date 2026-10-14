package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"gvs-pr-agent/internal/agent"
	"gvs-pr-agent/internal/diff"
	"gvs-pr-agent/internal/llm"
	"gvs-pr-agent/internal/textbudget"
	"io"
	"sort"
	"strings"
)

const maxConfirmCandidates = 40

type confirmChatClient interface {
	Chat(context.Context, string, []llm.Message) (string, llm.Usage, error)
}
type Confirmer struct{ client confirmChatClient }

func NewConfirmer(client confirmChatClient) *Confirmer { return &Confirmer{client: client} }

type ConfirmationInput struct {
	ChangeContext string
	Diff          string
	Candidates    []agent.Issue
	Model         string
}
type ConfirmationResult struct {
	Overview          string
	Issues            []agent.Issue
	RejectedCount     int
	UnconfirmedCount  int
	DiffTotalBytes    int
	DiffReviewedBytes int
}
type confirmationCandidate struct {
	ID       string      `json:"candidate_id"`
	Issue    agent.Issue `json:"issue"`
	Evidence string      `json:"evidence"`
}

func (c *Confirmer) Confirm(ctx context.Context, in ConfirmationInput) (ConfirmationResult, llm.Usage, error) {
	if c == nil || c.client == nil {
		return ConfirmationResult{}, llm.Usage{}, fmt.Errorf("统筹确认 Agent 未配置 LLM 客户端")
	}
	result := ConfirmationResult{DiffTotalBytes: len(in.Diff)}
	visible := textbudget.Limit(in.Diff, 80000)
	result.DiffReviewedBytes = len(visible)
	if len(in.Candidates) == 0 {
		result.Overview = "无候选问题，不调用确认模型"
		return result, llm.Usage{}, nil
	}
	// Never validate against a hunk cut in half by the confirmation input budget.
	if len(visible) < len(in.Diff) {
		if i := strings.LastIndex(visible, "\ndiff --git "); i >= 0 {
			visible = visible[:i+1]
		} else {
			visible = ""
		}
	}
	result.DiffReviewedBytes = len(visible)
	files := map[string]*diff.FileDiff{}
	for _, f := range diff.Parse(visible) {
		files[f.Path] = f
	}
	candidates := append([]agent.Issue(nil), in.Candidates...)
	sort.SliceStable(candidates, func(i, j int) bool {
		return agent.SeverityRank(candidates[i].Severity) > agent.SeverityRank(candidates[j].Severity)
	})
	allowed := map[string]agent.Issue{}
	var input []confirmationCandidate
	for _, is := range candidates {
		f := files[is.File]
		if f == nil || is.Line < 0 || (is.Line > 0 && !f.ContainsLine(is.Line)) {
			continue
		}
		b, _ := json.Marshal(is)
		id := fmt.Sprintf("%x", sha256.Sum256(b))
		if _, ok := allowed[id]; ok {
			continue
		}
		if len(input) >= maxConfirmCandidates {
			break
		}
		// Evidence is the exact, versioned diff file. No model-generated replacement.
		allowed[id] = is
		input = append(input, confirmationCandidate{ID: id, Issue: is, Evidence: f.Patch})
	}
	if len(input) == 0 {
		return result, llm.Usage{}, fmt.Errorf("统筹确认缺少完整 diff 证据，拒绝确认")
	}
	encoded, _ := json.Marshal(input)
	system := `你是最终代码审查统筹确认 Agent。逐项核对候选、其源码证据、变更意图和实际逻辑。禁止新增或改写问题、位置和建议。只为输入 candidate_id 返回 confirmed=true/false 和 reason（确认、驳回或重复原因）。所有输入 ID 各返回一次；只输出 JSON：{"overview":"简述","decisions":[{"candidate_id":"输入ID","confirmed":true,"reason":"证据理由"}]}`
	user := "## 全局理解与变更意图\n" + in.ChangeContext + "\n\n## 当前 Diff\n" + visible + "\n\n## 待确认候选问题\n" + string(encoded)
	content, usage, err := c.client.Chat(ctx, in.Model, []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}})
	if err != nil {
		return result, usage, fmt.Errorf("统筹确认 Agent: %w", err)
	}
	var raw struct {
		Overview  string `json:"overview"`
		Decisions *[]struct {
			ID        string `json:"candidate_id"`
			Confirmed *bool  `json:"confirmed"`
			Reason    string `json:"reason"`
		} `json:"decisions"`
	}
	content = strings.TrimSpace(content)
	if i := strings.Index(content, "{"); i >= 0 {
		if j := strings.LastIndex(content, "}"); j > i {
			content = content[i : j+1]
		}
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return result, usage, fmt.Errorf("统筹确认协议错误: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return result, usage, fmt.Errorf("统筹确认存在多余 JSON 内容")
	}
	if raw.Decisions == nil {
		return result, usage, fmt.Errorf("统筹确认缺少 decisions")
	}
	seen := map[string]bool{}
	for _, d := range *raw.Decisions {
		original, ok := allowed[d.ID]
		if !ok || seen[d.ID] || d.Confirmed == nil || strings.TrimSpace(d.Reason) == "" {
			return result, usage, fmt.Errorf("统筹确认 ID/证据决定无效或重复")
		}
		seen[d.ID] = true
		if !*d.Confirmed {
			result.RejectedCount++
			continue
		}
		original.AgentRole = "coordinator"
		original.AgentName = "统筹确认 Agent"
		result.Issues = append(result.Issues, original)
	}
	result.UnconfirmedCount = len(in.Candidates) - len(seen)
	result.Overview = fmt.Sprintf("确认 %d，驳回 %d，未确认 %d（候选或文本预算/遗漏）。%s", len(result.Issues), result.RejectedCount, result.UnconfirmedCount, raw.Overview)
	sort.SliceStable(result.Issues, func(i, j int) bool {
		a, b := result.Issues[i], result.Issues[j]
		if agent.SeverityRank(a.Severity) != agent.SeverityRank(b.Severity) {
			return agent.SeverityRank(a.Severity) > agent.SeverityRank(b.Severity)
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return result, usage, nil
}
func limitConfirmation(value string, max int) string { return textbudget.Limit(value, max) }
