package terminal

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/openvibely/openvibely-terminal/internal/client"
)

const attachmentTaskBoardHTML = `<div>
	<div class="card" data-task-id="t-1" data-task-status="pending" data-task-category="backlog">
		<a href="/tasks/t-1?from=tasks" title="Refactor the API">Refactor the API</a>
	</div>
</div>`

const attachmentRowsHTML = `<div id="attachment-list" data-project-id="p1">
	<div class="attachment-row"><div><p class="text-sm font-medium">request.txt</p><p class="text-xs">11 B</p></div><button hx-delete="/attachments/att-1?project_id=p1"></button></div>
	<div class="attachment-row"><div><p class="text-sm font-medium">trace.json</p><p class="text-xs">2.0 KB</p></div><button hx-delete="/attachments/att-2?project_id=p1"></button></div>
</div>`

const refreshedAttachmentRowsHTML = `<div id="attachment-list" data-project-id="p1">
	<div class="attachment-row"><div><p class="text-sm font-medium">trace.json</p><p class="text-xs">2.0 KB</p></div><button hx-delete="/attachments/att-2?project_id=p1"></button></div>
</div>`

func TestTasksAttachmentsAddDispatchesAndRendersRefreshedFiles(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "request.txt")
	secondPath := filepath.Join(dir, "trace.json")
	if err := os.WriteFile(firstPath, []byte("request body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte(strings.Repeat("x", 2048)), 0o600); err != nil {
		t.Fatal(err)
	}

	m, rec := dispatchModel(t, map[string]string{
		"/tasks":                           attachmentTaskBoardHTML,
		"GET /api/tasks/reference-catalog": `{"tasks":[{"id":"t-1","project_id":"p1","title":"Refactor the API","category":"backlog","status":"pending","attachments":[]}]}`,
		"POST /tasks/t-1/attachments":      attachmentRowsHTML,
		"/attachments/att-1":               refreshedAttachmentRowsHTML,
		"/attachments/att-2":               refreshedAttachmentRowsHTML,
	})
	m = runLine(t, m, "/tasks attachments add Refactor "+firstPath+" "+secondPath)

	if !rec.saw("POST", "/tasks/t-1/attachments") {
		t.Fatalf("expected attachment upload, calls:\n%s", rec.all())
	}
	if !rec.sawQuery("POST /tasks/t-1/attachments?project_id=p1") {
		t.Fatalf("upload was not scoped to selected project:\n%s", strings.Join(rec.urls, "\n"))
	}
	if got := rec.count("GET", "/tasks/t-1"); got != 0 {
		t.Fatalf("task attachment preflight reads = %d, want 0 with resolution snapshot; calls:\n%s", got, rec.all())
	}
	out := stripANSI(transcript(m))
	for _, want := range []string{"uploaded 2 attachment(s)", "request.txt", "trace.json", "11 B", "2.0 KB"} {
		if !strings.Contains(out, want) {
			t.Errorf("upload output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "error:") {
		t.Errorf("successful upload reported an error:\n%s", out)
	}
}

func TestTaskAttachmentCommandsDoNotLoadLazyTaskDetailFragments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "request.txt")
	if err := os.WriteFile(path, []byte("request"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name              string
		line              string
		wantSelector      bool
		wantPost          bool
		wantSnapshot      bool
		wantLegacy        bool
		wantTaskPageReads int
	}{
		{name: "list", line: "/tasks attachments list Refactor", wantTaskPageReads: 1},
		{name: "add with resolution snapshot", line: "/tasks attachments add Refactor " + path, wantPost: true, wantSnapshot: true},
		{name: "add legacy fallback", line: "/tasks attachments add Refactor " + path, wantPost: true, wantLegacy: true, wantTaskPageReads: 1},
		{name: "typed delete lookup", line: "/tasks attachments delete Refactor att-1", wantTaskPageReads: 1},
		{name: "picker loading", line: "/tasks attachments delete Refactor", wantSelector: true, wantTaskPageReads: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attachmentPage := attachmentRowsHTML
			if tc.wantPost {
				attachmentPage = `<div id="attachment-list" data-project-id="p1"></div>`
			}
			bodies := map[string]string{
				"/tasks":                      attachmentTaskBoardHTML,
				"GET /tasks/t-1":              attachmentPage,
				"POST /tasks/t-1/attachments": attachmentRowsHTML,
				"/attachments/att-1":          refreshedAttachmentRowsHTML,
			}
			if tc.wantSnapshot {
				bodies["GET /api/tasks/reference-catalog"] = `{"tasks":[{"id":"t-1","project_id":"p1","title":"Refactor the API","category":"backlog","status":"pending","attachments":[]}]}`
			} else if tc.wantLegacy {
				bodies["GET /api/tasks/reference-catalog"] = `{"tasks":[{"id":"t-1","project_id":"p1","title":"Refactor the API","category":"backlog","status":"pending"}]}`
			}
			m, rec := dispatchModel(t, bodies)

			m = runLine(t, m, tc.line)
			if tc.wantSelector && !m.selectorActive {
				t.Fatal("attachment command did not open selector")
			}
			if tc.wantPost && !rec.sawQuery("POST /tasks/t-1/attachments?project_id=p1") {
				t.Fatalf("upload was not sent with selected project scope:\n%s", rec.all())
			}
			if got := rec.count("GET", "/tasks/t-1"); got != tc.wantTaskPageReads {
				t.Fatalf("attachment command task-page reads = %d, want %d; calls:\n%s", got, tc.wantTaskPageReads, rec.all())
			}
			for _, disallowed := range []string{"/tasks/t-1/thread", "/tasks/t-1/changes", "/api/tasks/t-1/lifecycle-executions"} {
				if rec.saw("GET", disallowed) {
					t.Fatalf("attachment command requested unrelated lazy detail endpoint %s; calls:\n%s", disallowed, rec.all())
				}
			}
		})
	}
}

