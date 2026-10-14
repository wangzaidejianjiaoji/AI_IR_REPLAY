// Package instance provides an advisory OS lock for single-host single-instance deployments.
package instance

import (
	"fmt"
	"os"
	"path/filepath"
)

type Lock struct{ file *os.File }

func Acquire(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("已有实例占用 %s: %w", path, err)
	}
	return &Lock{file: f}, nil
}
func (l *Lock) Close() error { return l.file.Close() }
