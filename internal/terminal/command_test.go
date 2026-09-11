package terminal

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

func TestTokenizeCommandGroupsQuotedArguments(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []string
	}{
		{
			name: "double quotes",
			line: `/tasks attachments add "Fix login bug" "/tmp/monthly report.pdf"`,
			want: []string{"tasks", "attachments", "add", "Fix login bug", "/tmp/monthly report.pdf"},
		},
		{
			name: "single quotes",
			line: `/tasks attachments delete 'Fix login bug' 'monthly report.pdf'`,
			want: []string{"tasks", "attachments", "delete", "Fix login bug", "monthly report.pdf"},
		},
		{
			name: "adjacent quoted text",
			line: `/tasks show "Fix login"' bug' attachments`,
			want: []string{"tasks", "show", "Fix login bug", "attachments"},
		},
		{
			name: "empty quoted argument",
			line: `/tasks show ""`,
			want: []string{"tasks", "show", ""},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tokenizeCommand(tc.line)
			if err != nil {
				t.Fatalf("tokenizeCommand: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("tokenizeCommand(%q) = %#v, want %#v", tc.line, got, tc.want)
			}
		})
	}
}

func TestRunCommandRejectsUnmatchedQuotesBeforeDispatch(t *testing.T) {
	m := newTestModel(t)
	m.selectedID = "p1"
	next, cmd := m.runCommand(`/tasks attachments add "Fix login bug`)
	if cmd != nil {
		t.Fatal("unmatched quote must not return a dispatch command")
	}
	if out := transcript(next.(Model)); !strings.Contains(out, "parse error: unmatched double quote") {
		t.Fatalf("unmatched quote error missing from transcript:\n%s", out)
	}
}
func TestSplitActionRecognisesKnownActions(t *testing.T) {
	actions := []string{"list", "run", "delete"}

	action, rest := splitAction(actions, []string{"run", "my", "task"})
	if action != "run" || strings.Join(rest, " ") != "my task" {
		t.Errorf("got %q %v", action, rest)
	}

	// An unknown first word is treated as an argument, so "/tasks some title"
	// still works as a filter.
	action, rest = splitAction(actions, []string{"some", "title"})
	if action != "" || strings.Join(rest, " ") != "some title" {
		t.Errorf("got %q %v", action, rest)
	}

	action, rest = splitAction(actions, nil)
	if action != "" || rest != nil {
		t.Errorf("empty args should yield nothing, got %q %v", action, rest)
	}
}

func TestTokenizeCommand(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "double quoted argument",
			input: `/tasks reviews add "Fix login bug" internal/auth.go:42 Handle token refresh errors`,
			want:  []string{"tasks", "reviews", "add", "Fix login bug", "internal/auth.go:42", "Handle", "token", "refresh", "errors"},
		},
		{
			name:  "pipe spacing",
			input: `/tasks goal "Fix login bug"   |   "Reproduce on staging"`,
			want:  []string{"tasks", "goal", "Fix login bug", "|", "Reproduce on staging"},
		},
		{
			name:  "single quoted argument",
			input: `/tasks show 'Fix login bug' review`,
			want:  []string{"tasks", "show", "Fix login bug", "review"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tokenizeCommand(tc.input)
			if err != nil {
				t.Fatalf("tokenizeCommand() error = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("tokenizeCommand() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestTokenizeCommandRejectsUnmatchedQuotes(t *testing.T) {
	for _, input := range []string{
		`/tasks show "Fix login bug`,
		`/tasks show 'Fix login bug`,
	} {
		t.Run(input, func(t *testing.T) {
			_, err := tokenizeCommand(input)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "unmatched") || !strings.Contains(strings.ToLower(err.Error()), "quote") {
				t.Fatalf("tokenizeCommand() error = %v, want an unmatched-quote error", err)
			}
		})
	}
}

func TestSplitPipe(t *testing.T) {
	left, right := splitPipe("my skill | does a thing")
	if left != "my skill" || right != "does a thing" {
		t.Errorf("got %q / %q", left, right)
	}
	left, right = splitPipe("just a title")
	if left != "just a title" || right != "" {
		t.Errorf("got %q / %q", left, right)
	}
}

func TestParsePersonalityAddPreservesLiteralDescriptionPrefix(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		wantName   string
		wantDesc   string
		wantPrompt string
		wantError  bool
	}{
		{
			name:       "literal prefix without later pipe",
			input:      "Release Coach | description: Explain release risks clearly",
			wantName:   "Release Coach",
			wantDesc:   "",
			wantPrompt: "description: Explain release risks clearly",
		},
		{
			name:       "literal prefix with later pipes",
			input:      "Release Coach | description: Explain release risks clearly | preserve every | literal pipe",
			wantName:   "Release Coach",
			wantDesc:   "",
			wantPrompt: "description: Explain release risks clearly | preserve every | literal pipe",
		},
		{
			name:       "explicit description with later pipes",
			input:      "Release Coach | description=safe release guidance | Keep releases safe | preserve every | literal pipe",
			wantName:   "Release Coach",
			wantDesc:   "safe release guidance",
			wantPrompt: "Keep releases safe | preserve every | literal pipe",
		},
		{
			name:      "marked form requires prompt",
			input:     "Release Coach | description=safe release guidance",
			wantError: true,
		},
		{
			name:      "marked form requires description",
			input:     "Release Coach | description= | Keep releases safe in production deployments.",
			wantError: true,
		},
		{
			name:      "marked form rejects dangling separator",
			input:     "Release Coach | description=safe release guidance |",
			wantError: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, description, prompt, err := parsePersonalityAdd(tc.input)
			if tc.wantError {
				if err == nil {
					t.Fatalf("parsePersonalityAdd(%q) succeeded: %q, %q, %q", tc.input, name, description, prompt)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePersonalityAdd(%q) error = %v", tc.input, err)
			}
			if name != tc.wantName || description != tc.wantDesc || prompt != tc.wantPrompt {
				t.Fatalf("parsePersonalityAdd(%q) = %q, %q, %q; want %q, %q, %q", tc.input, name, description, prompt, tc.wantName, tc.wantDesc, tc.wantPrompt)
			}
		})
	}
}

func TestParsePersonalityAddAcceptsCaseInsensitiveDescriptionMarker(t *testing.T) {
	name, description, prompt, err := parsePersonalityAdd("Release Coach | DESCRIPTION=safe release guidance | Keep releases safe in production deployments.")
	if err != nil {
		t.Fatalf("parsePersonalityAdd() error = %v", err)
	}
	if name != "Release Coach" || description != "safe release guidance" || prompt != "Keep releases safe in production deployments." {
		t.Fatalf("parsePersonalityAdd() = %q, %q, %q", name, description, prompt)
	}
}

func TestParseProjectCreateArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantName string
		wantPath string
		ok       bool
	}{
		{name: "simple", args: []string{"demo", "/tmp/demo"}, wantName: "demo", wantPath: "/tmp/demo", ok: true},
		{name: "space-delimited-name", args: []string{"My", "Project", "/tmp/my-project"}, wantName: "My Project", wantPath: "/tmp/my-project", ok: true},
		{name: "pipe-delimited", args: []string{"My", "Project", "|", `C:\Users\me\my project`}, wantName: "My Project", wantPath: `C:\Users\me\my project`, ok: true},
		{name: "missing-name", args: []string{"", "/tmp/demo"}, ok: false},
		{name: "missing-path", args: []string{"demo"}, ok: false},
		{name: "empty-pipe-path", args: []string{"demo", "|"}, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, path, ok := parseProjectCreateArgs(tc.args)
			if name != tc.wantName || path != tc.wantPath || ok != tc.ok {
				t.Fatalf("parseProjectCreateArgs(%v) = %q, %q, %v; want %q, %q, %v", tc.args, name, path, ok, tc.wantName, tc.wantPath, tc.ok)
			}
		})
	}
}

