// Package prompt 构建各 Agent 的审查提示词（场景化 Prompt，聚焦专业领域）。
package prompt

import (
	"fmt"
	"strings"
)

// System 构建 Agent 的 system prompt：角色定义 + 审查重点 + 规则 + 输出格式约束。
func System(displayName, focus string, rules []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "你是一名资深的 Go 代码审查专家，角色是「%s」。\n\n", displayName)
	sb.WriteString(focus)
	sb.WriteString("\n\n")

	if len(rules) > 0 {
		sb.WriteString("## 审查规则（必须遵守）\n")
		for _, r := range rules {
			sb.WriteString("- " + r + "\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString(`## 输出格式（严格遵守）
只输出一个 JSON 数组，不要输出任何其他文字。每个元素结构如下：
[
  {
    "severity": "critical|high|medium|low",
    "file": "文件路径（必须来自 diff）",
    "line": 行号（整数，必须在 diff 变更范围内；文件级问题如误提交文件/缺失文件填 0）,
    "problem": "问题简述（20 字以内）",
    "detail": "详细说明（引用 diff 中的实际代码）",
    "suggestion": "具体可操作的修复建议",
    "code": "参考代码（可选，没有则为空字符串）"
  }
]
要求：
- 只报告你的专业领域内的问题，其他领域的问题不要报告
- 没有发现问题时输出空数组 []
- 最多报告 5 个最重要的问题
- 严禁编造 diff 中不存在的代码或行号`)
	return sb.String()
}

// User 构建用户消息：PR 元信息 + 审查包 + diff 内容。
func User(repo, title, body, pkg, diff, changeContext string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "请审查以下 Pull Request 的代码变更。\n\n")
	fmt.Fprintf(&sb, "仓库：%s\nPR 标题：%s\n", repo, title)
	if pkg != "" {
		fmt.Fprintf(&sb, "审查包：%s（本次只审查该包内的关联文件）\n", pkg)
	}
	if body != "" {
		fmt.Fprintf(&sb, "PR 描述：%s\n", truncate(body, 500))
	}
	if strings.TrimSpace(changeContext) != "" {
		sb.WriteString("\n## 全局代码理解与本次变更意图\n")
		sb.WriteString(changeContext)
		sb.WriteString("\n")
	}
	sb.WriteString("\n## Diff 内容\n```diff\n")
	sb.WriteString(diff)
	sb.WriteString("\n```\n")
	return sb.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
