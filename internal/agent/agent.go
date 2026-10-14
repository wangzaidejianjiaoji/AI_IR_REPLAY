// Package agent 定义审查 Agent 接口与 11 个角色的实现。
// 每个 Agent 聚焦一个专业领域，通过场景化提示词调用 LLM 完成审查。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"gvs-pr-agent/internal/llm"
	"gvs-pr-agent/internal/prompt"
)

// Issue 一条审查问题（LLM 按此 JSON 结构输出）。
type Issue struct {
	Severity   string `json:"severity"`   // critical/high/medium/low
	File       string `json:"file"`       // 文件路径
	Line       int    `json:"line"`       // 行号（新文件）
	Problem    string `json:"problem"`    // 问题简述
	Detail     string `json:"detail"`     // 详细说明
	Suggestion string `json:"suggestion"` // 修复建议
	Code       string `json:"code"`       // 参考代码（可选）
	AgentRole  string `json:"-"`          // 来源 Agent（协调器填充）
	AgentName  string `json:"-"`          // 来源 Agent 显示名
}

// Result 单个 Agent 的审查结果。
type Result struct {
	Role   string
	Name   string
	Issues []Issue
	Usage  llm.Usage
	Err    error
}

// Task 审查任务输入。
type Task struct {
	RepoFullName  string   // owner/repo
	PRTitle       string   // PR 标题
	PRBody        string   // PR 描述
	Package       string   // 审查包名（智能文件打包后的审查单元）
	Diff          string   // 该审查包截断后的 diff 文本
	Rules         []string // 该 Agent 适用的规则集合
	ChangeContext string   // 每轮全局理解 Agent 生成的变更意图与行为约束
	Model         string   // 分级选择后的模型
}

// Agent 审查 Agent 接口。
type Agent interface {
	Role() string        // 角色标识（与配置 agents.<role> 对应）
	DisplayName() string // 中文显示名（用于评论报告）
	Priority() int       // 优先级（1 最高，协调器按优先级分批）
	Review(ctx context.Context, task Task) Result
}

// baseAgent 通用 LLM 审查实现：所有角色共享同一审查流程，仅提示词不同。
type baseAgent struct {
	role     string
	name     string
	focus    string // 角色审查重点描述（注入 system prompt）
	priority int
	client   *llm.Client
	logger   *slog.Logger
}

func (a *baseAgent) Role() string        { return a.role }
func (a *baseAgent) DisplayName() string { return a.name }
func (a *baseAgent) Priority() int       { return a.priority }

// Review 构建提示词、调用 LLM 并解析问题列表。
// 解析失败（如推理模型思考耗尽 max_tokens 导致输出截断）时自动重试一次。
func (a *baseAgent) Review(ctx context.Context, task Task) Result {
	res := Result{Role: a.role, Name: a.name}

	sys := prompt.System(a.name, a.focus, task.Rules)
	usr := prompt.User(task.RepoFullName, task.PRTitle, task.PRBody, task.Package, task.Diff, task.ChangeContext)

	const maxParseAttempts = 2
	for attempt := 1; attempt <= maxParseAttempts; attempt++ {
		content, usage, err := a.client.Chat(ctx, task.Model, []llm.Message{
			{Role: "system", Content: sys},
			{Role: "user", Content: usr},
		})
		res.Usage.PromptTokens += usage.PromptTokens
		res.Usage.CompletionTokens += usage.CompletionTokens
		res.Usage.TotalTokens += usage.TotalTokens
		if err != nil {
			res.Err = fmt.Errorf("agent %s: %w", a.role, err)
			return res
		}

		issues, perr := parseIssues(content)
		if perr != nil {
			a.logger.Warn("解析 agent 输出失败", "agent", a.role, "attempt", attempt,
				"error", perr.Error(), "raw", truncate(content, 300))
			res.Err = fmt.Errorf("agent %s parse: %w", a.role, perr)
			continue // 输出截断/格式异常：重试一次（temperature 采样具有随机性）
		}
		for i := range issues {
			issues[i].AgentRole = a.role
			issues[i].AgentName = a.name
			issues[i].Severity = normalizeSeverity(issues[i].Severity)
		}
		res.Issues = issues
		res.Err = nil
		return res
	}
	return res
}

var jsonBlockRe = regexp.MustCompile("(?s)```(?:json)?\\s*(\\[.*?\\])\\s*```")

// parseIssues 从 LLM 输出中提取 JSON 问题数组（容忍 markdown 代码块包裹）。
func parseIssues(content string) ([]Issue, error) {
	text := strings.TrimSpace(content)

	// 优先提取 ```json ... ``` 代码块
	if m := jsonBlockRe.FindStringSubmatch(text); m != nil {
		text = m[1]
	} else if i := strings.Index(text, "["); i >= 0 {
		// 退化处理：截取首个 [ 到最后一个 ] 之间的内容
		if j := strings.LastIndex(text, "]"); j > i {
			text = text[i : j+1]
		}
	}

	var issues []Issue
	if err := json.Unmarshal([]byte(text), &issues); err != nil {
		return nil, err
	}
	return issues, nil
}

// normalizeSeverity 归一化严重度取值。
func normalizeSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical":
		return "critical"
	case "high":
		return "high"
	case "low":
		return "low"
	default:
		return "medium"
	}
}

// SeverityRank 严重度排序权重（critical 最高）。
func SeverityRank(s string) int {
	switch s {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	default:
		return 1
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
