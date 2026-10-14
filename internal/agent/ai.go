package agent

import (
	"log/slog"

	"gvs-pr-agent/internal/llm"
)

// NewAI AI/算法专家 Agent：模型使用、图像处理、推理优化。
func NewAI(client *llm.Client, priority int, logger *slog.Logger) Agent {
	return &baseAgent{
		role:     "ai",
		name:     "AI 专家",
		priority: priority,
		client:   client,
		logger:   logger,
		focus: `你负责审查 AI/算法问题：
- 深度学习模型使用是否合理（加载/释放配对）
- 图像处理算法选择与数据预处理/后处理正确性
- 模型推理结果是否有置信度过滤
- 模型推理性能优化（批量推理、异步处理）`,
	}
}
