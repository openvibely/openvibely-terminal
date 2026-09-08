package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

// htmlServer serves a fixed HTML body and returns a client pointed at it.
func htmlServer(t *testing.T, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAggregateAlertPagesPreservesFirstSeenAndEmptyShape(t *testing.T) {
	parsePage := func(body string) htmlPage {
		root, err := html.Parse(strings.NewReader(body))
		if err != nil {
			t.Fatalf("parse page: %v", err)
		}
		return htmlPage{root: root}
	}

	pages := []htmlPage{
		parsePage(`<div data-alert-id="a1" data-alert-scroll-anchor="a1" data-alert-scope="global" data-alert-type="custom" data-alert-severity="warning" data-alert-source="first-source" data-alert-decision-state="approved" data-alert-processing-state="claimed" data-search-text="first text"><p class="font-semibold">First title</p></div>`),
		parsePage(`<div data-alert-id="a1" data-alert-scroll-anchor="a1" data-alert-source="later-source"><p class="font-semibold">Later title</p></div><div data-alert-id="a2" data-alert-scroll-anchor="a2"><p class="font-semibold">Second title</p></div>`),
	}
	alerts := aggregateAlertPages(pages, "project-2")
	if len(alerts) != 2 {
		t.Fatalf("alerts = %#v, want two unique alerts", alerts)
	}
	first := alerts[0]
	if first.ID != "a1" || first.Title != "First title" || first.ProjectID != "project-2" || first.Scope != "global" || first.Type != "custom" || first.Severity != "warning" || first.Source != "first-source" || first.DecisionState != "approved" || first.ProcessingState != "claimed" || first.Text != "first text" {
		t.Fatalf("first alert = %#v, want first-page fields", first)
	}
	if alerts[1].ID != "a2" {
		t.Fatalf("second alert = %#v, want a2", alerts[1])
	}

	empty := aggregateAlertPages(nil, "project-2")
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty alerts = %#v, want non-nil empty slice", empty)
	}
	encoded, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty alerts: %v", err)
	}
	if string(encoded) != "[]" {
		t.Fatalf("empty JSON = %s, want []", encoded)
	}
}

func TestListAlertsScrapesCards(t *testing.T) {
	// Mirrors the real alertRow markup.
	const page = `<div>
	  <div class="card" data-alert-id="a1" data-alert-scroll-anchor="a1"
	       data-search-card data-search-text="build failed on main">
	    <div class="card-body">
	      <p class="font-semibold font-bold">Build failed</p>
	      <div><span class="badge badge-ghost badge-sm">task_failed</span>
	           <span class="badge badge-warning badge-sm">pending</span></div>
	      <p class="text-sm opacity-60 mt-1">exit status 1 on main</p>
	    </div>
	    <button data-alert-id="a1">delete</button>
	  </div>
	  <div class="card opacity-60" data-alert-id="a2" data-alert-scroll-anchor="a2"
	       data-search-card data-search-text="review requested">
	    <div class="card-body"><p class="font-semibold">Review requested</p></div>
	  </div>
	</div>`
	c := htmlServer(t, page)

	alerts, err := c.ListAlerts(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 2 {
		t.Fatalf("got %d alerts, want 2: %+v", len(alerts), alerts)
	}
	if alerts[0].ID != "a1" || alerts[0].Title != "Build failed" {
		t.Errorf("alert[0] = %+v", alerts[0])
	}
	if alerts[0].Text != "build failed on main" {
		t.Errorf("text = %q", alerts[0].Text)
	}
	if alerts[0].Message != "exit status 1 on main" {
		t.Errorf("message = %q", alerts[0].Message)
	}
	if alerts[0].Read {
		t.Error("alert a1 should be unread")
	}
	if !alerts[1].Read {
		t.Error("alert a2 (opacity-60) should be read")
	}
	if got := strings.Join(alerts[0].Badges, ","); !strings.Contains(got, "task_failed") {
		t.Errorf("badges = %q", got)
	}
}

func alertListPage(ids []string, hasMore bool) string {
	var body strings.Builder
	fmt.Fprintf(&body, `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-page-size="20" data-card-pagination-has-more="%t">`, hasMore)
	for _, id := range ids {
		fmt.Fprintf(&body, `<div data-alert-id="%s" data-alert-scroll-anchor="%s"><p class="font-semibold">Alert %s</p></div>`, id, id, id)
	}
	body.WriteString(`</div>`)
	return body.String()
}

func TestListAlertsTraversesPagesInStableScopedOrder(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/alerts" || r.URL.Query().Get("project_id") != "project-2" {
			t.Errorf("request was not project scoped: %s", r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "text/html")
		if requests == 1 {
			if r.URL.Query().Get("card_page") != "" {
				t.Errorf("initial request unexpectedly used continuation parameters: %s", r.URL.RequestURI())
			}
			_, _ = io.WriteString(w, alertListPage([]string{"a-1", "shared"}, true))
			return
		}
		q := r.URL.Query()
		if q.Get("card_page") != "1" || q.Get("page") != "1" || q.Get("page_size") != "50" || q.Get("offset") != "2" {
			t.Errorf("continuation query = %s", r.URL.RawQuery)
		}
		w.Header().Set(cardPageMoreHeader, "false")
		_, _ = io.WriteString(w, alertListPage([]string{"shared", "a-3"}, false))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	alerts, err := c.ListAlerts(context.Background(), "project-2")
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	got := make([]string, 0, len(alerts))
	for _, alert := range alerts {
		got = append(got, alert.ID)
		if alert.ProjectID != "project-2" {
			t.Fatalf("alert %s project = %q", alert.ID, alert.ProjectID)
		}
	}
	if want := []string{"a-1", "shared", "a-3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("alert order = %#v, want %#v", got, want)
	}
}

func TestFindAlertByIDStopsOnMatchingPageBoundaries(t *testing.T) {
	const total = 151
	ids := make([]string, total)
	for i := range ids {
		ids[i] = fmt.Sprintf("%032x", i+1)
	}

	tests := []struct {
		name         string
		index        int
		wantRequests int
	}{
		{name: "first", index: 0, wantRequests: 1},
		{name: "fiftieth", index: 49, wantRequests: 1},
		{name: "fifty first", index: 50, wantRequests: 2},
		{name: "middle", index: 100, wantRequests: 3},
		{name: "final", index: 150, wantRequests: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if got := r.URL.Query().Get("project_id"); got != "project/one" {
					t.Errorf("project_id = %q", got)
				}
				offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
				end := offset + cardPageSize
				if end > len(ids) {
					end = len(ids)
				}
				w.Header().Set(cardPageMoreHeader, strconv.FormatBool(end < len(ids)))
				_, _ = io.WriteString(w, alertListPage(ids[offset:end], end < len(ids)))
			}))
			defer srv.Close()
			c, _ := New(srv.URL)

			alert, _, found, err := c.FindAlertByID(context.Background(), ids[tt.index], "project/one")
			if err != nil {
				t.Fatalf("FindAlertByID: %v", err)
			}
			if !found || alert.ID != ids[tt.index] {
				t.Fatalf("alert = %+v, found = %t", alert, found)
			}
			if requests != tt.wantRequests {
				t.Fatalf("requests = %d, want %d", requests, tt.wantRequests)
			}
		})
	}
}

func TestFindAlertByIDUnknownTraversesAllPages(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		hasMore := requests < 3
		w.Header().Set(cardPageMoreHeader, strconv.FormatBool(hasMore))
		_, _ = io.WriteString(w, alertListPage([]string{fmt.Sprintf("%032x", requests)}, hasMore))
	}))
	defer srv.Close()
	c, _ := New(srv.URL)

	alert, _, found, err := c.FindAlertByID(context.Background(), strings.Repeat("f", 32), "p1")
	if err != nil || found || alert.ID != "" {
		t.Fatalf("alert = %+v, found = %t, error = %v", alert, found, err)
	}
	if requests != 3 {
		t.Fatalf("requests = %d, want 3", requests)
	}
}

func TestFindAlertByIDPreservesPaginationFailures(t *testing.T) {
	tests := []struct {
		name  string
		serve func(http.ResponseWriter, *http.Request, int)
		want  string
	}{
		{
			name: "malformed continuation",
			serve: func(w http.ResponseWriter, _ *http.Request, request int) {
				if request == 2 {
					w.Header().Set(cardPageMoreHeader, "invalid")
				}
				_, _ = io.WriteString(w, alertListPage([]string{fmt.Sprintf("%032x", request)}, true))
			},
			want: cardPageMoreHeader,
		},
		{
			name: "backend failure",
			serve: func(w http.ResponseWriter, _ *http.Request, request int) {
				if request == 2 {
					http.Error(w, "failed", http.StatusBadGateway)
					return
				}
				_, _ = io.WriteString(w, alertListPage([]string{fmt.Sprintf("%032x", request)}, true))
			},
			want: "loading card page 2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				tt.serve(w, r, requests)
			}))
			defer srv.Close()
			c, _ := New(srv.URL)
			_, _, _, err := c.FindAlertByID(context.Background(), strings.Repeat("f", 32), "p1")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestFindAlertByIDPreservesCancellationAndAuthentication(t *testing.T) {
	t.Run("authentication", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}))
		defer srv.Close()
		c, _ := New(srv.URL)
		_, _, _, err := c.FindAlertByID(context.Background(), strings.Repeat("f", 32), "p1")
		if !IsAuthRequired(err) {
			t.Fatalf("error = %v, want authentication required", err)
		}
	})

	for _, tt := range []struct {
		name    string
		timeout time.Duration
	}{
		{name: "timeout", timeout: 10 * time.Millisecond},
		{name: "cancellation", timeout: time.Hour},
	} {
		t.Run(tt.name, func(t *testing.T) {
			continued := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("card_page") == "" {
					_, _ = io.WriteString(w, alertListPage([]string{strings.Repeat("1", 32)}, true))
					return
				}
				close(continued)
				<-r.Context().Done()
			}))
			defer srv.Close()
			c, _ := New(srv.URL)
			ctx, cancel := context.WithTimeout(context.Background(), tt.timeout)
			if tt.name == "cancellation" {
				go func() {
					<-continued
					cancel()
				}()
			} else {
				defer cancel()
			}
			_, _, _, err := c.FindAlertByID(ctx, strings.Repeat("f", 32), "p1")
			if err == nil || (!strings.Contains(err.Error(), context.Canceled.Error()) && !strings.Contains(err.Error(), context.DeadlineExceeded.Error())) {
				t.Fatalf("error = %v, want context termination", err)
			}
		})
	}
}

