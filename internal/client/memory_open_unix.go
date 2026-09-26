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
	return openUnixFile(name, unix.O_RDONLY|unix.O_NONBLOCK)
}