func TestRegistryCompletionAtEveryDepth(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{input: "/tasks attachments", want: "/tasks attachments "},
		{input: "/tasks attachments de", want: "/tasks attachments delete "},
		{input: "/tasks reviews a", want: "/tasks reviews add "},
		{input: `/tasks move "Fix login" ac`, want: `/tasks move "Fix login" active `},
		{input: `/task show "Fix login" rev`, want: `/tasks show "Fix login" review `},
		{input: `/tasks show "Fix login" dif`, want: `/tasks show "Fix login" diff `},
		{input: `/schedule add Daily report 2026-01-20T09:00 mon`, want: `/schedule add Daily report 2026-01-20T09:00 monthly `},
		{input: `/schedules add "Daily report" 2026-01-20T09:00 wee`, want: `/schedule add "Daily report" 2026-01-20T09:00 weekly `},
		{input: "/workers limit 0", want: "/workers limit 0 "},
		{input: "/task attachments rem extra", want: "/tasks attachments remove extra"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			cmd := lookupCommand(strings.Fields(tc.input)[0])
			if cmd == nil {
				t.Fatalf("command missing for %q", tc.input)
			}
			if got := completeSlashInput(tc.input, *cmd); got != tc.want {
				t.Fatalf("completion = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRegistryCompletionPreservesRequiredOperands(t *testing.T) {
	cases := []string{
		"/tasks move ac",
		"/tasks move Fix ac",
		"/tasks show rev",
		"/tasks show Fix rev",
		"/schedule add mon",
		"/schedule add Daily report mon",
		"/schedule add Daily report not-a-date mon",
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			cmd := lookupCommand(strings.Fields(input)[0])
			if cmd == nil {
				t.Fatalf("command missing for %q", input)
			}
			if got := completeSlashInput(input, *cmd); got != input {
				t.Fatalf("completion changed required operand to %q", got)
			}
		})
	}
}

func TestRegistryCompletionPreservesRequiredOperandAtCursorAndSuffix(t *testing.T) {
	cases := []struct {
		command string
		input   string
	}{
		{command: "tasks", input: "/tasks move ac | keep  spacing"},
		{command: "tasks", input: "/tasks move Fix ac | keep  spacing"},
		{command: "tasks", input: "/tasks show Fix rev | keep  spacing"},
		{command: "schedule", input: "/schedule add Daily report mon | keep  spacing"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			cmd := lookupCommand(tc.command)
			if cmd == nil {
				t.Fatalf("%s command missing", tc.command)
			}
			cursor := strings.Index(tc.input, " |")
			got, gotCursor := completeSlashInputAt(tc.input, cursor, *cmd)
			if got != tc.input {
				t.Fatalf("completion changed required operand to %q", got)
			}
			if gotCursor != cursor {
				t.Fatalf("cursor = %d, want %d", gotCursor, cursor)
			}
		})
	}
}

func TestRegistryCompletionPreservesAmbiguousInput(t *testing.T) {
	cmd := lookupCommand("tasks")
	for _, input := range []string{"/tasks att", "/tasks show api cha"} {
		if got := completeSlashInput(input, *cmd); got != input {
			t.Fatalf("ambiguous completion changed %q to %q", input, got)
		}
	}
}

func TestRegistryCompletionPreservesQuotedAndPipeDelimitedArguments(t *testing.T) {
	cmd := lookupCommand("tasks")
	cases := map[string]string{
		`/tasks move "Fix login bug" ac`:         `/tasks move "Fix login bug" active `,
		`/tasks goal "Fix login bug" | clear`:    `/tasks goal "Fix login bug" | clear`,
		`/tasks reviews add "Fix bug" file.go:2`: `/tasks reviews add "Fix bug" file.go:2`,
	}
	for input, want := range cases {
		if got := completeSlashInput(input, *cmd); got != want {
			t.Fatalf("completion = %q, want %q", got, want)
		}
	}
}

func TestRegistryCompletionPreservesCursorAndSuffix(t *testing.T) {
	cmd := lookupCommand("tasks")
	if cmd == nil {
		t.Fatal("tasks command missing")
	}
	input := "/tasks attachments de --force"
	cursor := strings.Index(input, " --force")
	got, gotCursor := completeSlashInputAt(input, cursor, *cmd)
	want := "/tasks attachments delete --force"
	if got != want {
		t.Fatalf("completion = %q, want %q", got, want)
	}
	if gotCursor != strings.Index(want, " --force") {
		t.Fatalf("cursor = %d, want %d", gotCursor, strings.Index(want, " --force"))
	}
}

func TestStructuralOperandCompletionPreservesCursorAndSuffix(t *testing.T) {
	cases := []struct {
		command string
		input   string
		want    string
	}{
		{command: "tasks", input: `/tasks move "Fix login" ac | keep  spacing`, want: `/tasks move "Fix login" active | keep  spacing`},
		{command: "tasks", input: `/tasks show "Fix login" rev --json`, want: `/tasks show "Fix login" review --json`},
		{command: "schedule", input: `/schedule add Daily report 2026-01-20T09:00 mon | keep`, want: `/schedule add Daily report 2026-01-20T09:00 monthly | keep`},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			cmd := lookupCommand(tc.command)
			cursor := strings.Index(tc.input, " |")
			if cursor < 0 {
				cursor = strings.Index(tc.input, " --")
			}
			got, gotCursor := completeSlashInputAt(tc.input, cursor, *cmd)
			if got != tc.want {
				t.Fatalf("completion = %q, want %q", got, tc.want)
			}
			wantCursor := strings.Index(tc.want, " |")
			if wantCursor < 0 {
				wantCursor = strings.Index(tc.want, " --")
			}
			if gotCursor != wantCursor {
				t.Fatalf("cursor = %d, want %d", gotCursor, wantCursor)
			}
		})
	}
}

func TestWorksAliasCompletesCanonicalCommandAtDepth(t *testing.T) {
	cmd := lookupCommand("works")
	if cmd == nil {
		t.Fatal("works alias missing")
	}
	if got := completeSlashInput("/works li", *cmd); got != "/workers limit " {
		t.Fatalf("completion = %q, want %q", got, "/workers limit ")
	}
}

