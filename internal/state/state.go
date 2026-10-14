// Package state 持久化分支审查基线（repo:branch -> 最后已审查 Commit SHA），
// 供"提交分支即审查"的增量 diff 计算与去重使用。
// 存储介质为 JSON 文件，写入采用临时文件 + rename 原子替换（进程崩溃不丢数据）。
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry 单个分支的审查基线。
type Entry struct {
	SHA        string    `json:"sha"`         // 最后已审查的 Commit SHA
	ReviewedAt time.Time `json:"reviewed_at"` // 基线更新时间
}

// Store 分支基线存储（线程安全）。
type Store struct {
	mu   sync.Mutex
	path string
	data map[string]Entry
}

// Open 加载基线文件；文件不存在时返回空存储（首轮运行）。
func Open(path string) (*Store, error) {
	s := &Store{path: path, data: map[string]Entry{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path, err)
	}
	return s, nil
}

// Get 返回指定 key（repoID:branch）的基线 SHA，不存在时返回空串。
func (s *Store) Get(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[key].SHA
}

// Set 更新基线并立即落盘。
func (s *Store) Set(key, sha string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, existed := s.data[key]
	s.data[key] = Entry{SHA: sha, ReviewedAt: time.Now()}
	if err := s.save(); err != nil {
		if existed {
			s.data[key] = previous
		} else {
			delete(s.data, key)
		}
		return err
	}
	return nil
}

// save 原子写入：先写临时文件，再 rename 覆盖（调用方需持锁）。
func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("write state tmp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename state: %w", err)
	}
	return nil
}
