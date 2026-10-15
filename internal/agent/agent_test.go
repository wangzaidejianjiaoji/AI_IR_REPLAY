// agent 解析重试回归测试（源自真实演练）：推理模型输出被 max_tokens
// 截断导致 JSON 解析失败时，应自动重试一次而非直接标记失败。
package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"gvs-pr-agent/internal/config"
	"gvs-pr-agent/internal/llm"
)

// newLLM 指向 httptest 的 LLM 客户端。
func newLLM(t *testing.T, handler http.HandlerFunc) *llm.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := config.LLMConfig{APIKey: "k", BaseURL: srv.URL, Timeout: "5s"}
	cfg.Retry.MaxAttempts = 1
	return llm.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func chatResp(content string) map[string]any {
	return map[string]any{
		"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}},
		"usage":   map[string]int{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150},
	}
}

func TestReviewRetriesOnParseFailure(t *testing.T) {
	var calls int32
	client := newLLM(t, func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			// 第一次：模拟被 max_tokens 截断的残缺 JSON
			_ = json.NewEncoder(w).Encode(chatResp(`[{"severity":"high","file":"a.go","li`))
			return
		}
		_ = json.NewEncoder(w).Encode(chatResp(`[{"severity":"high","file":"a.go","line":2,"problem":"p","suggestion":"s"}]`))
	})

	a := NewSecurity(client, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	res := a.Review(context.Background(), Task{Model: "m", Diff: "diff"})

	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("LLM 调用次数 = %d, want 2（截断后重试一次）", calls)
	}
	if res.Err != nil {
		t.Fatalf("重试成功后不应报错: %v", res.Err)
	}
	if len(res.Issues) != 1 || res.Issues[0].File != "a.go" {
		t.Fatalf("issues = %+v", res.Issues)
	}
	if res.Usage.TotalTokens != 300 { // 两次调用的 token 均计入
		t.Errorf("累计 token = %d, want 300", res.Usage.TotalTokens)
	}
}

func TestReviewCheckpointDoesNotReplayRejectedOutput(t *testing.T) {
	var calls atomic.Int32
	client := newLLM(t, func(w http.ResponseWriter, _ *http.Request) {
		content := "[]"
		if calls.Add(1) == 1 {
			content = "[truncated"
		}
		_ = json.NewEncoder(w).Encode(chatResp(content))
	})
	a := NewSecurity(client, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := llm.WithCheckpoint(context.Background(), t.TempDir(), "gvs/source/target")
	res := a.Review(ctx, Task{Model: "m", Diff: "diff"})
	if res.Err != nil || calls.Load() != 2 {
		t.Fatalf("parse retry replayed invalid checkpoint: calls=%d err=%v", calls.Load(), res.Err)
	}
	res = a.Review(ctx, Task{Model: "m", Diff: "diff"})
	if res.Err != nil || calls.Load() != 2 || res.Usage.TotalTokens != 0 {
		t.Fatalf("successful review not reused: calls=%d usage=%+v err=%v", calls.Load(), res.Usage, res.Err)
	}
}

func TestReviewFailsAfterRetryExhausted(t *testing.T) {
	var calls int32
	client := newLLM(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_ = json.NewEncoder(w).Encode(chatResp(`[{"broken`)) // 始终截断
	})

	a := NewSecurity(client, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	res := a.Review(context.Background(), Task{Model: "m", Diff: "diff"})

	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("LLM 调用次数 = %d, want 2", calls)
	}
	if res.Err == nil {
		t.Fatal("两次均截断应保留解析错误")
	}
}