func BenchmarkAlertLookup(b *testing.B) {
	for _, total := range []int{10, 100, 1000} {
		ids := make([]string, total)
		for i := range ids {
			ids[i] = fmt.Sprintf("%032x", i+1)
		}
		for _, delay := range []time.Duration{0, 25 * time.Millisecond} {
			delayName := "no_delay"
			if delay > 0 {
				delayName = "25ms_delay"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if delay > 0 {
					time.Sleep(delay)
				}
				offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
				end := offset + cardPageSize
				if end > len(ids) {
					end = len(ids)
				}
				w.Header().Set(cardPageMoreHeader, strconv.FormatBool(end < len(ids)))
				_, _ = io.WriteString(w, alertListPage(ids[offset:end], end < len(ids)))
			}))
			c, _ := New(srv.URL)
			for _, lookup := range []string{"full", "incremental"} {
				b.Run(fmt.Sprintf("%d/%s/%s", total, delayName, lookup), func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						if lookup == "full" {
							alerts, err := c.ListAlerts(context.Background(), "p1")
							if err != nil || len(alerts) != total {
								b.Fatalf("ListAlerts = %d, %v", len(alerts), err)
							}
						} else {
							alert, _, found, err := c.FindAlertByID(context.Background(), ids[0], "p1")
							if err != nil || !found || alert.ID != ids[0] {
								b.Fatalf("FindAlertByID = %+v, %t, %v", alert, found, err)
							}
						}
					}
				})
			}
			srv.Close()
		}
	}
}

func BenchmarkAlertLookupLatencyPercentiles(b *testing.B) {
	const samples = 21
	for _, total := range []int{10, 100, 1000} {
		ids := make([]string, total)
		for i := range ids {
			ids[i] = fmt.Sprintf("%032x", i+1)
		}
		for _, delay := range []time.Duration{0, 25 * time.Millisecond} {
			delayName := "no_delay"
			if delay > 0 {
				delayName = "25ms_delay"
			}
			b.Run(fmt.Sprintf("%d/%s", total, delayName), func(b *testing.B) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if delay > 0 {
						time.Sleep(delay)
					}
					offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
					end := offset + cardPageSize
					if end > len(ids) {
						end = len(ids)
					}
					w.Header().Set(cardPageMoreHeader, strconv.FormatBool(end < len(ids)))
					_, _ = io.WriteString(w, alertListPage(ids[offset:end], end < len(ids)))
				}))
				defer srv.Close()
				c, err := New(srv.URL)
				if err != nil {
					b.Fatal(err)
				}

				run := func(incremental bool) time.Duration {
					start := time.Now()
					if incremental {
						alert, _, found, err := c.FindAlertByID(context.Background(), ids[0], "p1")
						if err != nil || !found || alert.ID != ids[0] {
							b.Fatalf("FindAlertByID = %+v, %t, %v", alert, found, err)
						}
					} else {
						alerts, err := c.ListAlerts(context.Background(), "p1")
						if err != nil || len(alerts) != total {
							b.Fatalf("ListAlerts = %d, %v", len(alerts), err)
						}
					}
					return time.Since(start)
				}

				// Warm both paths before sampling, then alternate their order to avoid
				// systematically favoring either side through connection or CPU drift.
				run(false)
				run(true)
				fullSamples := make([]time.Duration, 0, samples)
				incrementalSamples := make([]time.Duration, 0, samples)
				b.ResetTimer()
				for i := 0; i < samples; i++ {
					if i%2 == 0 {
						fullSamples = append(fullSamples, run(false))
						incrementalSamples = append(incrementalSamples, run(true))
					} else {
						incrementalSamples = append(incrementalSamples, run(true))
						fullSamples = append(fullSamples, run(false))
					}
				}
				b.StopTimer()

				fullMedian := durationPercentile(fullSamples, 50)
				incrementalMedian := durationPercentile(incrementalSamples, 50)
				fullP95 := durationPercentile(fullSamples, 95)
				incrementalP95 := durationPercentile(incrementalSamples, 95)
				medianImprovement := latencyImprovement(fullMedian, incrementalMedian)
				p95Improvement := latencyImprovement(fullP95, incrementalP95)
				b.ReportMetric(float64(fullMedian.Microseconds()), "full-median-us")
				b.ReportMetric(float64(incrementalMedian.Microseconds()), "incremental-median-us")
				b.ReportMetric(float64(fullP95.Microseconds()), "full-p95-us")
				b.ReportMetric(float64(incrementalP95.Microseconds()), "incremental-p95-us")
				b.ReportMetric(medianImprovement, "median-improvement-pct")
				b.ReportMetric(p95Improvement, "p95-improvement-pct")

				if total == 1000 && delay == 25*time.Millisecond && medianImprovement < 80 {
					b.Fatalf("delayed median improvement = %.1f%%, want at least 80%%", medianImprovement)
				}
				if incrementalP95 > fullP95*120/100 && incrementalP95-fullP95 > 2*time.Millisecond {
					b.Fatalf("incremental p95 regressed materially: %s versus %s", incrementalP95, fullP95)
				}
				if total == 10 && incrementalMedian > fullMedian*120/100 && incrementalMedian-fullMedian > 2*time.Millisecond {
					b.Fatalf("small-history median regressed materially: %s versus %s", incrementalMedian, fullMedian)
				}
			})
		}
	}
}

