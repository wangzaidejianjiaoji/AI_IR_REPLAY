package analyzer

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"gvs-pr-agent/internal/config"
	"gvs-pr-agent/internal/llm"
)

func TestAnalyzeBuildsChangeContextFromGlobalUnderstanding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"{\"summary\":\"将请求校验前移\",\"intent\":\"拒绝无效请求\",\"affected_modules\":[\"api\"],\"behavior_constraints\":[\"无效请求不得进入服务层\"],\"comment_logic_mismatches\":[\"注释仍称服务层负责校验\"]}"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	}))
	defer server.Close()

	cfg := config.LLMConfig{BaseURL: server.URL, MaxTokens: 100, Temperature: 1}
	cfg.Models = map[string]string{"small": "test-model"}
	cfg.Retry.MaxAttempts = 1
	a := New(llm.New(cfg, slog.New(slog.NewTextHandler(testWriter{t}, nil))))

	got, usage, err := a.Analyze(context.Background(), Input{
		Repository:          "lzvsg/gvs",
		Title:               "前移参数校验",
		Diff:                "diff --git a/api.go b/api.go\n+if err := validate(req); err != nil { return err }",
		GlobalUnderstanding: "# GVS 代码理解\n服务层原先承担参数校验。",
		Model:               "test-model",
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if got.Summary != "将请求校验前移" || got.Intent != "拒绝无效请求" {
		t.Fatalf("unexpected context: %#v", got)
	}
	if len(got.AffectedModules) != 1 || got.AffectedModules[0] != "api" {
		t.Fatalf("affected modules = %#v", got.AffectedModules)
	}
	if usage.TotalTokens != 15 {
		t.Fatalf("usage = %#v", usage)
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) { return len(p), nil }