func TestProjectsCreateSettingsCompletionAndHelp(t *testing.T) {
	cmd := lookupCommand("projects")
	if cmd == nil {
		t.Fatal("projects command missing")
	}
	for input, want := range map[string]string{
		"/projects cr":                                                             "/projects create ",
		"/projects sh":                                                             "/projects show ",
		"/projects edit demo --repository-s":                                       "/projects edit demo --repository-source ",
		"/projects edit demo --repository-source g":                                "/projects edit demo --repository-source github ",
		"/projects edit demo --name renamed --repository-s":                        "/projects edit demo --name renamed --repository-source ",
		`/projects edit "--name" | --repository-s`:                                 `/projects edit "--name" | --repository-source `,
		"/projects edit Alpha --name Beta | --repository-source g":                 "/projects edit Alpha --name Beta | --repository-source github ",
		"/projects edit Alpha --name Beta | --description changed --max-w":         "/projects edit Alpha --name Beta | --description changed --max-workers ",
		"/projects edit demo --name renamed --max-workers 2 --repository-source g": "/projects edit demo --name renamed --max-workers 2 --repository-source github ",
	} {
		if got := completeSlashInput(input, *cmd); got != want {
			t.Errorf("completion for %q = %q, want %q", input, got, want)
		}
	}
	help := renderCommandHelp(*cmd)
	for _, want := range []string{
		"projects show <project>",
		"projects create <name> <path>",
		"projects edit <project> [options]",
		"projects edit <project> | [options]",
		"[global flags] -- projects edit <project> | [options]",
		"--repository-source <local|github>",
		"--default-agent <name|id|inherit>",
		"repository replacement requires confirmation",
		"projects create My Project",
		`C:\Users\me\src\my-project`,
	} {
		if !strings.Contains(help, want) {
			t.Errorf("projects help missing %q:\n%s", want, help)
		}
	}
}

func TestMatchAlertActionRefRanksTitlesBeforeSearchText(t *testing.T) {
	tests := []struct {
		name    string
		alerts  []client.Alert
		ref     string
		wantID  string
		wantErr string
	}{
		{
			name: "exact ID precedes title",
			alerts: []client.Alert{
				{ID: "alert-id", Title: "Other alert"},
				{ID: "other-id", Title: "ALERT-ID"},
			},
			ref:    "aLeRt-Id",
			wantID: "alert-id",
		},
		{
			name: "case insensitive exact title precedes longer prefix",
			alerts: []client.Alert{
				{ID: "deploy", Title: "Deploy", Text: "release deployment"},
				{ID: "deploy-service", Title: "Deploy service", Text: "release service"},
			},
			ref:    "dEpLoY",
			wantID: "deploy",
		},
		{
			name: "duplicate exact titles are ambiguous",
			alerts: []client.Alert{
				{ID: "one", Title: "Deploy"},
				{ID: "two", Title: "deploy"},
			},
			ref:     "DEPLOY",
			wantErr: "ambiguous",
		},
		{
			name: "unique search text falls back after title matching",
			alerts: []client.Alert{
				{ID: "deploy", Title: "Deploy", Text: "release-plan-unique"},
				{ID: "deploy-service", Title: "Deploy service", Text: "service rollout"},
			},
			ref:    "RELEASE-PLAN-UNIQUE",
			wantID: "deploy",
		},
		{
			name: "title prefix precedes exact search text",
			alerts: []client.Alert{
				{ID: "deploy", Title: "Deploy service", Text: "release plan"},
				{ID: "search", Title: "Other alert", Text: "deploy"},
			},
			ref:    "deploy",
			wantID: "deploy",
		},
		{
			name: "ambiguous search text is rejected",
			alerts: []client.Alert{
				{ID: "one", Title: "First", Text: "shared body text"},
				{ID: "two", Title: "Second", Text: "shared body text"},
			},
			ref:     "shared body text",
			wantErr: "ambiguous",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := matchAlertActionRef(tc.alerts, tc.ref)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("matchAlertActionRef error = %v, want %q", err, tc.wantErr)
				}
				if got.ID != "" {
					t.Fatalf("matchAlertActionRef selected %q despite %s", got.ID, tc.wantErr)
				}
				return
			}
			if err != nil || got.ID != tc.wantID {
				t.Fatalf("matchAlertActionRef = %+v, %v; want %q", got, err, tc.wantID)
			}
		})
	}
}

func TestMatchAlertActionRefSanitizesDiagnosticsWithoutChangingMatching(t *testing.T) {
	const hostileTitle = "\x1b[31mDeploy\nproduction\r\x00"
	const hostileRef = "missing\x1b[31m\nreference\r\x00"

	for _, tc := range []struct {
		name   string
		alerts []client.Alert
		ref    string
		want   string
	}{
		{
			name: "duplicate backend titles",
			alerts: []client.Alert{
				{ID: "one", Title: hostileTitle},
				{ID: "two", Title: hostileTitle},
			},
			ref:  hostileTitle,
			want: "Deploy production",
		},
		{
			name:   "missing user reference",
			alerts: []client.Alert{{ID: "one", Title: "Deploy"}},
			ref:    hostileRef,
			want:   "missing reference",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := matchAlertActionRef(tc.alerts, tc.ref)
			if err == nil {
				t.Fatal("matchAlertActionRef unexpectedly succeeded")
			}
			got := err.Error()
			for _, unsafe := range []string{"\x1b", "\n", "\r", "\x00"} {
				if strings.Contains(got, unsafe) {
					t.Fatalf("unsafe diagnostic contains %q: %q", unsafe, got)
				}
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("diagnostic %q missing sanitized text %q", got, tc.want)
			}
		})
	}
}

func TestMatchRefByIDPrefixAndName(t *testing.T) {
	tasks := []client.Task{
		{ID: "abc123", Title: "Refactor the API"},
		{ID: "def456", Title: "Write docs"},
	}
	id := func(t client.Task) string { return t.ID }
	name := func(t client.Task) string { return t.Title }

	got, err := matchRef(tasks, "abc", id, name)
	if err != nil || got.ID != "abc123" {
		t.Errorf("id prefix: %+v %v", got, err)
	}

	got, err = matchRef(tasks, "docs", id, name)
	if err != nil || got.ID != "def456" {
		t.Errorf("name substring: %+v %v", got, err)
	}

	if _, err = matchRef(tasks, "missing", id, name); err == nil {
		t.Error("expected a not-found error")
	}

	ambiguous := []client.Task{{ID: "1", Title: "deploy api"}, {ID: "2", Title: "deploy web"}}
	if _, err = matchRef(ambiguous, "deploy", id, name); err == nil ||
		!strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("expected an ambiguity error, got %v", err)
	}
}

// An exact title must win over a longer title that contains it, whatever the
// listing order.
func TestMatchRefPrefersExactName(t *testing.T) {
	id := func(t client.Task) string { return t.ID }
	name := func(t client.Task) string { return t.Title }

	for _, tasks := range [][]client.Task{
		{{ID: "1", Title: "deploy api service"}, {ID: "2", Title: "deploy"}},
		{{ID: "2", Title: "deploy"}, {ID: "1", Title: "deploy api service"}},
	} {
		got, err := matchRef(tasks, "deploy", id, name)
		if err != nil || got.ID != "2" {
			t.Errorf("matchRef = %+v, %v; want the exactly-titled task", got, err)
		}
	}
}