func durationPercentile(samples []time.Duration, percentile int) time.Duration {
	ordered := append([]time.Duration(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := (len(ordered)*percentile+99)/100 - 1
	return ordered[index]
}

func latencyImprovement(before, after time.Duration) float64 {
	return (1 - float64(after)/float64(before)) * 100
}

func TestFindAlertByIDDuplicateCardsKeepFirstCardAndStop(t *testing.T) {
	const target = "0123456789abcdef0123456789abcdef"
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="true">
			<div data-alert-id="`+target+`" data-alert-scroll-anchor="`+target+`"><p class="font-semibold">First card</p></div>
			<div data-alert-id="`+target+`" data-alert-scroll-anchor="`+target+`"><p class="font-semibold">Duplicate card</p></div>
		</div>`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL)

	alert, _, found, err := c.FindAlertByID(context.Background(), target, "p1")
	if err != nil || !found || alert.Title != "First card" {
		t.Fatalf("alert = %+v, found = %t, error = %v", alert, found, err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestFindAlertByIDValidatesMatchingPageMetadataAndLimits(t *testing.T) {
	const target = "0123456789abcdef0123456789abcdef"
	t.Run("matching page malformed metadata", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(cardPageMoreHeader, "true")
			_, _ = io.WriteString(w, `<div data-alert-id="`+target+`" data-alert-scroll-anchor="`+target+`"></div>`)
		}))
		defer srv.Close()
		c, _ := New(srv.URL)
		_, _, _, err := c.FindAlertByID(context.Background(), target, "p1")
		if err == nil || !strings.Contains(err.Error(), "pagination metadata") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("page bound", func(t *testing.T) {
		requests := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests++
			w.Header().Set(cardPageMoreHeader, "true")
			_, _ = io.WriteString(w, alertListPage([]string{fmt.Sprintf("%032x", requests)}, true))
		}))
		defer srv.Close()
		c, _ := New(srv.URL)
		_, _, _, err := c.FindAlertByID(context.Background(), target, "p1")
		if err == nil || !strings.Contains(err.Error(), "card pagination exceeded safety limit") {
			t.Fatalf("error = %v", err)
		}
		if requests != maxCardPages {
			t.Fatalf("requests = %d, want %d", requests, maxCardPages)
		}
	})
}

func TestFindAlertByIDThousandCardHistoryStopsOnPageOne(t *testing.T) {
	const target = "0123456789abcdef0123456789abcdef"
	ids := make([]string, 1000)
	ids[0] = target
	for i := 1; i < len(ids); i++ {
		ids[i] = fmt.Sprintf("%032x", i+1)
	}
	requests, bytesServed := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		end := offset + cardPageSize
		if end > len(ids) {
			end = len(ids)
		}
		page := alertListPage(ids[offset:end], end < len(ids))
		bytesServed += len(page)
		w.Header().Set(cardPageMoreHeader, strconv.FormatBool(end < len(ids)))
		_, _ = io.WriteString(w, page)
	}))
	defer srv.Close()
	c, _ := New(srv.URL)

	alert, _, found, err := c.FindAlertByID(context.Background(), target, "p1")
	if err != nil || !found || alert.ID != target {
		t.Fatalf("FindAlertByID = %+v, %t, %v", alert, found, err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	onePageBytes := bytesServed

	requests, bytesServed = 0, 0
	alerts, err := c.ListAlerts(context.Background(), "p1")
	if err != nil || len(alerts) != len(ids) {
		t.Fatalf("ListAlerts = %d, %v", len(alerts), err)
	}
	if requests != 20 {
		t.Fatalf("full-list requests = %d, want 20", requests)
	}
	if reduction := 1 - float64(onePageBytes)/float64(bytesServed); reduction < 0.90 {
		t.Fatalf("list-byte reduction = %.1f%%, want at least 90%%", reduction*100)
	}
}

func TestListAlertsExactlyTwentyDoesNotContinue(t *testing.T) {
	ids := make([]string, 20)
	for i := range ids {
		ids[i] = fmt.Sprintf("a-%02d", i)
	}
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = io.WriteString(w, alertListPage(ids, false))
	}))
	defer srv.Close()
	c, _ := New(srv.URL)

	alerts, err := c.ListAlerts(context.Background(), "p1")
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(alerts) != 20 || requests != 1 {
		t.Fatalf("alerts = %d, requests = %d; want 20 and 1", len(alerts), requests)
	}
}

func TestListAlertsReportsInvalidContinuations(t *testing.T) {
	tests := []struct {
		name       string
		first      func(http.ResponseWriter)
		continuing func(http.ResponseWriter)
		want       string
	}{
		{
			name: "missing pagination metadata",
			first: func(w http.ResponseWriter) {
				w.Header().Set(cardPageMoreHeader, "true")
				_, _ = io.WriteString(w, `<div data-alert-id="a-1" data-alert-scroll-anchor="a-1"></div>`)
			},
			want: "pagination metadata",
		},
		{
			name: "malformed continuation header",
			first: func(w http.ResponseWriter) {
				_, _ = io.WriteString(w, alertListPage([]string{"a-1"}, true))
			},
			continuing: func(w http.ResponseWriter) {
				w.Header().Set(cardPageMoreHeader, "later")
				_, _ = io.WriteString(w, alertListPage([]string{"a-2"}, false))
			},
			want: cardPageMoreHeader,
		},
		{
			name: "continuation backend error",
			first: func(w http.ResponseWriter) {
				_, _ = io.WriteString(w, alertListPage([]string{"a-1"}, true))
			},
			continuing: func(w http.ResponseWriter) {
				http.Error(w, "continuation unavailable", http.StatusBadGateway)
			},
			want: "loading card page 2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				if requests == 1 {
					tt.first(w)
					return
				}
				tt.continuing(w)
			}))
			defer srv.Close()
			c, _ := New(srv.URL)
			alerts, err := c.ListAlerts(context.Background(), "p1")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("alerts = %#v, error = %v; want error containing %q", alerts, err, tt.want)
			}
		})
	}
}

func TestListAlertsEnforcesTraversalPageBound(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("project_id") != "p1" {
			t.Errorf("request lost project scope: %s", r.URL.RequestURI())
		}
		if requests > 1 {
			w.Header().Set(cardPageMoreHeader, "true")
		}
		_, _ = io.WriteString(w, alertListPage([]string{fmt.Sprintf("a-%03d", requests)}, true))
	}))
	defer srv.Close()
	c, _ := New(srv.URL)

	alerts, err := c.ListAlerts(context.Background(), "p1")
	if err == nil || !strings.Contains(err.Error(), "card pagination exceeded safety limit") {
		t.Fatalf("alerts = %#v, error = %v", alerts, err)
	}
	if requests != maxCardPages {
		t.Fatalf("requests = %d, want bounded traversal of %d pages", requests, maxCardPages)
	}
}

func TestGetAlertDetailUsesProjectScopedRouteAndPreservesDetail(t *testing.T) {
	const body = "# Build failure\n\nThe second line stays\n"
	var requests int
	var gotMethod, gotPath, gotProject, gotAccept, gotHX string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		gotMethod = r.Method
		gotPath = r.URL.EscapedPath()
		gotProject = r.URL.Query().Get("project_id")
		gotAccept = r.Header.Get("Accept")
		gotHX = r.Header.Get("HX-Request")
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<div data-alert-detail-loaded>
			<div data-alert-markdown data-raw-content="# Build failure&#10;&#10;The second line stays&#10;"></div>
			<pre>{
  "attempt": 2,
  "note": "keep  spacing"
}</pre>
			<pre data-alert-copy-text data-alert-copy-base64="ignored"></pre>
		</div>`)
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := c.GetAlertDetail(context.Background(), "alert-1", "project/one")
	if err != nil {
		t.Fatalf("GetAlertDetail: %v", err)
	}
	if requests != 1 {
		t.Fatalf("detail requests = %d, want exactly one", requests)
	}
	if gotMethod != http.MethodGet || gotPath != "/alerts/alert-1/details" {
		t.Fatalf("request = %s %s, want GET /alerts/alert-1/details", gotMethod, gotPath)
	}
	if gotProject != "project/one" {
		t.Fatalf("project_id = %q, want %q", gotProject, "project/one")
	}
	if gotAccept != "text/html" || gotHX != "true" {
		t.Fatalf("headers Accept=%q HX-Request=%q, want text/html/true", gotAccept, gotHX)
	}
	if detail.Body != body {
		t.Fatalf("body = %q, want %q", detail.Body, body)
	}
	if got, ok := detail.Metadata["attempt"].(float64); !ok || got != 2 {
		t.Fatalf("metadata attempt = %#v, want numeric 2", detail.Metadata["attempt"])
	}
	if got, want := detail.Metadata["note"], "keep  spacing"; got != want {
		t.Fatalf("metadata note = %#v, want %q", got, want)
	}
}

func TestGetAlertDetailRepresentsEmptyDetailExplicitly(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/alerts/empty/details" || r.URL.Query().Get("project_id") != "p1" {
			t.Errorf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<p class="text-sm opacity-60">No additional detail.</p>`)
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := c.GetAlertDetail(context.Background(), "empty", "p1")
	if err != nil {
		t.Fatalf("GetAlertDetail: %v", err)
	}
	if requests != 1 {
		t.Fatalf("detail requests = %d, want exactly one", requests)
	}
	if detail.Body != "" {
		t.Fatalf("empty detail body = %q, want empty", detail.Body)
	}
	if detail.Metadata == nil {
		t.Fatal("empty detail metadata must be a non-nil map")
	}
	if len(detail.Metadata) != 0 {
		t.Fatalf("empty detail metadata = %#v, want empty", detail.Metadata)
	}
}

func TestListSkillsReadsDataAttributes(t *testing.T) {
	const page = `<div>
	  <div data-skill-handle="deploy" data-skill-name="Deploy"
	       data-skill-description="ship to prod" data-skill-scope="project"
	       data-skill-source="repo" data-skill-content="# Deploy"
	       data-skill-enabled="true" data-skill-always-use="false"></div>
	  <div data-skill-handle="review" data-skill-name="Review"
	       data-skill-scope="user" data-skill-enabled="false"
	       data-skill-always-use="true"></div>
	</div>`
	c := htmlServer(t, page)

	skills, err := c.ListSkills(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 {
		t.Fatalf("got %d skills, want 2", len(skills))
	}
	s := skills[0]
	if s.Handle != "deploy" || s.Name != "Deploy" || s.Scope != "project" {
		t.Errorf("skill = %+v", s)
	}
	if !s.Enabled || s.AlwaysUse {
		t.Errorf("flags = enabled %t always %t", s.Enabled, s.AlwaysUse)
	}
	if skills[1].Enabled || !skills[1].AlwaysUse {
		t.Errorf("skill[1] flags = %+v", skills[1])
	}
}

func TestListModelsAndAgents(t *testing.T) {
	t.Run("models", func(t *testing.T) {
		c := htmlServer(t, `<div data-model-id="m1" data-model-name="Sonnet"
				data-model-provider="anthropic" data-model-model="claude-sonnet-4">Sonnet model details</div>`)
		list, err := c.ListModels(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].Name != "Sonnet" || list[0].Provider != "anthropic" {
			t.Fatalf("models = %+v", list)
		}
		if list[0].Text != "Sonnet model details" {
			t.Fatalf("model text = %q, want %q", list[0].Text, "Sonnet model details")
		}
	})

	t.Run("agents", func(t *testing.T) {
		c := htmlServer(t, `<div data-agent-id="ag1" data-agent-key="reviewer"
			data-agent-name="Reviewer" data-agent-description="reviews code"
			data-agent-model="sonnet" data-agent-scope="project"></div>`)
		list, err := c.ListAgents(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].Key != "reviewer" || list[0].Model != "sonnet" {
			t.Fatalf("agents = %+v", list)
		}
	})
}

func TestListAutomationsReadsCardMarkup(t *testing.T) {
	// Mirrors the delete-menu button markup on a real automation card, which
	// carries the id/name the TUI resolves references against.
	const page = `<div>
	  <div class="card" data-automation-url="/automations/au1?project_id=p1"
	       data-search-card data-search-text="native sdlc active">
	    <div class="card-body relative">
	      <span class="badge badge-outline badge-sm">active</span>
	      <button type="button" class="text-error" data-automation-card-delete="au1"
	              data-automation-name="Native SDLC"></button>
	    </div>
	  </div>
	  <div class="card" data-automation-url="/automations/au2?project_id=p1"
	       data-search-card data-search-text="github sdlc paused">
	    <div class="card-body relative">
	      <span class="badge badge-outline badge-sm">paused</span>
	      <button type="button" class="text-error" data-automation-card-delete="au2"
	              data-automation-name="GitHub SDLC"></button>
	    </div>
	  </div>
	</div>`
	c := htmlServer(t, page)

	automations, err := c.ListAutomations(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(automations) != 2 {
		t.Fatalf("got %d automations, want 2: %+v", len(automations), automations)
	}
	if automations[0].ID != "au1" || automations[0].Name != "Native SDLC" || automations[0].State != "active" {
		t.Errorf("automation[0] = %+v", automations[0])
	}
	if automations[1].ID != "au2" || automations[1].Name != "GitHub SDLC" || automations[1].State != "paused" {
		t.Errorf("automation[1] = %+v", automations[1])
	}
}

func parseAutomationCardFixture(t *testing.T, badges string) Automation {
	t.Helper()
	page := `<div data-automation-url="/automations/au1">
	  ` + badges + `
	  <button data-automation-card-delete="au1" data-automation-name="Fixture"></button>
	</div>`
	root, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatalf("html.Parse: %v", err)
	}
	automations := parseAutomations(root)
	if len(automations) != 1 {
		t.Fatalf("got %d automations, want 1: %+v", len(automations), automations)
	}
	return automations[0]
}

func TestParseAutomationCardRecognizesLifecycleStates(t *testing.T) {
	for _, state := range []string{"active", "paused", "draft", "archived"} {
		t.Run(state, func(t *testing.T) {
			got := parseAutomationCardFixture(t, `<span class="badge">`+state+`</span>`)
			if got.State != state {
				t.Errorf("state = %q, want %q", got.State, state)
			}
		})
	}
}

func TestParseAutomationCardIgnoresUnknownBadges(t *testing.T) {
	got := parseAutomationCardFixture(t, `<span class="badge">pending</span><span class="badge"><em>running</em></span>`)
	if got.State != "" {
		t.Errorf("state = %q, want empty for unknown badges", got.State)
	}
}

func TestParseAutomationCardSelectsHighestRankedLifecycleBadge(t *testing.T) {
	badges := `<span class="badge">active</span><span class="badge">paused</span>` +
		`<span class="badge"><strong>draft</strong></span><span class="badge">archived</span>`
	got := parseAutomationCardFixture(t, badges)
	if got.State != "archived" {
		t.Errorf("state = %q, want archived", got.State)
	}
}

// benchmarkAutomationsPage builds representative cards with nested action markup.
// The nested controls are intentionally noisy because that is the DOM work the
// old whole-page NodeText path normalized even though the list only needs the
// card's ID, name, and lifecycle state.
func benchmarkAutomationsPage(count int) string {
	var b strings.Builder
	b.Grow(count * 700)
	b.WriteString("<!doctype html><html><body><main>")
	for i := 0; i < count; i++ {
		state := "active"
		if i%2 == 1 {
			state = "paused"
		}
		fmt.Fprintf(&b, `<article class="card" data-automation-url="/automations/au-%d?project_id=p1" data-search-card data-search-text="automation-%d %s">`, i, i, state)
		b.WriteString(`<div class="dropdown"><ul>`)
		for action := 0; action < 4; action++ {
			fmt.Fprintf(&b, `<li><button type="button"><span class="icon">action</span><span>control %d</span></button></li>`, action)
		}
		b.WriteString(`</ul></div><div class="card-body relative">`)
		fmt.Fprintf(&b, `<span class="badge badge-outline badge-sm">%s</span>`, state)
		fmt.Fprintf(&b, `<button type="button" class="text-error" data-automation-card-delete="au-%d" data-automation-name="Automation %d"><span>delete</span></button>`, i, i)
		b.WriteString(`<div class="description"><p>long nested action metadata and display prose</p><div><span>not a lifecycle badge</span></div></div></div></article>`)
	}
	b.WriteString("</main></body></html>")
	return b.String()
}

// BenchmarkAutomationsDisplayPath compares the old GetAutomations work
// (parse plus whole-document NodeText) with the structured ListAutomations
// work (the same parse plus only card fields and badge state). The 100- and
// 1,000-card cases model normal and large automation pages; run with
// `go test -bench BenchmarkAutomationsDisplayPath -benchmem ./internal/client`
// to compare ns/op, B/op, and allocs/op.
func BenchmarkAutomationsDisplayPath(b *testing.B) {
	for _, count := range []int{100, 1000} {
		page := benchmarkAutomationsPage(count)
		b.Run(fmt.Sprintf("old_full_text/%d", count), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(page)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				root, err := html.Parse(strings.NewReader(page))
				if err != nil {
					b.Fatal(err)
				}
				_ = NodeText(root)
			}
		})
		b.Run(fmt.Sprintf("new_structured/%d", count), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(page)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				root, err := html.Parse(strings.NewReader(page))
				if err != nil {
					b.Fatal(err)
				}
				_ = parseAutomations(root)
			}
		})
	}
}

// BenchmarkAutomationsDOMWork isolates the changed post-parse operation. HTML
// parsing is deliberately outside the timer because it is identical in both
// production paths; this measures whole-document normalization versus the
// structured card extraction that replaced it.
func BenchmarkAutomationsDOMWork(b *testing.B) {
	for _, count := range []int{100, 1000} {
		page := benchmarkAutomationsPage(count)
		root, err := html.Parse(strings.NewReader(page))
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("old_node_text/%d", count), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = NodeText(root)
			}
		})
		b.Run(fmt.Sprintf("new_structured/%d", count), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = parseAutomations(root)
			}
		})
	}
}

func TestGetScheduleScrapesEntries(t *testing.T) {
	const page = `<div id="schedule-content">
	  <div data-task-id="t1" data-schedule-id="s1"><div class="font-semibold truncate leading-tight">Nightly build</div><div class="opacity-60 leading-tight">02:00</div></div>
	  <div data-task-id="t1" data-schedule-id="s2">Weekly report — duplicate wrapper</div>
	  <div data-task-id="t1" data-schedule-id="s2" data-schedule-enabled="true"><div class="font-semibold truncate leading-tight">Weekly report</div><div class="opacity-60 leading-tight">weekly mon</div></div>
	  <div data-task-id="t2" data-schedule-id="s3"><div class="font-semibold truncate leading-tight">Monthly cleanup</div><div class="opacity-60 leading-tight">monthly 03:00</div></div>
	</div>`
	c := htmlServer(t, page)

	entries, summary, err := c.GetSchedule(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(entries), entries)
	}
	want := []struct {
		scheduleID string
		taskID     string
		name       string
		text       string
	}{
		{scheduleID: "s1", taskID: "t1", name: "Nightly build", text: "Nightly build\n\n02:00"},
		{scheduleID: "s2", taskID: "t1", name: "Weekly report", text: "Weekly report\n\nweekly mon"},
		{scheduleID: "s3", taskID: "t2", name: "Monthly cleanup", text: "Monthly cleanup\n\nmonthly 03:00"},
	}
	for i, want := range want {
		if entries[i].ScheduleID != want.scheduleID || entries[i].TaskID != want.taskID {
			t.Errorf("entry[%d] = %+v, want schedule %s task %s", i, entries[i], want.scheduleID, want.taskID)
		}
		if entries[i].Name != want.name {
			t.Errorf("entry[%d] name = %q, want %q", i, entries[i].Name, want.name)
		}
		if entries[i].Text != want.text {
			t.Errorf("entry[%d] text = %q, want %q", i, entries[i].Text, want.text)
		}
	}
	if summary == "" {
		t.Error("expected summary text from #schedule-content")
	}
}

func TestDeleteAlertAndListParsesProjectScopedHTMXRefresh(t *testing.T) {
	var gotMethod, gotQuery, gotHX string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.Query().Get("project_id")
		gotHX = r.Header.Get("HX-Request")
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<div data-alert-id="remaining" data-alert-scroll-anchor="remaining" data-search-text="warning pending">
			<p class="font-semibold">Remaining alert</p>
		</div>`)
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	alerts, err := c.DeleteAlertAndList(context.Background(), "delete/me", "project-2")
	if err != nil {
		t.Fatalf("DeleteAlertAndList: %v", err)
	}
	if gotMethod != http.MethodDelete || gotQuery != "project-2" || gotHX != "true" {
		t.Fatalf("request = method %q project %q HX %q", gotMethod, gotQuery, gotHX)
	}
	if len(alerts) != 1 || alerts[0].ID != "remaining" || alerts[0].Title != "Remaining alert" || alerts[0].ProjectID != "project-2" {
		t.Fatalf("parsed alerts = %#v", alerts)
	}
}

