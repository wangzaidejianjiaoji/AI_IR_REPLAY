package analyzer

import (
	"context"
	"encoding/json"
	"fmt"
	"gvs-pr-agent/internal/llm"
	"strings"
)

// AnalyzeBatches reads every bounded diff, then hierarchically combines the
// facts into the same shared context used by all reviewers. No input is cut.
func (a *Analyzer) AnalyzeBatches(ctx context.Context, in Input, diffs []string, maxBytes int) (ChangeContext, llm.Usage, error) {
	if maxBytes <= 0 {
		return ChangeContext{}, llm.Usage{}, fmt.Errorf("全局分析批次预算必须大于零")
	}
	var usage llm.Usage
	add := func(u llm.Usage) {
		usage.PromptTokens += u.PromptTokens
		usage.CompletionTokens += u.CompletionTokens
		usage.TotalTokens += u.TotalTokens
	}
	var facts []ChangeContext
	for _, patch := range diffs {
		if len(patch) > maxBytes {
			return ChangeContext{}, usage, fmt.Errorf("全局分析 diff 超出批次预算，拒绝截断")
		}
		batch := in
		batch.Diff = patch
		fact, u, err := a.Analyze(ctx, batch)
		add(u)
		if err != nil {
			return ChangeContext{}, usage, err
		}
		facts = append(facts, fact)
	}
	if len(facts) == 0 {
		return ChangeContext{}, usage, fmt.Errorf("全局分析无 diff 批次")
	}
	for len(facts) > 1 {
		const prefix = "批次分析结果：\n"
		mergeBudget := maxBytes - len(prefix)
		var groups [][]string
		var group []string
		size := 0
		for _, fact := range facts {
			encoded, err := json.Marshal(fact)
			if err != nil {
				return ChangeContext{}, usage, err
			}
			if len(encoded)+1 > mergeBudget {
				return ChangeContext{}, usage, fmt.Errorf("全局分析摘要超出合并预算，拒绝截断")
			}
			if size+len(encoded)+1 > mergeBudget {
				groups = append(groups, group)
				group = nil
				size = 0
			}
			group = append(group, string(encoded))
			size += len(encoded) + 1
		}
		if len(group) > 0 {
			groups = append(groups, group)
		}
		if len(groups) >= len(facts) {
			return ChangeContext{}, usage, fmt.Errorf("全局分析摘要无法在预算内归并，请提高 max_diff_chars")
		}
		var combined []ChangeContext
		for _, group := range groups {
			batch := in
			batch.Body += "\n本步骤合并各批次分析结果；覆盖所有批次的变更意图、行为约束和模块关系，披露矛盾与不确定性。摘要是待综合的分析，不是原始源码证据。"
			batch.Diff = prefix + strings.Join(group, "\n")
			fact, u, err := a.Analyze(ctx, batch)
			add(u)
			if err != nil {
				return ChangeContext{}, usage, err
			}
			combined = append(combined, fact)
		}
		facts = combined
	}
	return facts[0], usage, nil
}
