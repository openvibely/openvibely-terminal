package terminal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

type reviewWorkflowBackend struct {
	mu          sync.Mutex
	comments    []client.ReviewComment
	deleteIDs   []string
	submitCount int
	followUps   int
	failSubmit  bool
	failDelete  bool
}

func (b *reviewWorkflowBackend) serveHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(cliProjects))
	case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog":
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("project_id") != "p1" {
			_, _ = w.Write([]byte(`{"tasks":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"tasks":[{"id":"t-1","project_id":"p1","title":"Target task","category":"active","status":"running"}]}`))
	case r.Method == http.MethodGet && r.URL.Path == "/tasks/t-1/reviews":
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(reviewWorkflowHTML(b.comments)))
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/reviews/"):
		if r.URL.Query().Get("project_id") != "p1" {
			http.Error(w, `{"error":"wrong project"}`, http.StatusNotFound)
			return
		}
		if b.failDelete {
			http.Error(w, `{"error":"delete unavailable"}`, http.StatusBadGateway)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/reviews/")
		b.deleteIDs = append(b.deleteIDs, id)
		filtered := make([]client.ReviewComment, 0, len(b.comments))
		for _, comment := range b.comments {
			if comment.ID != id {
				filtered = append(filtered, comment)
			}
		}
		b.comments = filtered
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && r.URL.Path == "/tasks/t-1/reviews/submit":
		b.submitCount++
		if r.URL.Query().Get("project_id") != "p1" {
			http.Error(w, `{"error":"wrong project"}`, http.StatusNotFound)
			return
		}
		if b.failSubmit {
			http.Error(w, `{"error":"task thread unavailable"}`, http.StatusBadGateway)
			return
		}
		if len(b.comments) == 0 {
			http.Error(w, `{"error":"no drafts"}`, http.StatusBadRequest)
			return
		}
		b.followUps++
		b.comments = []client.ReviewComment{}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func reviewWorkflowHTML(comments []client.ReviewComment) string {
	var builder strings.Builder
	builder.WriteString(`<div id="review-comments-list" data-task-id="t-1">`)
	for _, comment := range comments {
		fmt.Fprintf(&builder, `<div class="review-comment-item" data-comment-id="%s" data-file-path="%s" data-line-number="%d" data-line-type="new" data-state="open"><p>%s</p></div>`, comment.ID, comment.FilePath, comment.LineNumber, comment.CommentText)
	}
	builder.WriteString(`</div>`)
	return builder.String()
}

func newReviewWorkflowBackend(t *testing.T, comments ...client.ReviewComment) (*client.Client, *reviewWorkflowBackend) {
	t.Helper()
	backend := &reviewWorkflowBackend{comments: append([]client.ReviewComment(nil), comments...)}
	server := httptest.NewServer(http.HandlerFunc(backend.serveHTTP))
	t.Cleanup(server.Close)
	c, err := client.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c, backend
}

func reviewWorkflowComments() []client.ReviewComment {
	return []client.ReviewComment{
		{ID: "rc-1", TaskID: "t-1", FilePath: "internal/a.go", LineNumber: 12, CommentText: "first draft", State: "open"},
		{ID: "rc-2", TaskID: "t-1", FilePath: "internal/b.go", LineNumber: 34, CommentText: "second draft", State: "open"},
	}
}

func TestCLITaskReviewsRemoveDeletesOnlyResolvedComment(t *testing.T) {
	c, backend := newReviewWorkflowBackend(t, reviewWorkflowComments()...)
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "reviews", "remove", "t-1", "rc-1"}, false, true); err != nil {
		t.Fatalf("remove failed: %v\noutput: %s", err, out.String())
	}
	if len(backend.deleteIDs) != 1 || backend.deleteIDs[0] != "rc-1" {
		t.Fatalf("deleted IDs = %v, want [rc-1]", backend.deleteIDs)
	}
	if len(backend.comments) != 1 || backend.comments[0].ID != "rc-2" {
		t.Fatalf("remaining comments = %+v, want only rc-2", backend.comments)
	}
	var got taskReviewMutationJSON
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &got); err != nil {
		t.Fatalf("output is not mutation JSON: %v\n%s", err, out.String())
	}
	if got.Status != "removed" || got.TaskID != "t-1" || got.CommentID != "rc-1" || got.RemainingComments == nil || len(*got.RemainingComments) != 1 || (*got.RemainingComments)[0].ID != "rc-2" {
		t.Fatalf("mutation output = %+v", got)
	}
}

