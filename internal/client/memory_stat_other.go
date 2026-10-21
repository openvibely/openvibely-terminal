//go:build !windows

package client

import "os"

func statMemoryFile(name string) (os.FileInfo, error) {
	return os.Stat(name)
}
