//go:build !windows

package client

import "os"

func statFileIdentity(name string) (os.FileInfo, error) {
	return os.Stat(name)
}
