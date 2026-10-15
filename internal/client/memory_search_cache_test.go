package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMemorySearchCacheRefreshAndIsolation(t *testing.T) {
	repo := t.TempDir()
	index := "- [Note](note.md)\n- [Duplicate](note.md)\n- [Other](other.md)\n"
	writeProjectMemory(t, repo, index, map[string]string{"note.md": "alpha alpha\n", "other.md": "alpha other\n"})
	note := filepath.Join(repo, ".openvibely", "memories", "note.md")
	info, err := os.Stat(note)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := memoryFileFingerprintOf(info); !ok {
		t.Skip("platform has no reliable fingerprint")
	}
	time.Sleep(2100 * time.Millisecond)
	c := &Client{}
	project := Project{ID: "one", Path: repo}
	check := func() {
		t.Helper()
		for _, q := range []string{"alpha", "bravo", "note", "metadata", "absent"} {
			want, wantErr := searchLocalMemories(context.Background(), project, q)
			got, gotErr := c.SearchMemories(context.Background(), project, q)
			if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotErr, wantErr) {
				t.Fatalf("query %q: got %#v, %v; want %#v, %v", q, got, gotErr, want, wantErr)
			}
		}
	}
	check()
	_, before := memorySearchIOStats.snapshot()
	_, err = c.SearchMemories(context.Background(), project, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	_, after := memorySearchIOStats.snapshot()
	if after != before {
		t.Fatal("warm query read note content")
	}
	// In-place same-size rewrite with restored mtime must invalidate via ctime.
	if err := os.WriteFile(note, []byte("bravo bravo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(note, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	check()
	// Also verify after the conservative timestamp window: ctime, rather than
	// the temporary bypass, must invalidate the original cached result.
	time.Sleep(2100 * time.Millisecond)
	check()
	// Replacement inode with identical size and mtime.
	replacement := note + ".tmp"
	if err := os.WriteFile(replacement, []byte("alpha alpha\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, note); err != nil {
		t.Fatal(err)
	}
	check()
	if err := os.Chmod(note, 0600); err != nil {
		t.Fatal(err)
	}
	check()
	if err := os.Chmod(note, 0000); err != nil {
		t.Fatal(err)
	}
	check()
	if err := os.Chmod(note, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(note, note+".renamed.md"); err != nil {
		t.Fatal(err)
	}
	check()
	writeProjectMemory(t, repo, index+"- [Renamed](note.md.renamed.md)\n- [Added](added.md)\n", map[string]string{"added.md": "alpha added"})
	check()
	if err := os.Remove(note + ".renamed.md"); err != nil {
		t.Fatal(err)
	}
	check()
	// Replacing a previously indexed path by an escaping symlink must never hit.
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("alpha secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, note); err != nil {
		t.Fatal(err)
	}
	check()
	writeProjectMemory(t, repo, strings.ReplaceAll(index, "[Other]", "[Metadata]"), nil)
	check()
	other := t.TempDir()
	writeProjectMemory(t, other, index, map[string]string{"note.md": "bravo", "other.md": strings.Repeat("x", maxMemoryFileBytes+1)})
	project = Project{ID: "two", Path: other}
	check()
	project.ID = "one"
	check()
}

// Cancel deterministically during the scan, rather than depending on scheduling.
type memoryCancelContext struct {
	context.Context
	calls, limit int
}

func (c *memoryCancelContext) Err() error {
	c.calls++
	if c.calls >= c.limit {
		return context.Canceled
	}
	return nil
}
func TestMemorySearchCacheCancellationDuringScan(t *testing.T) {
	repo := t.TempDir()
	writeProjectMemory(t, repo, "- [Note](note.md)\n", map[string]string{"note.md": strings.Repeat("a line of content\n", 10000)})
	ctx := &memoryCancelContext{Context: context.Background(), limit: 100}
	c := &Client{}
	_, err := c.SearchMemories(ctx, Project{Path: repo}, "content")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if len(c.memorySearchCache.entries) != 0 {
		t.Fatal("canceled scan was cached")
	}
}

func TestMemorySearchCacheBudget(t *testing.T) {
	file := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(file, []byte("note"), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2100 * time.Millisecond)
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	cache := &memorySearchCache{}
	for i := 0; i < memorySearchCacheEntries+10; i++ {
		cache.store(memorySearchCacheKey{query: strings.Repeat("x", i%100) + string(rune(i))}, info, info, Memory{}, false, nil)
	}
	if cache.bytes > memorySearchCacheBytes || len(cache.entries) > memorySearchCacheEntries {
		t.Fatal("cache exceeded budget")
	}
}

func TestMemorySearchCachePreservesWarningsAndOwnership(t *testing.T) {
	repo := t.TempDir()
	writeProjectMemory(t, repo, "- [Note](note.md)\n", map[string]string{"note.md": "---\nbroken front matter\n---\nalpha body\n"})
	time.Sleep(2100 * time.Millisecond)
	c := &Client{}
	project := Project{Path: repo}
	want, err := searchLocalMemories(context.Background(), project, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		got, err := c.SearchMemories(context.Background(), project, "alpha")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, %v; want %#v", got, err, want)
		}
		if len(got.Warnings) == 0 || len(got.Memories) == 0 {
			t.Fatal("fixture must produce warning and match")
		}
		got.Warnings[0] = "caller mutation"
		got.Memories[0].Snippet = "caller mutation"
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := c.SearchMemories(context.Background(), project, "alpha")
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("concurrent search: %#v, %v", got, err)
			}
		}()
	}
	wg.Wait()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.SearchMemories(ctx, project, "alpha"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cached cancellation: %v", err)
	}
}

func TestMemorySearchNilClientFallback(t *testing.T) {
	repo := t.TempDir()
	writeProjectMemory(t, repo, "- [Note](note.md)\n", map[string]string{"note.md": "alpha"})
	var c *Client
	got, err := c.SearchMemories(context.Background(), Project{Path: repo}, "alpha")
	if err != nil || len(got.Memories) != 1 {
		t.Fatalf("nil client search: %#v, %v", got, err)
	}
}
