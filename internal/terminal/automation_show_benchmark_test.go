package terminal

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

// BenchmarkAutomationShowCanonicalID compares the former full-catalog lookup
// followed by detail loading with the show/open canonical-ID path. Each pair
// uses the same 20 measured requests against an identical paginated fixture.
// Custom metrics retain per-call request, wire-byte, latency percentile, and
// allocation evidence alongside the usual benchmark output.
func BenchmarkAutomationShowCanonicalID(b *testing.B) {
	const samples = 20
	for _, total := range []int{10, 100, 1000} {
		for _, delay := range []time.Duration{0, 25 * time.Millisecond} {
			for _, action := range []string{"show", "open"} {
				for _, path := range []string{"legacy-catalog", "canonical-id"} {
					name := fmt.Sprintf("cards=%d/delay=%dms/action=%s/path=%s", total, delay.Milliseconds(), action, path)
					b.Run(name, func(b *testing.B) {
						id := fmt.Sprintf("au-%04d", total-1)
						var requests, catalogRequests, catalogBytes, responseBytes atomic.Int64
						srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							requests.Add(1)
							var body string
							switch {
							case r.Method == http.MethodGet && r.URL.Path == "/automations":
								catalogRequests.Add(1)
								offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
								end := offset + 50
								if end > total {
									end = total
								}
								body = benchmarkAutomationPage(offset, end, total)
								w.Header().Set("X-OpenVibely-Card-Page-Has-More", strconv.FormatBool(end < total))
								w.Header().Set("X-OpenVibely-Card-Page-Total", strconv.Itoa(total))
								if delay > 0 {
									time.Sleep(delay)
								}
							case r.Method == http.MethodGet && r.URL.Path == "/automations/"+id:
								body = automationDetailHTML(id, "p1", fmt.Sprintf("Automation %04d", total-1))
							default:
								http.NotFound(w, r)
								return
							}
							responseBytes.Add(int64(len(body)))
							if r.URL.Path == "/automations" {
								catalogBytes.Add(int64(len(body)))
							}
							_, _ = io.WriteString(w, body)
						}))
						defer srv.Close()

						c, err := client.New(srv.URL)
						if err != nil {
							b.Fatal(err)
						}
						legacy := path == "legacy-catalog"
						latencies := make([]time.Duration, 0, samples)
						var allocatedBytes, allocatedObjects uint64
						var memoryBefore, memoryAfter runtime.MemStats
						b.ResetTimer()
						runtime.ReadMemStats(&memoryBefore)
						for i := 0; i < samples; i++ {
							started := time.Now()
							if legacy {
								automation, resolveErr := resolveAutomationRef(context.Background(), c, "p1", id)
								if resolveErr != nil {
									b.Fatal(resolveErr)
								}
								if _, err = loadAutomationDetail(context.Background(), c, "p1", automation); err != nil {
									b.Fatal(err)
								}
							} else if _, err = loadAutomationDetailForRef(context.Background(), c, "p1", id); err != nil {
								b.Fatal(err)
							}
							latencies = append(latencies, time.Since(started))
						}
						runtime.ReadMemStats(&memoryAfter)
						b.StopTimer()

						allocatedBytes = memoryAfter.TotalAlloc - memoryBefore.TotalAlloc
						allocatedObjects = memoryAfter.Mallocs - memoryBefore.Mallocs
						b.ReportMetric(float64(samples), "samples")
						b.ReportMetric(float64(requests.Load())/samples, "requests/op")
						b.ReportMetric(float64(catalogRequests.Load())/samples, "catalog-requests/op")
						b.ReportMetric(float64(catalogBytes.Load())/samples, "catalog-B/op")
						b.ReportMetric(float64(responseBytes.Load())/samples, "response-B/op")
						b.ReportMetric(float64(allocatedBytes)/samples, "allocated-B/op")
						b.ReportMetric(float64(allocatedObjects)/samples, "objects/op")
						b.ReportMetric(float64(percentileDuration(latencies, 0.50).Microseconds())/1000, "median-ms")
						b.ReportMetric(float64(percentileDuration(latencies, 0.95).Microseconds())/1000, "p95-ms")
					})
				}
			}
		}
	}
}

func benchmarkAutomationPage(start, end, total int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<div data-card-pagination-root data-card-pagination-card-selector="[data-automation-url]" data-card-pagination-key="data-automation-url" data-card-pagination-has-more="%t" data-card-pagination-total="%d">`, end < total, total)
	for i := start; i < end; i++ {
		b.WriteString(automationCardHTML(fmt.Sprintf("au-%04d", i), fmt.Sprintf("Automation %04d", i), "active"))
	}
	b.WriteString(`</div>`)
	return b.String()
}

func percentileDuration(values []time.Duration, percentile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(float64(len(sorted)-1) * percentile)
	return sorted[index]
}
