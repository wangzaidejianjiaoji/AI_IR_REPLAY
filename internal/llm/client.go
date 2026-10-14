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
	"time"

	"gvs-pr-agent/internal/config"
)

// Client kimi API 客户端。
type Client struct {
	cfg    config.LLMConfig
	http   *http.Client
	logger *slog.Logger
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
	return &Client{
		cfg:    cfg,
		http:   &http.Client{Timeout: cfg.TimeoutDuration()},
		logger: logger,
	}
}

// SelectModel 按变更行数选择模型（透传配置的模型分级策略）。
func (c *Client) SelectModel(changedLines int) string {
	return c.cfg.SelectModel(changedLines)
}

// Chat 调用 Chat Completions 接口，带指数退避重试。
func (c *Client) Chat(ctx context.Context, model string, messages []Message) (string, Usage, error) {
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