func TestDeleteAlertAndListLoadsPaginatedHTMXRefresh(t *testing.T) {
	var deleteRequests, continuationRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.Method {
		case http.MethodDelete:
			deleteRequests++
			if r.URL.Path != "/alerts/delete-me" || r.URL.Query().Get("project_id") != "project-2" {
				t.Errorf("delete request = %s?%s", r.URL.Path, r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="true">
				<div data-alert-id="a1" data-alert-scroll-anchor="a1"><p class="font-semibold">First alert</p></div>
				<div data-alert-id="a2" data-alert-scroll-anchor="a2"><p class="font-semibold">Second alert</p></div>
			</div>`)
		case http.MethodGet:
			continuationRequests++
			q := r.URL.Query()
			if r.URL.Path != "/alerts" || q.Get("project_id") != "project-2" || q.Get("card_page") != "1" || q.Get("page") != "1" || q.Get("page_size") != "50" || q.Get("offset") != "2" {
				t.Errorf("continuation request = %s?%s", r.URL.Path, r.URL.RawQuery)
			}
			w.Header().Set("X-OpenVibely-Card-Page-Has-More", "false")
			_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-alert-id]" data-card-pagination-key="data-alert-id" data-card-pagination-has-more="false">
				<div data-alert-id="a2" data-alert-scroll-anchor="a2"><p class="font-semibold">Second alert duplicate</p></div>
				<div data-alert-id="a3" data-alert-scroll-anchor="a3"><p class="font-semibold">Later alert</p></div>
			</div>`)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	alerts, err := c.DeleteAlertAndList(context.Background(), "delete-me", "project-2")
	if err != nil {
		t.Fatalf("DeleteAlertAndList: %v", err)
	}
	if deleteRequests != 1 || continuationRequests != 1 {
		t.Fatalf("requests = DELETE %d continuation GET %d, want 1 each", deleteRequests, continuationRequests)
	}
	gotIDs := make([]string, 0, len(alerts))
	for _, alert := range alerts {
		gotIDs = append(gotIDs, alert.ID)
	}
	if want := []string{"a1", "a2", "a3"}; !reflect.DeepEqual(gotIDs, want) {
		t.Fatalf("alert IDs = %#v, want %#v", gotIDs, want)
	}
	if alerts[1].Title != "Second alert" {
		t.Fatalf("overlapping alert title = %q, want first-page value", alerts[1].Title)
	}
	for _, alert := range alerts {
		if alert.ProjectID != "project-2" {
			t.Fatalf("alert %s project = %q", alert.ID, alert.ProjectID)
		}
	}
}

func TestDeleteAlertAndListReportsBackendFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"alert deletion unavailable"}`)
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteAlertAndList(context.Background(), "a1", "p1"); err == nil || !strings.Contains(err.Error(), "alert deletion unavailable") {
		t.Fatalf("error = %v", err)
	}
}

func TestResourceMutationRoutes(t *testing.T) {
	var gotMethod, gotPath string
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotMethod, gotPath, gotForm = r.Method, r.URL.Path, r.PostForm
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	ctx := context.Background()

	tests := []struct {
		name      string
		fn        func() error
		method    string
		path      string
		formKey   string
		formValue string
		checkForm bool
	}{
		{name: "alert approve", fn: func() error { return c.AlertAction(ctx, "a1", "approve", "p1") },
			method: "POST", path: "/alerts/a1/approve"},
		{name: "alert delete", fn: func() error { return c.DeleteAlert(ctx, "a1", "p1") },
			method: "DELETE", path: "/alerts/a1"},
		{name: "alerts read all", fn: func() error { return c.MarkAllAlertsRead(ctx, "p1") },
			method: "POST", path: "/alerts/read-all"},
		{name: "skill delete", fn: func() error { return c.DeleteSkill(ctx, "p1", "deploy", "project") },
			method: "DELETE", path: "/skills/deploy"},
		{name: "model default", fn: func() error { return c.SetDefaultModel(ctx, "m1") },
			method: "POST", path: "/models/m1/set-default"},
		{name: "agent delete", fn: func() error { return c.DeleteAgent(ctx, "ag1") },
			method: "DELETE", path: "/agents/ag1"},
		{name: "schedule delete", fn: func() error { return c.DeleteSchedule(ctx, "p1", "s1") },
			method: "DELETE", path: "/schedules/s1"},
		{name: "worker limit", fn: func() error { return c.SetGlobalWorkerLimit(ctx, 7) },
			method: "POST", path: "/workers",
			formKey: "max_workers", formValue: "7", checkForm: true},
		{name: "personality", fn: func() error { return c.SavePersonality(ctx, "p1", "concise") },
			method: "POST", path: "/personality/save",
			formKey: "personality", formValue: "concise", checkForm: true},
		{name: "pulse summary", fn: func() error { return c.GeneratePulseSummary(ctx, "p1") },
			method: "POST", path: "/upcoming/summary"},
		{name: "insights analyze", fn: func() error { return c.RunInsightsAnalysis(ctx, "p1") },
			method: "POST", path: "/insights/analyze"},
		{name: "automation run-now", fn: func() error { return c.AutomationAction(ctx, "au1", "run-now", "p1") },
			method: "POST", path: "/automations/au1/run-now"},
		{name: "automation pause", fn: func() error { return c.AutomationAction(ctx, "au1", "pause", "p1") },
			method: "POST", path: "/automations/au1/pause"},
		{name: "automation resume", fn: func() error { return c.AutomationAction(ctx, "au1", "resume", "p1") },
			method: "POST", path: "/automations/au1/resume"},
		{name: "automation delete", fn: func() error { return c.AutomationAction(ctx, "au1", "delete", "p1") },
			method: "POST", path: "/automations/au1/delete"},
		{name: "channel test telegram", fn: func() error { return c.ChannelAction(ctx, "telegram", "test", "p1") },
			method: "POST", path: "/channels/telegram/test"},
		{name: "channel remove telegram", fn: func() error { return c.ChannelAction(ctx, "telegram", "remove", "p1") },
			method: "POST", path: "/channels/telegram/remove"},
		{name: "channel test discord", fn: func() error { return c.ChannelAction(ctx, "discord", "test", "p1") },
			method: "POST", path: "/channels/discord/test"},
		{name: "channel remove discord", fn: func() error { return c.ChannelAction(ctx, "discord", "remove", "p1") },
			method: "POST", path: "/channels/discord/remove"},
		{name: "channel test email", fn: func() error { return c.ChannelAction(ctx, "email", "test", "p1") },
			method: "POST", path: "/channels/email/test"},
		{name: "channel remove email", fn: func() error { return c.ChannelAction(ctx, "email", "remove", "p1") },
			method: "POST", path: "/channels/email/remove"},
		{name: "channel test slack", fn: func() error { return c.ChannelAction(ctx, "slack", "test", "p1") },
			method: "POST", path: "/channels/slack/test"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); err != nil {
				t.Fatal(err)
			}
			if gotMethod != tc.method || gotPath != tc.path {
				t.Errorf("got %s %s, want %s %s", gotMethod, gotPath, tc.method, tc.path)
			}
			if tc.checkForm && gotForm.Get(tc.formKey) != tc.formValue {
				t.Errorf("%s = %q, want %q", tc.formKey, gotForm.Get(tc.formKey), tc.formValue)
			}
		})
	}
}

func TestWorkerLimitMutationsUseExpectedRoutesAndForms(t *testing.T) {
	var gotMethod, gotPath string
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.EscapedPath()
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
			return
		}
		gotForm = r.PostForm
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const projectID = "project/with spaces"
	projectPath := "/workers/projects/project%2Fwith%20spaces/limit"
	tests := []struct {
		name      string
		fn        func() error
		path      string
		formValue string
	}{
		{
			name:      "global positive",
			fn:        func() error { return c.SetGlobalWorkerLimit(ctx, 7) },
			path:      "/workers",
			formValue: "7",
		},
		{
			name:      "project escapes ID",
			fn:        func() error { return c.SetProjectWorkerLimit(ctx, projectID, 12) },
			path:      projectPath,
			formValue: "12",
		},
		{
			name:      "global zero",
			fn:        func() error { return c.SetGlobalWorkerLimit(ctx, 0) },
			path:      "/workers",
			formValue: "0",
		},
		{
			name:      "project zero",
			fn:        func() error { return c.SetProjectWorkerLimit(ctx, projectID, 0) },
			path:      projectPath,
			formValue: "0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotMethod, gotPath, gotForm = "", "", nil
			if err := tc.fn(); err != nil {
				t.Fatal(err)
			}
			if gotMethod != http.MethodPost || gotPath != tc.path {
				t.Errorf("request = %s %s, want %s %s", gotMethod, gotPath, http.MethodPost, tc.path)
			}
			wantForm := url.Values{"max_workers": []string{tc.formValue}}
			if !reflect.DeepEqual(gotForm, wantForm) {
				t.Errorf("form = %v, want %v", gotForm, wantForm)
			}
		})
	}
}

func TestWorkerLimitMutationsPropagateServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"worker limit rejected"}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tests := []struct {
		name string
		fn   func() error
	}{
		{name: "global", fn: func() error { return c.SetGlobalWorkerLimit(ctx, 7) }},
		{name: "project", fn: func() error { return c.SetProjectWorkerLimit(ctx, "p1", 7) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			if err == nil {
				t.Fatal("expected worker limit error")
			}
			if got := err.Error(); got != "server error (422): worker limit rejected" {
				t.Errorf("error = %q, want server error propagation", got)
			}
		})
	}
}

func TestSkillMutationsSendBackendJSONContract(t *testing.T) {
	type capturedRequest struct {
		method      string
		path        string
		projectID   string
		contentType string
		accept      string
		hxRequest   string
		body        map[string]any
	}
	var got capturedRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode JSON request body %q: %v", raw, err)
		}
		got = capturedRequest{
			method:      r.Method,
			path:        r.URL.Path,
			projectID:   r.URL.Query().Get("project_id"),
			contentType: r.Header.Get("Content-Type"),
			accept:      r.Header.Get("Accept"),
			hxRequest:   r.Header.Get("HX-Request"),
			body:        body,
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tests := []struct {
		name     string
		fn       func() error
		method   string
		path     string
		wantBody map[string]any
	}{
		{
			name:   "create",
			fn:     func() error { return c.CreateSkill(ctx, "p1", "retry-logic", "wrap retries", "# Retry") },
			method: http.MethodPost,
			path:   "/skills",
			wantBody: map[string]any{
				"handle":      "retry-logic",
				"name":        "retry-logic",
				"description": "wrap retries",
				"scope":       "project",
				"body":        "# Retry",
			},
		},
		{
			name: "edit",
			fn: func() error {
				return c.UpdateSkill(ctx, "p1", "retry-logic", "project", "Retry Logic", "wrap retries", false, "new body")
			},
			method:   http.MethodPut,
			path:     "/skills/retry-logic",
			wantBody: map[string]any{"handle": "retry-logic", "name": "Retry Logic", "description": "wrap retries", "scope": "project", "body": "new body", "enabled": false},
		},
		{
			name:     "enable",
			fn:       func() error { return c.SetSkillEnabled(ctx, "p1", "retry-logic", "project", true) },
			method:   http.MethodPost,
			path:     "/skills/retry-logic/enabled",
			wantBody: map[string]any{"enabled": true, "scope": "project"},
		},
		{
			name:     "disable",
			fn:       func() error { return c.SetSkillEnabled(ctx, "p1", "retry-logic", "project", false) },
			method:   http.MethodPost,
			path:     "/skills/retry-logic/enabled",
			wantBody: map[string]any{"enabled": false, "scope": "project"},
		},
		{
			name:     "always use",
			fn:       func() error { return c.SetSkillAlwaysUse(ctx, "p1", "retry-logic", "project", true) },
			method:   http.MethodPost,
			path:     "/skills/retry-logic/always_use",
			wantBody: map[string]any{"always_use": true, "scope": "project"},
		},
		{
			name:     "remove always use",
			fn:       func() error { return c.SetSkillAlwaysUse(ctx, "p1", "retry-logic", "project", false) },
			method:   http.MethodPost,
			path:     "/skills/retry-logic/always_use",
			wantBody: map[string]any{"always_use": false, "scope": "project"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); err != nil {
				t.Fatal(err)
			}
			if got.method != tc.method || got.path != tc.path || got.projectID != "p1" {
				t.Fatalf("request = %s %s?project_id=%s, want %s %s?project_id=p1", got.method, got.path, got.projectID, tc.method, tc.path)
			}
			if got.contentType != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got.contentType)
			}
			if got.accept != "text/html, application/json" {
				t.Errorf("Accept = %q, want text/html, application/json", got.accept)
			}
			if got.hxRequest != "true" {
				t.Errorf("HX-Request = %q, want true", got.hxRequest)
			}
			if !reflect.DeepEqual(got.body, tc.wantBody) {
				t.Errorf("JSON body = %#v, want %#v", got.body, tc.wantBody)
			}
		})
	}
}

func TestGlobalSkillMutationsSendGlobalScope(t *testing.T) {
	type capturedRequest struct {
		method      string
		path        string
		projectID   string
		scope       string
		contentType string
		body        map[string]any
	}
	var got capturedRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = capturedRequest{
			method:      r.Method,
			path:        r.URL.Path,
			projectID:   r.URL.Query().Get("project_id"),
			scope:       r.URL.Query().Get("scope"),
			contentType: r.Header.Get("Content-Type"),
		}
		if r.Method != http.MethodDelete {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read request body: %v", err)
			}
			if err := json.Unmarshal(raw, &got.body); err != nil {
				t.Errorf("decode JSON request body %q: %v", raw, err)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tests := []struct {
		name string
		fn   func() error
		path string
		body map[string]any
	}{
		{
			name: "edit",
			fn: func() error {
				return c.UpdateSkill(ctx, "p1", "global-skill", "global", "Global Skill", "global description", false, "new body")
			},
			path: "/skills/global-skill",
			body: map[string]any{
				"handle": "global-skill", "name": "Global Skill", "description": "global description",
				"scope": "global", "body": "new body", "enabled": false,
			},
		},
		{
			name: "disable",
			fn:   func() error { return c.SetSkillEnabled(ctx, "p1", "global-skill", "global", false) },
			path: "/skills/global-skill/enabled",
			body: map[string]any{"enabled": false, "scope": "global"},
		},
		{
			name: "always use",
			fn:   func() error { return c.SetSkillAlwaysUse(ctx, "p1", "global-skill", "global", true) },
			path: "/skills/global-skill/always_use",
			body: map[string]any{"always_use": true, "scope": "global"},
		},
		{
			name: "delete",
			fn:   func() error { return c.DeleteSkill(ctx, "p1", "global-skill", "global") },
			path: "/skills/global-skill",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got = capturedRequest{}
			if err := tc.fn(); err != nil {
				t.Fatal(err)
			}
			if got.projectID != "p1" {
				t.Fatalf("project_id = %q, want p1", got.projectID)
			}
			if got.path != tc.path {
				t.Fatalf("path = %q, want %q", got.path, tc.path)
			}
			if tc.body == nil {
				if got.method != http.MethodDelete {
					t.Fatalf("method = %q, want DELETE", got.method)
				}
				if got.scope != "global" {
					t.Fatalf("scope query = %q, want global", got.scope)
				}
				return
			}
			if got.contentType != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got.contentType)
			}
			if !reflect.DeepEqual(got.body, tc.body) {
				t.Errorf("JSON body = %#v, want %#v", got.body, tc.body)
			}
		})
	}
}

func TestNormalizeScheduleRepeat(t *testing.T) {
	cases := []struct {
		name   string
		repeat string
		want   string
	}{
		{name: "hourly alias", repeat: "hourly", want: "hours"},
		{name: "once", repeat: "once", want: "once"},
		{name: "daily", repeat: "daily", want: "daily"},
		{name: "weekly", repeat: "weekly", want: "weekly"},
		{name: "monthly", repeat: "monthly", want: "monthly"},
		{name: "seconds", repeat: "seconds", want: "seconds"},
		{name: "minutes", repeat: "minutes", want: "minutes"},
		{name: "hours", repeat: "hours", want: "hours"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeScheduleRepeat(tc.repeat); got != tc.want {
				t.Errorf("NormalizeScheduleRepeat(%q) = %q, want %q", tc.repeat, got, tc.want)
			}
		})
	}
}

func TestScheduleMutationsSendExactProjectScopeAndPreserveOwnershipErrors(t *testing.T) {
	const projectB = "project B&mode=terminal"
	cases := []struct {
		name   string
		method string
		path   string
		call   func(*Client, string) error
	}{
		{
			name:   "create",
			method: http.MethodPost,
			path:   "/tasks/t1/schedule",
			call: func(c *Client, projectID string) error {
				return c.CreateSchedule(context.Background(), projectID, "t1", "2026-01-02T09:00", "daily", 1)
			},
		},
		{
			name:   "toggle",
			method: http.MethodPost,
			path:   "/api/schedules/s1/toggle",
			call: func(c *Client, projectID string) error {
				_, err := c.ToggleSchedule(context.Background(), projectID, "s1")
				return err
			},
		},
		{
			name:   "delete",
			method: http.MethodDelete,
			path:   "/schedules/s1",
			call: func(c *Client, projectID string) error {
				return c.DeleteSchedule(context.Background(), projectID, "s1")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != tc.method || r.URL.Path != tc.path {
					t.Errorf("request = %s %s, want %s %s", r.Method, r.URL.Path, tc.method, tc.path)
				}
				if got := r.URL.Query().Get("project_id"); got != projectB {
					http.Error(w, "schedule belongs to another project", http.StatusForbidden)
					return
				}
				if tc.name == "toggle" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"s1","enabled":true}`))
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()

			c, _ := New(srv.URL)
			if err := tc.call(c, projectB); err != nil {
				t.Fatalf("Project B mutation failed with stale Project A preference: %v", err)
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want 1", requests)
			}

			err := tc.call(c, "project A")
			if err == nil || !strings.Contains(err.Error(), "403") {
				t.Fatalf("mismatched explicit scope error = %v, want HTTP 403", err)
			}
		})
	}
}

