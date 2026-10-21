//go:build windows

package client

import "os"

// Capture identity from a handle: Windows path-based os.Stat defers loading
// the file ID until os.SameFile, when the path may already refer to a replacement.
func statMemoryFile(name string) (os.FileInfo, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.Stat()
}
