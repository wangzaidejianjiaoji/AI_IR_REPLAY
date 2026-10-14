package agent

import (
	"log/slog"

	"gvs-pr-agent/internal/llm"
)

// NewStandard 代码规范专家 Agent：命名、风格、错误处理。
func NewStandard(client *llm.Client, priority int, logger *slog.Logger) Agent {
	return &baseAgent{
		role:     "standard",
		name:     "代码规范专家",
		priority: priority,
		client:   client,
		logger:   logger,
		focus: `你负责审查代码规范问题：
- 命名规范（包名、函数名、变量名符合 Go 惯例）
- 代码风格（gofmt、goimports）
- 注释和文档是否与代码一致
- 错误处理（if err != nil 模式，禁止裸 _ 忽略错误）
- 不必要的抽象（过度使用 interface）`,
	}
}
