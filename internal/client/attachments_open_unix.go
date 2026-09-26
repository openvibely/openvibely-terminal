//go:build aix || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package client

import (
	"os"

	"golang.org/x/sys/unix"
)

// openAttachmentPath prevents FIFO opens from waiting for a peer. Callers must
// verify the opened descriptor is a regular file before reading it.
func openAttachmentPath(path string) (*os.File, error) {
	return openUnixFile(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC)
}