func TestTasksAttachmentsDeleteRequiresTUIConfirmationAndRefreshes(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":             attachmentTaskBoardHTML,
		"/tasks/t-1":         attachmentRowsHTML,
		"/attachments/att-1": refreshedAttachmentRowsHTML,
	})

	m = runLine(t, m, "/tasks attachments delete Refactor att-1")
	if m.pendingConfirmation == nil {
		t.Fatal("delete must set a pending TUI confirmation")
	}
	if rec.saw("DELETE", "/attachments/att-1") {
		t.Fatal("delete ran before TUI confirmation")
	}

	m = runLine(t, m, "yes")
	if !rec.saw("DELETE", "/attachments/att-1") {
		t.Fatalf("confirmed delete did not call backend:\n%s", rec.all())
	}
	if !rec.sawQuery("DELETE /attachments/att-1?project_id=p1") {
		t.Fatalf("delete was not scoped to selected project:\n%s", strings.Join(rec.urls, "\n"))
	}
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "deleted attachment") || !strings.Contains(out, "trace.json") {
		t.Fatalf("delete output did not show refreshed list:\n%s", out)
	}
	if strings.Count(out, "request.txt") != 1 {
		t.Fatalf("deleted attachment was not limited to the deletion result:\n%s", out)
	}
}

func TestTasksAttachmentsDeleteCanBeCanceledWithoutBackendMutation(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":     attachmentTaskBoardHTML,
		"/tasks/t-1": attachmentRowsHTML,
	})
	m = runLine(t, m, "/tasks attachments delete Refactor att-1")
	if m.pendingConfirmation == nil {
		t.Fatal("delete must set a pending confirmation")
	}
	m = runLine(t, m, "esc")
	if m.pendingConfirmation != nil {
		t.Fatal("Esc must clear the pending confirmation")
	}
	if rec.saw("DELETE", "/attachments/att-1") {
		t.Fatal("canceled delete called backend")
	}
	if !strings.Contains(stripANSI(transcript(m)), "cancelled") {
		t.Fatalf("cancellation was not reported:\n%s", transcript(m))
	}
}

