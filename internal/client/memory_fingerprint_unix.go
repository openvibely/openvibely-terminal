//go:build linux || openbsd || dragonfly || solaris

package client

import (
	"os"
	"syscall"
	"time"
)

func memoryFileFingerprintOf(info os.FileInfo) (memoryFileFingerprint, time.Time, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return memoryFileFingerprint{}, time.Time{}, false
	}
	sec, nsec := st.Ctim.Unix()
	return memoryFileFingerprint{
		dev:   uint64(st.Dev),
		ino:   uint64(st.Ino),
		size:  info.Size(),
		mode:  info.Mode(),
		mtime: info.ModTime().UnixNano(),
		ctime: sec*int64(time.Second) + nsec,
	}, time.Unix(sec, nsec), true
}
