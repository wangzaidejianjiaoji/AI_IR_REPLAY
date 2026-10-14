package agent

import (
	"log/slog"

	"gvs-pr-agent/internal/llm"
)

// NewPerformance 性能专家 Agent：泄漏、复杂度、锁竞争。
func NewPerformance(client *llm.Client, priority int, logger *slog.Logger) Agent {
	return &baseAgent{
		role:     "performance",
		name:     "性能专家",
		priority: priority,
		client:   client,
		logger:   logger,
		focus: `你负责审查性能问题：
- 内存泄漏（goroutine 泄漏、未关闭资源、cgo 对象未释放如 gocv Mat）
- 算法复杂度（O(n^2) 以上需关注）
- 频繁的内存分配（GC 压力）、大对象拷贝
- 锁竞争和死锁风险`,
	}
}
