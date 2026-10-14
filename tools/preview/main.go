package main

import (
	"fmt"
	"gvs-pr-agent/internal/agent"
	"gvs-pr-agent/internal/coordinator"
	"gvs-pr-agent/internal/notifier"
)

func main() {
	sum := coordinator.Summary{
		Issues: []agent.Issue{
			{Severity: "critical", File: "; git diff --stat 2026-05-29 HEAD", Line: 0,
				Problem: "误提交生成的 diff 统计文件", AgentName: "架构师",
				Detail:     "PR 中新增了一个名为 ; git diff --stat 2026-05-29 HEAD 的文件，属于明显误提交的 shell 命令输出产物。",
				Suggestion: "立即删除该文件：`git rm '; git diff --stat 2026-05-29 HEAD'`，并在 `.gitignore` 中增加规则避免类似产物被提交"},
			{Severity: "medium", File: "appimage_qatool.sh", Line: 13,
				Problem:    "构建脚本隐式探测 OpenBLAS 路径", AgentName: "架构师",
				Detail:     "find 可能匹配到错误版本或返回空值，导致 OPENBLAS_PATH 未设置也不报错。",
				Suggestion: "改为显式声明，未找到时明确报错退出"},
		},
		AgentNames:  []string{"架构师", "安全专家", "代码规范专家"},
		Packages:    []string{"根目录-1", "根目录-2", "appimage-build", "deb-pkg-1", "deb-pkg-2"},
		FailedRoles: []string{"security"},
		TotalAgents: 8, TotalFiles: 372, Reviewed: 20,
	}
	sum.TotalUsage.TotalTokens = 75778
	fmt.Println(notifier.BuildComment(sum, notifier.Options{
		MentionAuthor: true, Author: "张腾予", MaxIssuesDisplay: 10, CommitSHA: "567c9ebd31921ea4"}))
}
