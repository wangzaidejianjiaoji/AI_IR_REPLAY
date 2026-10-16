package understanding

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareRejectsUnidentifiedVersionedDocumentEvenWhenSHAEqualsHead(t *testing.T) {
	cfg := understandingConfig(t)
	if err := os.MkdirAll(filepath.Dir(cfg.DocumentPath), 0755); err != nil {
		t.Fatal(err)
	}
	old := "<!-- SOURCE-SHA: same -->\n# Unidentified repository knowledge"
	if err := os.WriteFile(cfg.DocumentPath, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	model := &fakeModel{}
	snap, _, err := New(cfg, &fakeRepo{head: "same"}, model, "seed", nil).Prepare(context.Background(), "model")
	if err == nil || snap.Document != "" || snap.SourceSHA != "" || model.calls != 0 {
		t.Fatalf("unidentified versioned document trusted: snap=%+v err=%v calls=%d", snap, err, model.calls)
	}
	raw, readErr := os.ReadFile(cfg.DocumentPath)
	if readErr != nil || string(raw) != old {
		t.Fatalf("rejected document mutated: %q %v", raw, readErr)
	}
}

func TestPrepareRejectsConflictingSourceSHAMarkers(t *testing.T) {
	cfg := understandingConfig(t)
	if err := os.MkdirAll(filepath.Dir(cfg.DocumentPath), 0755); err != nil {
		t.Fatal(err)
	}
	old := "<!-- SOURCE-REPOSITORY: " + cfg.SourceRepository + " -->\n<!-- SOURCE-SHA: same -->\n<!-- GVS-SOURCE-SHA: different -->\n# ambiguous"
	if err := os.WriteFile(cfg.DocumentPath, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	model := &fakeModel{}
	snap, _, err := New(cfg, &fakeRepo{head: "same"}, model, "seed", nil).Prepare(context.Background(), "model")
	if err == nil || snap.Document != "" || snap.SourceSHA != "" || model.calls != 0 {
		t.Fatalf("ambiguous version reused: snap=%+v err=%v calls=%d", snap, err, model.calls)
	}
}
