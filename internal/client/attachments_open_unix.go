//go:build aix || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package client

import (
	"os"

	"golang.org/x/sys/unix"
)

// openAttachmentPath prevents FIFO opens from waiting for a peer. Callers must
// verify the opened descriptor is a regular file before reading it.
func openAttachmentPath(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
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