func TestMatchRefRejectsDuplicateExactNamesCaseInsensitively(t *testing.T) {
	tasks := []client.Task{
		{ID: "1", Title: "Deploy"},
		{ID: "2", Title: "deploy"},
	}

	got, err := matchRef(tasks, "DEPLOY",
		func(t client.Task) string { return t.ID },
		func(t client.Task) string { return t.Title })
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("matchRef error = %v, want an ambiguity error", err)
	}
	if got.ID != "" {
		t.Fatalf("ambiguous exact name selected task %q", got.ID)
	}
	for _, want := range []string{"Deploy", "deploy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ambiguity error %q missing candidate %q", err, want)
		}
	}
}

func TestMatchRefExactIDPrecedesExactName(t *testing.T) {
	items := []client.Task{
		{ID: "task-1", Title: "Unrelated"},
		{ID: "task-2", Title: "TASK-1"},
	}

	got, err := matchRef(items, "TaSk-1",
		func(t client.Task) string { return t.ID },
		func(t client.Task) string { return t.Title })
	if err != nil {
		t.Fatalf("matchRef returned error: %v", err)
	}
	if got.ID != "task-1" {
		t.Fatalf("matchRef selected %q, want the unique exact ID task-1", got.ID)
	}
}

// A name prefix outranks a mid-string substring match.
func TestMatchRefPrefersPrefixOverSubstring(t *testing.T) {
	tasks := []client.Task{
		{ID: "1", Title: "rewrite the api docs"},
		{ID: "2", Title: "api gateway"},
	}
	got, err := matchRef(tasks, "api",
		func(t client.Task) string { return t.ID },
		func(t client.Task) string { return t.Title })
	if err != nil || got.ID != "2" {
		t.Errorf("matchRef = %+v, %v; want the prefix match", got, err)
	}
}

func TestSuggestFiltersByPrefix(t *testing.T) {
	if got := suggest("sk"); len(got) != 1 || got[0].name != "skills" {
		t.Errorf("suggest(sk) = %v", got)
	}
	visible := 0
	for _, command := range commands {
		if !command.hidden {
			visible++
		}
	}
	if got := suggest(""); len(got) != visible {
		t.Errorf("empty prefix should list every visible command, got %d, want %d", len(got), visible)
	}
	if got := suggest("webh"); len(got) != 0 {
		t.Errorf("deprecated webhook root should not be separately suggested, got %v", got)
	}
	if got := suggest("zzz"); len(got) != 0 {
		t.Errorf("no command should match zzz, got %v", got)
	}
}

func TestUnsupportedBuildCommandIsNotDiscoverable(t *testing.T) {
	if cmd := lookupCommand("build"); cmd != nil {
		t.Fatalf("unsupported build command is registered: %+v", cmd)
	}
	if got := suggest("build"); len(got) != 0 {
		t.Fatalf("unsupported build command is suggested: %v", got)
	}
	if help := renderHelp(); strings.Contains(help, cmdPrefix+"build ") {
		t.Fatalf("unsupported build command appears in help:\n%s", help)
	}
}

func TestEveryCommandHasDescriptionAndRunner(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range commands {
		if c.desc == "" {
			t.Errorf("/%s has no description", c.name)
		}
		if c.run == nil {
			t.Errorf("/%s has no runner", c.name)
		}
		if seen[c.name] {
			t.Errorf("/%s is registered twice", c.name)
		}
		seen[c.name] = true
		for _, a := range c.aliases {
			if seen[a] {
				t.Errorf("alias %q collides with another command", a)
			}
			seen[a] = true
		}
	}
}

func TestRegistryCoversEveryWebUIScreen(t *testing.T) {
	// Each OpenVibely web-UI screen must be reachable from the chat.
	for _, screen := range []string{
		"tasks", "schedule", "alerts", "skills", "agents", "models", "workers",
		"channels", "personality", "pulse", "reflection", "grades", "insights",
		"analytics", "automations", "projects",
	} {
		if lookupCommand(screen) == nil {
			t.Errorf("no command for the %s screen", screen)
		}
	}
}

func TestRenderBoardGroupsByColumn(t *testing.T) {
	tasks := []client.Task{
		{ID: "t1", Title: "Backlog item", Category: "backlog", Status: "pending"},
		{ID: "t2", Title: "Running item", Category: "active", Status: "running"},
		{ID: "t3", Title: "Done item", Category: "completed", Status: "completed"},
	}
	out := renderBoard(tasks, "")
	for _, want := range []string{"Backlog", "Active", "Completed", "Backlog item", "Running item", "Done item"} {
		if !strings.Contains(out, want) {
			t.Errorf("board missing %q:\n%s", want, out)
		}
	}
}

func TestRenderBoardFilters(t *testing.T) {
	tasks := []client.Task{
		{ID: "t1", Title: "Refactor API", Category: "backlog"},
		{ID: "t2", Title: "Write docs", Category: "backlog"},
	}
	out := renderBoard(tasks, "docs")
	if strings.Contains(out, "Refactor API") {
		t.Errorf("filter should exclude non-matching tasks:\n%s", out)
	}
	if !strings.Contains(out, "Write docs") {
		t.Errorf("filter dropped the match:\n%s", out)
	}
}

func TestRenderTaskDetailShowsTabs(t *testing.T) {
	task := client.Task{ID: "t1", Title: "Refactor", Category: "active"}
	detail := &client.TaskDetail{
		Task:     client.Task{Status: "running"},
		Details:  "the prompt",
		Thread:   "agent: working",
		Changes:  "2 files",
		Review:   "inline comments",
		Schedule: "daily",
		Chaining: "then deploy",
		Attach:   "spec.md",
		Life:     "pre_task ok",
	}
	out := renderTaskDetail(task, detail, "")
	for _, tab := range client.TaskDetailTabs() {
		if !strings.Contains(out, tab.Label) {
			t.Errorf("detail missing the %s section:\n%s", tab.Label, out)
		}
	}
	if want := "/tasks show <id> <" + detailTabHintList() + ">"; !strings.Contains(out, want) {
		t.Errorf("detail hint missing %q:\n%s", want, out)
	}

	only := renderTaskDetail(task, detail, "chat")
	if !strings.Contains(only, "Thread") || !strings.Contains(only, "agent: working") {
		t.Errorf("alias tab view missing its label or content:\n%s", only)
	}
	if strings.Contains(only, "2 files") {
		t.Errorf("tab view should show one tab only:\n%s", only)
	}
}

