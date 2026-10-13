package terminal

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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

// TestStatusPerformanceEvidence compares the selected-project capacity fetch
// used by status with the previous full-collection fetch. It is opt-in because
// the controlled response bandwidth intentionally makes the 5,000-project
// full-collection case take several seconds. Run it with
// OPENVIBELY_STATUS_PERF_EVIDENCE=1.
func TestStatusPerformanceEvidence(t *testing.T) {
	if os.Getenv("OPENVIBELY_STATUS_PERF_EVIDENCE") != "1" {
		t.Skip("set OPENVIBELY_STATUS_PERF_EVIDENCE=1 to run the status performance harness")
	}

	const (
		fixedDelay      = 10 * time.Millisecond
		responseBytesPS = 2_000_000
		runs            = 20
	)
	for _, projectCount := range []int{1, 100, 1000, 5000} {
		t.Run(fmt.Sprintf("projects_%d", projectCount), func(t *testing.T) {
			allCapacities, scopedCapacity := statusPerformanceCapacityResponses(projectCount)
			var fullBytes, scopedBytes atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body []byte
				switch r.URL.Path {
				case "/api/capacity/projects":
					body = allCapacities
					fullBytes.Add(int64(len(body)))
				case "/api/capacity/projects/p-target":
					body = scopedCapacity
					scopedBytes.Add(int64(len(body)))
				case "/api/alerts/pending-count":
					body = []byte(`{"count":1}`)
				case "/api/tasks/status-counts":
					body = []byte(`{"active_tasks":2,"queued_tasks":1}`)
				default:
					http.NotFound(w, r)
					return
				}
				time.Sleep(fixedDelay)
				if transferDelay := time.Duration(math.Ceil(float64(len(body)) / float64(responseBytesPS) * float64(time.Second))); transferDelay > 0 {
					time.Sleep(transferDelay)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			}))
			defer srv.Close()

			c, err := client.New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			fullDurations := make([]time.Duration, 0, runs)
			scopedDurations := make([]time.Duration, 0, runs)
			for i := 0; i < runs; i++ {
				// Alternate order to reduce bias from load and connection warmup.
				if i%2 == 0 {
					fullDurations = append(fullDurations, statusPerformanceSnapshot(t, c, false))
					scopedDurations = append(scopedDurations, statusPerformanceSnapshot(t, c, true))
				} else {
					scopedDurations = append(scopedDurations, statusPerformanceSnapshot(t, c, true))
					fullDurations = append(fullDurations, statusPerformanceSnapshot(t, c, false))
				}
			}

			fullP50, fullP95 := statusPerformancePercentiles(fullDurations)
			scopedP50, scopedP95 := statusPerformancePercentiles(scopedDurations)
			fullAllocations := statusPerformanceDecodeAllocs(allCapacities, false)
			scopedAllocations := statusPerformanceDecodeAllocs(scopedCapacity, true)
			fullBytesPerRun := int64(len(allCapacities))
			scopedBytesPerRun := int64(len(scopedCapacity))
			if got := fullBytes.Load(); got != fullBytesPerRun*int64(runs) {
				t.Fatalf("full capacity bytes = %d, want %d", got, fullBytesPerRun*int64(runs))
			}
			if got := scopedBytes.Load(); got != scopedBytesPerRun*int64(runs) {
				t.Fatalf("scoped capacity bytes = %d, want %d", got, scopedBytesPerRun*int64(runs))
			}
			if projectCount == 1 && scopedP95 > fullP95+fullP95/20 {
				t.Fatalf("one-project scoped p95 = %s, full p95 = %s; want within 5%%", scopedP95, fullP95)
			}
			if projectCount == 1000 {
				if scopedBytesPerRun*10 > fullBytesPerRun {
					t.Fatalf("1,000-project scoped capacity bytes = %d, full = %d; want at least 90%% fewer", scopedBytesPerRun, fullBytesPerRun)
				}
				if scopedAllocations*5 > fullAllocations {
					t.Fatalf("1,000-project scoped decode allocations = %.0f, full = %.0f; want at least 80%% fewer", scopedAllocations, fullAllocations)
				}
			}
			if projectCount == 5000 && scopedP95*100 > fullP95*85 {
				t.Fatalf("5,000-project scoped p95 = %s, full = %s; want at least 15%% lower", scopedP95, fullP95)
			}
			t.Logf("status_perf projects=%d runs_per_path=%d fixed_delay=%s response_bandwidth=%dB/s full_p50=%s full_p95=%s scoped_p50=%s scoped_p95=%s full_capacity_bytes=%d scoped_capacity_bytes=%d full_decode_allocs=%.0f scoped_decode_allocs=%.0f",
				projectCount, runs, fixedDelay, responseBytesPS, fullP50, fullP95, scopedP50, scopedP95,
				fullBytesPerRun, scopedBytesPerRun, fullAllocations, scopedAllocations)
		})
	}
}

// statusPerformanceSnapshot executes the application's status snapshot loader
// with either the scoped endpoint or the former full-collection selection path.
func statusPerformanceSnapshot(t *testing.T, c *client.Client, scoped bool) time.Duration {
	t.Helper()
	m := Model{client: c, selectedID: "p-target"}
	capacityLoader := func(ctx context.Context, projectID string) (*client.ProjectCapacity, error) {
		if scoped {
			return c.GetProjectCapacity(ctx, projectID)
		}
		capacities, err := c.GetProjectCapacities(ctx)
		if err != nil {
			return nil, err
		}
		capacity := findProjectCapacity(capacities, projectID)
		if capacity == nil {
			return nil, fmt.Errorf("full capacity response omitted selected project")
		}
		return capacity, nil
	}
	start := time.Now()
	msg := m.fetchStatusSnapshotWithCapacityLoader(false, true, capacityLoader)()
	if _, ok := msg.(statusCountsMsg); !ok {
		t.Fatalf("status snapshot returned %T, want statusCountsMsg", msg)
	}
	return time.Since(start)
}

func statusPerformanceCapacityResponses(projectCount int) ([]byte, []byte) {
	maxWorkers := 4
	capacities := make([]client.ProjectCapacity, projectCount)
	for i := range capacities {
		id := fmt.Sprintf("unrelated-%05d", i)
		name := "unrelated"
		if i == projectCount-1 {
			id = "p-target"
			name = "selected"
		}
		capacities[i] = client.ProjectCapacity{
			ID: id, Name: name, Running: i % 4, QueueSize: i % 3, MaxWorkers: &maxWorkers,
		}
	}
	all, _ := json.Marshal(capacities)
	scoped, _ := json.Marshal(capacities[len(capacities)-1])
	return all, scoped
}

func statusPerformanceDecodeAllocs(body []byte, scoped bool) float64 {
	return testing.AllocsPerRun(20, func() {
		if scoped {
			var capacity client.ProjectCapacity
			if err := json.Unmarshal(body, &capacity); err != nil {
				panic(err)
			}
			return
		}
		var capacities []client.ProjectCapacity
		if err := json.Unmarshal(body, &capacities); err != nil {
			panic(err)
		}
		_ = findProjectCapacity(capacities, "p-target")
	})
}

func statusPerformanceEnvInt(t *testing.T, key string, fallback int) int {
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

func statusPerformancePercentiles(samples []time.Duration) (time.Duration, time.Duration) {
	ordered := append([]time.Duration(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	p50 := ordered[(len(ordered)-1)/2]
	p95Index := int(math.Ceil(0.95*float64(len(ordered)))) - 1
	return p50, ordered[p95Index]
}
