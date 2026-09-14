package terminal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

// TestProjectsListPerformanceEvidence is an opt-in comparison harness for the
// headless project-list preload optimization. Run it on the current checkout
// with mode=optimized and on the historical checkout with mode=redundant using
// the same fixed-delay server and record sizes.
func TestProjectsListPerformanceEvidence(t *testing.T) {
	if os.Getenv("OPENVIBELY_PROJECTS_PERF_EVIDENCE") != "1" {
		t.Skip("set OPENVIBELY_PROJECTS_PERF_EVIDENCE=1 to run the project-list performance harness")
	}
	mode := strings.TrimSpace(os.Getenv("OPENVIBELY_PROJECTS_PERF_MODE"))
	if mode == "" {
		mode = "optimized"
	}
	if mode != "optimized" && mode != "redundant" {
		t.Fatalf("OPENVIBELY_PROJECTS_PERF_MODE = %q, want optimized or redundant", mode)
	}
	runs := projectsPerformanceEnvInt(t, "OPENVIBELY_PROJECTS_PERF_RUNS", 7)
	if runs < 3 {
		t.Fatalf("OPENVIBELY_PROJECTS_PERF_RUNS = %d, want at least 3", runs)
	}
	delay := time.Duration(projectsPerformanceEnvInt(t, "OPENVIBELY_PROJECTS_PERF_DELAY_MS", 2)) * time.Millisecond
	catalogDelay := time.Duration(projectsPerformanceEnvInt(t, "OPENVIBELY_PROJECTS_PERF_CATALOG_DELAY_MS", int(delay/time.Millisecond))) * time.Millisecond
	capacityDelay := time.Duration(projectsPerformanceEnvInt(t, "OPENVIBELY_PROJECTS_PERF_CAPACITY_DELAY_MS", int(delay/time.Millisecond))) * time.Millisecond

	for _, records := range []int{10, 100, 1000} {
		catalog, capacities := projectsPerformanceFixtures(records)
		for _, jsonMode := range []bool{true, false} {
			name := fmt.Sprintf("records=%d/mode=%s/output=%s", records, mode, projectsPerformanceOutputName(jsonMode))
			t.Run(name, func(t *testing.T) {
				var catalogRequests, capacityRequests atomic.Int32
				var catalogBytes, capacityBytes atomic.Int64
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/projects":
						if catalogDelay > 0 {
							time.Sleep(catalogDelay)
						}
						catalogRequests.Add(1)
						catalogBytes.Add(int64(len(catalog)))
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, catalog)
					case "/api/capacity/projects":
						if capacityDelay > 0 {
							time.Sleep(capacityDelay)
						}
						capacityRequests.Add(1)
						capacityBytes.Add(int64(len(capacities)))
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, capacities)
					default:
						http.NotFound(w, r)
					}
				}))
				defer srv.Close()

				c, err := client.New(srv.URL)
				if err != nil {
					t.Fatal(err)
				}
				durations := make([]time.Duration, 0, runs)
				for i := 0; i < runs; i++ {
					catalogRequests.Store(0)
					capacityRequests.Store(0)
					catalogBytes.Store(0)
					capacityBytes.Store(0)
					start := time.Now()
					if err := RunCLI(c, &bytes.Buffer{}, "", []string{"projects", "list"}, false, jsonMode); err != nil {
						t.Fatalf("RunCLI iteration %d failed: %v", i+1, err)
					}
					durations = append(durations, time.Since(start))
				}
				sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
				median := durations[len(durations)/2]
				wantCatalogRequests := int32(1)
				if mode == "redundant" {
					wantCatalogRequests = 2
				}
				if got := catalogRequests.Load(); got != wantCatalogRequests {
					t.Fatalf("catalog requests = %d, want %d", got, wantCatalogRequests)
				}
				wantCapacityRequests := int32(0)
				if !jsonMode {
					wantCapacityRequests = 1
				}
				if got := capacityRequests.Load(); got != wantCapacityRequests {
					t.Fatalf("capacity requests = %d, want %d", got, wantCapacityRequests)
				}

				catalogBytesPerRun := catalogBytes.Load()
				capacityBytesPerRun := capacityBytes.Load()
				catalogRequestsPerRun := catalogRequests.Load()
				capacityRequestsPerRun := capacityRequests.Load()
				if got, want := catalogBytesPerRun, int64(len(catalog))*int64(wantCatalogRequests); got != want {
					t.Fatalf("catalog response bytes = %d, want %d", got, want)
				}
				if !jsonMode {
					if got, want := capacityBytesPerRun, int64(len(capacities)); got != want {
						t.Fatalf("capacity response bytes = %d, want %d", got, want)
					}
				}
				var allocErr error
				allocs := testing.AllocsPerRun(3, func() {
					if err := RunCLI(c, io.Discard, "", []string{"projects", "list"}, false, jsonMode); err != nil {
						allocErr = err
					}
				})
				if allocErr != nil {
					t.Fatalf("allocation run failed: %v", allocErr)
				}
				t.Logf("projects_perf mode=%s records=%d output=%s catalog_delay=%s capacity_delay=%s runs=%d catalog_bytes=%d capacity_bytes=%d catalog_requests=%d capacity_requests=%d median=%s allocs=%.0f durations=%v", mode, records, projectsPerformanceOutputName(jsonMode), catalogDelay, capacityDelay, runs, catalogBytesPerRun, capacityBytesPerRun, catalogRequestsPerRun, capacityRequestsPerRun, median, allocs, durations)
			})
		}
	}
}

func projectsPerformanceFixtures(records int) (string, string) {
	projects := make([]client.Project, records)
	capacities := make([]client.ProjectCapacity, records)
	for i := range records {
		id := fmt.Sprintf("project-%04d", i)
		projects[i] = client.Project{ID: id, Name: fmt.Sprintf("Project %04d", i), Path: fmt.Sprintf("/workspace/projects/%04d", i)}
		capacities[i] = client.ProjectCapacity{ID: id, Name: projects[i].Name, Running: i % 7, QueueSize: i % 11}
	}
	catalog, _ := json.Marshal(struct {
		Projects []client.Project `json:"projects"`
	}{Projects: projects})
	capacity, _ := json.Marshal(capacities)
	return string(catalog), string(capacity)
}

func projectsPerformanceOutputName(jsonMode bool) string {
	if jsonMode {
		return "json"
	}
	return "plain"
}

func projectsPerformanceEnvInt(t *testing.T, key string, fallback int) int {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("%s = %q: %v", key, value, err)
	}
	return parsed
}