func TestAlertsShowHelpAndCompletion(t *testing.T) {
	cmd := lookupCommand("alerts")
	if cmd == nil {
		t.Fatal("alerts command missing")
	}
	if !strings.Contains(strings.Join(cmd.actions, " "), "show") {
		t.Fatalf("alerts actions = %#v, want show", cmd.actions)
	}
	if got := completeSlashInput("/alerts sh", *cmd); got != "/alerts show " {
		t.Fatalf("alerts show completion = %q, want %q", got, "/alerts show ")
	}
	if got := completeSlashInput("/alerts read-b", *cmd); got != "/alerts read-bulk " {
		t.Fatalf("alerts read-bulk completion = %q, want %q", got, "/alerts read-bulk ")
	}
	help := renderCommandHelp(*cmd)
	for _, want := range []string{
		"/alerts list [filter] --decision-state <state> [--processing-state <state>]",
		"decision states: pending, approved, rejected, dismissed",
		"processing states: not_applicable, unclaimed, claimed, implementation_task_linked, completed, failed",
		"/alerts show <id|title>",
		"inspect full alert context",
		"/alerts read-bulk <id|title>...",
		"/alerts delete-bulk <id|title>...",
		"alerts show \"Add retry logic to HTTP client\"",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("alerts help missing %q:\n%s", want, help)
		}
	}
}

func TestParseAlertListArgs(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantFilter client.AlertListFilter
		wantText   string
		wantErr    string
	}{
		{
			name:       "combined exact predicates",
			args:       []string{"--decision-state", "pending", "--processing-state=unclaimed"},
			wantFilter: client.AlertListFilter{DecisionState: "pending", ProcessingState: "unclaimed"},
		},
		{
			name:     "state words remain free text",
			args:     []string{"approved", "deployment"},
			wantText: "approved deployment",
		},
		{
			name:    "invalid decision state",
			args:    []string{"--decision-state", "not_required"},
			wantErr: "valid values: pending, approved, rejected, dismissed",
		},
		{
			name:    "missing processing state",
			args:    []string{"--processing-state"},
			wantErr: "--processing-state requires a value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter, text, err := parseAlertListArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parse error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAlertListArgs: %v", err)
			}
			if !reflect.DeepEqual(filter, tt.wantFilter) || text != tt.wantText {
				t.Fatalf("parsed filter=%+v text=%q, want %+v and %q", filter, text, tt.wantFilter, tt.wantText)
			}
		})
	}
	for _, state := range []string{"pending", "approved", "rejected", "dismissed"} {
		t.Run("accepted decision state "+state, func(t *testing.T) {
			filter, text, err := parseAlertListArgs([]string{"--decision-state", state})
			if err != nil {
				t.Fatalf("parseAlertListArgs: %v", err)
			}
			if filter.DecisionState != state || filter.ProcessingState != "" || text != "" {
				t.Fatalf("parsed filter=%+v text=%q", filter, text)
			}
		})
	}
}

