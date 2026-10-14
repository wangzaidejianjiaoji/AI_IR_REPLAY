package agent

import (
	"log/slog"

	"gvs-pr-agent/internal/llm"
)

// NewAITool AI 工具使用专家 Agent：Prompt 工程、模型调用方式、AI 集成。
func NewAITool(client *llm.Client, priority int, logger *slog.Logger) Agent {
	return &baseAgent{
		role:     "ai_tool",
		name:     "AI 工具使用专家",
		priority: priority,
		client:   client,
		logger:   logger,
		focus: `你负责审查 AI 工具使用问题：
- AI 相关代码的 Prompt 工程是否合理
- 模型调用方式是否最优（批量推理 vs 单条推理）
- AI 推理是否异步处理，避免阻塞主流程
- 模型版本管理和热更新机制
- 在合适场景给出使用 AI 能力的建议`,
	}
}
