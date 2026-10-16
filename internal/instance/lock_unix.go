//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package instance

import (
	"errors"
	"os"
	"syscall"
)

func IsBusy(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

func lockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }
