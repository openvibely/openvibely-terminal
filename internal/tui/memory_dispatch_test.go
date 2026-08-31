package tui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-tui/internal/client"
)

func writeTUIProjectMemory(t *testing.T, repo, index string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(repo, ".openvibely", "memories")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MEMORIES.md"), []byte(index), 0o644); err != nil {
		t.Fatalf("WriteFile index: %v", err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
}

func memoryDispatchModel(t *testing.T, repo string) (Model, *int32) {
	t.Helper()
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(teaWindowSize())
	m = updated.(Model)
	m.projects = []client.Project{{ID: "p1", Name: "Demo", Path: repo}}
	m.selectedID = "p1"
	m.selectedName = "Demo"
	return m, &requests
}

// teaWindowSize keeps this test helper independent of the dispatch recorder's
// HTTP behavior while giving the model a normal transcript viewport.
func teaWindowSize() tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: 100, Height: 30}
}

func TestMemoryCommandDispatchesListShowAndSearchForSelectedProject(t *testing.T) {
	repo := t.TempDir()
	writeTUIProjectMemory(t, repo, "# Memory Index\n- [Managed Memory](managed_memory.md) - lifecycle contracts\n", map[string]string{
		"managed_memory.md": "---\ntitle: Managed Memory\n---\n\nMemory is project scoped and read-only here.\n",
	})
	m, requests := memoryDispatchModel(t, repo)

	m = runLine(t, m, "/memory list")
	out := stripANSI(transcript(m))
	for _, want := range []string{"Managed Memory", "managed_memory.md", "lifecycle contracts"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list output missing %q:\n%s", want, out)
		}
	}
	m = runLine(t, m, "/memories show managed_memory.md")
	m = runLine(t, m, "/memory search project scoped")
	out = stripANSI(transcript(m))
	for _, want := range []string{"Memory is project scoped", "Memory search", "project scoped"} {
		if !strings.Contains(out, want) {
			t.Fatalf("memory output missing %q:\n%s", want, out)
		}
	}
	if got := atomic.LoadInt32(requests); got != 0 {
		t.Fatalf("memory inspection made %d backend requests; canonical files should be read locally", got)
	}
}

func TestMemoryCommandNoProjectStopsBeforeSelectorOrMemoryRead(t *testing.T) {
	m, requests := memoryDispatchModel(t, t.TempDir())
	m.projects = []client.Project{{ID: "p1", Name: "Demo", Path: "\x00invalid"}}
	m.selectedID = ""
	m.selectedName = ""

	m = runLine(t, m, "/memory show known.md")
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "no project selected — use /project <name>") {
		t.Fatalf("no-project guidance missing:\n%s", out)
	}
	if m.selectorActive {
		t.Fatal("no-project memory command opened a selector")
	}
	if strings.Contains(out, "repository path is invalid") {
		t.Fatalf("memory path was inspected despite no selected project:\n%s", out)
	}
	if got := atomic.LoadInt32(requests); got != 0 {
		t.Fatalf("no-project memory command made %d backend requests", got)
	}
}

func TestMemoryCommandReportsSafeSelectedProjectPathError(t *testing.T) {
	m, requests := memoryDispatchModel(t, t.TempDir())
	const secretPath = "backend-password\x00repo"
	m.projects = []client.Project{{ID: "p1", Name: "Demo", Path: secretPath}}
	m.selectedID = "p1"
	m.selectedName = "Demo"

	m = runLine(t, m, "/memory list")
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "repository path is invalid") {
		t.Fatalf("safe path error missing:\n%s", out)
	}
	if strings.Contains(out, "backend-password") {
		t.Fatalf("selected path leaked into memory error:\n%s", out)
	}
	if got := atomic.LoadInt32(requests); got != 0 {
		t.Fatalf("memory path error made %d backend requests", got)
	}
}

func TestMemoryAliasCompletionHelpAndJSONEmptyState(t *testing.T) {
	command := lookupCommand("memory")
	if command == nil || lookupCommand("memories") != command {
		t.Fatal("memory compatibility alias is not registered")
	}
	if got := completeSlashInput("/memories se", *command); got != "/memory search " {
		t.Errorf("alias action completion = %q, want /memory search ", got)
	}
	if got := suggest("mem"); len(got) != 1 || got[0].name != "memory" {
		t.Fatalf("memory completion suggestions = %+v", got)
	}
	help := renderCommandHelp(*command)
	for _, want := range []string{"memory list", "memory show <file|title>", "memory search <query>", "aliases: memories"} {
		if !strings.Contains(help, want) {
			t.Errorf("memory help missing %q:\n%s", want, help)
		}
	}
	if !strings.Contains(renderHelp(), "/memories") {
		t.Fatal("main help does not expose the memory compatibility alias")
	}

	repo := t.TempDir()
	m, _ := memoryDispatchModel(t, repo)
	previous := jsonMode
	jsonMode = true
	defer func() { jsonMode = previous }()
	m = runLine(t, m, "/memory list")
	out := transcript(m)
	if !strings.Contains(out, `"memories":[]`) || !strings.Contains(out, `"warnings":["project memory directory is missing"]`) {
		t.Fatalf("empty memory JSON is not explicit/non-nil:\n%s", out)
	}
}

