//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package client

import (
	"os"

	"golang.org/x/sys/unix"
)

// openMemoryFile uses nonblocking descriptor creation on Unix so a path that
// changes to a FIFO cannot stall the bounded memory read before its type is
// checked. The caller compares the opened descriptor with the pre-open stat.
func openMemoryFile(name string) (*os.File, error) {
	fd, err := unix.Open(name, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrInvalid}
	}
	return file, nil
}
