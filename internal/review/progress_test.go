package review

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gvs-pr-agent/internal/progress"
)

func TestFocusedMRPublishesReviewIdentityTimingAndActualUsage(t *testing.T) {
	_, _, service := focusedFixture(t)
	path := filepath.Join(t.TempDir(), "status.json")
	tracker := progress.New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := progress.WithTracker(context.Background(), tracker)
	if err := service.ReviewCodeupMR(ctx, "lzvsg/gvs", 8); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot progress.Snapshot
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.State != "completed" || snapshot.Task != "mr:lzvsg/gvs:8" || snapshot.Repository != "lzvsg/gvs" || !strings.Contains(snapshot.Title, "MR !8 fix") || snapshot.StartedAt.IsZero() || snapshot.FinishedAt.Before(snapshot.StartedAt) || snapshot.ElapsedSeconds <= 0 || snapshot.PromptTokens != 100 || snapshot.CompletionTokens != 50 || snapshot.TotalTokens != 150 || len(snapshot.ActiveJobs) != 0 {
		t.Fatalf("status=%+v", snapshot)
	}
}
