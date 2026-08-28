package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func attachmentListMarkup(projectID string, rows string) string {
	return `<div id="attachment-list" data-project-id="` + projectID + `">` + rows + `</div>`
}

func attachmentRowMarkup(id, projectID, fileName, size string) string {
	return `<div class="attachment-row"><div><p class="text-sm font-medium">` + fileName + `</p><p class="text-xs text-base-content/60">` + size + `</p></div><button hx-delete="/attachments/` + id + `?project_id=` + projectID + `"></button></div>`
}

func TestAddTaskAttachmentsUsesRepeatedFilesAndProjectScope(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "request.txt")
	secondPath := filepath.Join(dir, "trace.json")
	if err := os.WriteFile(firstPath, []byte("request body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte(strings.Repeat("x", 2048)), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotNames []string
	var gotContents = map[string]string{}
	var gotProject string
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tasks/t-1" && r.URL.Path != "/tasks/t-1/attachments" {
			t.Errorf("request path = %q, want task detail or attachment upload", r.URL.Path)
		}
		if r.URL.Query().Get("project_id") != "p1" {
			t.Errorf("project_id = %q, want p1", r.URL.Query().Get("project_id"))
		}
		w.Header().Set("Content-Type", "text/html")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(attachmentListMarkup("p1", "")))
		case http.MethodPost:
			gotProject = r.URL.Query().Get("project_id")
			gotContentType = r.Header.Get("Content-Type")
			if r.Header.Get("HX-Request") != "true" {
				t.Error("upload must send HX-Request")
			}
			if err := r.ParseMultipartForm(4 << 20); err != nil {
				t.Fatalf("ParseMultipartForm: %v", err)
			}
			for _, header := range r.MultipartForm.File["files"] {
				file, err := header.Open()
				if err != nil {
					t.Fatalf("open multipart file: %v", err)
				}
				contents, readErr := io.ReadAll(file)
				closeErr := file.Close()
				if readErr != nil || closeErr != nil {
					t.Fatalf("read multipart file %q: read=%v close=%v", header.Filename, readErr, closeErr)
				}
				gotNames = append(gotNames, header.Filename)
				gotContents[header.Filename] = string(contents)
			}
			_, _ = w.Write([]byte(attachmentListMarkup("p1", attachmentRowMarkup("att-1", "p1", "request.txt", "11 B")+attachmentRowMarkup("att-2", "p1", "trace.json", "2.0 KB"))))
		default:
			t.Errorf("method = %s, want GET or POST", r.Method)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := c.AddTaskAttachments(context.Background(), "t-1", "p1", []string{firstPath, secondPath})
	if err != nil {
		t.Fatalf("AddTaskAttachments: %v", err)
	}

	sort.Strings(gotNames)
	if got, want := gotNames, []string{"request.txt", "trace.json"}; !sameStrings(got, want) {
		t.Errorf("multipart filenames = %v, want %v", got, want)
	}
	if gotProject != "p1" {
		t.Errorf("project_id = %q, want p1", gotProject)
	}
	if !strings.HasPrefix(gotContentType, "multipart/form-data; boundary=") {
		t.Errorf("Content-Type = %q, want multipart boundary", gotContentType)
	}
	if gotContents["request.txt"] != "request body" || len(gotContents["trace.json"]) != 2048 {
		t.Errorf("multipart contents = request %q, trace bytes %d", gotContents["request.txt"], len(gotContents["trace.json"]))
	}
	if len(attachments) != 2 || attachments[0].ID != "att-1" || attachments[0].TaskID != "t-1" || attachments[0].FileName != "request.txt" || attachments[0].FileSize != 11 {
		t.Errorf("refreshed attachments = %+v", attachments)
	}
	if attachments[1].FileSize != 2048 {
		t.Errorf("second attachment size = %d, want 2048", attachments[1].FileSize)
	}
}