func TestMemoryListFilterReportsNoMatchesDistinctFromEmptyIndex(t *testing.T) {
	repo := t.TempDir()
	writeTUIProjectMemory(t, repo, "- [Notes](notes.md) - terminal notes\n", map[string]string{
		"notes.md": "# Notes\n\nUse the terminal.\n",
	})
	m, _ := memoryDispatchModel(t, repo)

	m = runLine(t, m, "/memory list absent")
	out := stripANSI(transcript(m))
	if !strings.Contains(out, `no memory matches for "absent"`) {
		t.Fatalf("filtered-empty output missing no-match state:\n%s", out)
	}
	if strings.Contains(out, "has no topic files") {
		t.Fatalf("filtered-empty output claimed the index was empty:\n%s", out)
	}
}

func TestRenderMemoryDocumentBoundsAndSanitizesTerminalText(t *testing.T) {
	body := "\x1b[2J" + strings.Repeat("x", 8192) + "\a\r\t"
	output := renderMemoryDocument(client.MemoryDocument{
		File:    "notes.md",
		Title:   "\x1b[31mUnsafe title\x1b[0m",
		Summary: "summary\a\r\t",
		Body:    body,
	})
	plain := stripANSI(output)
	for _, r := range plain {
		if r != '\n' && (r < 0x20 || (r >= 0x7f && r <= 0x9f)) {
			t.Fatalf("rendered memory contains terminal control %U: %q", r, plain)
		}
	}
	if strings.Contains(output, "\x1b[2J") || strings.Contains(output, "\x1b[31m") {
		t.Fatalf("rendered memory retained injected ANSI sequence: %q", output)
	}
	if len(plain) >= 2000 {
		t.Fatalf("rendered single-line memory was not bounded: %d bytes", len(plain))
	}
	if !strings.Contains(plain, "…") {
		t.Fatalf("bounded memory output missing truncation marker:\n%s", plain)
	}
}

func TestMemoryCLINoProjectFailsBeforeMemoryInspection(t *testing.T) {
	var projectRequests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects" {
			t.Errorf("unexpected CLI request %s", r.URL.Path)
		}
		atomic.AddInt32(&projectRequests, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"projects":[]}`))
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = RunCLI(c, &out, "", []string{"memory", "list"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "no project selected") {
		t.Fatalf("RunCLI no-project error = %v, want actionable guidance", err)
	}
	if out.Len() != 0 {
		t.Fatalf("no-project CLI wrote memory output: %q", out.String())
	}
	if got := atomic.LoadInt32(&projectRequests); got != 1 {
		t.Fatalf("no-project CLI made %d project requests, want one preload and no memory request", got)
	}
}

func TestMemoryCLIUsesSelectedProjectAndEmitsStableJSON(t *testing.T) {
	repo := t.TempDir()
	writeTUIProjectMemory(t, repo, "# Memory Index\n- [Notes](notes.md) - terminal notes\n", map[string]string{"notes.md": "# Notes\n\nUse the terminal.\n"})
	var projectRequests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects" {
			t.Errorf("unexpected CLI request %s", r.URL.Path)
		}
		atomic.AddInt32(&projectRequests, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"projects":[{"id":"p1","name":"Demo","path":"` + repo + `"}]}`))
	}))
	defer srv.Close()
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunCLI(c, &out, "p1", []string{"memory", "list"}, false, true); err != nil {
		t.Fatalf("RunCLI memory list: %v", err)
	}
	var result client.MemoryList
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("CLI output is not stable JSON: %v (%s)", err, out.String())
	}
	if len(result.Memories) != 1 || result.Memories[0].File != "notes.md" || result.Memories == nil || result.Warnings == nil {
		t.Fatalf("unexpected CLI memory result: %+v", result)
	}
	if got := atomic.LoadInt32(&projectRequests); got != 1 {
		t.Fatalf("CLI made %d project requests, want one", got)
	}
}
