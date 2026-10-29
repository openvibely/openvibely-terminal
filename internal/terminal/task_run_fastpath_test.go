package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/openvibely/openvibely-terminal/internal/client"
)

func TestTaskRunCatalogFastPathMeasurement(t *testing.T) {
	if testing.Short() {
		t.Skip("controlled latency measurement")
	}
	const (
		catalogDelay = 100 * time.Millisecond
		samples      = 10
	)
	for _, size := range []int{100, 1000, 5000} {
		t.Run(fmt.Sprintf("tasks_%d", size), func(t *testing.T) {
			catalogBody, targetID := taskRunMeasurementCatalog(t, size)
			var catalogReads, mutations, responseBytes atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog":
					time.Sleep(catalogDelay)
					catalogReads.Add(1)
					responseBytes.Add(int64(len(catalogBody)))
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, catalogBody)
				case r.Method == http.MethodPost && r.URL.Path == "/tasks/"+targetID+"/run":
					if r.URL.Query().Get("project_id") != "p1" {
						t.Errorf("mutation project_id = %q, want p1", r.URL.Query().Get("project_id"))
					}
					mutations.Add(1)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected measurement request %s %s", r.Method, r.URL.RequestURI())
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			c, err := client.New(server.URL)
			if err != nil {
				t.Fatal(err)
			}

			latencies := make([]time.Duration, 0, samples)
			var responseBytesPerRun []int64
			for i := 0; i < samples; i++ {
				catalogReads.Store(0)
				mutations.Store(0)
				responseBytes.Store(0)
				m := New(c)
				m.selectedID, m.selectedName = "p1", "Measurement"
				updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
				m = updated.(Model)
				start := time.Now()
				m = runLine(t, m, "/tasks run "+targetID)
				latencies = append(latencies, time.Since(start))
				if got := catalogReads.Load(); got != 1 {
					t.Fatalf("candidate catalog GETs = %d, want 1; mutations=%d output=%s", got, mutations.Load(), transcript(m))
				}
				if got := mutations.Load(); got != 1 {
					t.Fatalf("mutation requests = %d, want 1", got)
				}
				responseBytesPerRun = append(responseBytesPerRun, responseBytes.Load())
				if !strings.Contains(transcript(m), "run: Synthetic task "+fmt.Sprintf("%05d", size-1)) {
					t.Fatalf("successful run status/title missing from output")
				}
			}
			sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
			median := (latencies[(samples-1)/2] + latencies[samples/2]) / 2
			p95 := latencies[(samples*95+99)/100-1]
			var minBytes, maxBytes int64
			for i, n := range responseBytesPerRun {
				if i == 0 || n < minBytes {
					minBytes = n
				}
				if i == 0 || n > maxBytes {
					maxBytes = n
				}
			}
			t.Logf("candidate n=%d samples=%d catalog_delay=%s median=%s p95=%s response_body_bytes/run=%d..%d catalog_bytes/run=%d requests=1 GET + 1 POST", size, samples, catalogDelay, median, p95, minBytes, maxBytes, len(catalogBody))
		})
	}
}

func taskRunMeasurementCatalog(t *testing.T, size int) (string, string) {
	t.Helper()
	const prompt = "Review this representative task context, identify the relevant implementation points, preserve existing behavior, and validate the change with focused coverage. "
	tasks := make([]client.Task, size)
	for i := range tasks {
		id := fmt.Sprintf("%032x", i+1)
		attachments := []client.Attachment{
			{ID: fmt.Sprintf("attachment-%05d-a", i), TaskID: id, FileName: "design-notes.md", FilePath: "docs/design-notes.md", MediaType: "text/markdown", FileSize: 32768},
			{ID: fmt.Sprintf("attachment-%05d-b", i), TaskID: id, FileName: "reference-image.png", FilePath: "assets/reference-image.png", MediaType: "image/png", FileSize: 262144},
		}
		tasks[i] = client.Task{
			ID: id, ProjectID: "p1", Title: fmt.Sprintf("Synthetic task %05d", i),
			Prompt: strings.Repeat(prompt, 10), Category: "backlog", Status: "pending", GoalState: "active",
			Badges: []string{"Go", "Priority: medium", "Goal"}, Attachments: attachments,
		}
	}
	targetID := tasks[size-1].ID
	encoded, err := json.Marshal(struct {
		Tasks []client.Task `json:"tasks"`
	}{Tasks: tasks})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded), targetID
}

