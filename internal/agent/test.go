package agent

import (
	"log/slog"

	"gvs-pr-agent/internal/llm"
)

// NewTest 测试专家 Agent：覆盖率、边界条件、并发测试。
func NewTest(client *llm.Client, priority int, logger *slog.Logger) Agent {
	return &baseAgent{
		role:     "test",
		name:     "测试专家",
		priority: priority,
		client:   client,
		logger:   logger,
		focus: `你负责审查测试问题：
- 新增/变更的导出函数是否有对应测试
- 边界条件测试（空输入、超大输入、并发）
- 并发测试是否覆盖 race condition
- Mock 使用是否合理，集成测试是否与单元测试解耦`,
	}
}