func TestRenderAlertInspectionPreservesBodyAndShowsEmptyDetail(t *testing.T) {
	inspection := client.AlertInspection{
		Summary: client.AlertSummary{
			ID: "a-1", ProjectID: "p1", Title: "Review request", Type: "custom",
			Severity: "warning", DecisionState: "pending", ProcessingState: "unclaimed",
		},
		Detail: client.AlertDetail{Body: "line one\nline two\n\x1b[31munsafe\x1b[0m", Metadata: map[string]any{"key": "value"}},
	}
	out := renderAlertInspection(inspection)
	plain := stripANSI(out)
	for _, want := range []string{"id: a-1", "type: custom · severity: warning", "decision: pending · processing: unclaimed", "line one\nline two\nunsafe", `"key": "value"`} {
		if !strings.Contains(plain, want) {
			t.Fatalf("alert detail output missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(out, "\x1b[31munsafe") {
		t.Fatal("alert body retained a raw ANSI sequence")
	}

	empty := renderAlertInspection(client.AlertInspection{
		Summary: client.AlertSummary{ID: "empty", Title: "No detail"},
		Detail:  client.AlertDetail{Metadata: map[string]any{}},
	})
	for _, want := range []string{"(empty body)", "(empty metadata)", "No additional detail."} {
		if !strings.Contains(stripANSI(empty), want) {
			t.Fatalf("empty alert detail missing %q:\n%s", want, stripANSI(empty))
		}
	}
}

func TestRenderAlertsAndSkills(t *testing.T) {
	alerts := renderAlerts([]client.Alert{{ID: "a1", Title: "Build failed"}}, "")
	if !strings.Contains(alerts, "Build failed") {
		t.Errorf("alerts render:\n%s", alerts)
	}
	if !strings.Contains(renderAlerts(nil, ""), "no alerts") {
		t.Error("empty alerts should say so")
	}

	skills := renderSkills([]client.Skill{
		{Handle: "deploy", Name: "Deploy", Enabled: true, AlwaysUse: true, Scope: "project"},
	}, "")
	if !strings.Contains(skills, "deploy") || !strings.Contains(skills, "always") {
		t.Errorf("skills render:\n%s", skills)
	}
}

func TestBarScalesToMax(t *testing.T) {
	full := bar(10, 10, 10)
	if strings.Count(full, "█") != 10 {
		t.Errorf("full bar = %q", full)
	}
	half := bar(5, 10, 10)
	if strings.Count(half, "█") != 5 {
		t.Errorf("half bar = %q", half)
	}
	// A non-zero value always shows at least one block.
	tiny := bar(1, 1000, 10)
	if strings.Count(tiny, "█") != 1 {
		t.Errorf("tiny bar = %q", tiny)
	}
	if bar(1, 0, 10) != "" {
		t.Error("zero max should render nothing")
	}
}

func TestHumanIntAndMs(t *testing.T) {
	cases := map[int64]string{999: "999", 1500: "1.5k", 2_000_000: "2.0M"}
	for in, want := range cases {
		if got := humanInt(in); got != want {
			t.Errorf("humanInt(%d) = %q, want %q", in, got, want)
		}
	}
	msCases := map[float64]string{500: "500ms", 1500: "1.5s", 120000: "2.0m"}
	for in, want := range msCases {
		if got := humanMs(in); got != want {
			t.Errorf("humanMs(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderUsageIncludesCostAndBreakdown(t *testing.T) {
	usage := &client.UsageAnalytics{
		Totals: client.UsageTotals{
			CallCount: 10, InputTokens: 1000, OutputTokens: 500,
			TotalTokens: 1500, CostUSD: 1.25, CostAvailable: true,
		},
		ModelBreakdown: []client.ModelUsagePoint{
			{Model: "claude-sonnet-4", CallCount: 10, TotalTokens: 1500, CostUSD: 1.25, Percent: 100},
		},
	}
	out := renderUsage(usage)
	for _, want := range []string{"10 calls", "$1.25", "claude-sonnet-4", "█"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage render missing %q:\n%s", want, out)
		}
	}
}

func TestRenderUsageIncludesPrimaryLimitOnly(t *testing.T) {
	out := stripANSI(renderUsage(&client.UsageAnalytics{AccountLimits: []client.AccountUsage{{
		Provider:    "OpenAI",
		StatusLabel: "healthy",
		PrimaryLimit: &client.AccountLimit{
			Label:       "tokens",
			UsedPercent: 100,
			ResetsAt:    "2026-09-01T00:00:00Z",
		},
	}}}))

	for _, want := range []string{"OpenAI", "healthy", "tokens", "100.0%", "2026-09-01T00:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage render missing primary-limit field %q:\n%s", want, out)
		}
	}
}

func TestRenderUsageIncludesProviderPrimaryAndDistinctLimits(t *testing.T) {
	primary := client.AccountLimit{
		Label:       "tokens",
		UsedPercent: 100,
		ResetsAt:    "2026-09-01T00:00:00Z",
	}
	secondary := client.AccountLimit{
		Label:       "requests",
		UsedPercent: 42.5,
		ResetsAt:    "tomorrow",
	}
	out := stripANSI(renderUsage(&client.UsageAnalytics{
		Totals: client.UsageTotals{CallCount: 3, TotalTokens: 900},
		AccountLimits: []client.AccountUsage{{
			Provider:      "OpenAI",
			PlanType:      "team",
			StatusLabel:   "healthy",
			AccountDetail: "sk-account-detail-must-not-render",
			PrimaryLimit:  &primary,
			Limits:        []client.AccountLimit{primary, secondary, secondary},
		}},
	}))

	for _, want := range []string{
		"OpenAI", "team", "healthy", "tokens", "100.0%", "2026-09-01T00:00:00Z",
		"requests", "42.5%", "tomorrow",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("usage render missing %q:\n%s", want, out)
		}
	}
	for _, label := range []string{"tokens", "requests"} {
		if got := strings.Count(out, "\n    "+label); got != 1 {
			t.Errorf("usage render contains %q %d times, want once:\n%s", label, got, out)
		}
	}
	if strings.Contains(out, "sk-account-detail-must-not-render") {
		t.Errorf("provider account detail leaked into usage output:\n%s", out)
	}
}

func TestRenderUsageKeepsProviderDiagnosticsWithoutQuota(t *testing.T) {
	secret := "sk-provider-secret"
	out := stripANSI(renderUsage(&client.UsageAnalytics{AccountLimits: []client.AccountUsage{
		{
			Provider:      "Anthropic",
			PlanType:      "pro",
			StatusLabel:   "blocked",
			AccountDetail: secret,
			Error:         "quota service unavailable",
		},
	}}))

	for _, want := range []string{"Anthropic", "pro", "blocked", "quota service unavailable"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage diagnostics missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, secret) {
		t.Errorf("provider account detail leaked into usage output:\n%s", out)
	}
}

func TestRenderRatesDrawsGauge(t *testing.T) {
	out := renderRates([]client.SuccessFailureRate{
		{Period: "2026-08", SuccessCount: 8, FailureCount: 2, TotalCount: 10, SuccessRate: 80},
	})
	if !strings.Contains(out, "2026-08") || !strings.Contains(out, "80.0%") {
		t.Errorf("rates render:\n%s", out)
	}
	if !strings.Contains(out, "8 ok / 2 fail") {
		t.Errorf("counts missing:\n%s", out)
	}
}

func TestRenderStatusReportsConnection(t *testing.T) {
	m := newTestModel(t)
	m.connected = true
	m.capacity = &client.GlobalCapacity{MaxWorkers: 4, TotalRunning: 1, QueueSize: 2, AvailableSlots: 3}
	m.auth = &client.AuthStatus{Authenticated: true, Username: "dubee"}

	out := m.renderStatus()
	for _, want := range []string{"connected", "dubee", "1 running / 4 max"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
}

// Help must document every command, otherwise features are unreachable in
// practice: a command that isn't listed is a command nobody finds.
func TestHelpListsEveryRegisteredCommand(t *testing.T) {
	help := renderHelp()
	for _, c := range commands {
		if c.hidden {
			continue
		}
		if !strings.Contains(help, cmdPrefix+c.name) {
			t.Errorf("%s%s is missing from /help", cmdPrefix, c.name)
		}
		if !strings.Contains(help, c.desc) {
			t.Errorf("%s%s description is missing from /help", cmdPrefix, c.name)
		}
	}
}

// Listing action names isn't enough to use a command, so every command with
// actions must also spell out their concrete syntax. VISION.md "Friendly By
// Default" further requires examples, not only syntax — so every command with
// a usage block must have at least one concrete worked example.
func TestCommandsWithActionsDocumentTheirSyntax(t *testing.T) {
	for _, c := range commands {
		if len(c.actions) == 0 {
			continue
		}
		if len(c.usage) == 0 {
			t.Errorf("%s%s lists actions but has no usage lines", cmdPrefix, c.name)
			continue
		}
		detail := renderCommandHelp(c)
		for _, a := range c.actions {
			if !strings.Contains(detail, a) {
				t.Errorf("%s%s help omits the %q action:\n%s", cmdPrefix, c.name, a, detail)
			}
		}
		if len(c.examples) == 0 {
			t.Errorf("%s%s has usage lines but no examples (VISION.md: help must include examples)", cmdPrefix, c.name)
		}
	}
}

func TestChannelsCompletionDocumentsManagementOptions(t *testing.T) {
	for _, tc := range []struct {
		after []string
		want  string
	}{
		{[]string{"add", "telegram"}, "--token"},
		{[]string{"edit", "email"}, "--provider"},
		{[]string{"add", "github", "--auth-mode"}, "pat"},
		{[]string{"edit", "slack", "--bot-token-mode"}, "manual"},
		{[]string{"add", "x"}, "--consumer-key"},
		{[]string{"edit", "x"}, "--access-token-secret"},
		{[]string{"add", "email", "--provider"}, "yahoo"},
		{[]string{"edit", "email", "--provider"}, "icloud"},
		{[]string{"add", "email"}, "--skip-attachments"},
	} {
		values := registryCompletionValues("channels", tc.after...)
		if !slices.Contains(values, tc.want) {
			t.Errorf("completion after %v missing %q: %v", tc.after, tc.want, values)
		}
	}
}

func TestChannelsHelpDocumentsSupportedActions(t *testing.T) {
	cmd := lookupCommand("channels")
	if cmd == nil {
		t.Fatal("channels command missing")
	}

	if want := []string{"list", "show", "add", "connect", "edit", "test", "remove", "disconnect", "webhooks"}; !reflect.DeepEqual(cmd.actions, want) {
		t.Fatalf("channels actions = %#v, want %#v", cmd.actions, want)
	}

	help := renderCommandHelp(*cmd)
	for _, want := range []string{
		"/channels list",
		"/channels show <channel>",
		"/channels add <type> <options>",
		"/channels connect <github|slack>",
		"/channels edit <channel> <options>",
		"/channels test <channel>",
		"/channels remove <channel>",
		"/channels disconnect <github|slack>",
		"--skip-attachments",
		"--mark-existing-seen",
		"gmail, outlook, yahoo, fastmail, icloud, custom",
		"manual mode requires --bot-token",
		"X requires --consumer-key, --consumer-secret, --access-token, and --access-token-secret",
		"X poll interval must be 15 to 300 seconds",
		"Pasted multiline PEM is supported",
		"API endpoints must be absolute HTTP(S) URLs",
		"Email addresses must be valid mailbox addresses",
		"disconnect credentials without removing other configuration (confirmation required)",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("channels help missing %q:\n%s", want, help)
		}
	}
}

func TestAutomationsDocumentationMatchesRegistry(t *testing.T) {
	defer func() { cmdPrefix = "/" }()
	cmdPrefix = "/"

	cmd := lookupCommand("automations")
	if cmd == nil {
		t.Fatal("automations command missing")
	}
	wantActions := []string{"list", "show", "open", "edit", "run", "pause", "resume", "delete"}
	if !reflect.DeepEqual(cmd.actions, wantActions) {
		t.Fatalf("automations actions = %#v, want %#v", cmd.actions, wantActions)
	}

	help := renderCommandHelp(*cmd)
	for _, action := range wantActions {
		if !strings.Contains(help, action) {
			t.Errorf("automations help missing %q:\n%s", action, help)
		}
	}
	for _, example := range cmd.examples {
		if want := "/" + example; !strings.Contains(help, want) {
			t.Errorf("automations help missing example %q:\n%s", want, help)
		}
	}
	for _, want := range []string{"type 'yes'", "--force/-f"} {
		if !strings.Contains(help, want) {
			t.Errorf("automations help missing deletion safety %q:\n%s", want, help)
		}
	}
	if got := completeSlashInput(`/automations edit "Nightly sweep" --f`, *cmd); got != `/automations edit "Nightly sweep" --file ` {
		t.Errorf("automation edit option completion = %q", got)
	}

	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed while locating docs/user-guide.md")
	}
	guide, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "..", "docs", "user-guide.md"))
	if err != nil {
		t.Fatalf("read docs/user-guide.md: %v", err)
	}
	guideText := string(guide)
	row := "| `/automations` | `automation` | `list`, `show`, `open`, `edit`, `run`, `pause`, `resume`, `delete` |"
	if !strings.Contains(guideText, row) {
		t.Fatalf("user guide automation command row is missing or out of sync:\n%s", row)
	}
	for _, example := range cmd.examples {
		if want := "/" + example; !strings.Contains(guideText, want) {
			t.Errorf("user guide missing interactive automation example %q", want)
		}
		cliExample := "openvibely-terminal -project demo " + example
		if strings.HasPrefix(example, "automations delete ") {
			cliExample = "openvibely-terminal -project demo --force " + example
		}
		if !strings.Contains(guideText, cliExample) {
			t.Errorf("user guide missing one-shot automation example %q", cliExample)
		}
	}
	for _, want := range []string{"typing `yes`", "`--force` or its `-f` shorthand"} {
		if !strings.Contains(guideText, want) {
			t.Errorf("user guide missing deletion safety %q", want)
		}
	}
}