func TestTasksAttachmentsPartialUploadIsVisibleWithoutInflatedSuccess(t *testing.T) {
	dir := t.TempDir()
	keptPath := filepath.Join(dir, "kept.txt")
	skippedPath := filepath.Join(dir, "skipped.txt")
	for path, contents := range map[string]string{keptPath: "kept", skippedPath: "skipped"} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tasks":[{"id":"t-1","project_id":"p1","title":"Refactor the API","category":"backlog","status":"pending","attachments":[{"id":"att-existing","task_id":"t-1","file_name":"skipped.txt","file_size":7}]}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/tasks":
			_, _ = w.Write([]byte(attachmentTaskBoardHTML))
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1":
			t.Errorf("unexpected attachment preflight GET %s", r.URL.Path)
			_, _ = w.Write([]byte(`<div id="attachment-list" data-project-id="p1"></div>`))
		case r.Method == http.MethodPost && r.URL.Path == "/tasks/t-1/attachments":
			if r.URL.Query().Get("project_id") != "p1" {
				t.Errorf("upload project_id = %q, want p1", r.URL.Query().Get("project_id"))
			}
			_, _ = w.Write([]byte(`<div id="attachment-list" data-project-id="p1"><div class="attachment-row"><p class="font-medium">skipped.txt</p><p class="text-xs">7 B</p><button hx-delete="/attachments/att-existing?project_id=p1"></button></div><div class="attachment-row"><p class="font-medium">kept.txt</p><p class="text-xs">4 B</p><button hx-delete="/attachments/att-1?project_id=p1"></button></div></div>`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	})

	m = runLine(t, m, "/tasks attachments add Refactor "+keptPath+" "+skippedPath)
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "partial attachment upload") || !strings.Contains(out, "skipped.txt") {
		t.Fatalf("partial upload failure was not visible:\n%s", out)
	}
	if !strings.Contains(out, "kept.txt") || !strings.Contains(out, "uploaded 1 of 2") {
		t.Fatalf("partial upload did not report the successful file:\n%s", out)
	}
	if strings.Contains(out, "uploaded 2 attachment(s)") {
		t.Fatalf("partial upload claimed every file succeeded:\n%s", out)
	}
}

