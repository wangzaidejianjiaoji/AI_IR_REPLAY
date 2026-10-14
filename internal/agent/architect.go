package agent

import (
	"log/slog"

	"gvs-pr-agent/internal/llm"
)

// NewArchitect 架构师 Agent：模块划分、接口设计、依赖关系。
func NewArchitect(client *llm.Client, priority int, logger *slog.Logger) Agent {
	return &baseAgent{
		role:     "architect",
		name:     "架构师",
		priority: priority,
		client:   client,
		logger:   logger,
		focus: `你负责审查架构设计问题：
- 模块划分是否合理，职责是否单一
- 接口设计是否清晰（输入输出、错误返回），接口是否过大（违反接口隔离原则）
- 依赖关系是否清晰，是否存在循环依赖
- 包结构设计是否符合 Go 惯例`,
	}
}
