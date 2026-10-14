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

const maxConfirmationBytes = 160000

const confirmationSystem = `你是最终代码审查统筹确认 Agent。逐项核对候选、其源码证据、变更意图和实际逻辑。禁止新增或改写问题、位置和建议。只为输入 candidate_id 返回 confirmed=true/false 和 reason（确认、驳回或重复原因）。所有输入 ID 各返回一次；只输出 JSON：{"overview":"简述","decisions":[{"candidate_id":"输入ID","confirmed":true,"reason":"证据理由"}]}`

func confirmationUser(changeContext string, encoded []byte) string {
	return "## 全局理解与变更意图\n" + changeContext + "\n\n每个候选的 evidence 是当前版本的完整文件 diff；逐项核对，不得省略任何输入 ID。\n\n## 待确认候选问题\n" + string(encoded)
}

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
	var result ConfirmationResult
	var usage llm.Usage
	files := map[string]*diff.FileDiff{}
	for _, f := range diff.Parse(in.Diff) {
		files[f.Path] = f
	}
	candidates := append([]agent.Issue(nil), in.Candidates...)
	sort.SliceStable(candidates, func(i, j int) bool {
		return agent.SeverityRank(candidates[i].Severity) > agent.SeverityRank(candidates[j].Severity)
	})
	seen := map[string]bool{}
	reviewedFiles := map[string]bool{}
	expectedFiles := map[string]bool{}
	var batch []agent.Issue
	var evidence []confirmationCandidate
	var overviews []string
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		got, u, err := c.confirmBatch(ctx, in, evidence)
		usage.PromptTokens += u.PromptTokens
		usage.CompletionTokens += u.CompletionTokens
		usage.TotalTokens += u.TotalTokens
		result.UnconfirmedCount += got.UnconfirmedCount
		if err != nil {
			return err
		}
		result.Issues = append(result.Issues, got.Issues...)
		result.RejectedCount += got.RejectedCount
		overviews = append(overviews, got.Overview)
		for _, issue := range batch {
			if !reviewedFiles[issue.File] {
				result.DiffReviewedBytes += len(files[issue.File].Patch)
				reviewedFiles[issue.File] = true
			}
		}
		batch = nil
		evidence = nil
		return nil
	}
	for _, issue := range candidates {
		f := files[issue.File]
		if f == nil || issue.Line < 0 || (issue.Line > 0 && !f.ContainsLine(issue.Line)) {
			return result, usage, fmt.Errorf("统筹确认缺少完整 diff 证据：%s:%d", issue.File, issue.Line)
		}
		raw, _ := json.Marshal(issue)
		id := fmt.Sprintf("%x", sha256.Sum256(raw))
		if seen[id] {
			continue
		}
		seen[id] = true
		if !expectedFiles[issue.File] {
			result.DiffTotalBytes += len(f.Patch)
			expectedFiles[issue.File] = true
		}
		candidate := confirmationCandidate{ID: id, Issue: issue, Evidence: f.Patch}
		next := append(append([]confirmationCandidate(nil), evidence...), candidate)
		encoded, _ := json.Marshal(next)
		if len(batch) >= maxConfirmCandidates || len(confirmationUser(in.ChangeContext, encoded))+len(confirmationSystem) > maxConfirmationBytes {
			if err := flush(); err != nil {
				return result, usage, err
			}
		}
		encoded, _ = json.Marshal(append(evidence, candidate))
		if len(confirmationUser(in.ChangeContext, encoded))+len(confirmationSystem) > maxConfirmationBytes {
			return result, usage, fmt.Errorf("统筹确认完整候选证据超出批次文本预算：%s；拒绝截断", issue.File)
		}
		batch = append(batch, issue)
		evidence = append(evidence, candidate)
	}
	if err := flush(); err != nil {
		return result, usage, err
	}
	// Confirmation coverage is the evidence for candidate files, not unrelated diff.
	result.Overview = fmt.Sprintf("分批确认 %d，驳回 %d，未确认 %d。%s", len(result.Issues), result.RejectedCount, result.UnconfirmedCount, strings.Join(overviews, "；"))
	if len(candidates) == 0 {
		result.Overview = "无候选问题，不调用确认模型"
	}
	sort.SliceStable(result.Issues, func(i, j int) bool {
		a, b := result.Issues[i], result.Issues[j]
		if a.Severity != b.Severity {
			return agent.SeverityRank(a.Severity) > agent.SeverityRank(b.Severity)
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return result, usage, nil
}

func (c *Confirmer) confirmBatch(ctx context.Context, in ConfirmationInput, input []confirmationCandidate) (ConfirmationResult, llm.Usage, error) {
	var result ConfirmationResult
	allowed := map[string]agent.Issue{}
	for _, candidate := range input {
		allowed[candidate.ID] = candidate.Issue
	}
	encoded, _ := json.Marshal(input)
	content, usage, err := c.client.Chat(ctx, in.Model, []llm.Message{{Role: "system", Content: confirmationSystem}, {Role: "user", Content: confirmationUser(in.ChangeContext, encoded)}})
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
	result.UnconfirmedCount = len(input) - len(seen)
	if result.UnconfirmedCount != 0 {
		return result, usage, fmt.Errorf("统筹确认遗漏 %d 个候选，拒绝发布不完整结论", result.UnconfirmedCount)
	}
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
