// Package reflector 实现评论反思器（确定性工程层）：
//  1. 幻觉校验：问题引用的代码是否真实存在于 diff 中
//  2. 语法校验：建议参考代码是否为合法 Go 片段（go/parser）
//  3. 跨 Agent 去重：同一文件同一行的相似问题合并，标注所有相关 Agent
package reflector

import (
	"go/parser"
	"go/token"
	"log/slog"
	"path/filepath"
	"strings"

	"gvs-pr-agent/internal/agent"
	"gvs-pr-agent/internal/diff"
)

// Reflector 评论反思器。
type Reflector struct {
	checkHallucination bool // 是否启用幻觉校验（对应配置 comment_reflection.check_hallucination）
	logger             *slog.Logger
}

// New 创建评论反思器。
func New(checkHallucination bool, logger *slog.Logger) *Reflector {
	return &Reflector{checkHallucination: checkHallucination, logger: logger}
}

// Reflect 对定位后的问题执行反思校验与跨 Agent 去重，返回保留的问题与丢弃数量。
func (r *Reflector) Reflect(issues []agent.Issue, files []*diff.FileDiff) ([]agent.Issue, int) {
	dropped := 0

	// 构建 diff 全文索引（用于幻觉校验的代码引用检查）
	var diffText strings.Builder
	for _, f := range files {
		diffText.WriteString(f.Patch)
	}
	fullDiff := diffText.String()

	var checked []agent.Issue
	for _, is := range issues {
		if r.checkHallucination && isHallucination(is, fullDiff) {
			dropped++
			r.logger.Debug("反思器丢弃问题：引用代码不在 diff 中（疑似幻觉）",
				"file", is.File, "problem", is.Problem, "agent", is.AgentRole)
			continue
		}
		// 建议代码语法不合法时不丢弃问题，只清空参考代码字段
		if is.Code != "" && strings.EqualFold(filepath.Ext(is.File), ".go") && !isValidGoSnippet(is.Code) {
			r.logger.Debug("反思器清除非法参考代码", "file", is.File, "agent", is.AgentRole)
			is.Code = ""
		}
		checked = append(checked, is)
	}

	return dedupe(checked, &dropped), dropped
}

// isHallucination 幻觉启发式校验：detail 中以反引号引用的代码标识符
// 若全部不存在于 diff 文本中，判定为幻觉。无引用时不判定（宁可保守）。
func isHallucination(is agent.Issue, fullDiff string) bool {
	refs := backtickRefs(is.Detail)
	if len(refs) == 0 {
		return false
	}
	for _, ref := range refs {
		if strings.Contains(fullDiff, ref) {
			return false // 至少一个引用真实存在
		}
	}
	return true
}

// backtickRefs 提取文本中 `...` 包裹的代码引用（过滤过短或过长的片段）。
func backtickRefs(s string) []string {
	var out []string
	for {
		i := strings.IndexByte(s, '`')
		if i < 0 {
			break
		}
		s = s[i+1:]
		j := strings.IndexByte(s, '`')
		if j < 0 {
			break
		}
		ref := strings.TrimSpace(s[:j])
		s = s[j+1:]
		if len(ref) >= 3 && len(ref) <= 120 {
			out = append(out, ref)
		}
	}
	return out
}

// isValidGoSnippet 校验参考代码是否为合法 Go 片段：
// 依次尝试按整文件、顶层声明、语句块、表达式解析，任一成功即认为合法。
func isValidGoSnippet(code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return true
	}
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "", code, 0); err == nil {
		return true
	}
	if _, err := parser.ParseFile(fset, "", "package p\n"+code, 0); err == nil {
		return true
	}
	wrapped := "package p\nfunc _() {\n" + code + "\n}"
	if _, err := parser.ParseFile(fset, "", wrapped, 0); err == nil {
		return true
	}
	if _, err := parser.ParseExpr(code); err == nil {
		return true
	}
	return false
}

// dedupe 跨 Agent 去重：同一 (file, line) 且问题描述相似的合并，
// 保留描述更详细的一条，并标注所有相关 Agent。
func dedupe(issues []agent.Issue, dropped *int) []agent.Issue {
	type key struct {
		file string
		line int
	}
	grouped := map[key][]agent.Issue{}
	var order []key
	for _, is := range issues {
		k := key{is.File, is.Line}
		if _, seen := grouped[k]; !seen {
			order = append(order, k)
		}
		grouped[k] = append(grouped[k], is)
	}

	var out []agent.Issue
	for _, k := range order {
		group := grouped[k]
		var kept []agent.Issue
		for _, cand := range group {
			merged := false
			for i := range kept {
				if similar(kept[i].Problem, cand.Problem) {
					// 合并：保留描述更详细的，标注所有相关 Agent
					if len(cand.Detail) > len(kept[i].Detail) {
						agents := kept[i].AgentName
						kept[i] = cand
						kept[i].AgentName = agents + "、" + cand.AgentName
					} else if !strings.Contains(kept[i].AgentName, cand.AgentName) {
						kept[i].AgentName += "、" + cand.AgentName
					}
					*dropped++
					merged = true
					break
				}
			}
			if !merged {
				kept = append(kept, cand)
			}
		}
		out = append(out, kept...)
	}
	return out
}

// similar 简易相似判断：问题简述有包含关系或字符重合率高。
// 仅用于同一文件同一行的问题，阈值可适当放宽。
func similar(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if a == b || strings.Contains(a, b) || strings.Contains(b, a) {
		return true
	}
	// 字符集重合率（中文场景下近似度量）
	setA := map[rune]bool{}
	for _, r := range a {
		setA[r] = true
	}
	common := 0
	total := 0
	for _, r := range b {
		total++
		if setA[r] {
			common++
		}
	}
	return total > 0 && float64(common)/float64(total) > 0.5
}
