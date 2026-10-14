package instance

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProcessLock(t *testing.T) {
	if p := os.Getenv("TEST_INSTANCE_LOCK"); p != "" {
		lock, err := Acquire(p)
		if err == nil {
			lock.Close()
			os.Exit(2)
		}
		os.Exit(0)
	}
	p := filepath.Join(t.TempDir(), "service.lock")
	lock, err := Acquire(p)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessLock$")
	cmd.Env = append(os.Environ(), "TEST_INSTANCE_LOCK="+p)
	if err := cmd.Run(); err != nil {
		lock.Close()
		t.Fatalf("second process acquired same lock: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := Acquire(p)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	other.Close()
}
