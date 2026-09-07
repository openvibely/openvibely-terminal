//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package client

import (
	"fmt"
	"os"
)

// Platforms without a portable nonblocking file-open flag reject known special
// targets before opening, then callers verify the opened descriptor again.
func openAttachmentPath(path string) (*os.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("attachment %q is not a regular file", path)
	}
	return os.Open(path)
}