func TestTasksAttachmentsUploadErrorIsVisibleWithoutSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "request.txt")
	if err := os.WriteFile(path, []byte("request"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(compactTaskCatalogForTest(attachmentTaskBoardHTML)))
			return
		}
		switch r.URL.Path {
		case "/tasks":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(attachmentTaskBoardHTML))
		case "/tasks/t-1/attachments":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_, _ = w.Write([]byte(`{"error":"attachment rejected"}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	})

	m = runLine(t, m, "/tasks attachments add Refactor "+path)
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "attachment rejected") {
		t.Fatalf("backend upload error was not visible:\n%s", out)
	}
	if strings.Contains(out, "uploaded 1 attachment") {
		t.Fatalf("failed upload claimed success:\n%s", out)
	}
}

func TestTasksAttachmentsAddSupportsQuotedTaskAndPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monthly report.pdf")
	if err := os.WriteFile(path, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}

	m, rec := dispatchModel(t, map[string]string{
		"/tasks":                      attachmentTaskBoardHTML,
		"GET /tasks/t-1":              `<div id="attachment-list" data-project-id="p1"></div>`,
		"POST /tasks/t-1/attachments": `<div id="attachment-list" data-project-id="p1"><div class="attachment-row"><p class="font-medium">monthly report.pdf</p><p class="text-xs">6 B</p><button hx-delete="/attachments/att-3?project_id=p1"></button></div></div>`,
	})
	m = runLine(t, m, `/tasks attachments add "Refactor the API" "`+path+`"`)

	if !rec.sawQuery("POST /tasks/t-1/attachments?project_id=p1") {
		t.Fatalf("quoted upload was missing or unscoped:\n%s", strings.Join(rec.urls, "\n"))
	}
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "uploaded 1 attachment(s)") || !strings.Contains(out, "monthly report.pdf") || !strings.Contains(out, "6 B") {
		t.Fatalf("quoted upload output was incomplete:\n%s", out)
	}
}

func TestTasksAttachmentsDeleteInteractiveAndCLIResolveEquivalentRefs(t *testing.T) {
	rows := `<div id="attachment-list" data-project-id="p1"><div class="attachment-row"><div><p class="text-sm font-medium">monthly report.pdf</p><p class="text-xs">7 B</p></div><button hx-delete="/attachments/att-1?project_id=p1"></button></div></div>`
	refreshed := `<div id="attachment-list" data-project-id="p1"></div>`

	t.Run("interactive", func(t *testing.T) {
		m, rec := dispatchModel(t, map[string]string{
			"/tasks":             attachmentTaskBoardHTML,
			"/tasks/t-1":         rows,
			"/attachments/att-1": refreshed,
		})

		m = runLine(t, m, `/tasks attachments delete "Refactor the API" "monthly report.pdf"`)
		if m.pendingConfirmation == nil {
			t.Fatal("delete must set a pending confirmation")
		}
		wantPrompt := `Delete attachment "monthly report.pdf" from task "Refactor the API"?`
		if !strings.Contains(m.pendingConfirmation.message, wantPrompt) {
			t.Fatalf("confirmation = %q, want resolved target %q", m.pendingConfirmation.message, wantPrompt)
		}
		if rec.saw("DELETE", "/attachments/att-1") {
			t.Fatal("interactive delete ran before confirmation")
		}

		m = runLine(t, m, "yes")
		if !rec.sawQuery("DELETE /attachments/att-1?project_id=p1") {
			t.Fatalf("interactive delete was missing or unscoped:\n%s", strings.Join(rec.urls, "\n"))
		}
		if !strings.Contains(stripANSI(transcript(m)), "deleted attachment \"monthly report.pdf\"") {
			t.Fatalf("interactive delete output used the wrong attachment:\n%s", transcript(m))
		}
	})

	t.Run("forced CLI", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects":      cliProjects,
			"/tasks":             attachmentTaskBoardHTML,
			"/tasks/t-1":         rows,
			"/attachments/att-1": refreshed,
		})
		var out bytes.Buffer
		if err := RunCLI(c, &out, "demo", []string{"tasks", "attachments", "delete", "Refactor the API", "monthly report.pdf"}, true, false); err != nil {
			t.Fatalf("forced CLI delete failed: %v", err)
		}
		if !rec.sawQuery("DELETE /attachments/att-1?project_id=p1") {
			t.Fatalf("forced CLI delete was missing or unscoped:\n%s", strings.Join(rec.urls, "\n"))
		}
		if !strings.Contains(out.String(), "deleted attachment \"monthly report.pdf\"") {
			t.Fatalf("forced CLI delete output used the wrong attachment:\n%s", out.String())
		}
	})
}

func TestTasksAttachmentsDownloadByIDFromCLIWritesExactBackendBytes(t *testing.T) {
	payload := []byte("\x00remote-bytes\xff\n")
	dir := t.TempDir()
	outPath := filepath.Join(dir, "request.bin")
	c, rec := cliServer(t, map[string]string{
		"/api/projects":      cliProjects,
		"/tasks":             attachmentTaskBoardHTML,
		"/tasks/t-1":         attachmentRowsHTML,
		"/attachments/att-1": string(payload),
	})
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "attachments", "download", "Refactor", "att-1", "--output", outPath}, false, false); err != nil {
		t.Fatalf("CLI attachment download failed: %v", err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("downloaded bytes = %q, want %q", got, payload)
	}
	if !rec.sawQuery("GET /attachments/att-1?project_id=p1") {
		t.Fatalf("download was not scoped to selected project through HTTP route:\n%s", strings.Join(rec.urls, "\n"))
	}
	if strings.Contains(out.String(), "file_path") {
		t.Fatalf("download output exposed backend-local file path: %s", out.String())
	}
}

func TestTasksAttachmentsDownloadByUniqueFilenameFromSlashCommand(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "trace.json")
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":             attachmentTaskBoardHTML,
		"/tasks/t-1":         attachmentRowsHTML,
		"/attachments/att-2": `{"ok":true}`,
	})

	m = runLine(t, m, `/tasks attachments download Refactor trace.json --output "`+outPath+`"`)
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if string(got) != `{"ok":true}` {
		t.Fatalf("downloaded file = %q", got)
	}
	if !rec.sawQuery("GET /attachments/att-2?project_id=p1") {
		t.Fatalf("slash download was not project-scoped:\n%s", strings.Join(rec.urls, "\n"))
	}
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "downloaded attachment \"trace.json\"") || !strings.Contains(out, outPath) {
		t.Fatalf("download result did not name attachment and output path:\n%s", out)
	}
}

func TestTasksAttachmentsDownloadDuplicateFilenameIsAmbiguous(t *testing.T) {
	rows := `<div id="attachment-list" data-project-id="p1">` +
		`<div class="attachment-row"><p class="font-medium">request.txt</p><p class="text-xs">4 B</p><button hx-delete="/attachments/att-1?project_id=p1"></button></div>` +
		`<div class="attachment-row"><p class="font-medium">request.txt</p><p class="text-xs">5 B</p><button hx-delete="/attachments/att-2?project_id=p1"></button></div>` +
		`</div>`
	dir := t.TempDir()
	outPath := filepath.Join(dir, "request.txt")
	c, rec := cliServer(t, map[string]string{
		"/api/projects": cliProjects,
		"/tasks":        attachmentTaskBoardHTML,
		"/tasks/t-1":    rows,
	})
	var out bytes.Buffer
	err := RunCLI(c, &out, "demo", []string{"tasks", "attachments", "download", "Refactor", "request.txt", "--output", outPath}, false, false)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate filename error = %v, want ambiguity", err)
	}
	if rec.saw("GET", "/attachments/att-1") || rec.saw("GET", "/attachments/att-2") {
		t.Fatalf("ambiguous download guessed an attachment:\n%s", rec.all())
	}
	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Fatalf("ambiguous download created output path: %v", statErr)
	}
}

func TestTasksAttachmentsDownloadFailuresAreClear(t *testing.T) {
	t.Run("missing task", func(t *testing.T) {
		c, _ := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			"/tasks":        attachmentTaskBoardHTML,
		})
		var out bytes.Buffer
		err := RunCLI(c, &out, "demo", []string{"tasks", "attachments", "download", "Missing", "att-1", "--output", filepath.Join(t.TempDir(), "out.txt")}, false, false)
		if err == nil || !strings.Contains(err.Error(), "nothing matches") {
			t.Fatalf("missing task error = %v", err)
		}
	})

	t.Run("missing attachment", func(t *testing.T) {
		c, _ := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			"/tasks":        attachmentTaskBoardHTML,
			"/tasks/t-1":    attachmentRowsHTML,
		})
		var out bytes.Buffer
		err := RunCLI(c, &out, "demo", []string{"tasks", "attachments", "download", "Refactor", "missing.txt", "--output", filepath.Join(t.TempDir(), "out.txt")}, false, false)
		if err == nil || !strings.Contains(err.Error(), "nothing matches") {
			t.Fatalf("missing attachment error = %v", err)
		}
	})

	t.Run("backend rejected", func(t *testing.T) {
		rec := &recorder{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec.recordURL(r.Method, r.URL.RequestURI())
			switch r.URL.Path {
			case "/api/projects":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(cliProjects))
			case "/api/tasks/reference-catalog":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(compactTaskCatalogForTest(attachmentTaskBoardHTML)))
			case "/tasks/t-1":
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte(attachmentRowsHTML))
			case "/attachments/att-1":
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"attachment access denied"}`))
			default:
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte(attachmentTaskBoardHTML))
			}
		}))
		defer srv.Close()
		c, err := client.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		outPath := filepath.Join(t.TempDir(), "out.txt")
		var out bytes.Buffer
		err = RunCLI(c, &out, "demo", []string{"tasks", "attachments", "download", "Refactor", "att-1", "--output", outPath}, false, false)
		if err == nil || !strings.Contains(err.Error(), "attachment access denied") {
			t.Fatalf("backend rejection error = %v", err)
		}
		if !rec.sawQuery("GET /attachments/att-1?project_id=p1") {
			t.Fatalf("backend rejection did not use scoped download route:\n%s", rec.all())
		}
	})

	t.Run("output path cannot be written", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			"/tasks":        attachmentTaskBoardHTML,
			"/tasks/t-1":    attachmentRowsHTML,
		})
		var out bytes.Buffer
		badPath := filepath.Join(t.TempDir(), "missing", "out.txt")
		err := RunCLI(c, &out, "demo", []string{"tasks", "attachments", "download", "Refactor", "att-1", "--output", badPath}, false, false)
		if err == nil || !strings.Contains(err.Error(), "open download path") {
			t.Fatalf("unwritable output error = %v", err)
		}
		if rec.saw("GET", "/attachments/att-1") {
			t.Fatalf("download called backend before opening output path:\n%s", rec.all())
		}
	})
}

