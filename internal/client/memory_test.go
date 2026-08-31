package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
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
- This prose mentions a .md suffix but is not an entry.
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

func TestMemoryRejectsCanonicalAndTopicSymlinkEscapes(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	memoryDir := filepath.Join(repo, ".openvibely", "memories")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatalf("MkdirAll memory dir: %v", err)
	}

	outsideIndex := filepath.Join(outside, "MEMORIES.md")
	if err := os.WriteFile(outsideIndex, []byte("outside index secret"), 0o644); err != nil {
		t.Fatalf("WriteFile outside index: %v", err)
	}
	indexPath := filepath.Join(memoryDir, projectMemoryIndex)
	if err := os.Symlink(outsideIndex, indexPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	list, err := (&Client{}).ListMemories(context.Background(), Project{Path: repo})
	if err == nil || !strings.Contains(err.Error(), "unable to read project memory index") {
		t.Fatalf("symlinked index error = %v, want safe unavailable error", err)
	}
	if len(list.Warnings) != 1 || !strings.Contains(list.Warnings[0], "unsafe path") {
		t.Fatalf("symlinked index warnings = %#v", list.Warnings)
	}
	if strings.Contains(err.Error(), outside) || strings.Contains(strings.Join(list.Warnings, "\n"), outside) {
		t.Fatalf("symlinked index leaked outside path: err=%q warnings=%v", err, list.Warnings)
	}

	if err := os.Remove(indexPath); err != nil {
		t.Fatalf("remove symlinked index: %v", err)
	}
	if err := os.WriteFile(indexPath, []byte("- [Escape](escape.md)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile index: %v", err)
	}
	outsideMemory := filepath.Join(outside, "escape.md")
	if err := os.WriteFile(outsideMemory, []byte("outside topic secret"), 0o644); err != nil {
		t.Fatalf("WriteFile outside memory: %v", err)
	}
	if err := os.Symlink(outsideMemory, filepath.Join(memoryDir, "escape.md")); err != nil {
		t.Fatalf("Symlink topic: %v", err)
	}

	list, err = (&Client{}).ListMemories(context.Background(), Project{Path: repo})
	if err != nil || len(list.Memories) != 1 {
		t.Fatalf("topic symlink list = %#v, err=%v", list, err)
	}
	if len(list.Warnings) != 1 || !strings.Contains(list.Warnings[0], "unsafe path") {
		t.Fatalf("topic symlink list warnings = %#v", list.Warnings)
	}
	document, err := (&Client{}).ShowMemory(context.Background(), Project{Path: repo}, "escape.md")
	if err == nil || !strings.Contains(err.Error(), "indexed file reference is unsafe") {
		t.Fatalf("topic symlink show error = %v, want safe path error", err)
	}
	if len(document.Warnings) != 1 || !strings.Contains(document.Warnings[0], "unsafe path") {
		t.Fatalf("topic symlink show warnings = %#v", document.Warnings)
	}
	search, err := (&Client{}).SearchMemories(context.Background(), Project{Path: repo}, "not-present")
	if err != nil {
		t.Fatalf("topic symlink search: %v", err)
	}
	if len(search.Memories) != 0 || len(search.Warnings) != 1 || !strings.Contains(search.Warnings[0], "unsafe path") {
		t.Fatalf("topic symlink search = %#v", search)
	}
	for _, body := range []string{document.Body, strings.Join(search.Warnings, "\n")} {
		if strings.Contains(body, "outside topic secret") || strings.Contains(body, outside) {
			t.Fatalf("topic symlink exposed external content: %q", body)
		}
	}
}

func TestMemoryRejectsMemoryDirectorySymlinkEscape(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	memoryParent := filepath.Join(repo, ".openvibely")
	if err := os.MkdirAll(memoryParent, 0o755); err != nil {
		t.Fatalf("MkdirAll memory parent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, projectMemoryIndex), []byte("- [Outside](outside.md)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile outside index: %v", err)
	}
	memoryDir := filepath.Join(memoryParent, "memories")
	if err := os.Symlink(outside, memoryDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	list, err := (&Client{}).ListMemories(context.Background(), Project{Path: repo})
	if err == nil || !strings.Contains(err.Error(), "unable to read project memory index") {
		t.Fatalf("memory directory symlink error = %v, want safe unavailable error", err)
	}
	if len(list.Memories) != 0 || len(list.Warnings) != 1 || !strings.Contains(list.Warnings[0], "unsafe path") {
		t.Fatalf("memory directory symlink result = %#v", list)
	}
	if strings.Contains(err.Error(), outside) || strings.Contains(strings.Join(list.Warnings, "\n"), outside) {
		t.Fatalf("memory directory symlink leaked outside path: err=%q warnings=%v", err, list.Warnings)
	}
}

func TestParseMemoryIndexSupportsBackendUnmarkedEntriesAndWarnsEmptyTargets(t *testing.T) {
	index := "# Memory Index\nmanaged_memory.md: lifecycle summary\n- [Empty]() - missing target\n- [Known](known.md) - usable topic\n"
	entries, warnings := parseMemoryIndex(index)
	if len(entries) != 2 {
		t.Fatalf("parsed %d entries, want 2: %+v", len(entries), entries)
	}
	if entries[0] != (memoryIndexEntry{File: "managed_memory.md", Title: "managed_memory", Summary: "lifecycle summary"}) {
		t.Fatalf("unmarked entry = %+v", entries[0])
	}
	if entries[1] != (memoryIndexEntry{File: "known.md", Title: "Known", Summary: "usable topic"}) {
		t.Fatalf("canonical entry = %+v", entries[1])
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "line 3 is malformed") {
		t.Fatalf("empty target warning missing: %v", warnings)
	}
}

func TestReadMemoryFileRejectsReplacementAfterValidation(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "memory.md")
	replacementPath := filepath.Join(dir, "replacement.md")
	if err := os.WriteFile(filePath, []byte("original"), 0o644); err != nil {
		t.Fatalf("WriteFile original: %v", err)
	}
	expectedInfo, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("Stat original: %v", err)
	}
	if err := os.WriteFile(replacementPath, []byte("replacement"), 0o644); err != nil {
		t.Fatalf("WriteFile replacement: %v", err)
	}
	replacementInfo, err := os.Stat(replacementPath)
	if err != nil {
		t.Fatalf("Stat replacement: %v", err)
	}
	if os.SameFile(expectedInfo, replacementInfo) {
		t.Skip("filesystem reused the same file identity")
	}
	if err := os.Remove(filePath); err != nil {
		t.Fatalf("Remove original: %v", err)
	}
	if err := os.Rename(replacementPath, filePath); err != nil {
		t.Fatalf("Rename replacement: %v", err)
	}

	_, err = readMemoryFile(context.Background(), filePath, expectedInfo, maxMemoryFileBytes)
	if !errors.Is(err, errMemoryFileChanged) {
		t.Fatalf("replacement read error = %v, want %v", err, errMemoryFileChanged)
	}
}

