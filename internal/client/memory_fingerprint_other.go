//go:build !darwin && !freebsd && !netbsd && !linux && !openbsd && !dragonfly && !solaris

package client

import (
	"os"
	"time"
)

// Without an inode change time there is no reliable way to detect same-size
// rewrites, so the search cache is disabled on these platforms.
func memoryFileFingerprintOf(os.FileInfo) (memoryFileFingerprint, time.Time, bool) {
	return memoryFileFingerprint{}, time.Time{}, false
}
