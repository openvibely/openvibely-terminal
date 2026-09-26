//go:build aix || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package client

import (
	"os"

	"golang.org/x/sys/unix"
)

// openUnixFile opens a path with the caller's flags and wraps the descriptor,
// preserving path-specific errors for both Unix openers.
func openUnixFile(path string, flags int) (*os.File, error) {
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrInvalid}
	}
	return file, nil
}
