//go:build aix || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package client

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenUnixFilePreservesPathAndUnderlyingError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	_, err := openUnixFile(path, unix.O_RDONLY|unix.O_NONBLOCK)
	if err == nil {
		t.Fatal("openUnixFile succeeded for a missing path")
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("error type = %T, want *os.PathError", err)
	}
	if pathErr.Op != "open" || pathErr.Path != path {
		t.Fatalf("PathError = %#v, want open path %q", pathErr, path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want underlying not-exist error", err)
	}
}
