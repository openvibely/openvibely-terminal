//go:build windows

package client

import "os"

// Capture identity from a handle: Windows path-based os.Stat defers loading
// the file ID until os.SameFile, when the path may already refer to a replacement.
func statFileIdentity(name string) (os.FileInfo, error) {
	// Leave special-file rejection to the caller without opening a device or pipe.
	info, err := os.Stat(name)
	if err != nil || !info.Mode().IsRegular() {
		return info, err
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.Stat()
}