func TestTasksRunAndStopCanonicalFullIDFastPath(t *testing.T) {
	const taskID = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name   string
		action string
		path   string
	}{
		{name: "run", action: "run", path: "/tasks/" + taskID + "/run"},
		{name: "stop", action: "stop", path: "/tasks/" + taskID + "/cancel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, requests := newTaskRunDispatchFixture(t, "p1", []client.Task{{
				ID: taskID, ProjectID: "p1", Title: "Refreshed canonical title", Category: "active", Status: "pending",
			}}, http.StatusNoContent, http.StatusOK)
			m = runLine(t, m, "/tasks "+tc.action+" "+taskID)
			if requests.count("GET /api/tasks/reference-catalog?project_id=p1") != 1 {
				t.Fatalf("catalog reads = %d, want only the post-mutation refresh; requests=%v", requests.count("GET /api/tasks/reference-catalog"), requests.snapshot())
			}
			if requests.count("POST "+tc.path+"?project_id=p1") != 1 {
				t.Fatalf("mutation target missing or unscoped; requests=%v", requests.snapshot())
			}
			if out := transcript(m); !strings.Contains(out, tc.action+": Refreshed canonical title") {
				t.Fatalf("successful refresh did not supply the displayed title:\n%s", out)
			}
		})
	}
}

func TestTasksRunAndStopCanonicalFullIDErrorsAndRefresh(t *testing.T) {
	const taskID = "0123456789abcdef0123456789abcdef"
	t.Run("unknown canonical ID", func(t *testing.T) {
		m, requests := newTaskRunDispatchFixture(t, "p1", nil, http.StatusNotFound, http.StatusOK)
		m = runLine(t, m, "/tasks run "+taskID)
		if requests.count("GET /api/tasks/reference-catalog") != 0 || requests.count("POST /tasks/"+taskID+"/run?project_id=p1") != 1 {
			t.Fatalf("unknown canonical ID requests = %v", requests.snapshot())
		}
		if !strings.Contains(transcript(m), "nothing matches \""+taskID+"\"") {
			t.Fatalf("unknown ID error changed:\n%s", transcript(m))
		}
	})

	t.Run("project mismatch", func(t *testing.T) {
		m, requests := newTaskRunDispatchFixture(t, "p2", []client.Task{{
			ID: taskID, ProjectID: "p1", Title: "Foreign task", Category: "active", Status: "pending",
		}}, http.StatusNotFound, http.StatusOK)
		m = runLine(t, m, "/tasks run "+taskID)
		if requests.count("GET /api/tasks/reference-catalog") != 0 || requests.count("POST /tasks/"+taskID+"/run?project_id=p2") != 1 {
			t.Fatalf("project-mismatched target was not scoped to the selected project: %v", requests.snapshot())
		}
		if strings.Contains(transcript(m), "Foreign task") || !strings.Contains(transcript(m), "nothing matches \""+taskID+"\"") {
			t.Fatalf("project mismatch did not fail as a missing scoped task:\n%s", transcript(m))
		}
	})

	t.Run("authentication error", func(t *testing.T) {
		m, requests := newTaskRunDispatchFixture(t, "p1", []client.Task{{
			ID: taskID, ProjectID: "p1", Title: "Authorized task", Category: "active", Status: "pending",
		}}, http.StatusUnauthorized, http.StatusOK)
		m = runLine(t, m, "/tasks stop "+taskID)
		if requests.count("GET /api/tasks/reference-catalog") != 0 || requests.count("POST /tasks/"+taskID+"/cancel?project_id=p1") != 1 {
			t.Fatalf("authentication failure requests = %v", requests.snapshot())
		}
		if !strings.Contains(strings.ToLower(transcript(m)), "requires sign-in") {
			t.Fatalf("authentication error was not preserved:\n%s", transcript(m))
		}
	})

	t.Run("mutation error", func(t *testing.T) {
		m, requests := newTaskRunDispatchFixture(t, "p1", []client.Task{{
			ID: taskID, ProjectID: "p1", Title: "Failing task", Category: "active", Status: "pending",
		}}, http.StatusInternalServerError, http.StatusOK)
		m = runLine(t, m, "/tasks run "+taskID)
		if requests.count("GET /api/tasks/reference-catalog") != 0 || requests.count("POST /tasks/"+taskID+"/run?project_id=p1") != 1 {
			t.Fatalf("mutation failure requests = %v", requests.snapshot())
		}
		if !strings.Contains(transcript(m), "server error (500)") {
			t.Fatalf("mutation failure was not preserved:\n%s", transcript(m))
		}
	})

	t.Run("successful mutation with failed refresh", func(t *testing.T) {
		m, requests := newTaskRunDispatchFixture(t, "p1", []client.Task{{
			ID: taskID, ProjectID: "p1", Title: "Task title", Category: "active", Status: "pending",
		}}, http.StatusNoContent, http.StatusInternalServerError)
		m = runLine(t, m, "/tasks run "+taskID)
		if requests.count("GET /api/tasks/reference-catalog?project_id=p1") != 1 || requests.count("POST /tasks/"+taskID+"/run?project_id=p1") != 1 {
			t.Fatalf("refresh failure requests = %v", requests.snapshot())
		}
		out := transcript(m)
		if !strings.Contains(out, "run: "+taskID) || strings.Contains(out, "server error (500)") {
			t.Fatalf("refresh failure replaced successful mutation status:\n%s", out)
		}
	})
}