func TestTasksAttachmentsDownloadPickerDoesNotDumpBinaryToTerminal(t *testing.T) {
	dir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWD) }()
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":             attachmentTaskBoardHTML,
		"/tasks/t-1":         `<div id="attachment-list" data-project-id="p1"><div class="attachment-row"><p class="font-medium">raw.bin</p><p class="text-xs">4 B</p><button hx-delete="/attachments/att-1?project_id=p1"></button></div></div>`,
		"/attachments/att-1": "\x00\x01\x02\xff",
	})

	m = runLineWithFollowUp(t, m, "/tasks attachments download Refactor")
	if !rec.sawQuery("GET /attachments/att-1?project_id=p1") {
		t.Fatalf("picker download did not use scoped HTTP route:\n%s", rec.all())
	}
	got, err := os.ReadFile(filepath.Join(dir, "raw.bin"))
	if err != nil {
		t.Fatalf("reading default download: %v", err)
	}
	if !bytes.Equal(got, []byte("\x00\x01\x02\xff")) {
		t.Fatalf("default download bytes = %v", got)
	}
	if strings.Contains(stripANSI(transcript(m)), "\x00") || strings.Contains(stripANSI(transcript(m)), "\xff") {
		t.Fatalf("binary bytes were dumped into terminal transcript:\n%s", transcript(m))
	}
}

