//go:build windows

package instance

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

func IsBusy(err error) bool { return errors.Is(err, syscall.Errno(33)) }

var lockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

func lockFile(f *os.File) error {
	var overlapped syscall.Overlapped
	r, _, err := lockFileEx.Call(f.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if r == 0 {
		return err
	}
	return nil
}