func TestAddTaskAttachmentsRejectsPartialRefresh(t *testing.T) {
	dir := t.TempDir()
	goodPath := filepath.Join(dir, "kept.txt")
	missingPath := filepath.Join(dir, "skipped.txt")
	for path, contents := range map[string]string{goodPath: "kept", missingPath: "skipped"} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var gotPostNames []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tasks/t-1" && r.URL.Path != "/tasks/t-1/attachments" {
			t.Errorf("request path = %q, want task detail or attachment upload", r.URL.Path)
		}
		if r.URL.Query().Get("project_id") != "p1" {
			t.Errorf("project_id = %q, want p1", r.URL.Query().Get("project_id"))
		}
		w.Header().Set("Content-Type", "text/html")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(attachmentListMarkup("p1", attachmentRowMarkup("att-existing", "p1", "skipped.txt", "7 B"))))
		case http.MethodPost:
			if err := r.ParseMultipartForm(4 << 20); err != nil {
				t.Fatalf("ParseMultipartForm: %v", err)
			}
			for _, header := range r.MultipartForm.File["files"] {
				gotPostNames = append(gotPostNames, header.Filename)
			}
			_, _ = w.Write([]byte(attachmentListMarkup("p1", attachmentRowMarkup("att-existing", "p1", "skipped.txt", "7 B")+attachmentRowMarkup("att-1", "p1", "kept.txt", "5 B"))))
		default:
			t.Errorf("method = %s, want GET or POST", r.Method)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := c.AddTaskAttachments(context.Background(), "t-1", "p1", []string{goodPath, missingPath})
	var partialErr *PartialAttachmentUploadError
	if err == nil || !errors.As(err, &partialErr) || !strings.Contains(err.Error(), "partial attachment upload") || !strings.Contains(err.Error(), "skipped.txt") {
		t.Fatalf("partial upload error = %v, want explicit typed error with skipped filename", err)
	}
	if len(attachments) != 2 || attachments[0].FileName != "skipped.txt" || attachments[1].FileName != "kept.txt" {
		t.Fatalf("partial upload result = %+v, want complete refreshed list", attachments)
	}
	if len(partialErr.Uploaded) != 1 || partialErr.Uploaded[0].FileName != "kept.txt" || partialErr.Uploaded[0].FileSize != 5 {
		t.Fatalf("partial uploaded records = %+v, want kept.txt/5 B", partialErr.Uploaded)
	}
	if !sameStrings(partialErr.Missing, []string{"skipped.txt"}) {
		t.Fatalf("partial missing files = %v, want skipped.txt", partialErr.Missing)
	}
	if !strings.Contains(err.Error(), "kept.txt (5 B)") {
		t.Fatalf("partial upload error omitted successful file details: %v", err)
	}
	if !sameStrings(gotPostNames, []string{"kept.txt", "skipped.txt"}) {
		t.Fatalf("uploaded filenames = %v, want both requested files", gotPostNames)
	}
}

func TestListTaskAttachmentsParsesRefreshedRowsAndScopesProject(t *testing.T) {
	var gotProject string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/tasks/t-1" {
			t.Errorf("request = %s %s, want GET /tasks/t-1", r.Method, r.URL.Path)
		}
		gotProject = r.URL.Query().Get("project_id")
		if r.Header.Get("HX-Request") != "true" {
			t.Error("attachment read must send HX-Request")
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(attachmentListMarkup("p1",
			attachmentRowMarkup("att-1", "p1", "report.pdf", "1.5 KB")+
				attachmentRowMarkup("att-2", "p1", "image.png", "2.0 MB"))))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := c.ListTaskAttachments(context.Background(), "t-1", "p1")
	if err != nil {
		t.Fatalf("ListTaskAttachments: %v", err)
	}
	if gotProject != "p1" {
		t.Errorf("project_id = %q, want p1", gotProject)
	}
	if len(attachments) != 2 {
		t.Fatalf("attachments = %+v, want two rows", attachments)
	}
	if attachments[0].ID != "att-1" || attachments[0].TaskID != "t-1" || attachments[0].FileName != "report.pdf" || attachments[0].FileSize != 1536 {
		t.Errorf("first attachment = %+v", attachments[0])
	}
	if attachments[1].ID != "att-2" || attachments[1].FileName != "image.png" || attachments[1].FileSize != 2<<20 {
		t.Errorf("second attachment = %+v", attachments[1])
	}
}

func TestDeleteTaskAttachmentUsesStableIDProjectScopeAndParsesRefresh(t *testing.T) {
	var gotProject string
	var gotID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/attachments/att-1" {
			t.Errorf("request = %s %s, want DELETE /attachments/att-1", r.Method, r.URL.Path)
		}
		gotID = strings.TrimPrefix(r.URL.Path, "/attachments/")
		gotProject = r.URL.Query().Get("project_id")
		if r.Header.Get("HX-Request") != "true" {
			t.Error("delete must send HX-Request")
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(attachmentListMarkup("p1", attachmentRowMarkup("att-2", "p1", "remaining.txt", "7 B"))))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	remaining, err := c.DeleteTaskAttachment(context.Background(), "att-1", "p1")
	if err != nil {
		t.Fatalf("DeleteTaskAttachment: %v", err)
	}
	if gotID != "att-1" || gotProject != "p1" {
		t.Errorf("delete target = id %q project %q, want att-1/p1", gotID, gotProject)
	}
	if len(remaining) != 1 || remaining[0].ID != "att-2" || remaining[0].FileName != "remaining.txt" || remaining[0].FileSize != 7 {
		t.Errorf("remaining attachments = %+v", remaining)
	}
}