func TestMemoryRejectsNonRegularIndexedFiles(t *testing.T) {
	repo := t.TempDir()
	writeProjectMemory(t, repo, "- [Directory](directory.md)\n", nil)
	if err := os.Mkdir(filepath.Join(repo, ".openvibely", "memories", "directory.md"), 0o755); err != nil {
		t.Fatalf("Mkdir directory memory: %v", err)
	}

	client := &Client{}
	list, err := client.ListMemories(context.Background(), Project{Path: repo})
	if err != nil || len(list.Memories) != 1 {
		t.Fatalf("non-regular list = %#v, err=%v", list, err)
	}
	if len(list.Warnings) != 1 || !strings.Contains(list.Warnings[0], "not a regular file") {
		t.Fatalf("non-regular list warnings = %#v", list.Warnings)
	}
	document, err := client.ShowMemory(context.Background(), Project{Path: repo}, "directory.md")
	if err == nil || !strings.Contains(err.Error(), "unable to read indexed memory file") {
		t.Fatalf("non-regular show error = %v", err)
	}
	if len(document.Warnings) != 1 || !strings.Contains(document.Warnings[0], "not a regular file") {
		t.Fatalf("non-regular show warnings = %#v", document.Warnings)
	}
	if document.Available {
		t.Fatal("non-regular show must be marked unavailable")
	}
}

func TestMemoryDocumentAvailabilityDistinguishesEmptyFile(t *testing.T) {
	repo := t.TempDir()
	writeProjectMemory(t, repo, "- [Empty](empty.md)\n", map[string]string{"empty.md": ""})
	document, err := (&Client{}).ShowMemory(context.Background(), Project{Path: repo}, "empty.md")
	if err != nil {
		t.Fatalf("empty ShowMemory: %v", err)
	}
	if !document.Available || document.Body != "" || len(document.Warnings) != 0 {
		t.Fatalf("empty document availability = %#v", document)
	}
}

func TestResolveMemoryEntryAmbiguityErrorIsSanitizedAndBounded(t *testing.T) {
	entries := make([]memoryIndexEntry, 0, maxMemoryAmbiguityCandidates+4)
	for i := 0; i < cap(entries); i++ {
		entries = append(entries, memoryIndexEntry{
			File:  fmt.Sprintf("candidate-%02d-\x1b[31m%s.md", i, strings.Repeat("x", 120)),
			Title: "Same title",
		})
	}

	_, err := resolveMemoryEntry(entries, "Same title")
	if err == nil {
		t.Fatal("ambiguous memory reference unexpectedly resolved")
	}
	message := err.Error()
	for _, r := range message {
		if unicode.IsControl(r) {
			t.Fatalf("ambiguity error contains terminal control %U: %q", r, message)
		}
	}
	if got := len([]rune(message)); got > maxMemoryWarningRunes {
		t.Fatalf("ambiguity error is unbounded: %d runes", got)
	}
	if !strings.Contains(message, "candidate-00-") || !strings.Contains(message, "ambiguous") {
		t.Fatalf("ambiguity error lost useful candidate context: %q", message)
	}
}

func TestParseMemoryIndexRejectsControlCharacterHandles(t *testing.T) {
	entries, warnings := parseMemoryIndex("- [Unsafe](unsafe-\x1b[31m.md)\n")
	if len(entries) != 0 {
		t.Fatalf("control-bearing memory handle was accepted: %+v", entries)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "unsafe file reference") {
		t.Fatalf("unsafe-handle warning missing: %v", warnings)
	}
	for _, warning := range warnings {
		for _, r := range warning {
			if unicode.IsControl(r) {
				t.Fatalf("unsafe-handle warning contains terminal control %U: %q", r, warning)
			}
		}
	}
}