func TestTasksAttachmentsDeleteConfirmationUsesResolvedTarget(t *testing.T) {
	rows := `<div id="attachment-list" data-project-id="p1"><div class="attachment-row"><div><p class="text-sm font-medium">monthly report.pdf</p><p class="text-xs">7 B</p></div><button hx-delete="/attachments/att-1?project_id=p1"></button></div></div>`
	refreshed := `<div id="attachment-list" data-project-id="p1"></div>`
	m, rec := dispatchModel(t, map[string]string{
		"/tasks":             attachmentTaskBoardHTML,
		"/tasks/t-1":         rows,
		"/attachments/att-1": refreshed,
	})

	m = runLine(t, m, `/tasks attachments delete Refactor "monthly report.pdf"`)
	if m.pendingConfirmation == nil {
		t.Fatal("delete must set a pending confirmation after target resolution")
	}
	prompt := m.pendingConfirmation.message
	want := `Delete attachment "monthly report.pdf" from task "Refactor the API"?`
	if !strings.Contains(prompt, want) {
		t.Fatalf("confirmation = %q, want resolved task and filename %q", prompt, want)
	}
	if strings.Contains(prompt, `from task "Refactor"`) || strings.Contains(prompt, `attachment "report.pdf"`) {
		t.Fatalf("confirmation used a partial target: %q", prompt)
	}
	if rec.saw("DELETE", "/attachments/att-1") {
		t.Fatal("delete ran before confirmation")
	}

	m = runLine(t, m, "yes")
	if !rec.sawQuery("DELETE /attachments/att-1?project_id=p1") {
		t.Fatalf("confirmed delete was missing or unscoped:\n%s", strings.Join(rec.urls, "\n"))
	}
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "deleted attachment") || !strings.Contains(out, "monthly report.pdf") {
		t.Fatalf("confirmed delete output was incomplete:\n%s", out)
	}
}

func TestTasksAttachmentsDeleteTypedAndPickerHaveIdenticalConfirmationAndResult(t *testing.T) {
	paths := map[string]string{
		"/tasks":             attachmentTaskBoardHTML,
		"/tasks/t-1":         attachmentRowsHTML,
		"/attachments/att-1": refreshedAttachmentRowsHTML,
	}

	typed, typedRec := dispatchModel(t, paths)
	typed = runLine(t, typed, "/tasks attachments delete Refactor att-1")
	if typed.pendingConfirmation == nil {
		t.Fatal("typed delete must set a pending confirmation")
	}
	if typedRec.saw("DELETE", "/attachments/att-1") {
		t.Fatal("typed delete ran before confirmation")
	}

	picked, pickerRec := dispatchModel(t, paths)
	picked = runLine(t, picked, "/tasks attachments delete Refactor")
	if !picked.selectorActive {
		t.Fatal("attachment delete must open the picker when multiple attachments are available")
	}
	picked = selKey(t, picked, tea.KeyMsg{Type: tea.KeyEnter})
	if picked.pendingConfirmation == nil {
		t.Fatal("picker selection must set a pending confirmation")
	}
	if pickerRec.saw("DELETE", "/attachments/att-1") {
		t.Fatal("picker delete ran before confirmation")
	}
	if got, want := picked.pendingConfirmation.message, typed.pendingConfirmation.message; got != want {
		t.Fatalf("picker confirmation = %q, want typed confirmation %q", got, want)
	}

	typed = runLine(t, typed, "yes")
	picked = runLine(t, picked, "yes")
	for name, model := range map[string]Model{"typed": typed, "picker": picked} {
		out := stripANSI(transcript(model))
		if !strings.Contains(out, "deleted attachment \"request.txt\" from Refactor the API") || !strings.Contains(out, "trace.json") {
			t.Fatalf("%s delete result did not render the refreshed attachment list:\n%s", name, out)
		}
	}
	for name, rec := range map[string]*recorder{"typed": typedRec, "picker": pickerRec} {
		if !rec.sawQuery("DELETE /attachments/att-1?project_id=p1") {
			t.Fatalf("%s confirmed delete was not project-scoped:\n%s", name, strings.Join(rec.urls, "\n"))
		}
	}
}

