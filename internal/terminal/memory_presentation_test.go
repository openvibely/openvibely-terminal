package terminal

import (
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

func runMemoryPresentationCommand(t *testing.T, m Model, args []string, jsonOutput bool) resultMsg {
	t.Helper()
	previous := jsonMode
	jsonMode = jsonOutput
	defer func() { jsonMode = previous }()

	command := lookupCommand("memory")
	if command == nil {
		t.Fatal("memory command is not registered")
	}
	_, cmd := command.run(m, args)
	if cmd == nil {
		t.Fatalf("memory command %v returned no result command", args)
	}
	message, ok := cmd().(resultMsg)
	if !ok {
		t.Fatalf("memory command %v returned %T, want resultMsg", args, message)
	}
	return message
}

func TestMemoryCommandsKeepPlainAndJSONPresentationAligned(t *testing.T) {
	repo := t.TempDir()
	writeTUIProjectMemory(t, repo,
		"# Memory Index\n- [Topic](topic.md) - indexed summary\n- [Missing](missing.md) - absent entry\n",
		map[string]string{"topic.md": "# Topic\n\nalpha target content.\n"},
	)
	m, requests := memoryDispatchModel(t, repo)

	tests := []struct {
		name string
		args []string
	}{
		{name: "list", args: []string{"list"}},
		{name: "show", args: []string{"show", "topic.md"}},
		{name: "search", args: []string{"search", "alpha"}},
		{name: "list no match", args: []string{"list", "does-not-match"}},
		{name: "search no match", args: []string{"search", "does-not-match"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plain := runMemoryPresentationCommand(t, m, tc.args, false)
			if plain.err != nil {
				t.Fatalf("plain command error: %v", plain.err)
			}
			jsonResult := runMemoryPresentationCommand(t, m, tc.args, true)
			if jsonResult.err != nil {
				t.Fatalf("JSON command error: %v", jsonResult.err)
			}

			var expectedJSON, expectedPlain string
			switch tc.args[0] {
			case "list":
				var result client.MemoryList
				if err := json.Unmarshal([]byte(jsonResult.body), &result); err != nil {
					t.Fatalf("list JSON output: %v (%s)", err, jsonResult.body)
				}
				filter := ""
				if len(tc.args) > 1 {
					filter = strings.Join(tc.args[1:], " ")
				}
				if filter == "" && (len(result.Memories) != 2 || result.Memories[0].File != "topic.md" || result.Memories[0].Title != "Topic" || result.Memories[0].Summary != "indexed summary" || result.Memories[1].File != "missing.md") {
					t.Fatalf("unexpected list JSON values: %+v", result)
				}
				if filter != "" && len(result.Memories) != 0 {
					t.Fatalf("filtered list JSON retained non-matching memories: %+v", result.Memories)
				}
				if len(result.Warnings) == 0 {
					t.Fatalf("list JSON lost missing-file warning: %+v", result)
				}
				expectedJSON, _ = marshalJSON(result)
				expectedPlain = renderMemoryListForFilter(result, filter)
				if tc.name == "list no match" && !strings.Contains(expectedPlain, `no memory matches for "does-not-match"`) {
					t.Fatalf("list no-match output lost its distinct empty state: %q", expectedPlain)
				}
			case "show":
				var result client.MemoryDocument
				if err := json.Unmarshal([]byte(jsonResult.body), &result); err != nil {
					t.Fatalf("show JSON output: %v (%s)", err, jsonResult.body)
				}
				if result.File != "topic.md" || result.Title != "Topic" || result.Summary != "indexed summary" || result.Body != "# Topic\n\nalpha target content.\n" || !result.Available {
					t.Fatalf("unexpected show JSON values: %+v", result)
				}
				expectedJSON, _ = marshalJSON(result)
				expectedPlain = renderMemoryDocument(result)
			case "search":
				var result client.MemorySearch
				if err := json.Unmarshal([]byte(jsonResult.body), &result); err != nil {
					t.Fatalf("search JSON output: %v (%s)", err, jsonResult.body)
				}
				if result.Query != tc.args[1] {
					t.Fatalf("search JSON query = %q, want %q", result.Query, tc.args[1])
				}
				if tc.name == "search" && (len(result.Memories) != 1 || result.Memories[0].File != "topic.md" || !strings.Contains(result.Memories[0].Snippet, "alpha")) {
					t.Fatalf("unexpected search JSON values: %+v", result)
				}
				if tc.name == "search no match" && len(result.Memories) != 0 {
					t.Fatalf("search no-match JSON retained results: %+v", result.Memories)
				}
				if len(result.Warnings) == 0 {
					t.Fatalf("search JSON lost missing-file warning: %+v", result)
				}
				expectedJSON, _ = marshalJSON(result)
				expectedPlain = renderMemorySearch(result)
				if tc.name == "search no match" && !strings.Contains(expectedPlain, `no memory matches for "does-not-match"`) {
					t.Fatalf("search no-match output lost its distinct empty state: %q", expectedPlain)
				}
			}
			if jsonResult.body != expectedJSON {
				t.Errorf("JSON output changed shape or values:\n got: %s\nwant: %s", jsonResult.body, expectedJSON)
			}
			if plain.body != expectedPlain {
				t.Errorf("plain output differs from its renderer:\n got: %q\nwant: %q", plain.body, expectedPlain)
			}
		})
	}

	if got := atomic.LoadInt32(requests); got != 1 {
		t.Fatalf("memory commands made %d backend requests; unsupported backend should be probed once", got)
	}
}

func TestMemoryOutputPreservesReadErrorsAndMarshalErrorPrecedence(t *testing.T) {
	readErr := errors.New("memory read failed")
	values := []struct {
		name  string
		value any
	}{
		{name: "list", value: client.MemoryList{Memories: []client.Memory{}, Warnings: []string{"index warning"}}},
		{name: "show", value: client.MemoryDocument{File: "topic.md", Available: false, Warnings: []string{"file warning"}}},
		{name: "search", value: client.MemorySearch{Query: "topic", Memories: []client.Memory{}, Warnings: []string{"search warning"}}},
	}
	for _, tc := range values {
		t.Run(tc.name, func(t *testing.T) {
			const rendered = "rendered memory body"
			plain, err := memoryOutput(tc.value, readErr, func() string { return rendered }, false)
			if plain != rendered || err != readErr {
				t.Fatalf("plain result = (%q, %v), want (%q, original read error)", plain, err, rendered)
			}
			encoded, err := memoryOutput(tc.value, readErr, func() string { return rendered }, true)
			want, marshalErr := marshalJSON(tc.value)
			if marshalErr != nil {
				t.Fatalf("marshal test fixture: %v", marshalErr)
			}
			if encoded != want || err != readErr {
				t.Fatalf("JSON result = (%q, %v), want (%q, original read error)", encoded, err, want)
			}
		})
	}

	encoded, err := memoryOutput(make(chan int), readErr, func() string { return "unused" }, true)
	if encoded != "" || err == nil || err == readErr || !strings.Contains(err.Error(), "json:") {
		t.Fatalf("marshal failure result = (%q, %v), want marshal error to take precedence over %v", encoded, err, readErr)
	}
}

func TestMemoryShowReadErrorKeepsRenderedUnavailableDocument(t *testing.T) {
	repo := t.TempDir()
	writeTUIProjectMemory(t, repo, "- [Missing](missing.md)\n", nil)
	m, _ := memoryDispatchModel(t, repo)

	for _, jsonOutput := range []bool{false, true} {
		result := runMemoryPresentationCommand(t, m, []string{"show", "missing.md"}, jsonOutput)
		if result.err == nil || !strings.Contains(result.err.Error(), "unable to read indexed memory file") {
			t.Fatalf("show read error = %v, want the original local read error", result.err)
		}
		if jsonOutput {
			var document client.MemoryDocument
			if err := json.Unmarshal([]byte(result.body), &document); err != nil {
				t.Fatalf("show JSON body was lost alongside read error: %v (%q)", err, result.body)
			}
			if document.Available || document.File != "missing.md" || len(document.Warnings) == 0 {
				t.Fatalf("unexpected unavailable JSON document: %+v", document)
			}
		} else if !strings.Contains(result.body, "(memory file unavailable)") || !strings.Contains(result.body, `memory file "missing.md" is missing`) {
			t.Fatalf("plain unavailable document or warning missing alongside read error: %q", result.body)
		}
	}
}