func TestTaskAttachmentOperationsRejectCrossProjectResponsesAndErrors(t *testing.T) {
	t.Run("read response project mismatch", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("project_id"); got != "p1" {
				t.Errorf("project_id = %q, want p1", got)
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(attachmentListMarkup("p2", attachmentRowMarkup("att-2", "p2", "other.txt", "3 B"))))
		}))
		defer srv.Close()
		c, _ := New(srv.URL)
		if _, err := c.ListTaskAttachments(context.Background(), "t-1", "p1"); err == nil || !strings.Contains(err.Error(), "not selected project") {
			t.Fatalf("cross-project read error = %v, want selected-project rejection", err)
		}
	})

	t.Run("delete response control project mismatch", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("project_id"); got != "p1" {
				t.Errorf("project_id = %q, want p1", got)
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(attachmentListMarkup("p1", attachmentRowMarkup("att-2", "p2", "other.txt", "3 B"))))
		}))
		defer srv.Close()
		c, _ := New(srv.URL)
		if _, err := c.DeleteTaskAttachment(context.Background(), "att-1", "p1"); err == nil || !strings.Contains(err.Error(), "not selected project") {
			t.Fatalf("cross-project delete response error = %v, want selected-project rejection", err)
		}
	})

	t.Run("upload backend error", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.txt")
		if err := os.WriteFile(path, []byte("bad"), 0o600); err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("project_id") != "p1" {
				t.Errorf("project_id = %q, want p1", r.URL.Query().Get("project_id"))
			}
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte(attachmentListMarkup("p1", "")))
				return
			}
			if r.Method != http.MethodPost || r.URL.Path != "/tasks/t-1/attachments" {
				t.Errorf("request = %s %s, want POST /tasks/t-1/attachments", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_, _ = w.Write([]byte(`{"error":"file exceeds limit"}`))
		}))
		defer srv.Close()
		c, _ := New(srv.URL)
		if _, err := c.AddTaskAttachments(context.Background(), "t-1", "p1", []string{path}); err == nil || !strings.Contains(err.Error(), "file exceeds limit") {
			t.Fatalf("upload error = %v, want backend error", err)
		}
	})
}

func TestGetTaskForProjectParsesAttachmentsAndScopesDetailLifecycleRead(t *testing.T) {
	var detailProject, lifecycleProject string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/tasks/t-1":
			detailProject = r.URL.Query().Get("project_id")
			_, _ = w.Write([]byte(`<div data-project-id="p1" data-task-status="active"><h2 class="font-bold">Task</h2><div id="tab-details">details</div>` + attachmentListMarkup("p1", attachmentRowMarkup("att-1", "p1", "notes.md", "4 B")) + `</div>`))
		case "/tasks/t-1/thread", "/tasks/t-1/changes":
			_, _ = w.Write([]byte(`<div></div>`))
		default:
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/api/tasks/t-1/lifecycle-executions" {
				lifecycleProject = r.URL.Query().Get("project_id")
			}
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := c.GetTaskForProject(context.Background(), "t-1", "p1")
	if err != nil {
		t.Fatalf("GetTaskForProject: %v", err)
	}
	if detailProject != "p1" || lifecycleProject != "p1" {
		t.Errorf("detail/lifecycle project scopes = %q/%q, want p1/p1", detailProject, lifecycleProject)
	}
	if len(detail.Attachments) != 1 || detail.Attachments[0].ID != "att-1" || detail.Attachments[0].FileName != "notes.md" {
		t.Errorf("detail attachments = %+v", detail.Attachments)
	}
}

func TestGetTaskKeepsLegacyAttachmentTextWithoutStructuredScope(t *testing.T) {
	var detailProjectQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tasks/t-1":
			detailProjectQuery = r.URL.Query().Get("project_id")
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div data-task-status="running"><h2 class="font-bold">Task</h2><div id="tab-details">details</div><div id="tab-attachments">legacy attachment text<div id="attachment-list" data-project-id="p1"><div class="attachment-row"><p class="font-medium">secret.txt</p><p class="text-xs">6 B</p><button hx-delete="/attachments/att-1?project_id=p1"></button></div></div></div></div>`))
		case "/tasks/t-1/thread", "/tasks/t-1/changes":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div></div>`))
		case "/api/tasks/t-1/lifecycle-executions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := c.GetTask(context.Background(), "t-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detailProjectQuery != "" {
		t.Fatalf("legacy detail request unexpectedly supplied project_id=%q", detailProjectQuery)
	}
	if !strings.Contains(detail.Attach, "legacy attachment text") {
		t.Errorf("legacy attachment tab text = %q, want rendered attachment text", detail.Attach)
	}
	if len(detail.Attachments) != 0 {
		t.Fatalf("legacy detail exposed structured attachments: %+v", detail.Attachments)
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
