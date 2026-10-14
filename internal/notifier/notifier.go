// Package notifier 生成统一 PR 评论（多 Agent 汇总报告）并发布到 GitHub。
package notifier

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"gvs-pr-agent/internal/agent"
	"gvs-pr-agent/internal/coordinator"
)

// MarkerPrefix 审查存证标识前缀，配合 Commit SHA 实现去重。
const MarkerPrefix = "<!-- AI-REVIEW: "

// Marker 生成指定 Commit SHA 的隐藏存证标识。
func Marker(commitSHA string) string {
	return MarkerPrefix + commitSHA + " -->"
}

// Options 评论生成选项。
type Options struct {
	MentionAuthor    bool
	MentionReviewers bool
	MaxIssuesDisplay int
	Author           string   // PR 提交人 login
	Reviewers        []string // Assignee/Reviewer login 列表
	CommitSHA        string
	Timezone         *time.Location
}

// BuildComment 按第八章统一评论格式生成 Markdown 评论正文。
func BuildComment(sum coordinator.Summary, opt Options) string {
	var sb strings.Builder
	sb.WriteString("## AI 代码审查报告（多 Agent 协作）\n\n")

	// @mention 提交人与合并人
	var mentions []string
	if opt.MentionAuthor && opt.Author != "" {
		mentions = append(mentions, "@"+opt.Author)
	}
	if opt.MentionReviewers {
		for _, r := range opt.Reviewers {
			if r != "" && r != opt.Author {
				mentions = append(mentions, "@"+r)
			}
		}
	}
	if len(mentions) > 0 {
		sb.WriteString(strings.Join(mentions, " ") + "\n\n")
	}

	sb.WriteString("### 本次代码变更总结\n\n")
	writeOverview(&sb, sum.ChangeSummary, "全局理解阶段未返回变更总结。")

	sb.WriteString("### 验证总览\n\n")
	writeOverview(&sb, sum.VerificationOverview, "全局理解、规则匹配与专业 Agent 审查已执行；详见审查元数据。")
	if sum.AnalysisTotalBytes > 0 && sum.AnalysisReviewedBytes > 0 {
		fmt.Fprintf(&sb, "变更分析输入：%d/%d 字节。\n\n", sum.AnalysisReviewedBytes, sum.AnalysisTotalBytes)
	}
	if len(sum.TruncatedPackages) > 0 {
		fmt.Fprintf(&sb, "> ⚠️ 专业审查 diff 已截断的包：%s。文件纳入不代表全文审查。\n\n", strings.Join(sum.TruncatedPackages, "、"))
	}

	sb.WriteString("### 统筹确认总览\n\n")
	writeOverview(&sb, sum.CoordinationOverview, "统筹确认阶段未返回额外说明；以下为经定位和反思过滤后的问题。")
	if sum.ConfirmationTotalBytes > 0 {
		fmt.Fprintf(&sb, "最终确认候选文件证据：%d/%d 字节；未确认候选：%d。\n\n", sum.ConfirmationReviewedBytes, sum.ConfirmationTotalBytes, sum.UnconfirmedCount)
	}

	sb.WriteString("### 确认的问题\n\n")

	if len(sum.Issues) == 0 && (len(sum.TruncatedPackages) > 0 || sum.UnconfirmedCount > 0 || sum.FailedJobs > 0 || (sum.AnalysisReviewedBytes > 0 && sum.AnalysisReviewedBytes < sum.AnalysisTotalBytes)) {
		sb.WriteString("**审查不完整**：已执行范围未确认问题，不能视为通过。\n\n")
	} else if len(sum.Issues) == 0 {
		sb.WriteString("**审查结果：通过** ✅ 本次变更未发现明显问题。\n\n")
	} else {
		fmt.Fprintf(&sb, "**审查结果：发现问题**（共 %d 个，由 %d 个 Agent 发现，%d 个审查包）\n\n",
			len(sum.Issues), countAgents(sum.Issues), len(sum.Packages))
		writeIssuesByAgent(&sb, sum.Issues, opt.MaxIssuesDisplay)
	}

	// 【覆盖范围披露】变更文件超限时明确告知未全量审查，避免误导
	if sum.TotalFiles > sum.Reviewed && sum.Reviewed > 0 {
		fmt.Fprintf(&sb, "> ⚠️ 变更文件共 %d 个（筛选后），本次仅审查前 %d 个，其余未覆盖。\n\n",
			sum.TotalFiles, sum.Reviewed)
	}

	if len(sum.FailedRoles) > 0 {
		fmt.Fprintf(&sb, "> ⚠️ 以下 Agent 本次审查失败（已跳过）：%s\n\n",
			strings.Join(sum.FailedRoles, "、"))
	}

	// 尾部元信息 + 隐藏存证标识
	loc := opt.Timezone
	if loc == nil {
		loc = time.Local
	}
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "*审查时间：%s | Commit：`%s` | Token 消耗：%d*\n",
		time.Now().In(loc).Format("2006-01-02 15:04:05"), short(opt.CommitSHA), sum.TotalUsage.TotalTokens)
	if sum.TotalAgents > len(sum.AgentNames) {
		// 智能打包按变更文件路径分配角色，未命中的角色不参与（非故障）
		fmt.Fprintf(&sb, "*参与 Agent：%s（%d/%d，按变更文件路径智能分配）*\n",
			strings.Join(sum.AgentNames, "、"), len(sum.AgentNames), sum.TotalAgents)
	} else {
		fmt.Fprintf(&sb, "*参与 Agent：%s*\n", strings.Join(sum.AgentNames, "、"))
	}
	if len(sum.Packages) > 0 {
		fmt.Fprintf(&sb, "*审查包：%s*\n", strings.Join(sum.Packages, "、"))
	}
	sb.WriteString("\n")
	sb.WriteString(Marker(opt.CommitSHA) + "\n")
	return sb.String()
}

