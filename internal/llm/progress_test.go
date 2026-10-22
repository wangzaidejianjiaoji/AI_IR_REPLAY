package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"gvs-pr-agent/internal/progress"
)

func TestProgressCountsProviderUsageOnceAndMarksUnreportedRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkpointReply(w, "[]")
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "status.json")
	tracker := progress.New(path, checkpointLogger())
	ctx := progress.WithTracker(context.Background(), tracker)
	finish := progress.Begin(ctx, "MR-7", "sha")
	ctx = WithCheckpoint(progress.ReviewContext(ctx), t.TempDir(), "review")
	client := New(checkpointClientConfig(server.URL), checkpointLogger())
	messages := []Message{{Role: "user", Content: "source"}}
	for i := 0; i < 2; i++ {
		if _, _, err := client.Chat(ctx, "model", messages); err != nil {
			t.Fatal(err)
		}
	}
	// An invalid response has unknown billing and must never look like known zero usage.
	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"[]"}}]}`))
	}))
	defer missing.Close()
	if _, _, err := New(checkpointClientConfig(missing.URL), checkpointLogger()).Chat(ctx, "model", []Message{{Role: "user", Content: "other"}}); err != nil {
		t.Fatal(err)
	}
	finish("completed")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot progress.Snapshot
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.TotalTokens != 12 || snapshot.Requests != 2 || snapshot.CacheHits != 1 || snapshot.UsageMissing != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}
