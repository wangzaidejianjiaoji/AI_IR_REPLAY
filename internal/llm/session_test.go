package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gvs-pr-agent/internal/config"
)

func TestTransientFailureRetriesThenFallsBack(t *testing.T) {
	var primaryCalls, fallbackCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { primaryCalls.Add(1); http.Error(w, "unavailable", 503) }))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fallbackCalls.Add(1); checkpointReply(w, "fallback") }))
	defer fallback.Close()
	cfg := checkpointClientConfig(primary.URL)
	cfg.Retry.MaxAttempts = 2
	cfg.Retry.BaseDelay, cfg.Retry.MaxDelay = "1ms", "1ms"
	cfg.Fallback = &config.FallbackConfig{Enabled: true, BaseURL: fallback.URL, Models: map[string]string{"small": "mini"}}
	got, _, err := New(cfg, checkpointLogger()).Chat(context.Background(), "kimi", []Message{{Content: "diff"}})
	if err != nil || got != "fallback" || primaryCalls.Load() != 2 || fallbackCalls.Load() != 1 {
		t.Fatalf("got=%q primary=%d fallback=%d err=%v", got, primaryCalls.Load(), fallbackCalls.Load(), err)
	}
}

func TestCheckpointWriteFailureFailsRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { checkpointReply(w, "[]") }))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := New(checkpointClientConfig(srv.URL), checkpointLogger()).Chat(WithCheckpoint(context.Background(), path, "snapshot"), "kimi", []Message{{Content: "diff"}})
	if err == nil {
		t.Fatal("successful response reported despite checkpoint write failure")
	}
}

func TestCancelledPrimaryDoesNotDispatchFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var fallbackCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { cancel(); http.Error(w, "unavailable", 503) }))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fallbackCalls.Add(1); checkpointReply(w, "[]") }))
	defer fallback.Close()
	cfg := checkpointClientConfig(primary.URL)
	cfg.Fallback = &config.FallbackConfig{Enabled: true, BaseURL: fallback.URL}
	_, _, err := New(cfg, checkpointLogger()).Chat(ctx, "kimi", nil)
	if !errors.Is(err, context.Canceled) || fallbackCalls.Load() != 0 {
		t.Fatalf("err=%v fallback=%d", err, fallbackCalls.Load())
	}
}

func TestReviewSessionStaysDowngradedAndNextPRRestartsPrimary(t *testing.T) {
	var primaryCalls, fallbackCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if primaryCalls.Add(1) == 1 {
			http.Error(w, "unavailable", 503)
			return
		}
		checkpointReply(w, "primary")
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fallbackCalls.Add(1); checkpointReply(w, "fallback") }))
	defer fallback.Close()
	cfg := checkpointClientConfig(primary.URL)
	cfg.Fallback = &config.FallbackConfig{Enabled: true, BaseURL: fallback.URL, Models: map[string]string{"small": "mini"}}
	client := New(cfg, checkpointLogger())
	ctx := WithReviewSession(context.Background())
	for _, prompt := range []string{"first", "second"} {
		got, _, err := client.Chat(ctx, "kimi", []Message{{Content: prompt}})
		if err != nil || got != "fallback" {
			t.Fatalf("got=%q err=%v", got, err)
		}
	}
	got, _, err := client.Chat(WithReviewSession(ctx), "kimi", []Message{{Content: "new PR"}})
	if err != nil || got != "primary" || primaryCalls.Load() != 2 || fallbackCalls.Load() != 2 {
		t.Fatalf("got=%q primary=%d fallback=%d err=%v", got, primaryCalls.Load(), fallbackCalls.Load(), err)
	}
}