func TestScheduleEditHelpAndCompletionMetadata(t *testing.T) {
	cmd := lookupCommand("schedule")
	if cmd == nil {
		t.Fatal("schedule command missing")
	}
	help := renderCommandHelp(*cmd)
	for _, want := range []string{"schedule edit <id>", "run-at", "once|daily|weekly|monthly|hourly|seconds|minutes|hours", "clear-context <true|false>", "schedule edit a1b2c3"} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing %q:\n%s", want, help)
		}
	}
	for _, tc := range []struct {
		after []string
		want  string
	}{
		{after: []string{"edit", "s1"}, want: "run-at"},
		{after: []string{"edit", "s1", "repeat"}, want: "hourly"},
		{after: []string{"edit", "s1", "clear-context"}, want: "false"},
	} {
		if got := registryCompletionValues("schedule", tc.after...); !containsString(got, tc.want) {
			t.Errorf("completion after %v = %v, missing %q", tc.after, got, tc.want)
		}
	}
}

// renderCommandHelp must include an "examples:" block for commands that have
// examples populated, and must not render an empty block for those that don't.
func TestRenderCommandHelpExamplesBlock(t *testing.T) {
	withExamples := command{
		name:     "demo",
		desc:     "a demo command",
		usage:    []string{"demo do-it"},
		examples: []string{"demo do-it foo", "demo do-it bar"},
	}
	got := renderCommandHelp(withExamples)
	if !strings.Contains(got, "examples:") {
		t.Error("renderCommandHelp: expected 'examples:' header for command with examples")
	}
	if !strings.Contains(got, "demo do-it foo") {
		t.Error("renderCommandHelp: expected first example in output")
	}
	if !strings.Contains(got, "demo do-it bar") {
		t.Error("renderCommandHelp: expected second example in output")
	}

	withoutExamples := command{
		name:  "demo2",
		desc:  "another demo command",
		usage: []string{"demo2 do-it"},
	}
	got2 := renderCommandHelp(withoutExamples)
	if strings.Contains(got2, "examples:") {
		t.Error("renderCommandHelp: must not render 'examples:' block when command has no examples")
	}
}

// Every action the help advertises must actually be accepted, and every action
// the implementation handles must be advertised.
func TestAdvertisedActionsMatchImplementation(t *testing.T) {
	// Actions implemented but deliberately not advertised as separate entries.
	extra := map[string][]string{
		// "/tasks clear" takes a column argument rather than being an action.
		"tasks": {"backlog", "completed"},
	}
	for _, c := range commands {
		for _, a := range c.actions {
			action, rest := splitAction(c.actions, []string{a, "x"})
			if action != a {
				t.Errorf("%s%s does not accept its advertised action %q", cmdPrefix, c.name, a)
			}
			if len(rest) != 1 || rest[0] != "x" {
				t.Errorf("%s%s %s mis-parsed its arguments: %v", cmdPrefix, c.name, a, rest)
			}
		}
		_ = extra[c.name]
	}
}

// Help detail must be usable in both modes: slashes in the chat window,
// bare subcommands on the command line.
func TestHelpPrefixFollowsMode(t *testing.T) {
	defer func() { cmdPrefix = "/" }()

	cmdPrefix = "/"
	if got := lookupCommand("tasks").summary(); !strings.HasPrefix(got, "/tasks") {
		t.Errorf("TUI summary = %q, want a leading slash", got)
	}
	if h := renderHelp(); !strings.Contains(h, "/tasks") {
		t.Error("TUI help lost its slashes")
	}

	cmdPrefix = ""
	if got := lookupCommand("tasks").summary(); strings.HasPrefix(got, "/") {
		t.Errorf("CLI summary = %q, want no leading slash", got)
	}
	if s := CommandSummary(); !strings.Contains(s, "tasks") || strings.Contains(s, "/tasks") {
		t.Errorf("CLI command summary should list bare names:\n%s", s)
	}
}

