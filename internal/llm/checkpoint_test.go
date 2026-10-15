package llm

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gvs-pr-agent/internal/config"
)

func checkpointClientConfig(url string) config.LLMConfig {
	cfg := config.LLMConfig{Provider: "test", APIKey: "private-api-key", BaseURL: url, MaxTokens: 128, Temperature: 1}
	cfg.Retry.MaxAttempts = 1
	return cfg
}

func checkpointLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func checkpointReply(w http.ResponseWriter, content string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"message": Message{Role: "assistant", Content: content}}},
		"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12},
	})
}

func TestCheckpointSurvivesClientRestartWithoutSpendingRoundBudget(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 {
			http.Error(w, "provider now unavailable", 503)
			return
		}
		checkpointReply(w, "[]")
	}))
	defer srv.Close()
	cfg := checkpointClientConfig(srv.URL)
	dir := t.TempDir()
	ctx := WithCheckpoint(context.Background(), dir, "gvs!556/source-a/target-a")
	messages := []Message{{Role: "user", Content: "private-source-prompt"}}
	_, first, err := New(cfg, checkpointLogger()).Chat(ctx, "model", messages)
	if err != nil || first.TotalTokens != 12 {
		t.Fatalf("first response: usage=%+v err=%v", first, err)
	}
	ctx, cancel := WithBudget(ctx, BudgetLimits{MaxCalls: 1, MaxTokens: 1})
	defer cancel()
	content, replay, err := New(cfg, checkpointLogger()).Chat(ctx, "model", messages)
	if err != nil || content != "[]" || replay.TotalTokens != 0 || calls.Load() != 1 {
		t.Fatalf("replay: content=%q usage=%+v calls=%d err=%v", content, replay, calls.Load(), err)
	}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if !d.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), cfg.APIKey) || strings.Contains(string(data), messages[0].Content) {
				t.Fatal("checkpoint persisted a credential or input prompt")
			}
		}
		return nil
	})
}

func TestCheckpointInvalidatesForVersionsPromptsAndModelSettings(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); checkpointReply(w, "[]") }))
	defer srv.Close()
	dir := t.TempDir()
	cfg := checkpointClientConfig(srv.URL)
	messages := []Message{{Role: "system", Content: "rules-a"}, {Role: "user", Content: "diff-a"}}
	cases := []struct {
		scope, model, rules string
		maxTokens           int
	}{
		{"source-a/target-a", "m1", "rules-a", 128},
		{"source-b/target-a", "m1", "rules-a", 128},
		{"source-a/target-b", "m1", "rules-a", 128},
		{"source-a/target-a", "m2", "rules-a", 128},
		{"source-a/target-a", "m1", "rules-b", 128},
		{"source-a/target-a", "m1", "rules-a", 256},
	}
	for _, tc := range cases {
		cfg.MaxTokens = tc.maxTokens
		messages[0].Content = tc.rules
		_, _, err := New(cfg, checkpointLogger()).Chat(WithCheckpoint(context.Background(), dir, tc.scope), tc.model, messages)
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != int32(len(cases)) {
		t.Fatalf("stale response reused: calls=%d", calls.Load())
	}
}

func TestCheckpointRejectsFailedResponsesAndEvictsInvalidParsedOutput(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			http.Error(w, "offline", 503)
		case 2:
			checkpointReply(w, "not valid JSON")
		default:
			checkpointReply(w, "[]")
		}
	}))
	defer srv.Close()
	ctx := WithCheckpoint(context.Background(), t.TempDir(), "version")
	client := New(checkpointClientConfig(srv.URL), checkpointLogger())
	messages := []Message{{Role: "user", Content: "diff"}}
	if _, _, err := client.Chat(ctx, "model", messages); err == nil {
		t.Fatal("expected failed request")
	}
	bad, _, err := client.Chat(ctx, "model", messages)
	if err != nil || bad != "not valid JSON" {
		t.Fatalf("failed request was persisted: %q %v", bad, err)
	}
	InvalidateCheckpoint(ctx, client, "model", messages)
	good, _, err := client.Chat(ctx, "model", messages)
	if err != nil || good != "[]" || calls.Load() != 3 {
		t.Fatalf("invalid output replayed: %q %v calls=%d", good, err, calls.Load())
	}
}

func TestCheckpointCorruptionFallsBackToModel(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); checkpointReply(w, "[]") }))
	defer srv.Close()
	dir := t.TempDir()
	ctx := WithCheckpoint(context.Background(), dir, "version")
	c := New(checkpointClientConfig(srv.URL), checkpointLogger())
	messages := []Message{{Role: "user", Content: "diff"}}
	if _, _, err := c.Chat(ctx, "model", messages); err != nil {
		t.Fatal(err)
	}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return err
	})
	if _, _, err := c.Chat(ctx, "model", messages); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("corrupt checkpoint reused, calls=%d", calls.Load())
	}
}
