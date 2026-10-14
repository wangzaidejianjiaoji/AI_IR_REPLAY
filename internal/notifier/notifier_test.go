// notifier 呈现层回归测试（源自真实演练评论内容评估）：
// 覆盖范围披露、参与 Agent 表述、表格截断反引号闭合、文件级问题行号。
package notifier

import (
	"strings"
	"testing"

	"gvs-pr-agent/internal/agent"
	"gvs-pr-agent/internal/coordinator"
)

func TestBuildCommentCoverageDisclosure(t *testing.T) {
	sum := coordinator.Summary{
		ChangeSummary:        "将鉴权校验移动到请求入口。",
		VerificationOverview: "全局理解已执行；8 个专业 Agent 中 8 个完成。",
		CoordinationOverview: "统筹 Agent 确认 1 个问题，驳回 2 个缺少 diff 证据的问题。",
		Issues: []agent.Issue{{
			Severity: "high", File: "a.go", Line: 2,
			Problem: "p", Suggestion: "s", AgentName: "架构师",
		}},
		AgentNames:  []string{"架构师"},
		Packages:    []string{"pkg-1"},
		TotalAgents: 8,
		TotalFiles:  372,
		Reviewed:    20,
	}
	body := BuildComment(sum, Options{MaxIssuesDisplay: 10, CommitSHA: "abc123"})
	for _, section := range []string{"### 本次代码变更总结", "### 验证总览", "### 统筹确认总览", "### 确认的问题"} {
		if !strings.Contains(body, section) {
			t.Errorf("评论缺少固定章节 %q:\n%s", section, body)
		}
	}
	if strings.Index(body, "### 本次代码变更总结") > strings.Index(body, "### 验证总览") ||
		strings.Index(body, "### 验证总览") > strings.Index(body, "### 统筹确认总览") ||
		strings.Index(body, "### 统筹确认总览") > strings.Index(body, "### 确认的问题") {
		t.Errorf("评论固定章节顺序错误:\n%s", body)
	}

	if !strings.Contains(body, "变更文件共 372 个（筛选后），本次仅审查前 20 个") {
		t.Error("缺少覆盖范围披露")
	}
	if !strings.Contains(body, "由 1 个 Agent 发现") {
		t.Error("问题来源表述应为\"由 N 个 Agent 发现\"")
	}
	if !strings.Contains(body, "（1/8，按变更文件路径智能分配）") {
		t.Errorf("缺少参与 Agent 比例说明:\n%s", body)
	}
}

func TestBuildCommentFullCoverageNoWarning(t *testing.T) {
	sum := coordinator.Summary{
		AgentNames:  []string{"架构师"},
		TotalAgents: 1,
		TotalFiles:  5,
		Reviewed:    5,
	}
	body := BuildComment(sum, Options{MaxIssuesDisplay: 10, CommitSHA: "abc123"})
	if strings.Contains(body, "仅审查前") {
		t.Error("全量审查不应出现覆盖警告")
	}
	if strings.Contains(body, "智能分配") {
		t.Error("全部 Agent 参与时不应出现分配说明")
	}
}

func TestCellTruncationClosesBacktick(t *testing.T) {
	// 构造在第 60 字符处恰好切断一对反引号的场景：
	// 前面填充 55 个字符后开引号，代码内容延伸超过 60，截断后右引号丢失。
	long := strings.Repeat("填", 55) + "`git rm 'x' 这是一段很长的代码内容需要被截断`后续文字"
	head := string([]rune(long)[:60])
	if strings.Count(head, "`")%2 != 1 {
		t.Fatalf("用例前提不成立：前 60 字符应含奇数个反引号，实际 %d", strings.Count(head, "`"))
	}
	got := cell(long)
	if strings.Count(got, "`")%2 != 0 {
		t.Errorf("截断后反引号未闭合: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("截断标记缺失: %q", got)
	}
}

func TestLineStrFileLevel(t *testing.T) {
	if got := lineStr(0); got != "-" {
		t.Errorf("文件级问题(line=0)应显示 -, got %q", got)
	}
	if got := lineStr(42); got != "42" {
		t.Errorf("lineStr(42) = %q", got)
	}
}