func TestCreateScheduleSendsRepeat(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		if r.URL.Path != "/tasks/t1/schedule" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	if err := c.CreateSchedule(context.Background(), "p1", "t1", "2026-01-02T09:00", "daily", 1); err != nil {
		t.Fatal(err)
	}
	if form.Get("run_at") != "2026-01-02T09:00" || form.Get("repeat_type") != "daily" {
		t.Errorf("form = %v", form)
	}
	if form.Get("repeat_interval") != "1" {
		t.Errorf("repeat_interval = %q", form.Get("repeat_interval"))
	}
}

func TestCreateScheduleTranslatesHourlyToHours(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	if err := c.CreateSchedule(context.Background(), "p1", "t1", "2026-01-02T09:00", "hourly", 1); err != nil {
		t.Fatal(err)
	}
	if form.Get("repeat_type") != "hours" {
		t.Errorf("repeat_type = %q, want %q", form.Get("repeat_type"), "hours")
	}
	if form.Get("repeat_interval") != "1" {
		t.Errorf("repeat_interval = %q", form.Get("repeat_interval"))
	}
}

func TestCreateScheduleSendsFastRepeatTypes(t *testing.T) {
	cases := []struct {
		repeat   string
		interval int
	}{
		{"seconds", 1},
		{"minutes", 15},
		{"hours", 4},
	}
	for _, tc := range cases {
		t.Run(tc.repeat, func(t *testing.T) {
			var form url.Values
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				form = r.PostForm
				if r.URL.Path != "/tasks/t1/schedule" {
					t.Errorf("path = %s", r.URL.Path)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			c, _ := New(srv.URL)
			if err := c.CreateSchedule(context.Background(), "p1", "t1", "2026-01-02T09:00", tc.repeat, tc.interval); err != nil {
				t.Fatal(err)
			}
			if form.Get("repeat_type") != tc.repeat {
				t.Errorf("repeat_type = %q, want %q", form.Get("repeat_type"), tc.repeat)
			}
			if form.Get("repeat_interval") != strconv.Itoa(tc.interval) {
				t.Errorf("repeat_interval = %q, want %d", form.Get("repeat_interval"), tc.interval)
			}
		})
	}
}

func TestCreateScheduleRejectsInvalidRepeatIntervalsBeforeRequest(t *testing.T) {
	cases := []int{0, -5, 366}
	for _, interval := range cases {
		t.Run(strconv.Itoa(interval), func(t *testing.T) {
			var requests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			c, _ := New(srv.URL)
			err := c.CreateSchedule(context.Background(), "p1", "t1", "2026-01-02T09:00", "minutes", interval)
			if err == nil || !strings.Contains(err.Error(), "repeat interval must be between 1 and 365") {
				t.Fatalf("expected interval validation error, got %v", err)
			}
			if requests != 0 {
				t.Fatalf("expected no request for invalid interval, got %d", requests)
			}
		})
	}
}

func TestScheduleEditLoadsSelectedConfigAndPreservesOmittedValues(t *testing.T) {
	const detail = `<div id="task-detail-content" data-project-id="p2">
		<div data-schedule-id="s1"><form action="/schedules/s1?project_id=p2">
			<input name="run_at" value="2026-01-02T09:00"><select name="repeat_type"><option value="daily" selected>Daily</option></select>
			<input name="repeat_interval" value="2"><input type="checkbox" name="clear_context_on_start" value="true" checked>
		</form></div>
		<div data-schedule-id="s2"><form action="/schedules/s2?project_id=p2">
			<input name="run_at" value="2026-02-03T10:30"><select name="repeat_type"><option value="daily">Daily</option><option value="weekly" selected>Weekly</option></select>
			<input name="repeat_interval" value="3"><input type="hidden" name="clear_context_on_start" value="false"><input type="checkbox" name="clear_context_on_start" value="true">
		</form></div>
	</div>`
	var requests int
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.URL.Query().Get("project_id"); got != "p2" {
			t.Errorf("project_id = %q, want p2", got)
		}
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path != "/tasks/t2" || r.URL.Query().Get("tab") != "schedules" {
				t.Errorf("config request = %s %s", r.Method, r.URL.RequestURI())
			}
			_, _ = io.WriteString(w, detail)
		case http.MethodPut:
			if r.URL.Path != "/schedules/s2" {
				t.Errorf("update path = %q", r.URL.Path)
			}
			_ = r.ParseForm()
			gotForm = r.PostForm
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s", r.Method)
		}
	}))
	defer srv.Close()
	c, _ := New(srv.URL)

	config, err := c.GetTaskSchedule(context.Background(), "p2", "t2", "s2")
	if err != nil {
		t.Fatal(err)
	}
	if config.ID != "s2" || config.TaskID != "t2" || config.ProjectID != "p2" || config.RunAt != "2026-02-03T10:30" || config.RepeatType != "weekly" || config.RepeatInterval != 3 || config.ClearContextOnStart {
		t.Fatalf("config = %#v", config)
	}
	if err := c.UpdateSchedule(context.Background(), config, ScheduleUpdate{RepeatType: ptr("hours")}); err != nil {
		t.Fatal(err)
	}
	want := url.Values{"run_at": {"2026-02-03T10:30"}, "repeat_type": {"hours"}, "repeat_interval": {"3"}}
	if !reflect.DeepEqual(gotForm, want) {
		t.Fatalf("form = %#v, want %#v", gotForm, want)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestUpdateScheduleSupportsEveryRecurrenceAndExplicitContext(t *testing.T) {
	for _, repeat := range []string{"once", "daily", "weekly", "monthly", "seconds", "minutes", "hours", "hourly"} {
		t.Run(repeat, func(t *testing.T) {
			var form url.Values
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				form = r.PostForm
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()
			c, _ := New(srv.URL)
			clear := false
			config := ScheduleConfig{ID: "s1", TaskID: "t1", ProjectID: "p1", RunAt: "2026-01-02T09:00", RepeatType: "daily", RepeatInterval: 1, ClearContextOnStart: true}
			if err := c.UpdateSchedule(context.Background(), config, ScheduleUpdate{RepeatType: &repeat, ClearContextOnStart: &clear}); err != nil {
				t.Fatal(err)
			}
			wantRepeat := NormalizeScheduleRepeat(repeat)
			if form.Get("repeat_type") != wantRepeat || form.Get("clear_context_on_start") != "false" {
				t.Fatalf("form = %#v, want repeat %q and explicit false", form, wantRepeat)
			}
		})
	}
}

func TestScheduleEditRejectsForeignProjectAndInvalidValuesBeforeMutation(t *testing.T) {
	const foreign = `<div id="task-detail-content" data-project-id="foreign"><div data-schedule-id="s1"><form action="/schedules/s1?project_id=foreign"><input name="run_at" value="2026-01-02T09:00"><select name="repeat_type"><option value="daily" selected>Daily</option></select><input name="repeat_interval" value="1"></form></div></div>`
	var puts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
		}
		_, _ = io.WriteString(w, foreign)
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	if _, err := c.GetTaskSchedule(context.Background(), "p1", "t1", "s1"); err == nil || !strings.Contains(err.Error(), "belongs to project") {
		t.Fatalf("foreign project error = %v", err)
	}

	base := ScheduleConfig{ID: "s1", TaskID: "t1", ProjectID: "p1", RunAt: "2026-01-02T09:00", RepeatType: "daily", RepeatInterval: 1}
	badTime, badType := "not-a-time", "yearly"
	zero, tooLarge := 0, 366
	for _, update := range []ScheduleUpdate{{RunAt: &badTime}, {RepeatType: &badType}, {RepeatInterval: &zero}, {RepeatInterval: &tooLarge}} {
		if err := c.UpdateSchedule(context.Background(), base, update); err == nil {
			t.Fatalf("UpdateSchedule(%#v) unexpectedly succeeded", update)
		}
	}
	if puts != 0 {
		t.Fatalf("invalid updates sent %d PUT requests", puts)
	}
}

