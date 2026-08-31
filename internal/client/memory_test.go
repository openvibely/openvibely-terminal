package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProjectMemory(t *testing.T, repo, index string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(repo, ".openvibely", "memories")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, projectMemoryIndex), []byte(index), 0o644); err != nil {
		t.Fatalf("WriteFile index: %v", err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
}

func TestParseMemoryIndexSupportsCanonicalAndPartialEntries(t *testing.T) {
	index := `# Memory Index

- [Managed Memory](managed_memory.md) - lifecycle and retrieval
- ` + "`project_notes.md`" + ` — current project notes
- [Broken](partial.md
- [Unsafe](../outside.md) - must not be read
- No topic files have been created yet.
`

	entries, warnings := parseMemoryIndex(index)
	if len(entries) != 3 {
		t.Fatalf("parsed %d entries, want 3: %+v", len(entries), entries)
	}
	if entries[0] != (memoryIndexEntry{File: "managed_memory.md", Title: "Managed Memory", Summary: "lifecycle and retrieval"}) {
		t.Fatalf("managed entry = %+v", entries[0])
	}
	if entries[1].File != "project_notes.md" || entries[1].Summary != "current project notes" {
		t.Fatalf("backtick entry = %+v", entries[1])
	}
	if entries[2].File != "partial.md" {
		t.Fatalf("partial entry = %+v", entries[2])
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"line 5 is malformed", "line 6 contains an unsafe file reference"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings missing %q: %v", want, warnings)
		}
	}
}

func TestMemoryListShowAndSearchUseCanonicalProjectFiles(t *testing.T) {
	repo := t.TempDir()
	writeProjectMemory(t, repo, `# Memory Index
- [Managed Memory](managed_memory.md) - lifecycle contracts
- [Project Notes](project_notes.md) - local notes
`, map[string]string{
		"managed_memory.md": "---\ntitle: Managed Memory\nsource: curator\n---\n\nMemory lifecycle is project scoped and read-only here.\n",
		"project_notes.md":  "# Project Notes\n\nUse the selected project path for inspection.\n",
	})

	c := &Client{}
	list, err := c.ListMemories(context.Background(), Project{ID: "p1", Name: "Demo", Path: repo})
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(list.Memories) != 2 || list.Memories[0].File != "managed_memory.md" {
		t.Fatalf("unexpected list: %+v", list)
	}
	if list.Memories[0].Body != "" || list.Memories[0].Title != "Managed Memory" {
		t.Fatalf("list should expose metadata only: %+v", list.Memories[0])
	}
	if list.Warnings == nil {
		t.Fatal("list warnings must be non-nil")
	}

	document, err := c.ShowMemory(context.Background(), Project{ID: "p1", Path: repo}, "Managed Memory")
	if err != nil {
		t.Fatalf("ShowMemory: %v", err)
	}
	if document.File != "managed_memory.md" || document.Title != "Managed Memory" || !strings.Contains(document.Body, "Memory lifecycle") {
		t.Fatalf("unexpected document: %+v", document)
	}
	if document.Warnings == nil {
		t.Fatal("document warnings must be non-nil")
	}

	search, err := c.SearchMemories(context.Background(), Project{ID: "p1", Path: repo}, "project scoped")
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(search.Memories) != 1 || search.Memories[0].File != "managed_memory.md" {
		t.Fatalf("unexpected search: %+v", search)
	}
	if search.Memories[0].Body != "" || !strings.Contains(strings.ToLower(search.Memories[0].Snippet), "project scoped") {
		t.Fatalf("search should return a concise snippet: %+v", search.Memories[0])
	}
	if search.Memories == nil || search.Warnings == nil {
		t.Fatal("search collections must be non-nil")
	}
}

func TestMemoryEmptyAndMalformedStatesStayMachineReadable(t *testing.T) {
	c := &Client{}
	empty, err := c.ListMemories(context.Background(), Project{ID: "empty", Path: t.TempDir()})
	if err != nil {
		t.Fatalf("empty ListMemories: %v", err)
	}
	if empty.Memories == nil || empty.Warnings == nil || len(empty.Memories) != 0 {
		t.Fatalf("empty result collections = %#v", empty)
	}
	encoded, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("json.Marshal empty: %v", err)
	}
	for _, want := range []string{`"memories":[]`, `"warnings":["project memory directory is missing"]`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("empty JSON missing %q: %s", want, encoded)
		}
	}

	repo := t.TempDir()
	writeProjectMemory(t, repo, "- [Broken](broken.md)\n", map[string]string{
		"broken.md": "---\ntitle: Broken\nmissing terminator\n\nbody still readable\n",
	})
	list, err := c.ListMemories(context.Background(), Project{Path: repo})
	if err != nil {
		t.Fatalf("malformed ListMemories: %v", err)
	}
	if len(list.Memories) != 1 || len(list.Warnings) != 0 {
		t.Fatalf("unexpected malformed list: %+v", list)
	}
	document, err := c.ShowMemory(context.Background(), Project{Path: repo}, "broken.md")
	if err != nil {
		t.Fatalf("malformed ShowMemory: %v", err)
	}
	if len(document.Warnings) == 0 || !strings.Contains(strings.Join(document.Warnings, "\n"), "unterminated") {
		t.Fatalf("malformed front matter warning missing: %+v", document)
	}
	if !strings.Contains(document.Body, "body still readable") {
		t.Fatalf("partial document body was lost: %+v", document)
	}
}

func TestShowMemoryRejectsUnindexedAndUnsafeReferences(t *testing.T) {
	repo := t.TempDir()
	writeProjectMemory(t, repo, "- [Known](known.md)\n", map[string]string{"known.md": "known"})
	c := &Client{}
	for _, reference := range []string{"MEMORIES.md", "../outside.md", "/etc/passwd", "missing.md"} {
		_, err := c.ShowMemory(context.Background(), Project{Path: repo}, reference)
		if err == nil || !strings.Contains(err.Error(), "not indexed") {
			t.Errorf("ShowMemory(%q) error = %v, want safe not-indexed error", reference, err)
		}
	}
}

func TestMemoryListReportsMissingIndexedFilesWithoutLeakingPaths(t *testing.T) {
	repo := t.TempDir()
	writeProjectMemory(t, repo, "- [Missing](missing.md)\n", nil)
	list, err := (&Client{}).ListMemories(context.Background(), Project{Path: repo})
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(list.Memories) != 1 || len(list.Warnings) != 1 {
		t.Fatalf("unexpected missing-file result: %+v", list)
	}
	if want := `memory file "missing.md" is missing`; list.Warnings[0] != want {
		t.Fatalf("warning = %q, want %q", list.Warnings[0], want)
	}
	if strings.Contains(list.Warnings[0], repo) {
		t.Fatalf("warning leaked repository path: %q", list.Warnings[0])
	}
}