func TestConfirmTaskAttachmentDeletionUsesCanonicalFallbackLabels(t *testing.T) {
	m, rec := dispatchModel(t, map[string]string{
		"/attachments/attachment-1": `<div id="attachment-list" data-project-id="p1"></div>`,
	})
	m, cmd := confirmTaskAttachmentDeletion(m, "p1", client.Task{ID: "task-1"}, client.Attachment{ID: "attachment-1"})
	if cmd != nil || m.pendingConfirmation == nil {
		t.Fatalf("confirmation setup = cmd:%v pending:%v, want pending confirmation", cmd, m.pendingConfirmation != nil)
	}
	if got, want := m.pendingConfirmation.message, `Delete attachment "attachment-1" from task "task-1"? Type 'yes' to confirm or Esc to cancel.`; got != want {
		t.Fatalf("fallback confirmation = %q, want %q", got, want)
	}
	if rec.saw("DELETE", "/attachments/attachment-1") {
		t.Fatal("fallback delete ran before confirmation")
	}

	m = runLine(t, m, "yes")
	if !rec.sawQuery("DELETE /attachments/attachment-1?project_id=p1") {
		t.Fatalf("fallback confirmed delete was not project-scoped:\n%s", strings.Join(rec.urls, "\n"))
	}
}

func TestTasksAttachmentsConfirmedDeleteErrorIsVisibleWithoutSuccess(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/tasks/reference-catalog":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(compactTaskCatalogForTest(attachmentTaskBoardHTML)))
		case "/tasks":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(attachmentTaskBoardHTML))
		case "/tasks/t-1":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(attachmentRowsHTML))
		case "/attachments/att-1":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"delete rejected"}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.selectedID = "p1"
	m.selectedName = "demo"

	m = runLine(t, m, "/tasks attachments delete Refactor att-1")
	if m.pendingConfirmation == nil {
		t.Fatal("delete must set a pending confirmation")
	}
	m = runLine(t, m, "yes")
	out := stripANSI(transcript(m))
	if !strings.Contains(out, "delete rejected") {
		t.Fatalf("delete failure was not visible:\n%s", out)
	}
	if strings.Contains(out, "deleted attachment") {
		t.Fatalf("failed delete claimed success:\n%s", out)
	}
	if !rec.sawQuery("DELETE /attachments/att-1?project_id=p1") {
		t.Fatalf("delete failure request was missing or unscoped:\n%s", strings.Join(rec.urls, "\n"))
	}
}

