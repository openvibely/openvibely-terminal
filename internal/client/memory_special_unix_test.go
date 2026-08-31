//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMemoryRejectsFIFOReplacementWithoutBlocking(t *testing.T) {
	repo := t.TempDir()
	filePath := filepath.Join(repo, "memory.md")
	if err := os.WriteFile(filePath, []byte("regular"), 0o600); err != nil {
		t.Fatalf("WriteFile regular: %v", err)
	}
	expectedInfo, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("Stat regular: %v", err)
	}
	if err := os.Remove(filePath); err != nil {
		t.Fatalf("Remove regular: %v", err)
	}
	if err := syscall.Mkfifo(filePath, 0o600); err != nil {
		t.Skipf("Mkfifo replacement unavailable: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, readErr := readMemoryFile(context.Background(), filePath, expectedInfo, maxMemoryFileBytes)
		done <- readErr
	}()
	select {
	case readErr := <-done:
		if readErr != errMemoryFileNotRegular {
			t.Fatalf("FIFO replacement error = %v, want %v", readErr, errMemoryFileNotRegular)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readMemoryFile blocked on a FIFO replacement")
	}
}

func TestMemoryRejectsFIFOWithoutBlocking(t *testing.T) {
	repo := t.TempDir()
	memoryDir := filepath.Join(repo, ".openvibely", "memories")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatalf("MkdirAll memory dir: %v", err)
	}

	indexPath := filepath.Join(memoryDir, projectMemoryIndex)
	if err := syscall.Mkfifo(indexPath, 0o600); err != nil {
		t.Skipf("named pipes unavailable: %v", err)
	}
	listDone := make(chan struct{})
	var list MemoryList
	var listErr error
	go func() {
		list, listErr = (&Client{}).ListMemories(context.Background(), Project{Path: repo})
		close(listDone)
	}()
	select {
	case <-listDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ListMemories blocked while inspecting a FIFO index")
	}
	if listErr == nil || !strings.Contains(listErr.Error(), "unable to read project memory index") {
		t.Fatalf("FIFO index error = %v, want safe unavailable error", listErr)
	}
	if len(list.Warnings) != 1 || !strings.Contains(list.Warnings[0], "not a regular file") {
		t.Fatalf("FIFO index warnings = %#v", list.Warnings)
	}

	if err := os.Remove(indexPath); err != nil {
		t.Fatalf("remove FIFO index: %v", err)
	}
	if err := os.WriteFile(indexPath, []byte("- [Pipe](pipe.md)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile index: %v", err)
	}
	pipePath := filepath.Join(memoryDir, "pipe.md")
	if err := syscall.Mkfifo(pipePath, 0o600); err != nil {
		t.Fatalf("Mkfifo topic: %v", err)
	}
	showDone := make(chan struct{})
	var document MemoryDocument
	var showErr error
	go func() {
		document, showErr = (&Client{}).ShowMemory(context.Background(), Project{Path: repo}, "pipe.md")
		close(showDone)
	}()
	select {
	case <-showDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ShowMemory blocked while reading a FIFO topic")
	}
	if showErr == nil || !strings.Contains(showErr.Error(), "unable to read indexed memory file") {
		t.Fatalf("FIFO topic error = %v, want safe read error", showErr)
	}
	if len(document.Warnings) != 1 || !strings.Contains(document.Warnings[0], "not a regular file") {
		t.Fatalf("FIFO topic warnings = %#v", document.Warnings)
	}
}
