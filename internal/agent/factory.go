package agent

import (
	"log/slog"

	"gvs-pr-agent/internal/config"
	"gvs-pr-agent/internal/llm"
)

// BuildEnabled 根据配置构建所有启用的 Agent（11 角色）。
func BuildEnabled(agents map[string]config.Agent, client *llm.Client, logger *slog.Logger) []Agent {
	type factory func(*llm.Client, int, *slog.Logger) Agent
	factories := map[string]factory{
		"architect":   NewArchitect,
		"security":    NewSecurity,
		"performance": NewPerformance,
		"standard":    NewStandard,
		"test":        NewTest,
		"ai":          NewAI,
		"ui":          NewUI,
		"ai_tool":     NewAITool,
		"concurrency": NewConcurrency,
		"resource":    NewResource,
		"dataflow":    NewDataflow,
	}
	// 固定顺序，保证评论报告的章节顺序稳定
	order := []string{"security", "performance", "architect", "ui", "ai_tool", "standard", "test", "ai", "concurrency", "resource", "dataflow"}

	var out []Agent
	for _, role := range order {
		cfg, ok := agents[role]
		if !ok || !cfg.Enabled {
			continue
		}
		priority := cfg.Priority
		if priority <= 0 {
			priority = 2
		}
		out = append(out, factories[role](client, priority, logger))
	}
	return out
}
