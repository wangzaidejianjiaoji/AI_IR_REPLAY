// Package llm 实现兼容 OpenAI Chat Completions 格式的 kimi (Moonshot) API 客户端，
// 支持模型分级选择与指数退避重试（与 GVS RPC 重连策略一致）。
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"gvs-pr-agent/internal/config"
)

// Client kimi API 客户端。
type Client struct {
	cfg       config.LLMConfig
	http      *http.Client
	logger    *slog.Logger
	// 备用模型相关
	fallbackClient *Client // 备用模型客户端
	isFallback     bool    // 标记当前是否是备用客户端（避免递归创建）
}

// Message 对话消息。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatRequest OpenAI 兼容请求体。
type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature"`
}

// chatResponse OpenAI 兼容响应体。
type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// Usage 单次调用的 Token 消耗。
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// New 创建 LLM 客户端。
func New(cfg config.LLMConfig, logger *slog.Logger) *Client {
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 4000
	}
	if cfg.Retry.MaxAttempts <= 0 {
		cfg.Retry.MaxAttempts = 3
	}

	client := &Client{
		cfg:    cfg,
		http:   &http.Client{Timeout: cfg.TimeoutDuration()},
		logger: logger,
		isFallback: false,
	}

	// 初始化备用模型客户端（非递归）
	if cfg.Fallback != nil && cfg.Fallback.Enabled && !client.isFallback {
		fallbackCfg := config.LLMConfig{
			Provider:        "fallback",
			APIKey:          cfg.Fallback.APIKey,
			BaseURL:         cfg.Fallback.BaseURL,
			Models:          cfg.Fallback.Models,
			MaxTokens:       cfg.MaxTokens,
			Temperature:     cfg.Temperature,
			Timeout:         cfg.Timeout,
			Retry:           cfg.Retry,
			ModelThresholds: cfg.ModelThresholds,
		}
		client.fallbackClient = &Client{
			cfg:        fallbackCfg,
			http:       &http.Client{Timeout: fallbackCfg.TimeoutDuration()},
			logger:     logger,
			isFallback: true, // 标记为备用客户端，避免递归创建
		}
		logger.Info("备用模型客户端已初始化", "provider", "minimax", "base_url", cfg.Fallback.BaseURL)
	}

	return client
}

// SelectModel 按变更行数选择模型（透传配置的模型分级策略）。
func (c *Client) SelectModel(changedLines int) string {
	return c.cfg.SelectModel(changedLines)
}

// Chat 调用 Chat Completions 接口，带指数退避重试。
// 当主模型遇到限额错误(429)时，自动切换到备用模型。
func (c *Client) Chat(ctx context.Context, model string, messages []Message) (string, Usage, error) {
	result, usage, err := c.chatWithFallback(ctx, model, messages, false)
	return result, usage, err
}

// chatWithFallback 内部方法，支持主备切换
func (c *Client) chatWithFallback(ctx context.Context, model string, messages []Message, isFallbackMode bool) (string, Usage, error) {
	var lastErr error
	for attempt := 1; attempt <= c.cfg.Retry.MaxAttempts; attempt++ {
		if err := reserveRequest(ctx, model, messages, c.cfg.MaxTokens); err != nil {
			return "", Usage{}, err
		}
		content, usage, retryable, err := c.chatOnce(ctx, model, messages)
		if err == nil {
			return content, usage, nil
		}
		lastErr = err

		// 检查是否为限额错误（429），且存在备用客户端且未处于备用模式
		if c.isRateLimitError(err) && c.fallbackClient != nil && !isFallbackMode && !c.isFallback {
			c.logger.Warn("主模型触发限额，切换到备用模型",
				"model", model, "error", err.Error())

			// 使用备用模型映射（将主模型映射到对应的备用模型）
			fallbackModel := c.getFallbackModel(model)

			// 调用备用客户端
			fallbackContent, fallbackUsage, fallbackErr := c.fallbackClient.chatWithFallback(ctx, fallbackModel, messages, true)
			if fallbackErr != nil {
				c.logger.Error("备用模型调用失败", "model", fallbackModel, "error", fallbackErr.Error())
				return "", Usage{}, fmt.Errorf("主模型和备用模型均失败: 主模型错误=%w, 备用模型错误=%v", lastErr, fallbackErr)
			}

			c.logger.Info("备用模型调用成功", "model", fallbackModel)
			return fallbackContent, fallbackUsage, nil
		}

		if !retryable || attempt == c.cfg.Retry.MaxAttempts {
			break
		}
		delay := c.backoff(attempt)
		c.logger.Warn("llm 请求失败，准备重试",
			"attempt", attempt, "delay", delay.String(), "error", err.Error())
		select {
		case <-ctx.Done():
			return "", Usage{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	return "", Usage{}, lastErr
}

// chatOnce 单次请求；返回 retryable 标记（5xx/429/网络错误可重试）。
func (c *Client) chatOnce(ctx context.Context, model string, messages []Message) (string, Usage, bool, error) {
	reqBody := chatRequest{
		Model:       model,
		Messages:    messages,
		MaxTokens:   c.cfg.MaxTokens,
		Temperature: c.cfg.Temperature,
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", Usage{}, false, fmt.Errorf("marshal request: %w", err)
	}

	url := c.cfg.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", Usage{}, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", Usage{}, true, fmt.Errorf("llm request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return "", Usage{}, true, fmt.Errorf("read llm response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return "", Usage{}, retryable,
			fmt.Errorf("llm status %d: %s", resp.StatusCode, truncate(string(body), 500))
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", Usage{}, false, fmt.Errorf("parse llm response: %w", err)
	}
	if parsed.Error != nil {
		return "", Usage{}, false, fmt.Errorf("llm error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", Usage{}, false, fmt.Errorf("llm 返回空 choices")
	}

	usage := Usage{
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		TotalTokens:      parsed.Usage.TotalTokens,
	}
	return parsed.Choices[0].Message.Content, usage, false, nil
}

// backoff 计算第 attempt 次重试的指数退避延迟。
func (c *Client) backoff(attempt int) time.Duration {
	base := c.cfg.RetryBaseDelay()
	max := c.cfg.RetryMaxDelay()
	d := time.Duration(float64(base) * math.Pow(2, float64(attempt-1)))
	if d > max {
		d = max
	}
	return d
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// isRateLimitError 检查是否为限额错误（429）
func (c *Client) isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	// 检查错误消息中是否包含 429 状态码
	errStr := err.Error()
	return strings.Contains(errStr, "429") ||
		strings.Contains(errStr, "rate limit") ||
		strings.Contains(errStr, "quota")
}

// getFallbackModel 将主模型映射到备用模型
// 如果备用配置中没有对应模型，则使用备用模型的 small 配置作为默认
func (c *Client) getFallbackModel(primaryModel string) string {
	if c.cfg.Fallback == nil || c.cfg.Fallback.Models == nil {
		return primaryModel
	}

	// 尝试映射：如果主模型是 small/medium/large，则使用备用模型的对应档位
	for tier, model := range c.cfg.Models {
		if model == primaryModel {
			if fallbackModel, ok := c.cfg.Fallback.Models[tier]; ok {
				return fallbackModel
			}
		}
	}

	// 如果没有找到映射，默认使用备用模型的第一个配置
	for _, model := range c.cfg.Fallback.Models {
		return model
	}

	// 如果备用模型配置为空，返回原始模型
	return primaryModel
}
