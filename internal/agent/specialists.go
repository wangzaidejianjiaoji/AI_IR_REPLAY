package agent

import (
	"gvs-pr-agent/internal/llm"
	"log/slog"
)

func NewConcurrency(client *llm.Client, priority int, logger *slog.Logger) Agent {
	return &baseAgent{role: "concurrency", name: "并发专家", priority: priority, client: client, logger: logger,
		focus: "检查数据竞争、锁粒度和锁顺序、原子操作、goroutine/线程退出、channel 阻塞、取消传播与异步回调时序。按实际控制流验证可触发的竞态和死锁，给出本次 diff 的具体证据，不把并发代码本身当缺陷。"}
}
func NewResource(client *llm.Client, priority int, logger *slog.Logger) Agent {
	return &baseAgent{role: "resource", name: "资源管理专家", priority: priority, client: client, logger: logger,
		focus: "检查资源获取/释放配对、成功与失败及取消路径、文件和连接、Mat/CGO、CUDA/TensorRT、mmap、定时器、对象池和重载生命周期。区分客户端 Close 与实际服务端释放，只报告有代码证据的泄漏、重复释放或越界使用。"}
}
func NewDataflow(client *llm.Client, priority int, logger *slog.Logger) Agent {
	return &baseAgent{role: "dataflow", name: "数据流专家", priority: priority, client: client, logger: logger,
		focus: "检查输入输出契约、字段与单位、跨仓库协议、状态机、不变量、帧队列背压和丢帧、错误传播、消息顺序与预处理。沿实际数据流验证变更影响，区分兼容字段扩展与业务语义丢失，并提供 diff 证据。"}
}
