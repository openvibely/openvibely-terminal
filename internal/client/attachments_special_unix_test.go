//go:build aix || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package client

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestAddTaskAttachmentsRejectsFIFOWithoutRequestOrBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attachment.fifo")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Skipf("named pipes unavailable: %v", err)
	}

	assertAttachmentRejectedPromptly(t, path, nil)
}

func TestAddTaskAttachmentsRejectsConnectedFIFOWithoutRequestOrBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attachment.fifo")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Skipf("named pipes unavailable: %v", err)
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Skipf("opening connected FIFO unavailable: %v", err)
	}
	defer unix.Close(fd)

	assertAttachmentRejectedPromptly(t, path, nil)
}

func TestAddTaskAttachmentsCancellationCannotLeaveFIFOBlocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attachment.fifo")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Skipf("named pipes unavailable: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assertAttachmentRejectedPromptly(t, path, ctx)
}

func TestAddTaskAttachmentsRejectsOtherSpecialFileWithoutRequest(t *testing.T) {
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skipf("/dev/null unavailable: %v", err)
	}
	assertAttachmentRejectedPromptly(t, "/dev/null", nil)
}

func TestAttachmentMultipartBodyRejectsRegularFileReplacedByFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attachment.bin")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := newAttachmentMultipartBody([]string{path})
	if err != nil {
		t.Fatalf("newAttachmentMultipartBody: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Skipf("named pipes unavailable: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, body)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("replacement error = %v, want path-specific not-regular error", err)
		}
	case <-time.After(2 * time.Second):
		// Release an old blocking implementation so the regression does not leak
		// a permanently blocked goroutine while reporting the failure.
		if fd, openErr := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK, 0); openErr == nil {
			_ = unix.Close(fd)
		}
		t.Fatal("lazy attachment open blocked after FIFO replacement")
	}
	if closeErr := body.Close(); closeErr == nil || !strings.Contains(closeErr.Error(), "not a regular file") {
		t.Fatalf("Close error = %v, want replacement error", closeErr)
	}
}

func TestAddTaskAttachmentsSupportsSymlinkToRegularFile(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "target")
	linkDir := filepath.Join(dir, "link")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(linkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(targetDir, "source.txt")
	link := filepath.Join(linkDir, "fixture.bin")
	if err := os.WriteFile(target, []byte("linked payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var requests atomic.Int32
	srv := newAttachmentUploadServer(t, func(*http.Request) {
		requests.Add(1)
	}, func(*http.Request) {
		requests.Add(1)
	})
	defer srv.Close()
	c, _ := New(srv.URL)
	if _, err := c.AddTaskAttachments(context.Background(), "t-1", "p1", []string{link}); err != nil {
		t.Fatalf("AddTaskAttachments symlink: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want lookup and upload", requests.Load())
	}
}

func assertAttachmentRejectedPromptly(t *testing.T, path string, ctx context.Context) {
	t.Helper()
	if ctx == nil {
		ctx = context.Background()
	}
	var requests atomic.Int32
	srv := newAttachmentUploadServer(t, func(*http.Request) {
		requests.Add(1)
	}, func(*http.Request) {
		requests.Add(1)
	})
	defer srv.Close()
	c, _ := New(srv.URL)

	done := make(chan error, 1)
	go func() {
		_, err := c.AddTaskAttachments(ctx, "t-1", "p1", []string{path})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("special-file error = %v, want path-specific not-regular error", err)
		}
	case <-time.After(2 * time.Second):
		// Release a blocked FIFO open in the buggy implementation so the test does
		// not leak a permanently blocked goroutine while reporting the regression.
		if info, err := os.Stat(path); err == nil && info.Mode()&os.ModeNamedPipe != 0 {
			if fd, openErr := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK, 0); openErr == nil {
				_ = unix.Close(fd)
			}
		}
		t.Fatal("attachment validation blocked on a special file")
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d, want zero for rejected local input", requests.Load())
	}
}