func ptr[T any](value T) *T { return &value }

func TestListPersonalitiesScrapesBuiltinsCustomsAndActiveState(t *testing.T) {
	const page = `<div id="personality-section" data-selected-personality="release_coach">
		<div data-personality-key="" data-personality-name="Base" data-personality-description="Standard professional assistant tone"
			data-personality-preview="" data-personality-is-preset="true" data-personality-has-custom="false"></div>
		<div data-personality-key="pirate_captain" data-personality-name="Pirate Captain (Edited)"
			data-personality-description="custom pirate style" data-personality-preview="Speak like a pirate..."
			data-personality-is-preset="true" data-personality-has-custom="true"></div>
		<div data-personality-key="release_coach" data-personality-name="Release Coach"
			data-personality-description="safe releases" data-personality-preview="Keep releases safe..."
			data-personality-is-preset="false" data-personality-has-custom="true"></div>
	</div>`
	c := htmlServer(t, page)

	personalities, err := c.ListPersonalities(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(personalities) != 3 {
		t.Fatalf("got %d personalities, want 3: %+v", len(personalities), personalities)
	}
	if personalities[0].Key != "" || personalities[0].Name != "Base" || !personalities[0].IsPreset {
		t.Errorf("base personality = %+v", personalities[0])
	}
	if !personalities[1].IsPreset || !personalities[1].HasCustom || personalities[1].Name != "Pirate Captain (Edited)" {
		t.Errorf("preset override = %+v", personalities[1])
	}
	if personalities[2].IsPreset || !personalities[2].Active || personalities[2].SystemPromptPreview != "Keep releases safe..." {
		t.Errorf("custom personality = %+v", personalities[2])
	}
	if personalities[1].Active {
		t.Error("inactive override marked active")
	}
}

func TestPersonalityJSONMutationsUseScopedRoutesAndPayloads(t *testing.T) {
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/personality/custom":
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("create Content-Type = %q", got)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create body: %v", err)
			}
			if body["name"] != "Release Coach" || body["description"] != "safe | observable releases" || body["system_prompt"] != "Keep releases safe | observable in production deployments." {
				t.Errorf("create body = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"cp1","key":"release_coach","name":"Release Coach","description":"safe releases","system_prompt":"Keep releases safe in production deployments."}`)
		case r.Method == http.MethodGet && r.URL.Path == "/personality/custom/release_coach":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"cp1","key":"release_coach","name":"Release Coach","description":"safe releases","system_prompt":"Keep releases safe in production deployments."}`)
		case r.Method == http.MethodPut && r.URL.Path == "/personality/custom/release_coach":
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("update Content-Type = %q", got)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode update body: %v", err)
			}
			if body["name"] != "Release Coach 2" || body["description"] != "updated" || body["system_prompt"] != "Keep every release reversible and observable." {
				t.Errorf("update body = %#v", body)
			}
			_, _ = fmt.Fprint(w, `{"id":"cp1","key":"release_coach","name":"Release Coach 2","description":"updated","system_prompt":"Keep every release reversible and observable."}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/personality/custom/release_coach":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	created, err := c.CreateCustomPersonality(ctx, "p1", "Release Coach", "safe | observable releases", "Keep releases safe | observable in production deployments.")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "cp1" || created.Key != "release_coach" {
		t.Fatalf("created = %+v", created)
	}
	got, err := c.GetCustomPersonality(ctx, "p1", "release_coach")
	if err != nil || got.SystemPrompt == "" {
		t.Fatalf("detail = %+v, err=%v", got, err)
	}
	updated, err := c.UpdateCustomPersonality(ctx, "p1", "release_coach", "Release Coach 2", "updated", "Keep every release reversible and observable.")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Release Coach 2" {
		t.Fatalf("updated = %+v", updated)
	}
	if err := c.DeleteCustomPersonality(ctx, "p1", "release_coach"); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"POST /personality/custom?project_id=p1",
		"GET /personality/custom/release_coach?project_id=p1",
		"PUT /personality/custom/release_coach?project_id=p1",
		"DELETE /personality/custom/release_coach?project_id=p1",
	} {
		found := false
		for _, request := range requests {
			if request == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("requests missing %q: %v", want, requests)
		}
	}
}