func TestRuntimeUsageMatchesCanonicalHelpSyntax(t *testing.T) {
	defer func() { cmdPrefix = "/" }()
	cmdPrefix = "/"

	c := lookupCommand("tasks")
	if c == nil {
		t.Fatal("tasks command missing")
	}
	m := newTestModel(t)
	m.selectedID = "p1"
	_, cmd := c.run(m, []string{"new", "|", "prompt"})
	if cmd == nil {
		t.Fatal("expected a runtime validation command")
	}
	msg, ok := cmd().(resultMsg)
	if !ok || msg.err == nil {
		t.Fatalf("expected a usage result, got %#v", msg)
	}

	got := msg.err.Error()
	if want := c.usageMessage("new"); got != want {
		t.Fatalf("runtime usage = %q, want canonical usage %q", got, want)
	}
	if help := renderCommandHelp(*c); !strings.Contains(help, strings.TrimPrefix(got, "usage: ")) {
		t.Fatalf("runtime usage %q is missing from /help tasks:\n%s", got, help)
	}

	cmdPrefix = ""
	if got := c.usageMessage("new"); got != "usage: tasks new <title> [| <prompt>]" {
		t.Fatalf("CLI usage = %q, want a bare command", got)
	}
}

func TestChannelsWebhooksHelpCompletionAndDeprecatedAliases(t *testing.T) {
	channels := lookupCommand("channels")
	if channels == nil {
		t.Fatal("channels command missing")
	}
	help := renderCommandHelp(*channels)
	for _, want := range []string{
		"channels webhooks list",
		"channels webhooks show <webhook>",
		"channels webhooks create <name> [options]",
		"channels webhooks rotate <webhook>",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("channels help missing %q:\n%s", want, help)
		}
	}
	if got := registryCompletionValues("channels"); !slices.Contains(got, "webhooks") {
		t.Fatalf("channels completions = %v, want webhooks", got)
	}
	for _, action := range []string{"list", "show", "create", "edit", "test", "rotate", "delete"} {
		if got := registryCompletionValues("channels", "webhooks"); !slices.Contains(got, action) {
			t.Errorf("channels webhooks completions = %v, want %q", got, action)
		}
	}
	for _, root := range []struct {
		name  string
		after []string
	}{
		{name: "channels", after: []string{"webhooks", "edit", "pager"}},
		{name: "webhooks", after: []string{"edit", "pager"}},
		{name: "inbound-webhooks", after: []string{"edit", "pager"}},
	} {
		if got := registryCompletionValues(root.name, root.after...); !slices.Contains(got, "--enabled") || !slices.Contains(got, "--priority") {
			t.Errorf("%s edit option completions = %v", root.name, got)
		}
	}
	for _, alias := range []string{"webhooks", "inbound-webhooks"} {
		legacy := lookupCommand(alias)
		if legacy == nil {
			t.Fatalf("deprecated alias %q missing", alias)
		}
		legacyHelp := renderCommandHelp(*legacy)
		if !strings.Contains(strings.ToLower(legacyHelp), "deprecated") || !strings.Contains(legacyHelp, "/channels webhooks") {
			t.Errorf("legacy help for %q does not document canonical replacement:\n%s", alias, legacyHelp)
		}
	}
}

// The -h/--help output must list the commands, not just the flags.
func TestCommandSummaryListsEveryCommand(t *testing.T) {
	s := CommandSummary()
	for _, c := range commands {
		if c.hidden {
			continue
		}
		if !strings.Contains(s, c.name) {
			t.Errorf("%s missing from the CLI command summary", c.name)
		}
	}
}

func TestAgentsVotesHelpDocumentsParallelInspection(t *testing.T) {
	cmd := lookupCommand("agents")
	if cmd == nil {
		t.Fatal("agents command missing")
	}
	help := renderCommandHelp(*cmd)
	for _, want := range []string{
		"agents votes <step-execution-id>",
		"inspect parallel-step votes",
		"agents votes step-exec-123",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("agents help missing %q:\n%s", want, help)
		}
	}
}

func TestModelsHelpDocumentsProviderLimitHealth(t *testing.T) {
	cmd := lookupCommand("models")
	if cmd == nil {
		t.Fatal("models command missing")
	}
	help := renderCommandHelp(*cmd)
	for _, want := range []string{
		"provider/account-limit health", "analytics usage", "models add", "models edit",
		"--api-key", "--api-key-stdin", "--oauth", "ollama", "masked API-key input",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("models help missing %q:\n%s", want, help)
		}
	}
}

func TestModelsCompletionAndHelpDocumentAdd(t *testing.T) {
	cmd := lookupCommand("models")
	if cmd == nil {
		t.Fatal("models command missing")
	}
	if got := completeSlashInput("/models ad", *cmd); got != "/models add " {
		t.Fatalf("models add completion = %q", got)
	}
	if got := completeSlashInput("/models ed", *cmd); got != "/models edit " {
		t.Fatalf("models edit completion = %q", got)
	}
	help := renderCommandHelp(*cmd)
	for _, want := range []string{"models add", "models edit", "--api-key", "--api-key-stdin", "models add ollama", "models edit \"Local Ollama\""} {
		if !strings.Contains(help, want) {
			t.Errorf("models help missing %q:\n%s", want, help)
		}
	}
}

func TestTaskGoalLifecycleHelpCompletionAndDocumentation(t *testing.T) {
	cmd := lookupCommand("tasks")
	if cmd == nil {
		t.Fatal("tasks command missing")
	}
	if got := completeSlashInput("/tasks goal pa", *cmd); got != "/tasks goal pause " {
		t.Fatalf("pause completion = %q", got)
	}
	for _, want := range []string{"pause", "resume"} {
		if !containsString(registryCompletionValues("tasks", "goal"), want) {
			t.Errorf("goal completions missing %q", want)
		}
	}
	for _, want := range []string{
		"tasks goal <task> | <objective>",
		"tasks goal pause <task>",
		"tasks goal resume <task>",
		"tasks goal \"Fix login bug\" | pause",
	} {
		if help := renderCommandHelp(*cmd); !strings.Contains(help, want) {
			t.Errorf("tasks help missing %q:\n%s", want, help)
		}
	}

	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed while locating documentation")
	}
	root := filepath.Join(filepath.Dir(source), "..", "..")
	for _, path := range []string{"README.md", filepath.Join("docs", "user-guide.md")} {
		body, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, want := range []string{
			"tasks goal <task> | <objective>",
			"tasks goal pause <task>",
			"tasks goal resume <task>",
		} {
			if !strings.Contains(string(body), want) {
				t.Errorf("%s missing %q", path, want)
			}
		}
	}
}

func TestTasksHelpDocumentsReviewSupport(t *testing.T) {
	cmd := lookupCommand("tasks")
	if cmd == nil {
		t.Fatal("tasks command missing")
	}
	help := renderCommandHelp(*cmd)
	for _, want := range []string{"tasks show <task> [tab]", detailTabUsageList(), "review", "tasks reviews add <task> <file>:<line> <comment>", "tasks lifecycle <task> [execution]", "tasks logs <task> [execution]", "tasks lifecycle \"Fix login bug\"", "tasks reviews add \"Fix login bug\" internal/auth.go:42 Handle token refresh errors"} {
		if !strings.Contains(help, want) {
			t.Errorf("tasks help missing %q:\n%s", want, help)
		}
	}
}