func TestTUITaskReviewsRemoveDeletesOnlyResolvedComment(t *testing.T) {
	c, backend := newReviewWorkflowBackend(t, reviewWorkflowComments()...)
	m := newTestModel(t)
	m.client = c
	m.selectedID = "p1"
	m.selectedName = "demo"
	m = runLine(t, m, "/tasks reviews remove t-1 rc-1")
	if len(backend.deleteIDs) != 1 || backend.deleteIDs[0] != "rc-1" || len(backend.comments) != 1 || backend.comments[0].ID != "rc-2" {
		t.Fatalf("TUI removal deleted %v and left %+v", backend.deleteIDs, backend.comments)
	}
}

func TestCLITaskReviewsSubmitQueuesOneFollowUpAndReportsClearedAfterAcceptance(t *testing.T) {
	c, backend := newReviewWorkflowBackend(t, reviewWorkflowComments()...)
	var out bytes.Buffer
	if err := RunCLI(c, &out, "demo", []string{"tasks", "reviews", "submit", "t-1"}, true, true); err != nil {
		t.Fatalf("submit failed: %v\noutput: %s", err, out.String())
	}
	if backend.submitCount != 1 || backend.followUps != 1 {
		t.Fatalf("submissions/follow-ups = %d/%d, want 1/1", backend.submitCount, backend.followUps)
	}
	if len(backend.comments) != 0 {
		t.Fatalf("accepted submit left comments uncleared: %+v", backend.comments)
	}
	var got taskReviewMutationJSON
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &got); err != nil {
		t.Fatalf("output is not mutation JSON: %v\n%s", err, out.String())
	}
	if got.Status != "submitted" || got.TaskID != "t-1" || got.SubmittedComments != 2 || got.CommentsCleared != 2 || got.TaskThread != "/tasks/t-1/thread" {
		t.Fatalf("mutation output = %+v", got)
	}
}

func TestTUITaskReviewSubmissionQueuesFollowUpAfterConfirmation(t *testing.T) {
	c, backend := newReviewWorkflowBackend(t, reviewWorkflowComments()...)
	m := newTestModel(t)
	m.client = c
	m.selectedID = "p1"
	m.selectedName = "demo"

	m, resolveCmd := typeLine(t, m, "/tasks reviews submit t-1")
	if resolveCmd == nil {
		t.Fatal("submit command did not resolve its target")
	}
	next, confirmCmd := m.Update(resolveCmd())
	m = next.(Model)
	if confirmCmd != nil || m.pendingConfirmation == nil {
		t.Fatalf("expected pending confirmation before submit: %s", transcript(m))
	}
	m, submitCmd := typeLine(t, m, "yes")
	if submitCmd == nil {
		t.Fatal("yes did not release the submission")
	}
	result, ok := submitCmd().(resultMsg)
	if !ok || result.err != nil {
		t.Fatalf("submission result = %#v", result)
	}
	if backend.submitCount != 1 || backend.followUps != 1 || len(backend.comments) != 0 {
		t.Fatalf("confirmed submission state: requests=%d follow-ups=%d comments=%d", backend.submitCount, backend.followUps, len(backend.comments))
	}
	if !strings.Contains(result.body, "/tasks/t-1/thread") || !strings.Contains(result.body, "cleared 2") {
		t.Fatalf("submission result does not report the accepted thread follow-up: %q", result.body)
	}
}