func TestPersonalityMutationErrorsPropagateValidationDuplicateAndNotFound(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/personality/custom":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = fmt.Fprint(w, `{"error":"System prompt must be at least 20 characters"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/personality/custom/release_coach":
			w.WriteHeader(http.StatusConflict)
			_, _ = fmt.Fprint(w, `{"error":"A custom personality with this key already exists"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/personality/custom/release_coach":
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":"Custom personality not found"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := c.CreateCustomPersonality(ctx, "p1", "Release Coach", "", "too short"); err == nil || err.Error() != "server error (422): System prompt must be at least 20 characters" {
		t.Fatalf("create validation error = %v", err)
	}
	if _, err := c.UpdateCustomPersonality(ctx, "p1", "release_coach", "Release Coach", "", "valid prompt that is long enough"); err == nil || err.Error() != "server error (409): A custom personality with this key already exists" {
		t.Fatalf("update duplicate error = %v", err)
	}
	if err := c.DeleteCustomPersonality(ctx, "p1", "release_coach"); err == nil || err.Error() != "server error (404): Custom personality not found" {
		t.Fatalf("delete not-found error = %v", err)
	}
	if requests != 3 {
		t.Fatalf("requests = %d, want 3", requests)
	}
}

func TestPersonalityMutationsRejectBlankKeysBeforeRequest(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.GetCustomPersonality(ctx, "p1", " "); err == nil || err.Error() != "personality key is required" {
		t.Fatalf("detail blank-key error = %v", err)
	}
	if _, err := c.UpdateCustomPersonality(ctx, "p1", " ", "Name", "", "valid prompt that is long enough"); err == nil || err.Error() != "personality key is required" {
		t.Fatalf("update blank-key error = %v", err)
	}
	if err := c.DeleteCustomPersonality(ctx, "p1", " "); err == nil || err.Error() != "personality key is required" {
		t.Fatalf("delete blank-key error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("blank-key methods made %d requests", requests)
	}
}

func TestListPersonalitiesReturnsEmptyCollectionForEmptyPage(t *testing.T) {
	c := htmlServer(t, `<div id="personality-section" data-selected-personality=""><p>No personalities</p></div>`)
	personalities, err := c.ListPersonalities(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if personalities == nil {
		t.Fatal("empty personality list must be non-nil")
	}
	if len(personalities) != 0 {
		t.Fatalf("personalities = %+v, want empty", personalities)
	}
}

func TestPersonalityJSONUsesStableSnakeCaseFields(t *testing.T) {
	payload, err := json.Marshal(Personality{
		ID: "cp1", Key: "release_coach", Name: "Release Coach",
		Description: "safe releases", SystemPromptPreview: "Keep releases safe...",
		IsPreset: false, HasCustom: true, Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, key := range []string{"\"id\"", "\"key\"", "\"name\"", "\"description\"", "\"system_prompt_preview\"", "\"is_preset\"", "\"has_custom\"", "\"active\""} {
		if !strings.Contains(text, key) {
			t.Errorf("JSON missing %s: %s", key, text)
		}
	}
}

func TestPageTextPrefersNamedElement(t *testing.T) {
	c := htmlServer(t, `<html><body><nav>skip me</nav>
		<div id="personality-container">friendly and concise</div></body></html>`)
	text, err := c.GetPersonality(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if text != "friendly and concise" {
		t.Errorf("text = %q", text)
	}
}

func TestGetPersonalityFallsBackToWholeDocument(t *testing.T) {
	c := htmlServer(t, `<html><body>no container here</body></html>`)
	text, err := c.GetPersonality(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if text != "no container here" {
		t.Errorf("text = %q", text)
	}
}

func TestGetWorkerSettingsReturnsPageText(t *testing.T) {
	c := htmlServer(t, `<html><body>worker settings text</body></html>`)
	text, err := c.GetWorkerSettings(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if text != "worker settings text" {
		t.Errorf("text = %q", text)
	}
}

func TestGetChannelsReturnsPageText(t *testing.T) {
	c := htmlServer(t, `<html><body>channels text</body></html>`)
	text, err := c.GetChannels(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if text != "channels text" {
		t.Errorf("text = %q", text)
	}
}

func TestPaginatedCardListsLoadAllPages(t *testing.T) {
	tests := []struct {
		name, path, firstBody, nextBody string
		list                            func(*Client) ([]string, error)
		want                            []string
	}{
		{
			name: "skills preserve order and deduplicate overlap", path: "/skills",
			firstBody: `<div data-card-pagination-root data-card-pagination-card-selector="[data-skill-handle]" data-card-pagination-key="data-skill-handle" data-card-pagination-has-more="true"><div data-skill-handle="first" data-skill-name="First"></div><div data-skill-handle="shared" data-skill-name="Shared"></div></div>`,
			nextBody:  `<div data-skill-handle="shared" data-skill-name="Duplicate"></div><div data-skill-handle="later" data-skill-name="Later"></div>`,
			list: func(c *Client) ([]string, error) {
				items, err := c.ListSkills(context.Background(), "project-two")
				out := make([]string, 0, len(items))
				for _, item := range items {
					out = append(out, item.Handle)
				}
				return out, err
			}, want: []string{"first", "shared", "later"},
		},
		{
			name: "automations include later structural cards", path: "/automations",
			firstBody: `<div data-card-pagination-root data-card-pagination-card-selector="[data-automation-url]" data-card-pagination-key="data-automation-url" data-card-pagination-has-more="true"><div data-automation-url="/automations/a1"><span class="badge">active</span><button data-automation-card-delete="a1" data-automation-name="First"></button></div></div>`,
			nextBody:  `<div data-automation-url="/automations/a2"><span class="badge">paused</span><button data-automation-card-delete="a2" data-automation-name="Later"></button></div>`,
			list: func(c *Client) ([]string, error) {
				items, err := c.ListAutomations(context.Background(), "project-two")
				out := make([]string, 0, len(items))
				for _, item := range items {
					out = append(out, item.ID)
				}
				return out, err
			}, want: []string{"a1", "a2"},
		},
		{
			name: "personality offset excludes built-in cards", path: "/personality",
			firstBody: `<div id="personality-section" data-selected-personality="base" data-card-pagination-root data-card-pagination-card-selector="[data-personality-pagination-card='true']" data-card-pagination-key="data-personality-key" data-card-pagination-has-more="true"><div data-personality-key="base" data-personality-name="Base" data-personality-is-preset="true" data-personality-pagination-card="false"></div><div data-personality-key="custom-one" data-personality-name="Custom One" data-personality-is-preset="false" data-personality-pagination-card="true"></div></div>`,
			nextBody:  `<div data-personality-key="custom-two" data-personality-name="Custom Two" data-personality-is-preset="false" data-personality-pagination-card="true"></div>`,
			list: func(c *Client) ([]string, error) {
				items, err := c.ListPersonalities(context.Background(), "project-two")
				out := make([]string, 0, len(items))
				for _, item := range items {
					out = append(out, item.Key)
				}
				return out, err
			}, want: []string{"base", "custom-one", "custom-two"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != tt.path {
					t.Errorf("path = %q, want %q", r.URL.Path, tt.path)
				}
				if got := r.URL.Query().Get("project_id"); got != "project-two" {
					t.Errorf("project_id = %q", got)
				}
				w.Header().Set("Content-Type", "text/html")
				if requests == 1 {
					w.Header().Set("X-OpenVibely-Card-Page-Has-More", "true")
					_, _ = io.WriteString(w, tt.firstBody)
					return
				}
				q := r.URL.Query()
				if q.Get("card_page") != "1" || q.Get("page_size") != "50" || q.Get("page") != "1" {
					t.Errorf("continuation query = %q", r.URL.RawQuery)
				}
				if tt.path == "/personality" && q.Get("offset") != "1" {
					t.Errorf("personality offset = %q, want 1", q.Get("offset"))
				}
				w.Header().Set("X-OpenVibely-Card-Page-Has-More", "false")
				_, _ = io.WriteString(w, tt.nextBody)
			}))
			defer srv.Close()
			c, err := New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			got, err := tt.list(c)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("items = %#v, want %#v", got, tt.want)
			}
			if requests != 2 {
				t.Fatalf("requests = %d, want 2", requests)
			}
		})
	}
}

func TestOtherPaginatedCardSurfacesLoadLaterPages(t *testing.T) {
	tests := []struct {
		name, path, marker, first, later string
		list                             func(*Client) ([]string, error)
		want                             []string
	}{
		{
			name: "alerts", path: "/alerts", marker: "data-alert-id",
			first: `<div data-alert-id="a1" data-alert-scroll-anchor="a1"><p class="font-semibold">First</p></div>`,
			later: `<div data-alert-id="a2" data-alert-scroll-anchor="a2"><p class="font-semibold">Later</p></div>`,
			list: func(c *Client) ([]string, error) {
				items, err := c.ListAlerts(context.Background(), "p1")
				out := make([]string, 0, len(items))
				for _, item := range items {
					out = append(out, item.ID)
				}
				return out, err
			},
			want: []string{"a1", "a2"},
		},
		{
			name: "models", path: "/models", marker: "data-model-id",
			first: `<div data-model-id="m1" data-model-name="First"></div>`, later: `<div data-model-id="m2" data-model-name="Later"></div>`,
			list: func(c *Client) ([]string, error) {
				items, err := c.ListModels(context.Background(), "p1")
				out := make([]string, 0, len(items))
				for _, item := range items {
					out = append(out, item.ID)
				}
				return out, err
			},
			want: []string{"m1", "m2"},
		},
		{
			name: "agents", path: "/agents", marker: "data-agent-id",
			first: `<div data-agent-id="g1" data-agent-name="First"></div>`, later: `<div data-agent-id="g2" data-agent-name="Later"></div>`,
			list: func(c *Client) ([]string, error) {
				items, err := c.ListAgents(context.Background(), "p1")
				out := make([]string, 0, len(items))
				for _, item := range items {
					out = append(out, item.ID)
				}
				return out, err
			},
			want: []string{"g1", "g2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "text/html")
				w.Header().Set("X-OpenVibely-Card-Page-Has-More", strconv.FormatBool(requests == 1))
				if requests == 1 {
					_, _ = fmt.Fprintf(w, `<div data-card-pagination-root data-card-pagination-card-selector="[%s]" data-card-pagination-key="%s" data-card-pagination-has-more="true">%s</div>`, tt.marker, tt.marker, tt.first)
					return
				}
				_, _ = io.WriteString(w, tt.later)
			}))
			defer srv.Close()
			c, _ := New(srv.URL)
			got, err := tt.list(c)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("items = %#v, want %#v", got, tt.want)
			}
			if requests != 2 {
				t.Fatalf("requests = %d, want 2", requests)
			}
		})
	}
}

func TestPaginatedPageTextAppendsOnlyUniqueContinuationCards(t *testing.T) {
	tests := []struct {
		name, path, firstBody, nextBody string
		load                            func(*Client) (string, error)
		fixed                           []string
		orderedCards                    []string
	}{
		{
			name: "channels", path: "/channels",
			firstBody: `<div id="channels-container" data-card-pagination-root data-card-pagination-card-selector="[data-webhook-id]" data-card-pagination-key="data-webhook-id" data-card-pagination-has-more="true">
				<h1>Channels heading</h1><button>Add channel control</button><div data-channel-type="telegram">Telegram fixed card</div>
				<div id="webhook-card-list"><div data-webhook-id="w1">First webhook</div><div data-webhook-id="shared">Shared webhook</div></div>
			</div>`,
			nextBody: `<div id="channels-container" data-card-pagination-root data-card-pagination-card-selector="[data-webhook-id]" data-card-pagination-key="data-webhook-id" data-card-pagination-has-more="false">
				<h1>Channels heading</h1><button>Add channel control</button><div data-channel-type="telegram">Telegram fixed card</div>
				<div id="webhook-card-list"><div data-webhook-id="shared">Shared webhook duplicate</div><div data-webhook-id="w2">Later webhook</div></div>
			</div>`,
			load:         func(c *Client) (string, error) { return c.GetChannels(context.Background(), "p1") },
			fixed:        []string{"Channels heading", "Add channel control", "Telegram fixed card"},
			orderedCards: []string{"First webhook", "Shared webhook", "Later webhook"},
		},
		{
			name: "automations", path: "/automations",
			firstBody: `<div id="automations-container" data-card-pagination-root data-card-pagination-card-selector="[data-automation-url]" data-card-pagination-key="data-automation-url" data-card-pagination-has-more="true">
				<h1>Automations heading</h1><button>New automation control</button><div id="automations-card-list"><div data-automation-url="/automations/a1">First automation</div><div data-automation-url="/automations/shared">Shared automation</div></div>
			</div>`,
			nextBody: `<div id="automations-container" data-card-pagination-root data-card-pagination-card-selector="[data-automation-url]" data-card-pagination-key="data-automation-url" data-card-pagination-has-more="false">
				<h1>Automations heading</h1><button>New automation control</button><div id="automations-card-list"><div data-automation-url="/automations/shared">Shared automation duplicate</div><div data-automation-url="/automations/a2">Later automation</div></div>
			</div>`,
			load:         func(c *Client) (string, error) { return c.GetAutomations(context.Background(), "p1") },
			fixed:        []string{"Automations heading", "New automation control"},
			orderedCards: []string{"First automation", "Shared automation", "Later automation"},
		},
		{
			name: "personality", path: "/personality",
			firstBody: `<div id="personality-container"><h1>Personality heading</h1><div id="personality-section" data-card-pagination-root data-card-pagination-card-selector="[data-personality-pagination-card='true']" data-card-pagination-key="data-personality-key" data-card-pagination-has-more="true">
				<button>Add personality control</button><div data-personality-key="base" data-personality-pagination-card="false">Base fixed card</div><div data-personality-key="custom-one" data-personality-pagination-card="true">First custom personality</div><div data-personality-key="shared" data-personality-pagination-card="true">Shared custom personality</div>
			</div></div>`,
			nextBody: `<div id="personality-container"><h1>Personality heading</h1><div id="personality-section" data-card-pagination-root data-card-pagination-card-selector="[data-personality-pagination-card='true']" data-card-pagination-key="data-personality-key" data-card-pagination-has-more="false">
				<button>Add personality control</button><div data-personality-key="base" data-personality-pagination-card="false">Base fixed card</div><div data-personality-key="shared" data-personality-pagination-card="true">Shared custom personality duplicate</div><div data-personality-key="custom-two" data-personality-pagination-card="true">Later custom personality</div>
			</div></div>`,
			load:         func(c *Client) (string, error) { return c.GetPersonality(context.Background(), "p1") },
			fixed:        []string{"Personality heading", "Add personality control", "Base fixed card"},
			orderedCards: []string{"First custom personality", "Shared custom personality", "Later custom personality"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != tt.path || r.URL.Query().Get("project_id") != "p1" {
					t.Errorf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
				}
				w.Header().Set("Content-Type", "text/html")
				w.Header().Set("X-OpenVibely-Card-Page-Has-More", strconv.FormatBool(requests == 1))
				if requests == 1 {
					_, _ = io.WriteString(w, tt.firstBody)
					return
				}
				_, _ = io.WriteString(w, tt.nextBody)
			}))
			defer srv.Close()

			c, err := New(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			text, err := tt.load(c)
			if err != nil {
				t.Fatal(err)
			}
			if requests != 2 {
				t.Fatalf("requests = %d, want 2", requests)
			}
			for _, fixed := range tt.fixed {
				if count := strings.Count(text, fixed); count != 1 {
					t.Errorf("%q count = %d, want 1 in %q", fixed, count, text)
				}
			}
			last := -1
			for _, card := range tt.orderedCards {
				if count := strings.Count(text, card); count != 1 {
					t.Errorf("%q count = %d, want 1 in %q", card, count, text)
				}
				index := strings.Index(text, card)
				if index <= last {
					t.Errorf("card %q index = %d after %d in %q", card, index, last, text)
				}
				last = index
			}
			if strings.Contains(text, "duplicate") {
				t.Errorf("duplicate continuation card leaked into text: %q", text)
			}
		})
	}
}

func TestPaginatedCardContinuationFailureIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("card_page") == "1" {
			http.Error(w, "failed", http.StatusBadGateway)
			return
		}
		w.Header().Set("X-OpenVibely-Card-Page-Has-More", "true")
		_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-skill-handle]" data-card-pagination-key="data-skill-handle" data-card-pagination-has-more="true"><div data-skill-handle="first" data-skill-name="First"></div></div>`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	items, err := c.ListSkills(context.Background(), "p1")
	if err == nil || !strings.Contains(err.Error(), "loading card page 2 after 1 cards") {
		t.Fatalf("error = %v", err)
	}
	if items != nil {
		t.Fatalf("partial items = %#v, want nil on reported failure", items)
	}
}

// TestChannelActionSlackRemoteTranslatesToDisconnect verifies that "remove" for
// Slack routes to /channels/slack/disconnect (not /channels/slack/remove),
// matching the backend's OAuth disconnect route.
func TestChannelActionSlackRemoveTranslatesToDisconnect(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c, _ := New(srv.URL)

	if err := c.ChannelAction(context.Background(), "slack", "remove", "p1"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/channels/slack/disconnect" {
		t.Errorf("path = %q, want /channels/slack/disconnect", gotPath)
	}
}

func TestCreateWebhookReturnsSecretFreeScopedDetail(t *testing.T) {
	const secret = "create-response-secret"
	var createForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/channels/webhooks":
			if r.URL.Query().Get("project_id") != "p1" {
				t.Errorf("create project = %q", r.URL.Query().Get("project_id"))
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			createForm = r.PostForm
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"id":"new-id","project_id":"p1","secret":%q}`, secret)
		case r.Method == http.MethodGet && r.URL.Path == "/channels/webhooks/new-id":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"id":"new-id","project_id":"p1","name":"Created","enabled":true,"path_token":"new-token","secret":%q,"default_priority":2,"agent_ids":[]}`, secret)
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	created, err := c.CreateWebhook(context.Background(), "p1", Webhook{Name: "Created", Enabled: true, DefaultPriority: 2, AgentIDs: []string{"a1"}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(created)
	if strings.Contains(string(encoded), secret) || created.Path != "/webhooks/inbound/new-token" {
		t.Fatalf("created = %s %#v", encoded, created)
	}
	if createForm.Get("enabled") != "true" || createForm.Get("agent_ids") != "a1" || createForm.Get("default_priority") != "2" {
		t.Fatalf("create form = %#v", createForm)
	}
}

func TestListWebhooksPaginatesStablyAndPreservesScope(t *testing.T) {
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RequestURI())
		if r.URL.Query().Get("project_id") != "project two" {
			t.Errorf("project_id = %q", r.URL.Query().Get("project_id"))
		}
		w.Header().Set(cardPageMoreHeader, strconv.FormatBool(r.URL.Query().Get("card_page") == ""))
		if r.URL.Query().Get("card_page") == "" {
			_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-webhook-id]" data-card-pagination-key="data-webhook-id"><div data-webhook-id="w1" data-webhook-name="Pager Duty" data-webhook-enabled="true" data-webhook-token="token-one" data-webhook-default-priority="3"></div><div data-webhook-id="shared" data-webhook-name="First"></div></div>`)
			return
		}
		_, _ = io.WriteString(w, `<div data-card-pagination-root data-card-pagination-card-selector="[data-webhook-id]" data-card-pagination-key="data-webhook-id"><div data-webhook-id="shared" data-webhook-name="Duplicate"></div><div data-webhook-id="w2" data-webhook-name="Build Hook" data-webhook-enabled="false" data-webhook-token="token-two" data-webhook-default-priority="2"></div></div>`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL)

	got, err := c.ListWebhooks(context.Background(), "project two")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "w1" || got[1].Name != "First" || got[2].ID != "w2" {
		t.Fatalf("webhooks = %#v", got)
	}
	if got[0].ProjectID != "project two" || got[0].Path != "/webhooks/inbound/token-one" || !got[0].Enabled || got[0].DefaultPriority != 3 {
		t.Fatalf("first webhook = %#v", got[0])
	}
	if len(requests) != 2 || !strings.Contains(requests[1], "offset=2") || !strings.Contains(requests[1], "project_id=project+two") {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestWebhookDetailAndMutationContracts(t *testing.T) {
	const secret = "must-not-appear-in-detail-json"
	var updateForm url.Values
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		if r.URL.Query().Get("project_id") != "p1" {
			t.Errorf("project_id = %q", r.URL.Query().Get("project_id"))
		}
		switch {
		case r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"id":"w1","project_id":"p1","name":"Hook","enabled":false,"path_token":"safe-token","secret":%q,"system_instructions":"keep system","title_template":"keep title","prompt_template":"keep prompt","default_priority":4,"agent_ids":["a1","a2"]}`, secret)
		case r.Method == http.MethodPut:
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			updateForm = r.PostForm
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/test"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"task_id":"task-123"}`)
		case strings.HasSuffix(r.URL.Path, "/rotate-secret"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"secret":"new-safe-secret"}`)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	ctx := context.Background()

	detail, err := c.GetWebhook(ctx, "p1", "w1")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(detail)
	if strings.Contains(string(encoded), secret) || strings.Contains(fmt.Sprintf("%#v", detail), secret) {
		t.Fatalf("detail disclosed secret: %s %#v", encoded, detail)
	}
	if detail.Path != "/webhooks/inbound/safe-token" || detail.Name != "Hook" || len(detail.AgentIDs) != 2 {
		t.Fatalf("detail = %#v", detail)
	}
	detail.Name = "Renamed"
	if _, err := c.UpdateWebhook(ctx, "p1", *detail); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"name": "Renamed", "enabled": "false", "system_instructions": "keep system", "title_template": "keep title", "prompt_template": "keep prompt", "default_priority": "4", "agent_ids": "a1,a2"} {
		if updateForm.Get(key) != want {
			t.Errorf("form[%s] = %q, want %q", key, updateForm.Get(key), want)
		}
	}
	testResult, err := c.TestWebhook(ctx, "p1", "w1")
	if err != nil || testResult.TaskID != "task-123" {
		t.Fatalf("test = %#v, %v", testResult, err)
	}
	rotation, err := c.RotateWebhookSecret(ctx, "p1", "w1")
	if err != nil || rotation.Secret != "new-safe-secret" {
		t.Fatalf("rotation = %#v, %v", rotation, err)
	}
	if err := c.DeleteWebhook(ctx, "p1", "w1"); err != nil {
		t.Fatal(err)
	}
	_ = calls
}