func TestTasksRunAndStopKeepCatalogResolutionForTitlesAndPartialIDs(t *testing.T) {
	const canonicalID = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name, reference, action, target, title, taskID string
	}{
		{name: "title", reference: "Refactor the API", action: "run", target: "/tasks/t-1/run", title: "Refactor the API", taskID: "t-1"},
		{name: "partial ID", reference: "t-", action: "stop", target: "/tasks/t-1/cancel", title: "Refactor the API", taskID: "t-1"},
		{name: "uppercase full ID is noncanonical", reference: strings.ToUpper(canonicalID), action: "run", target: "/tasks/" + canonicalID + "/run", title: "Canonical task", taskID: canonicalID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, requests := newTaskRunDispatchFixture(t, "p1", []client.Task{{
				ID: tc.taskID, ProjectID: "p1", Title: tc.title, Category: "active", Status: "pending",
			}}, http.StatusNoContent, http.StatusOK)
			m = runLine(t, m, "/tasks "+tc.action+" "+tc.reference)
			if requests.count("GET /api/tasks/reference-catalog?project_id=p1") != 2 {
				t.Fatalf("reference path catalog reads = %d, want lookup plus refresh; requests=%v", requests.count("GET /api/tasks/reference-catalog"), requests.snapshot())
			}
			if requests.count("POST "+tc.target+"?project_id=p1") != 1 {
				t.Fatalf("reference matched the wrong mutation target: %v", requests.snapshot())
			}
			if !strings.Contains(transcript(m), tc.action+": "+tc.title) {
				t.Fatalf("resolved title missing from output:\n%s", transcript(m))
			}
		})
	}
}

func TestTaskRunMutationCancellationBeforeDispatch(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	c, err := client.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = runTaskActionForModel(Model{}, ctx, c, "p1", "run", "0123456789abcdef0123456789abcdef")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled run error = %v, want context.Canceled", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("canceled run sent %d requests, want none", got)
	}
}

type taskRunDispatchRequests struct {
	mu       sync.Mutex
	requests []string
}

func (r *taskRunDispatchRequests) add(request string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, request)
}

func (r *taskRunDispatchRequests) count(request string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, got := range r.requests {
		if got == request {
			count++
		}
	}
	return count
}

func (r *taskRunDispatchRequests) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.requests...)
}

func newTaskRunDispatchFixture(t *testing.T, selectedProject string, tasks []client.Task, mutationStatus, refreshStatus int) (Model, *taskRunDispatchRequests) {
	t.Helper()
	encoded, err := json.Marshal(struct {
		Tasks []client.Task `json:"tasks"`
	}{Tasks: tasks})
	if err != nil {
		t.Fatal(err)
	}
	var requests taskRunDispatchRequests
	var mutationDone atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.Method + " " + r.URL.RequestURI())
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/reference-catalog":
			if mutationDone.Load() && refreshStatus != http.StatusOK {
				w.WriteHeader(refreshStatus)
				_, _ = io.WriteString(w, `{"error":"refresh failed"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(encoded)
		case strings.HasPrefix(r.URL.Path, "/tasks/") && (strings.HasSuffix(r.URL.Path, "/run") || strings.HasSuffix(r.URL.Path, "/cancel")):
			taskID := strings.TrimPrefix(r.URL.Path, "/tasks/")
			taskID = strings.TrimSuffix(strings.TrimSuffix(taskID, "/run"), "/cancel")
			foundInProject := false
			for _, task := range tasks {
				if task.ID == taskID && task.ProjectID == r.URL.Query().Get("project_id") {
					foundInProject = true
					break
				}
			}
			if !foundInProject {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"error":"task not found"}`)
				return
			}
			if mutationStatus != http.StatusNoContent {
				w.WriteHeader(mutationStatus)
				if mutationStatus == http.StatusUnauthorized {
					_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
				} else if mutationStatus == http.StatusNotFound {
					_, _ = io.WriteString(w, `{"error":"task not found"}`)
				} else {
					_, _ = io.WriteString(w, `{"error":"mutation failed"}`)
				}
				return
			}
			mutationDone.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected task run request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.selectedID, m.selectedName = selectedProject, "Selected"
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return updated.(Model), &requests
}
