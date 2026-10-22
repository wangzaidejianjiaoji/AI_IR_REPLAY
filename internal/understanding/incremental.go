package understanding

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gvs-pr-agent/internal/llm"
	"gvs-pr-agent/internal/progress"
)

// Evidence is never truncated. A batch is also bounded after JSON escaping.
func evidenceBatches(text string) []string {
	return boundedBatches(text, 12000)
}

func boundedBatches(text string, maxBytes int) []string {
	var batches []string
	for len(text) > 0 {
		n := len(text)
		if n > maxBytes {
			n = maxBytes
		}
		for n > 0 && n < len(text) && !utf8.RuneStart(text[n]) {
			n--
		}
		if n < len(text) {
			if newline := strings.LastIndexByte(text[:n], '\n'); newline >= n/2 {
				n = newline + 1
			}
		}
		for {
			encoded, _ := json.Marshal(text[:n])
			if len(encoded) <= 24000 {
				break
			}
			n /= 2
			for n > 0 && !utf8.RuneStart(text[n]) {
				n--
			}
		}
		batches = append(batches, text[:n])
		text = text[n:]
	}
	return batches
}

var contextWord = regexp.MustCompile(`[A-Za-z_][A-Za-z_0-9]{2,}`)

// Rank existing passages by symbols mentioned in this diff, retaining the opening
// overview. Unselected text stays untouched locally; it is never regenerated.
func incrementalContext(doc, evidence string) string {
	words := map[string]bool{}
	for _, word := range contextWord.FindAllString(strings.ToLower(evidence), -1) {
		words[word] = true
	}
	type passage struct {
		text  string
		score int
		index int
	}
	var passages []passage
	for i, part := range boundedBatches(doc, 4000) {
		score := 0
		seen := map[string]bool{}
		for _, word := range contextWord.FindAllString(strings.ToLower(part), -1) {
			if words[word] && !seen[word] {
				score++
				seen[word] = true
			}
		}
		if i == 0 {
			score += 1000000
		}
		passages = append(passages, passage{part, score, i})
	}
	sort.SliceStable(passages, func(i, j int) bool { return passages[i].score > passages[j].score })
	var selected []passage
	budget := 24000
	for _, part := range passages {
		encoded, _ := json.Marshal(part.text)
		if len(encoded) <= budget {
			selected = append(selected, part)
			budget -= len(encoded)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].index < selected[j].index })
	var out strings.Builder
	for _, part := range selected {
		out.WriteString(part.text)
		out.WriteString("\n\n")
	}
	return out.String()
}

type knowledgePatch struct {
	Replacements []struct {
		Old string `json:"old"`
		New string `json:"new"`
	} `json:"replacements"`
	Append string `json:"append"`
}

func applyKnowledgePatch(current, visible, response string) (string, error) {
	response = strings.TrimSpace(response)
	response = strings.TrimPrefix(response, "```json")
	response = strings.TrimSuffix(response, "```")
	var patch knowledgePatch
	decoder := json.NewDecoder(strings.NewReader(response))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&patch); err != nil {
		return "", fmt.Errorf("理解增量补丁格式无效: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", fmt.Errorf("理解增量补丁包含多余内容")
	}
	if patch.Replacements == nil {
		return "", fmt.Errorf("理解增量补丁缺少 replacements 数组")
	}
	updated := current
	for _, replacement := range patch.Replacements {
		if replacement.Old == "" || !strings.Contains(visible, replacement.Old) || strings.Count(updated, replacement.Old) != 1 {
			return "", fmt.Errorf("理解增量补丁的旧文本必须在可见上下文中且全局唯一")
		}
		if sourceSHARe.MatchString(replacement.New) || sourceRepositoryRe.MatchString(replacement.New) {
			return "", fmt.Errorf("理解增量补丁不得修改来源标记")
		}
		updated = strings.Replace(updated, replacement.Old, replacement.New, 1)
	}
	if sourceSHARe.MatchString(patch.Append) || sourceRepositoryRe.MatchString(patch.Append) {
		return "", fmt.Errorf("理解增量补丁不得追加来源标记")
	}
	if strings.TrimSpace(patch.Append) != "" {
		updated += "\n\n" + strings.TrimSpace(patch.Append)
	}
	if strings.TrimSpace(updated) == "" {
		return "", fmt.Errorf("理解增量补丁不能清空文档")
	}
	return updated, nil
}

