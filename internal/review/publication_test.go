package review

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicationReceiptSurvivesStateFailureAndRestart(t *testing.T) {
	cfg := testConfig(t, "", "")
	s := &Service{cfg: cfg}
	posts := 0
	post := func() error { posts++; return nil }
	if err := s.publishOnce(context.Background(), "mr:source:target", "report", post); err != nil {
		t.Fatal(err)
	}
	s = &Service{cfg: cfg}
	if err := s.publishOnce(context.Background(), "mr:source:target", "report regenerated at new time", post); err != nil {
		t.Fatal(err)
	}
	if posts != 1 {
		t.Fatalf("receipt replay posted again: %d", posts)
	}
	if err := s.publishOnce(context.Background(), "mr:source:new-target", "report", post); err != nil {
		t.Fatal(err)
	}
	if posts != 2 {
		t.Fatal("new target suppressed")
	}
	path := filepath.Join(cfg.Review.RuntimeDir, "published", fingerprint("mr:source:target")+".json")
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if s.publishOnce(context.Background(), "mr:source:target", "report", post) == nil || posts != 2 {
		t.Fatal("corrupt receipt caused duplicate post")
	}
}
