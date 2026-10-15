//go:build !linux && !windows

package lifecycle

import (
	"fmt"
	"io"
	"runtime"
)

func unsupported() error {
	return fmt.Errorf("persistent instance management supports Linux and Windows, not %s", runtime.GOOS)
}
func processes() ([]process, error)          { return nil, unsupported() }
func terminate(process) error                { return unsupported() }
func launch(string, string, io.Writer) error { return unsupported() }
