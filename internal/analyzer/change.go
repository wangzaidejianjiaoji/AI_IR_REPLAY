// Package analyzer builds one shared, model-generated understanding of every
// reviewed diff before specialised review agents inspect it.
package analyzer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gvs-pr-agent/internal/llm"
)

// ChatClient is the small portion of the LLM client used by Analyzer.
type ChatClient interface {
	Chat(context.Context, string, []llm.Message) (string, llm.Usage, error)
}

// Analyzer derives change intent and behavioural constraints from the global
// understanding document and the current diff. It never emits review issues.
type Analyzer struct{ client ChatClient }

// New creates an Analyzer.
func New(client ChatClient) *Analyzer { return &Analyzer{client: client} }

// Input is the context for one mandatory pre-review analysis.
type Input struct {
	Repository          string
	Title               string
	Body                string
	Diff                string
	GlobalUnderstanding string
	Model               string
}

// ChangeContext is injected into all specialised reviewers and the final
// coordinating confirmer.
type ChangeContext struct {
	Summary                string   `json:"summary"`
	Intent                 string   `json:"intent"`
	AffectedModules        []string `json:"affected_modules"`
	BehaviorConstraints    []string `json:"behavior_constraints"`
	CommentLogicMismatches []string `json:"comment_logic_mismatches"`
}

// Analyze invokes the model once for the reviewed diff and parses its strict
// JSON response. 部分模型（如 glm-4-flash）会用 ```json 代码块包裹输出，这里统一
// 截取首个 { 到最后一个 } 之间的内容再解析（与统筹确认 Agent 的解析策略一致）。
func (a *Analyzer) Analyze(ctx context.Context, in Input) (ChangeContext, llm.Usage, error) {
	if a == nil || a.client == nil {
		return ChangeContext{}, llm.Usage{}, fmt.Errorf("全局理解分析器未配置 LLM 客户端")
	}
	system := `你是代码变更意图分析器。你不报告缺陷，也不输出审查意见；只根据全局代码理解和当前 diff 建立所有审查 Agent 共用的上下文。必须核对代码注释、文档或配置说明是否与修改后的实际控制流、数据流和错误处理一致。规则仅作辅助，不得代替对实际代码的判断。
只输出一个 JSON 对象，不要 markdown：
{"summary":"本次实际变更总结","intent":"变更意图","affected_modules":["模块"],"behavior_constraints":["必须保持的行为约束"],"comment_logic_mismatches":["发现的注释/逻辑不一致；没有则为空数组"]}`
	user := fmt.Sprintf("仓库：%s\n标题：%s\n", in.Repository, in.Title)
	if strings.TrimSpace(in.Body) != "" {
		user += "描述：" + in.Body + "\n"
	}
	user += "\n## 最新全局代码理解\n" + in.GlobalUnderstanding
	user += "\n\n## 当前 Diff\n```diff\n" + in.Diff + "\n```"
	content, usage, err := a.client.Chat(ctx, in.Model, []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}})
	if err != nil {
		return ChangeContext{}, usage, fmt.Errorf("调用全局理解模型: %w", err)
	}
	text := strings.TrimSpace(content)
	// 别除推理模型思考块（兼容 MiniMax-M3 等输出 <think>...</think> 后续接 JSON 的场景）。
	// 若 think 块未被 </think> 闭合（MiniMax 响应被 length 截断或输出异常），保留原始文本，
	// 让后面的 { ... } 提取与 JSON 解析有机会在 reply 块中匹配，避免变成空串误报 "unexpected end of JSON input"。
	if i := strings.Index(text, "<think>"); i >= 0 {
		if j := strings.Index(text[i:], "</think>"); j >= 0 {
			text = strings.TrimSpace(text[:i] + text[i+j+len("</think>"):])
		}
	}
	if i := strings.Index(text, "{"); i >= 0 {
		if j := strings.LastIndex(text, "}"); j > i {
			text = text[i : j+1]
		}
	}
	var out ChangeContext
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return ChangeContext{}, usage, fmt.Errorf("解析全局理解模型输出: %w (raw 头 500 字=%s)", err, truncateForLog(content, 500))
	}
	return out, usage, nil
}

// truncateForLog 限制日志附加内容的长度，避免刷屏。
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...[truncated]"
}
