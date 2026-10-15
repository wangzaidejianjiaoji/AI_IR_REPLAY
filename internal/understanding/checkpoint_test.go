package understanding

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"gvs-pr-agent/internal/config"
	"gvs-pr-agent/internal/llm"
)

func TestRejectedKnowledgeResponseIsNotReplayedAfterRestart(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content := "# Correct knowledge"
		if calls.Add(1) == 1 {
			content = "```markdown\n```"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": llm.Message{Role: "assistant", Content: content}}}})
	}))
	defer srv.Close()
	cfg := understandingConfig(t)
	if err := os.MkdirAll(filepath.Dir(cfg.DocumentPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.DocumentPath, []byte("<!-- SOURCE-SHA: old -->\n# Previous knowledge"), 0600); err != nil {
		t.Fatal(err)
	}
	modelCfg := config.LLMConfig{APIKey: "key", BaseURL: srv.URL, Timeout: "2s"}
	modelCfg.Retry.MaxAttempts = 1
	ctx := llm.WithCheckpoint(context.Background(), t.TempDir(), "gvs/source/target")
	for attempt := 0; attempt < 2; attempt++ {
		client := llm.New(modelCfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		manager := New(cfg, &fakeRepo{head: "new", diff: "+changed"}, client, "", nil)
		snapshot, _, err := manager.Prepare(ctx, "model")
		if attempt == 0 && err == nil {
			t.Fatal("empty fenced response accepted")
		}
		if attempt == 1 && (err != nil || !snapshot.Updated || snapshot.SourceSHA != "new") {
			t.Fatalf("rejected response poisoned restart: %+v err=%v", snapshot, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("model calls=%d want 2", calls.Load())
	}
}
