// state 分支基线存储单元测试：空存储、落盘回读、原子覆盖、损坏文件报错。
package state

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestOpenMissingFile(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "not-exist.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Get("any"); got != "" {
		t.Errorf("空存储 Get = %q, want \"\"", got)
	}
}

func TestSetPersistAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "state.json") // 自动建目录
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("repo:feature/a", "sha-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("repo:feature/a", "sha-2"); err != nil { // 覆盖更新
		t.Fatal(err)
	}
	if err := s.Set("mr:repo:8", "mr-sha"); err != nil {
		t.Fatal(err)
	}

	// 重新打开验证持久化
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Get("repo:feature/a"); got != "sha-2" {
		t.Errorf("Get = %q, want sha-2", got)
	}
	if got := s2.Get("mr:repo:8"); got != "mr-sha" {
		t.Errorf("Get = %q, want mr-sha", got)
	}
	// 临时文件不应残留
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("临时文件残留")
	}
}

func TestOpenCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Error("损坏文件应返回错误")
	}
}

func TestConcurrentSet(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Set("k", "v")
			_ = s.Get("k")
		}()
	}
	wg.Wait()
	if got := s.Get("k"); got != "v" {
		t.Errorf("并发后 Get = %q, want v", got)
	}
}

func TestFailedSaveKeepsMemoryAndDiskBaseline(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("branch", "old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("branch", "new"); err == nil {
		t.Fatal("expected write failure")
	}
	if got := s.Get("branch"); got != "old" {
		t.Fatalf("memory baseline advanced on failed save: %s", got)
	}
	if err := s.Set("new-branch", "new"); err == nil {
		t.Fatal("expected write failure")
	}
	if got := s.Get("new-branch"); got != "" {
		t.Fatalf("failed insert persisted in memory: %s", got)
	}
	disk, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Get("branch") != "old" {
		t.Fatal("disk baseline advanced")
	}
}
