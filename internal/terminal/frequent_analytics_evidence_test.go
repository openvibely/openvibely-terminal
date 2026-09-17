package terminal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

const (
	frequentAnalyticsFixtureSize          = 5000
	frequentAnalyticsReviewedResponseSize = 581673
	frequentAnalyticsShortTitleRecords    = 2221
)

// evidenceFrequentAnalyticsFixture is intentionally shared by the target
// performance test and the historical comparison harness. Its first 2,221
// titles use the compact equivalent spelling needed to reproduce the reviewed
// 581,673-byte full response while retaining unique task IDs and stable data.
func evidenceFrequentAnalyticsFixture(count int) []client.TaskFrequency {
	items := make([]client.TaskFrequency, count)
	for i := range items {
		title := fmt.Sprintf("task title %05d", i)
		if i < frequentAnalyticsShortTitleRecords {
			title = fmt.Sprintf("task title %04d", i)
		}
		items[i] = client.TaskFrequency{
			TaskID:         fmt.Sprintf("task-%05d", i),
			TaskTitle:      title,
			ExecutionCount: count - i,
			LastExecutedAt: "2026-09-13T12:34:56Z",
		}
	}
	return items
}

// TestFrequentAnalyticsHistoricalComparisonEvidence runs the same committed
// harness at the optimized target and at the named pre-optimization ref. The
// baseline invocation is run from a detached checkout with this file copied
// into it; the baseline product tree is otherwise unchanged.
func TestFrequentAnalyticsHistoricalComparisonEvidence(t *testing.T) {
	mode := strings.TrimSpace(os.Getenv("OPENVIBELY_ANALYTICS_HISTORICAL_MODE"))
	if mode == "" {
		t.Skip("set OPENVIBELY_ANALYTICS_HISTORICAL_MODE=target or baseline to run historical comparison evidence")
	}
	if mode != "target" && mode != "baseline" {
		t.Fatalf("OPENVIBELY_ANALYTICS_HISTORICAL_MODE = %q, want target or baseline", mode)
	}
	baselineRef := strings.TrimSpace(os.Getenv("OPENVIBELY_ANALYTICS_BASELINE_REF"))
	if baselineRef == "" {
		t.Fatal("OPENVIBELY_ANALYTICS_BASELINE_REF is required")
	}
	const runs = 20
	const limit = 12

	history := evidenceFrequentAnalyticsFixture(frequentAnalyticsFixtureSize)
	fullBody, err := json.Marshal(history)
	if err != nil {
		t.Fatalf("marshal full fixture: %v", err)
	}
	if len(fullBody) != frequentAnalyticsReviewedResponseSize {
		t.Fatalf("full fixture response bytes = %d, want reviewed workload %d", len(fullBody), frequentAnalyticsReviewedResponseSize)
	}
	boundedBody, err := json.Marshal(history[:limit])
	if err != nil {
		t.Fatalf("marshal bounded fixture: %v", err)
	}

	var responseBytes atomic.Int64
	var responseRows atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queryLimit := r.URL.Query().Get("limit")
		wantLimit := ""
		body := fullBody
		rows := len(history)
		if mode == "target" {
			wantLimit = strconv.Itoa(limit)
			body = boundedBody
			rows = limit
		}
		if queryLimit != wantLimit {
			t.Errorf("mode=%s limit=%q, want %q", mode, queryLimit, wantLimit)
			return
		}
		if got := r.URL.Query().Get("project_id"); got != "historical-evidence-project" {
			t.Errorf("project_id = %q, want historical-evidence-project", got)
			return
		}
		responseBytes.Store(int64(len(body)))
		responseRows.Store(int64(rows))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}

	run := func() (time.Duration, string, error) {
		started := time.Now()
		out, err := loadAnalytics(context.Background(), c, "historical-evidence-project", "frequent")
		return time.Since(started), out, err
	}
	if _, _, err := run(); err != nil {
		t.Fatalf("warm-up: %v", err)
	}

	durations := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		elapsed, out, err := run()
		if err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
		durations = append(durations, elapsed)
		if got := strings.Count(out, "task title"); int64(got) != responseRows.Load() {
			t.Fatalf("run %d rendered rows = %d, want %d", i+1, got, responseRows.Load())
		}
	}

	allocsPerOp := testing.AllocsPerRun(runs, func() {
		if _, _, err := run(); err != nil {
			panic(err)
		}
	})
	allocatedBytesPerOp, mallocsPerOp := measureHistoricalAnalyticsAllocations(runs, func() {
		if _, _, err := run(); err != nil {
			panic(err)
		}
	})
	p50, p95 := historicalAnalyticsPercentiles(durations)
	t.Logf("frequent_analytics_historical mode=%s baseline_ref=%s fixture=%d limit=%d runs=%d transport=httptest.Server/reused-client/sequential/background-context/no-delay go=%s goos=%s goarch=%s gomaxprocs=%d response_bytes=%d decoded_rows=%d p50=%s p95=%s allocated_bytes_per_op=%.0f allocs_per_op=%.1f mallocs_per_op=%.1f durations=%v", mode, baselineRef, frequentAnalyticsFixtureSize, limit, runs, runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0), responseBytes.Load(), responseRows.Load(), p50, p95, allocatedBytesPerOp, allocsPerOp, mallocsPerOp, durations)

	if mode == "target" {
		if responseBytes.Load() != int64(len(boundedBody)) || responseRows.Load() != limit {
			t.Fatalf("target response boundary bytes=%d rows=%d, want bytes=%d rows=%d", responseBytes.Load(), responseRows.Load(), len(boundedBody), limit)
		}
	} else if responseBytes.Load() != int64(len(fullBody)) || responseRows.Load() != frequentAnalyticsFixtureSize {
		t.Fatalf("baseline response boundary bytes=%d rows=%d, want bytes=%d rows=%d", responseBytes.Load(), responseRows.Load(), len(fullBody), frequentAnalyticsFixtureSize)
	}
}

func measureHistoricalAnalyticsAllocations(runs int, operation func()) (bytesPerOp, mallocsPerOp float64) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < runs; i++ {
		operation()
	}
	runtime.ReadMemStats(&after)
	return float64(after.TotalAlloc-before.TotalAlloc) / float64(runs), float64(after.Mallocs-before.Mallocs) / float64(runs)
}

func historicalAnalyticsPercentiles(values []time.Duration) (time.Duration, time.Duration) {
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2], sorted[(len(sorted)*95)/100]
}