func (m *Manager) updateIncrementally(ctx context.Context, modelName, doc, delta, previous, head string) (string, llm.Usage, error) {
	current := sourceSHARe.ReplaceAllString(doc, "")
	current = sourceRepositoryRe.ReplaceAllString(current, "")
	batches := evidenceBatches(delta)
	var total llm.Usage
	fileScope, hunkScope := "", ""
	offset := 0
	for i, evidence := range batches {
		if err := ctx.Err(); err != nil {
			return "", total, err
		}
		progress.Stage(ctx, fmt.Sprintf("understanding %d/%d", i+1, len(batches)), "", "")
		scope := fileScope + "\n" + hunkScope
		if offset > 0 && delta[offset-1] != '\n' {
			lineStart := strings.LastIndexByte(delta[:offset], '\n') + 1
			scope += fmt.Sprintf("\nContinued diff line: line_prefix=%q byte_offset=%d; '+' is added, '-' is removed, space is unchanged context.", delta[lineStart:lineStart+1], offset-lineStart)
		}
		visible := incrementalContext(current, scope+"\n"+evidence)
		payload, _ := json.Marshal(struct {
			Repository string `json:"repository"`
			From       string `json:"from_sha"`
			To         string `json:"to_sha"`
			Batch      int    `json:"batch"`
			Batches    int    `json:"batches"`
			Current    string `json:"current_passages"`
			Evidence   string `json:"evidence"`
			Scope      string `json:"continuation_scope"`
		}{m.cfg.SourceRepository, previous, head, i + 1, len(batches), visible, evidence, scope})
		if len(payload) > 60000 {
			return "", total, fmt.Errorf("理解增量请求超过单批预算，未写入新版本")
		}
		messages := []llm.Message{{Role: "system", Content: `你是全局代码理解维护 Agent。仅根据本批新增代码证据增量维护理解，禁止重写整个文档。current_passages 是既有文档的相关片段，其余内容会原样保留。输出 JSON：{"replacements":[{"old":"逐字匹配且唯一的既有段落","new":"更新后的段落"}],"append":"需要新增的 Markdown 小节或为空"}。无知识变化时返回 {"replacements":[],"append":""}。优先替换被本次代码推翻的陈述，只有确需新增知识才追加，不重复流水账。只修改可见片段，不得删除或生成 SOURCE-SHA/SOURCE-REPOSITORY 标记。批次可能在文件或长行中间分割，不能把未出现的部分推断为已删除；跨模块结论必须有证据。`}, {Role: "user", Content: string(payload)}}
		response, usage, err := m.model.Chat(ctx, modelName, messages)
		total.PromptTokens += usage.PromptTokens
		total.CompletionTokens += usage.CompletionTokens
		total.TotalTokens += usage.TotalTokens
		if err != nil {
			return "", total, fmt.Errorf("理解增量批次 %d/%d: %w", i+1, len(batches), err)
		}
		updated, err := applyKnowledgePatch(current, visible, response)
		if err != nil {
			llm.InvalidateCheckpoint(ctx, m.model, modelName, messages)
			return "", total, err
		}
		current = updated
		offset += len(evidence)
		for _, line := range strings.Split(evidence, "\n") {
			if len(line) > 1000 {
				continue
			}
			if strings.HasPrefix(line, "diff --git ") {
				fileScope = line
				hunkScope = ""
			}
			if strings.HasPrefix(line, "@@ ") {
				hunkScope = line
			}
		}
		if m.logger != nil {
			m.logger.Info("理解增量批次完成", "batch", i+1, "batches", len(batches), "evidence_bytes", len(evidence), "total_tokens", total.TotalTokens)
		}
	}
	return current, total, nil
}
