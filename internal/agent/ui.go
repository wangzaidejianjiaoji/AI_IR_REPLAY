package agent

import (
	"log/slog"

	"gvs-pr-agent/internal/llm"
)

// NewUI UI 设计专家 Agent：API 响应格式、交互体验、可视化。
func NewUI(client *llm.Client, priority int, logger *slog.Logger) Agent {
	return &baseAgent{
		role:     "ui",
		name:     "UI 设计专家",
		priority: priority,
		client:   client,
		logger:   logger,
		focus: `你负责审查界面与交互设计问题：
- API 响应格式是否统一（统一包装结构），错误码设计是否规范
- 错误提示是否友好，日志输出是否便于排查
- 数据可视化方式是否清晰（如热图、轨迹图）
- 配置文件结构是否清晰，状态定义是否语义清晰`,
	}
}