func TestTaskReviewSubmissionRequiresTUIConfirmationAndCLIForceGate(t *testing.T) {
	c, backend := newReviewWorkflowBackend(t, reviewWorkflowComments()...)
	m := newTestModel(t)
	m.client = c
	m.selectedID = "p1"
	m.selectedName = "demo"

	m, cmd := typeLine(t, m, "/tasks reviews submit t-1")
	if cmd == nil {
		t.Fatal("submit command did not resolve its target")
	}
	msg := cmd()
	next, confirmCmd := m.Update(msg)
	m = next.(Model)
	if confirmCmd != nil || m.pendingConfirmation == nil {
		t.Fatalf("expected pending TUI confirmation, cmd=%v model=%s", confirmCmd != nil, transcript(m))
	}
	if !strings.Contains(m.pendingConfirmation.message, "Submit 2 draft review comments") {
		t.Fatalf("confirmation prompt = %q", m.pendingConfirmation.message)
	}
	next, confirmCmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if confirmCmd != nil || m.pendingConfirmation != nil || backend.submitCount != 0 {
		t.Fatalf("refused confirmation submitted drafts: cmd=%v pending=%v count=%d", confirmCmd != nil, m.pendingConfirmation != nil, backend.submitCount)
	}

	var out bytes.Buffer
	err := RunCLI(c, &out, "demo", []string{"tasks", "reviews", "submit", "t-1"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("CLI without force error = %v, want --force hint", err)
	}
	if backend.submitCount != 0 {
		t.Fatalf("omitted --force submitted drafts %d times", backend.submitCount)
	}
}

func TestTaskReviewsRejectEmptyAmbiguousBackendFailureAndWrongProject(t *testing.T) {
	comment := client.ReviewComment{ID: "rc-1", TaskID: "t-1", FilePath: "internal/a.go", LineNumber: 12, CommentText: "draft", State: "open"}
	t.Run("empty drafts", func(t *testing.T) {
		c, backend := newReviewWorkflowBackend(t)
		var out bytes.Buffer
		err := RunCLI(c, &out, "demo", []string{"tasks", "reviews", "submit", "t-1"}, true, true)
		if err == nil || !strings.Contains(err.Error(), "no draft review comments") {
			t.Fatalf("error = %v, want empty draft error", err)
		}
		if backend.submitCount != 0 || strings.Contains(out.String(), `"status":"submitted"`) {
			t.Fatalf("empty submit claimed success: count=%d output=%s", backend.submitCount, out.String())
		}
	})

	t.Run("ambiguous file line", func(t *testing.T) {
		first := comment
		second := comment
		second.ID = "rc-2"
		c, backend := newReviewWorkflowBackend(t, first, second)
		var out bytes.Buffer
		err := RunCLI(c, &out, "demo", []string{"tasks", "reviews", "remove", "t-1", "internal/a.go:12"}, false, true)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
			t.Fatalf("error = %v, want ambiguous comment error", err)
		}
		if len(backend.deleteIDs) != 0 {
			t.Fatalf("ambiguous reference deleted comments: %v", backend.deleteIDs)
		}
	})

	t.Run("missing comment reference", func(t *testing.T) {
		c, backend := newReviewWorkflowBackend(t, comment)
		var out bytes.Buffer
		err := RunCLI(c, &out, "demo", []string{"tasks", "reviews", "remove", "t-1", "unknown-comment"}, false, true)
		if err == nil || !strings.Contains(err.Error(), "nothing matches review comment") {
			t.Fatalf("error = %v, want missing comment error", err)
		}
		if len(backend.deleteIDs) != 0 {
			t.Fatalf("missing reference deleted comments: %v", backend.deleteIDs)
		}
	})

	t.Run("backend delete failure", func(t *testing.T) {
		c, backend := newReviewWorkflowBackend(t, comment)
		backend.failDelete = true
		var out bytes.Buffer
		err := RunCLI(c, &out, "demo", []string{"tasks", "reviews", "remove", "t-1", "rc-1"}, false, true)
		if err == nil || !strings.Contains(err.Error(), "delete unavailable") {
			t.Fatalf("error = %v, want backend failure", err)
		}
		if len(backend.comments) != 1 || len(backend.deleteIDs) != 0 || strings.Contains(out.String(), `"status":"removed"`) {
			t.Fatalf("failed delete claimed success or changed comments: %+v output=%s", backend.comments, out.String())
		}
	})

	t.Run("backend submit failure", func(t *testing.T) {
		c, backend := newReviewWorkflowBackend(t, comment)
		backend.failSubmit = true
		var out bytes.Buffer
		err := RunCLI(c, &out, "demo", []string{"tasks", "reviews", "submit", "t-1"}, true, true)
		if err == nil || !strings.Contains(err.Error(), "task thread unavailable") {
			t.Fatalf("error = %v, want backend failure", err)
		}
		if backend.submitCount != 1 || backend.followUps != 0 || len(backend.comments) != 1 {
			t.Fatalf("failed submit state: requests=%d follow-ups=%d comments=%d", backend.submitCount, backend.followUps, len(backend.comments))
		}
		if strings.Contains(out.String(), `"status":"submitted"`) || strings.Contains(out.String(), `"comments_cleared"`) {
			t.Fatalf("failed submit claimed success: %s", out.String())
		}
	})

	t.Run("wrong project task", func(t *testing.T) {
		c, backend := newReviewWorkflowBackend(t, comment)
		var out bytes.Buffer
		err := RunCLI(c, &out, "demo", []string{"tasks", "reviews", "submit", "foreign-task"}, true, true)
		if err == nil || !strings.Contains(err.Error(), "nothing matches") {
			t.Fatalf("error = %v, want project scoped task resolution failure", err)
		}
		if backend.submitCount != 0 {
			t.Fatalf("wrong project task submitted drafts %d times", backend.submitCount)
		}
	})
}