func TestCLITaskAttachmentsPartialUploadReturnsFailureWithoutInflatedSuccess(t *testing.T) {
	dir := t.TempDir()
	keptPath := filepath.Join(dir, "kept.txt")
	skippedPath := filepath.Join(dir, "skipped.txt")
	for path, contents := range map[string]string{keptPath: "kept", skippedPath: "skipped"} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
		case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tasks":[{"id":"t-1","project_id":"p1","title":"Refactor the API","category":"backlog","status":"pending","attachments":[{"id":"att-existing","task_id":"t-1","file_name":"skipped.txt","file_size":7}]}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/tasks":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(attachmentTaskBoardHTML))
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1":
			t.Errorf("unexpected attachment preflight GET %s", r.URL.Path)
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div id="attachment-list" data-project-id="p1"></div>`))
		case r.Method == http.MethodPost && r.URL.Path == "/tasks/t-1/attachments":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div id="attachment-list" data-project-id="p1"><div class="attachment-row"><p class="font-medium">skipped.txt</p><p class="text-xs">7 B</p><button hx-delete="/attachments/att-existing?project_id=p1"></button></div><div class="attachment-row"><p class="font-medium">kept.txt</p><p class="text-xs">4 B</p><button hx-delete="/attachments/att-1?project_id=p1"></button></div></div>`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = RunCLI(c, &out, "demo", []string{"tasks", "attachments", "add", "Refactor the API", keptPath, skippedPath}, false, false)
	if err == nil || !strings.Contains(err.Error(), "partial attachment upload") || !strings.Contains(err.Error(), "skipped.txt") || !strings.Contains(err.Error(), "kept.txt (4 B)") || !strings.Contains(err.Error(), "uploaded 1 of 2") {
		t.Fatalf("CLI partial upload error = %v, want explicit successful and skipped details", err)
	}
	if strings.Contains(out.String(), "uploaded 2 attachment(s)") {
		t.Fatalf("partial CLI upload claimed every file succeeded:\n%s", out.String())
	}
	if !rec.sawQuery("POST /tasks/t-1/attachments?project_id=p1") {
		t.Fatalf("partial CLI upload was missing or unscoped:\n%s", strings.Join(rec.urls, "\n"))
	}
	if rec.saw("GET", "/tasks/t-1") {
		t.Fatalf("complete attachment snapshot still triggered preflight:\n%s", rec.all())
	}
}
func TestCLITaskAttachmentsUploadErrorReturnsFailureWithoutSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "request body.txt")
	if err := os.WriteFile(path, []byte("request"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.recordURL(r.Method, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/projects":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cliProjects))
		case "/api/tasks/reference-catalog":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(compactTaskCatalogForTest(attachmentTaskBoardHTML)))
		case "/tasks":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(attachmentTaskBoardHTML))
		case "/tasks/t-1/attachments":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_, _ = w.Write([]byte(`{"error":"file rejected"}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = RunCLI(c, &out, "demo", []string{"tasks", "attachments", "add", "Refactor the API", path}, false, false)
	if err == nil || !strings.Contains(err.Error(), "file rejected") {
		t.Fatalf("CLI upload error = %v, want backend failure", err)
	}
	if strings.Contains(out.String(), "uploaded") {
		t.Fatalf("failed CLI upload claimed success:\n%s", out.String())
	}
	if !rec.sawQuery("POST /tasks/t-1/attachments?project_id=p1") {
		t.Fatalf("CLI upload failure request was missing or unscoped:\n%s", strings.Join(rec.urls, "\n"))
	}
}

func TestCLITaskAttachmentsAddUsesProjectAndPrintsRefresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "request.txt")
	if err := os.WriteFile(path, []byte("request body"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, rec := cliServer(t, map[string]string{
		"/api/projects":          cliProjects,
		"/tasks":                 attachmentTaskBoardHTML,
		"/tasks/t-1/attachments": attachmentRowsHTML,
	})

	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "attachments", "add", "Refactor", path}, false, false); err != nil {
		t.Fatalf("CLI attachment add failed: %v", err)
	}
	if !rec.saw("POST", "/tasks/t-1/attachments") || !rec.sawQuery("POST /tasks/t-1/attachments?project_id=p1") {
		t.Fatalf("upload request was missing or unscoped:\n%s", strings.Join(rec.urls, "\n"))
	}
	for _, want := range []string{"uploaded 1 attachment(s)", "request.txt", "trace.json", "2.0 KB"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("CLI upload output missing %q:\n%s", want, out.String())
		}
	}
}

func TestCLITaskAttachmentsDeleteRequiresForceAndRefreshes(t *testing.T) {
	t.Run("without force", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			"/tasks":        attachmentTaskBoardHTML,
		})
		var out bytes.Buffer
		err := RunCLI(c, &out, "demo", []string{"tasks", "attachments", "delete", "Refactor the API", "monthly report.pdf"}, false, false)
		if err == nil || !strings.Contains(err.Error(), `use --force to confirm deletion of attachment "monthly report.pdf"`) {
			t.Fatalf("delete without force error = %v, want raw attachment label and --force guidance", err)
		}
		if rec.saw("DELETE", "/attachments/att-1") {
			t.Fatal("CLI delete ran without --force")
		}
	})

	t.Run("without force keeps raw ID guidance", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects": cliProjects,
			"/tasks":        attachmentTaskBoardHTML,
		})
		var out bytes.Buffer
		err := RunCLI(c, &out, "demo", []string{"tasks", "attachments", "delete", "Refactor", "att-1"}, false, false)
		if err == nil || !strings.Contains(err.Error(), `use --force to confirm deletion of attachment "att-1"`) {
			t.Fatalf("delete without force error = %v, want raw attachment ID and --force guidance", err)
		}
		if rec.saw("DELETE", "/attachments/att-1") {
			t.Fatal("CLI delete ran without --force")
		}
	})

	t.Run("with force", func(t *testing.T) {
		c, rec := cliServer(t, map[string]string{
			"/api/projects":      cliProjects,
			"/tasks":             attachmentTaskBoardHTML,
			"/tasks/t-1":         attachmentRowsHTML,
			"/attachments/att-1": refreshedAttachmentRowsHTML,
		})
		var out bytes.Buffer
		if err := RunCLI(c, &out, "demo", []string{"tasks", "attachments", "delete", "Refactor", "att-1"}, true, false); err != nil {
			t.Fatalf("CLI attachment delete failed: %v", err)
		}
		if !rec.saw("DELETE", "/attachments/att-1") || !rec.sawQuery("DELETE /attachments/att-1?project_id=p1") {
			t.Fatalf("forced delete request was missing or unscoped:\n%s", strings.Join(rec.urls, "\n"))
		}
		if !strings.Contains(out.String(), "trace.json") || strings.Count(out.String(), "request.txt") != 1 {
			t.Fatalf("CLI delete did not print refreshed list:\n%s", out.String())
		}
	})
}
