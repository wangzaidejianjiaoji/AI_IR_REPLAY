package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"gvs-pr-agent/internal/config"
)

func TestQuotaCooldownSkipsPrimaryAcrossRestartAndUsesFallbackBudget(t *testing.T) {
	var primaryCalls, fallbackCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("{\"error\":{\"message\":\"You've reached your 5-hour usage limit.\"}}"))
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fallbackCalls.Add(1); checkpointReply(w, "[]") }))
	defer fallback.Close()
	cfg := checkpointClientConfig(primary.URL)
	cfg.Models = map[string]string{"small": "primary"}
	cfg.Fallback = &config.FallbackConfig{Enabled: true, APIKey: "fallback-key", BaseURL: fallback.URL, Models: map[string]string{"small": "fallback"}}
	ctx := WithCheckpoint(context.Background(), t.TempDir(), "source/target")
	if _, _, err := New(cfg, checkpointLogger()).Chat(ctx, "primary", []Message{{Role: "user", Content: "package-one"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := WithBudget(ctx, BudgetLimits{MaxCalls: 1, MaxTokens: 2000})
	defer cancel()
	if _, _, err := New(cfg, checkpointLogger()).Chat(ctx, "primary", []Message{{Role: "user", Content: "package-two"}}); err != nil {
		t.Fatalf("blocked primary spent budget instead of using fallback: %v", err)
	}
	if primaryCalls.Load() != 1 || fallbackCalls.Load() != 2 {
		t.Fatalf("quota request repeated: primary=%d fallback=%d", primaryCalls.Load(), fallbackCalls.Load())
	}
}

func TestWeeklyQuotaIsTypedAndDoesNotRetryUntilRecheck(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("{\"error\":{\"message\":\"You've reached your weekly (7-day) usage limit.\"}}"))
	}))
	defer srv.Close()
	cfg := checkpointClientConfig(srv.URL)
	cfg.Retry.MaxAttempts = 3
	ctx := WithCheckpoint(context.Background(), t.TempDir(), "fixed-version")
	for _, text := range []string{"stage-one", "stage-two"} {
		_, _, err := New(cfg, checkpointLogger()).Chat(ctx, "model", []Message{{Role: "user", Content: text}})
		var quota *QuotaError
		if !errors.As(err, &quota) || quota.Period != "weekly" {
			t.Fatalf("weekly quota not preserved: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("weekly quota retried %d times", calls.Load())
	}
}

func TestCorrectedModelCredentialDoesNotInheritQuotaCooldown(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") == "Bearer corrected-key" {
			checkpointReply(w, "[]")
			return
		}
		w.WriteHeader(403)
		_, _ = w.Write([]byte("{\"error\":{\"message\":\"5-hour usage limit reached\"}}"))
	}))
	defer srv.Close()
	cfg := checkpointClientConfig(srv.URL)
	ctx := WithCheckpoint(context.Background(), t.TempDir(), "fixed-version")
	msgs := []Message{{Role: "user", Content: "diff"}}
	if _, _, err := New(cfg, checkpointLogger()).Chat(ctx, "model", msgs); err == nil {
		t.Fatal("expected quota")
	}
	cfg.APIKey = "corrected-key"
	if _, _, err := New(cfg, checkpointLogger()).Chat(ctx, "model", msgs); err != nil {
		t.Fatalf("new credential incorrectly blocked: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestLateShortQuotaCannotReplaceLongCooldownInMemoryOrOnDisk(t *testing.T) {
	cfg := checkpointClientConfig("https://provider.invalid")
	ctx := WithCheckpoint(context.Background(), t.TempDir(), "fixed-version")
	c := New(cfg, checkpointLogger())
	now := time.Now()
	long := &QuotaError{Status: 403, Period: "weekly", RetryAt: now.Add(5 * time.Hour), Message: "weekly quota"}
	short := &QuotaError{Status: 429, Period: "rate_limit", RetryAt: now.Add(time.Minute), Message: "minute limit"}
	// Requests can already be in flight when the first quota response arrives.
	// This is the order of their responses, not the order they were sent.
	c.rememberQuota(ctx, long)
	c.rememberQuota(ctx, short)
	for _, client := range []*Client{c, New(cfg, checkpointLogger())} {
		blocked := client.blockedQuota(ctx)
		if blocked == nil || !blocked.RetryAt.Equal(long.RetryAt) || blocked.Period != "weekly" {
			t.Fatalf("long quota overwritten: %+v", blocked)
		}
	}
}
