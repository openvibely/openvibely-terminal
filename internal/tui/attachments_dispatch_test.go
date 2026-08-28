package tui

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		"/tasks":                 attachmentTaskBoardHTML,
		"/tasks/t-1/attachments": attachmentRowsHTML,
		"/tasks/t-1":             attachmentRowsHTML,
		"/attachments/att-1":     refreshedAttachmentRowsHTML,
		"/attachments/att-2":     refreshedAttachmentRowsHTML,
	})
	m = runLine(t, m, "/tasks attachments add Refactor "+firstPath+" "+secondPath)

	if !rec.saw("POST", "/tasks/t-1/attachments") {
		t.Fatalf("expected attachment upload, calls:\n%s", rec.all())
	}
	if !rec.sawQuery("POST /tasks/t-1/attachments?project_id=p1") {
		t.Fatalf("upload was not scoped to selected project:\n%s", strings.Join(rec.urls, "\n"))
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

func TestTasksAttachmentsUploadErrorIsVisibleWithoutSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "request.txt")
	if err := os.WriteFile(path, []byte("request"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newModelFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
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
		err := RunCLI(c, &out, "demo", []string{"tasks", "attachments", "delete", "Refactor", "att-1"}, false, false)
		if err == nil || !strings.Contains(err.Error(), "--force") {
			t.Fatalf("delete without force error = %v, want --force guidance", err)
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
