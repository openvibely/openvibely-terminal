//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package client

import "os"

func openMemoryFile(name string) (*os.File, error) {
	return os.Open(name)
}
