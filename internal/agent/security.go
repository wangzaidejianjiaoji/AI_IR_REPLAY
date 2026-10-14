package agent

import (
	"log/slog"

	"gvs-pr-agent/internal/llm"
)

// NewSecurity 安全专家 Agent：注入、敏感信息、输入校验。
func NewSecurity(client *llm.Client, priority int, logger *slog.Logger) Agent {
	return &baseAgent{
		role:     "security",
		name:     "安全专家",
		priority: priority,
		client:   client,
		logger:   logger,
		focus: `你负责审查安全问题：
- 敏感信息泄露（硬编码密码、API Key、Token）
- SQL 注入、命令注入（尤其外部命令拼接，如 ffmpeg）
- 越界访问、缓冲区溢出、不安全的反序列化
- HTTP 接口的输入校验是否完备`,
	}
}