func writeOverview(sb *strings.Builder, content, fallback string) {
	content = strings.TrimSpace(content)
	if content == "" {
		content = fallback
	}
	sb.WriteString(content + "\n\n")
}

// writeIssuesByAgent 按 Agent 分组输出问题表格与详细说明。
func writeIssuesByAgent(sb *strings.Builder, issues []agent.Issue, maxDisplay int) {
	// 按 Agent 分组（保持 issues 已有的严重度排序）
	groups := map[string][]agent.Issue{}
	var order []string
	for _, is := range issues {
		if _, ok := groups[is.AgentName]; !ok {
			order = append(order, is.AgentName)
		}
		groups[is.AgentName] = append(groups[is.AgentName], is)
	}

	displayed := 0
	var folded []agent.Issue
	for _, name := range order {
		group := groups[name]
		fmt.Fprintf(sb, "### %s（发现 %d 个问题）\n\n", name, len(group))
		sb.WriteString("| 严重度 | 文件 | 行号 | 问题 | 建议 |\n")
		sb.WriteString("|--------|------|------|------|------|\n")
		for _, is := range group {
			fmt.Fprintf(sb, "| %s | %s | %s | %s | %s |\n",
				is.Severity, is.File, lineStr(is.Line), cell(is.Problem), cell(is.Suggestion))
		}
		sb.WriteString("\n")

		// 详细说明（超出 maxDisplay 的折叠）
		for _, is := range group {
			if displayed >= maxDisplay {
				folded = append(folded, is)
				continue
			}
			displayed++
			fmt.Fprintf(sb, "**[%s] %s:%s %s**\n", is.Severity, is.File, lineStr(is.Line), is.Problem)
			if is.Detail != "" {
				fmt.Fprintf(sb, "- **问题**：%s\n", is.Detail)
			}
			if is.Suggestion != "" {
				fmt.Fprintf(sb, "- **建议**：%s\n", is.Suggestion)
			}
			if is.Code != "" {
				lang := "text"
				switch strings.ToLower(filepath.Ext(is.File)) {
				case ".go":
					lang = "go"
				case ".cpp", ".cc", ".cxx", ".h", ".hpp":
					lang = "cpp"
				case ".py":
					lang = "python"
				case ".js", ".ts", ".tsx":
					lang = "typescript"
				}
				fmt.Fprintf(sb, "- **参考代码**（%s；仅 Go 片段经过语法校验，仍需编译/测试）：\n```%s\n%s\n```\n", lang, lang, is.Code)
			}
			sb.WriteString("\n")
		}
		sb.WriteString("---\n\n")
	}

	if len(folded) > 0 {
		fmt.Fprintf(sb, "<details><summary>其余 %d 个问题（点击展开）</summary>\n\n", len(folded))
		for _, is := range folded {
			fmt.Fprintf(sb, "- **[%s]** %s:%s %s — %s\n",
				is.Severity, is.File, lineStr(is.Line), is.Problem, is.Suggestion)
		}
		sb.WriteString("\n</details>\n\n")
	}
}

func countAgents(issues []agent.Issue) int {
	set := map[string]bool{}
	for _, is := range issues {
		set[is.AgentName] = true
	}
	return len(set)
}

func lineStr(line int) string {
	if line <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", line)
}

// cell 清理表格单元格中的换行与竖线；截断时闭合落单的反引号，避免破坏 Markdown 渲染。
func cell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	if len([]rune(s)) > 60 {
		s = string([]rune(s)[:60])
		if strings.Count(s, "`")%2 == 1 {
			s += "`" // 截断切断了成对反引号，补齐闭合
		}
		s += "..."
	}
	return s
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
