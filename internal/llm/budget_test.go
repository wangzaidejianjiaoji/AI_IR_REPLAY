package llm

import (
	"context"
	"encoding/json"
	"gvs-pr-agent/internal/config"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestBudgetStopsHTTPRetryAndTokenOrCostOverspend(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limits  BudgetLimits
		failure bool
		want    int
	}{
		{"retry", BudgetLimits{MaxCalls: 1, MaxTokens: 100000}, true, 1},
		{"tokens", BudgetLimits{MaxCalls: 10, MaxTokens: 1}, false, 0},
		{"cost", BudgetLimits{MaxCalls: 10, MaxTokens: 100000, MaxCost: 0.01, Prices: map[string]config.ModelPrice{"m": {InputPerMillion: 1000, OutputPerMillion: 1000}}}, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.failure {
					http.Error(w, "retry", 503)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "[]"}}}})
			}))
			defer srv.Close()
			cfg := config.LLMConfig{BaseURL: srv.URL, MaxTokens: 100, Timeout: "1s"}
			cfg.Retry.MaxAttempts = 3
			cfg.Retry.BaseDelay = "1ms"
			cfg.Retry.MaxDelay = "1ms"
			ctx, cancel := WithBudget(context.Background(), tc.limits)
			defer cancel()
			_, _, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Chat(ctx, "m", []Message{{Content: "input"}})
			if err == nil || int(calls.Load()) != tc.want {
				t.Fatalf("budget requests=%d want=%d err=%v", calls.Load(), tc.want, err)
			}
		})
	}
}
