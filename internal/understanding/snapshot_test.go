package understanding

import (
	"context"
	"gvs-pr-agent/internal/config"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotReadsPinnedGitBlobsAndRejectsIncompleteBudget(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git required for initialization")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		b, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git failed: %v %s", err, b)
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "--quiet")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n// committed evidence\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "main.go")
	git("-c", "user.name=Test", "-c", "user.email=test@local", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=", "commit", "--quiet", "-m", "source")
	sha := git("rev-parse", "HEAD")
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("uncommitted assumptions"), 0600)
	cfg := config.UnderstandingConfig{SnapshotRepository: dir, MaxSnapshotBytes: 10000}
	snap, err := gitSnapshot(context.Background(), cfg, sha)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snap, "committed evidence") || !strings.Contains(snap, sha) || strings.Contains(snap, "uncommitted assumptions") {
		t.Fatalf("snapshot not version pinned: %s", snap)
	}
	cfg.MaxSnapshotBytes = 1
	if _, err := gitSnapshot(context.Background(), cfg, sha); err == nil {
		t.Fatal("oversized snapshot silently truncated")
	}
}