func TestAvailabilityUsesPersistedQuotaWithoutProbe(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "600")
		http.Error(w, "quota", 429)
	}))
	defer srv.Close()
	cfg := checkpointClientConfig(srv.URL)
	cfg.Fallback = &config.FallbackConfig{Enabled: true, BaseURL: srv.URL, Models: map[string]string{"small": "mini"}}
	ctx := WithCheckpoint(context.Background(), t.TempDir(), "PR")
	client := New(cfg, checkpointLogger())
	if err := client.Availability(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, err := client.Chat(WithReviewSession(ctx), "kimi", nil)
	var quota *QuotaError
	if !errors.As(err, &quota) {
		t.Fatalf("expected typed quota: %v", err)
	}
	if err := New(cfg, checkpointLogger()).Availability(ctx); !errors.As(err, &quota) {
		t.Fatalf("persisted unavailability missing: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("availability sent a paid probe: calls=%d", calls.Load())
	}
}

func TestAuthenticationFailureMentioningQuotaDoesNotFallback(t *testing.T) {
	var fallbackCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"API key not authorized for quota endpoint"}}`))
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fallbackCalls.Add(1); checkpointReply(w, "[]") }))
	defer fallback.Close()
	cfg := checkpointClientConfig(primary.URL)
	cfg.Fallback = &config.FallbackConfig{Enabled: true, BaseURL: fallback.URL}
	_, _, err := New(cfg, checkpointLogger()).Chat(WithReviewSession(context.Background()), "kimi", nil)
	var quota *QuotaError
	if err == nil || errors.As(err, &quota) || fallbackCalls.Load() != 0 {
		t.Fatalf("authentication incorrectly used quota fallback: err=%v fallback=%d", err, fallbackCalls.Load())
	}
}

func TestResponseCheckpointSurvivesDowngradeWithActualProviderMetadata(t *testing.T) {
	var primaryCalls, fallbackCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if primaryCalls.Add(1) == 1 {
			checkpointReply(w, "primary result")
			return
		}
		http.Error(w, "unavailable", 503)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		checkpointReply(w, "fallback result")
	}))
	defer fallback.Close()
	cfg := checkpointClientConfig(primary.URL)
	cfg.Fallback = &config.FallbackConfig{Enabled: true, BaseURL: fallback.URL, Models: map[string]string{"small": "mini"}}
	client := New(cfg, checkpointLogger())
	ctx := WithCheckpoint(WithReviewSession(context.Background()), t.TempDir(), "immutable PR")
	first := []Message{{Content: "first stage"}}
	if _, _, err := client.Chat(ctx, "kimi", first); err != nil {
		t.Fatal(err)
	}
	second := []Message{{Content: "second stage"}}
	_, usage, err := client.Chat(ctx, "kimi", second)
	if err != nil || usage.Provider != "minimax" || usage.Model != "mini" {
		t.Fatalf("actual metadata missing: %+v %v", usage, err)
	}
	path, key := client.checkpointPath(ctx, "kimi", second)
	saved, ok := readResponse(path, key)
	if !ok || saved.Provider != "minimax" || saved.Model != "mini" {
		t.Fatalf("actual checkpoint metadata missing: %+v", saved)
	}
	got, usage, err := client.Chat(ctx, "kimi", first)
	if err != nil || got != "primary result" || usage.TotalTokens != 0 || primaryCalls.Load() != 2 || fallbackCalls.Load() != 1 {
		t.Fatalf("lost primary checkpoint: got=%q usage=%+v err=%v primary=%d fallback=%d", got, usage, err, primaryCalls.Load(), fallbackCalls.Load())
	}
}

func TestNetworkFailureUsesFallback(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("closed server received a request") }))
	primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { checkpointReply(w, "fallback") }))
	defer fallback.Close()
	cfg := checkpointClientConfig(primary.URL)
	cfg.Fallback = &config.FallbackConfig{Enabled: true, BaseURL: fallback.URL, Models: map[string]string{"small": "mini"}}
	got, _, err := New(cfg, checkpointLogger()).Chat(WithReviewSession(context.Background()), "kimi", nil)
	if err != nil || got != "fallback" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestQuotaCheckpointWriteFailureStopsFallback(t *testing.T) {
	var fallbackCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "quota", 429) }))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fallbackCalls.Add(1); checkpointReply(w, "[]") }))
	defer fallback.Close()
	cfg := checkpointClientConfig(primary.URL)
	cfg.Fallback = &config.FallbackConfig{Enabled: true, BaseURL: fallback.URL}
	path := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(path, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := New(cfg, checkpointLogger()).Chat(WithCheckpoint(context.Background(), path, "snapshot"), "kimi", nil)
	if err == nil || fallbackCalls.Load() != 0 {
		t.Fatalf("quota persistence failure ignored: err=%v fallback=%d", err, fallbackCalls.Load())
	}
}

func TestSharedQuotaDirectoryWorksAcrossResponseCheckpoints(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "600")
		http.Error(w, "quota reached", 429)
	}))
	defer srv.Close()
	cfg := checkpointClientConfig(srv.URL)
	shared := t.TempDir()
	first := WithQuotaDirectory(WithCheckpoint(context.Background(), t.TempDir(), "gvs PR"), shared)
	second := WithQuotaDirectory(WithCheckpoint(context.Background(), t.TempDir(), "infer PR"), shared)
	if _, _, err := New(cfg, checkpointLogger()).Chat(first, "kimi", nil); err == nil {
		t.Fatal("expected quota")
	}
	for _, ctx := range []context.Context{second, WithQuotaDirectory(context.Background(), shared)} {
		var quota *QuotaError
		if err := New(cfg, checkpointLogger()).Availability(ctx); !errors.As(err, &quota) {
			t.Fatalf("shared quota missing: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("availability probed provider: calls=%d", calls.Load())
	}
	corrected := cfg
	corrected.APIKey = "another-account"
	if err := New(corrected, checkpointLogger()).Availability(second); err != nil {
		t.Fatalf("quota leaked across accounts: %v", err)
	}
}

func TestProviderErrorsNeverExposeConfiguredAPIKey(t *testing.T) {
	for _, status := range []int{401, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"message":"invalid supplied key private-api-key"}}`))
			}))
			defer srv.Close()
			_, _, err := New(checkpointClientConfig(srv.URL), checkpointLogger()).Chat(context.Background(), "kimi", nil)
			if err == nil || strings.Contains(err.Error(), "private-api-key") {
				t.Fatalf("provider error exposes credential: %v", err)
			}
		})
	}
}
