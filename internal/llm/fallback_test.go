package llm

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"gvs-pr-agent/internal/config"
)

// TestFallbackOnRateLimit 测试当主模型遇到 429 限额时切换到备用模型
func TestFallbackOnRateLimit(t *testing.T) {
	var primaryCalls, fallbackCalls atomic.Int32

	// 创建主模型服务器（总是返回 429）
	primarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limit exceeded","type":"rate_limit"}}`))
	}))
	defer primarySrv.Close()

	// 创建备用模型服务器（总是成功）
	fallbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"success from fallback"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer fallbackSrv.Close()

	// 创建配置
	cfg := config.LLMConfig{
		Provider: "kimi",
		APIKey:   "test-key",
		BaseURL:  primarySrv.URL,
		Models:   map[string]string{"small": "primary-model"},
		Timeout:  "5s",
		Retry:    struct {
			MaxAttempts int    `yaml:"max_attempts"`
			BaseDelay   string `yaml:"base_delay"`
			MaxDelay    string `yaml:"max_delay"`
		}{MaxAttempts: 1, BaseDelay: "1s", MaxDelay: "1s"},
		Fallback: &config.FallbackConfig{
			Enabled: true,
			APIKey:  "fallback-key",
			BaseURL: fallbackSrv.URL,
			Models:  map[string]string{"small": "fallback-model"},
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := New(cfg, logger)

	// 执行调用
	content, usage, err := client.Chat(context.Background(), "primary-model", []Message{
		{Role: "user", Content: "test"},
	})

	// 验证结果
	if err != nil {
		t.Fatalf("期望成功调用备用模型，但出错: %v", err)
	}
	if content != "success from fallback" {
		t.Fatalf("期望备用模型返回内容，实际: %s", content)
	}
	if usage.TotalTokens != 15 {
		t.Fatalf("期望备用模型返回 usage，实际: %+v", usage)
	}

	// 验证调用次数
	if primaryCalls.Load() == 0 {
		t.Fatal("主模型应该被调用")
	}
	if fallbackCalls.Load() == 0 {
		t.Fatal("备用模型应该被调用")
	}

	t.Logf("主模型调用: %d, 备用模型调用: %d", primaryCalls.Load(), fallbackCalls.Load())
}

// TestFallbackNotUsedOnSuccess 测试主模型成功时不使用备用模型
func TestFallbackNotUsedOnSuccess(t *testing.T) {
	var primaryCalls, fallbackCalls atomic.Int32

	// 创建主模型服务器（总是成功）
	primarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"success from primary"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer primarySrv.Close()

	// 创建备用模型服务器
	fallbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"should not be used"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer fallbackSrv.Close()

	// 创建配置
	cfg := config.LLMConfig{
		Provider: "kimi",
		APIKey:   "test-key",
		BaseURL:  primarySrv.URL,
		Models:   map[string]string{"small": "primary-model"},
		Timeout:  "5s",
		Retry:    struct {
			MaxAttempts int    `yaml:"max_attempts"`
			BaseDelay   string `yaml:"base_delay"`
			MaxDelay    string `yaml:"max_delay"`
		}{MaxAttempts: 1, BaseDelay: "1s", MaxDelay: "1s"},
		Fallback: &config.FallbackConfig{
			Enabled: true,
			APIKey:  "fallback-key",
			BaseURL: fallbackSrv.URL,
			Models:  map[string]string{"small": "fallback-model"},
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := New(cfg, logger)

	// 执行调用
	content, usage, err := client.Chat(context.Background(), "primary-model", []Message{
		{Role: "user", Content: "test"},
	})

	// 验证结果
	if err != nil {
		t.Fatalf("期望成功调用主模型，但出错: %v", err)
	}
	if content != "success from primary" {
		t.Fatalf("期望主模型返回内容，实际: %s", content)
	}
	if usage.TotalTokens != 15 {
		t.Fatalf("期望主模型返回 usage，实际: %+v", usage)
	}

	// 验证只有主模型被调用
	if primaryCalls.Load() == 0 {
		t.Fatal("主模型应该被调用")
	}
	if fallbackCalls.Load() != 0 {
		t.Fatal("备用模型不应该被调用")
	}

	t.Logf("主模型调用: %d, 备用模型调用: %d", primaryCalls.Load(), fallbackCalls.Load())
}