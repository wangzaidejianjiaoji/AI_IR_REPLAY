package llm

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"gvs-pr-agent/internal/config"
)

func guardTestConfig(baseURL string, attempts int) config.LLMConfig {
	return config.LLMConfig{
		Provider:  "kimi",
		APIKey:    "test-key",
		BaseURL:   baseURL,
		Models:    map[string]string{"small": "primary-model"},
		MaxTokens: 8000,
		Timeout:   "5s",
		Retry: struct {
			MaxAttempts int    `yaml:"max_attempts"`
			BaseDelay   string `yaml:"base_delay"`
			MaxDelay    string `yaml:"max_delay"`
		}{MaxAttempts: attempts, BaseDelay: "1ms", MaxDelay: "5ms"},
	}
}

// TestTruncatedOutputFailsWithExplicitError 验证 finish_reason=length 的截断响应
// 不再被当作成功内容返回，而是给出可定位的错误。
func TestTruncatedOutputFailsWithExplicitError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"[{\"issues\":"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":8000,"total_tokens":8010}}`))
	}))
	defer srv.Close()
	client := New(guardTestConfig(srv.URL, 2), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	_, _, err := client.Chat(context.Background(), "primary-model", []Message{{Role: "user", Content: "test"}})
	if err == nil {
		t.Fatal("truncated output presented as success")
	}
	if !strings.Contains(err.Error(), "max_tokens") || !strings.Contains(err.Error(), "length") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestEmptyContentFailsWithExplicitError 验证空内容响应按可重试错误处理并最终报错。
func TestEmptyContentFailsWithExplicitError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":0,"total_tokens":10}}`))
	}))
	defer srv.Close()
	client := New(guardTestConfig(srv.URL, 2), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	_, _, err := client.Chat(context.Background(), "primary-model", []Message{{Role: "user", Content: "test"}})
	if err == nil {
		t.Fatal("empty output presented as success")
	}
	if !strings.Contains(err.Error(), "空内容") {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 attempts, got %d", calls.Load())
	}
}

// TestConcurrentLimitIsRetried 验证供应商 403 并发上限按瞬态错误重试，恢复后成功。
func TestConcurrentLimitIsRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"message":"You've reached your concurrent request limit. Please wait for your ongoing requests to finish and try again.","type":"access_terminated_error"}}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"[]"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	}))
	defer srv.Close()
	client := New(guardTestConfig(srv.URL, 3), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	content, _, err := client.Chat(context.Background(), "primary-model", []Message{{Role: "user", Content: "test"}})
	if err != nil {
		t.Fatalf("concurrent limit should recover via retry: %v", err)
	}
	if content != "[]" {
		t.Fatalf("unexpected content: %q", content)
	}
}
