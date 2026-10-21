package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	scheduleListingBenchmarkDelay   = 25 * time.Millisecond
	scheduleListingBenchmarkSamples = 20
)

func BenchmarkScheduleListingProfiles(b *testing.B) {
	for _, taskCount := range []int{10, 100, 500} {
		var current, catalog scheduleBenchmarkStats
		b.Run(fmt.Sprintf("tasks_%d/current", taskCount), func(b *testing.B) {
			current = benchmarkScheduleListingProfile(b, taskCount, false)
		})
		b.Run(fmt.Sprintf("tasks_%d/catalog", taskCount), func(b *testing.B) {
			catalog = benchmarkScheduleListingProfile(b, taskCount, true)
		})
		if taskCount == 100 && float64(catalog.p95) > float64(current.p95)*0.30 {
			b.Errorf("100-task p95 reduction = %.1f%%, want at least 70%% (current %.1fms, catalog %.1fms)",
				100*(1-float64(catalog.p95)/float64(current.p95)),
				float64(current.p95)/float64(time.Millisecond),
				float64(catalog.p95)/float64(time.Millisecond))
		}
	}
}

type scheduleBenchmarkStats struct {
	p50 time.Duration
	p95 time.Duration
}

func benchmarkScheduleListingProfile(b *testing.B, taskCount int, catalog bool) scheduleBenchmarkStats {
	legacyTasks := make([]Task, taskCount)
	legacyJSON := make(map[string]any, taskCount)
	catalogRows := make([]scheduleCatalogRecord, taskCount)
	for i := range taskCount {
		taskID := fmt.Sprintf("task-%04d", i)
		scheduleID := fmt.Sprintf("schedule-%04d", i)
		title := fmt.Sprintf("Schedule task %04d", i)
		legacyTasks[i] = Task{ID: taskID, ProjectID: "p1", Title: title}
		legacyJSON[taskID] = struct {
			ID          string       `json:"id"`
			ProjectID   string       `json:"project_id"`
			Title       string       `json:"title"`
			Attachments []Attachment `json:"attachments"`
		}{
			ID: taskID, ProjectID: "p1", Title: title,
			Attachments: []Attachment{
				{ID: "attachment-1", TaskID: taskID, FileName: "design-notes.txt", FilePath: strings.Repeat("a", 512), FileSize: 8192},
				{ID: "attachment-2", TaskID: taskID, FileName: "fixture-data.json", FilePath: strings.Repeat("b", 512), FileSize: 16384},
			},
		}
		catalogRows[i] = scheduleCatalogRecord{
			ScheduleID:  scheduleID,
			TaskID:      taskID,
			Title:       title,
			TaskTitle:   title,
			NextRun:     scheduleBenchmarkNextRun(),
			Enabled:     scheduleBenchmarkEnabled(),
			DisplayText: title + " — daily",
		}
	}
	legacyPayload, err := json.Marshal(struct {
		Tasks []any `json:"tasks"`
	}{Tasks: mapValues(legacyJSON, legacyTasks)})
	if err != nil {
		b.Fatal(err)
	}
	catalogPayload, err := json.Marshal(scheduleCatalogResponse{
		ProjectID: "p1",
		Summary:   "Upcoming schedules",
		Schedules: &catalogRows,
	})
	if err != nil {
		b.Fatal(err)
	}

	var requests atomic.Int64
	var responseBytes atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(scheduleListingBenchmarkDelay)
		requests.Add(1)
		var body []byte
		switch r.URL.Path {
		case "/api/schedules/catalog":
			if !catalog {
				b.Errorf("current profile unexpectedly requested schedule catalog")
			}
			body = catalogPayload
		case "/schedule":
			body = []byte(`<div id="schedule-content">Upcoming schedules</div>`)
		case "/api/tasks/reference-catalog":
			body = legacyPayload
		default:
			if strings.HasPrefix(r.URL.Path, "/tasks/") {
				taskID := strings.TrimPrefix(r.URL.Path, "/tasks/")
				index := -1
				for i := range legacyTasks {
					if legacyTasks[i].ID == taskID {
						index = i
						break
					}
				}
				if index >= 0 {
					body = []byte(fmt.Sprintf(`<div id="task-detail-content"><div id="schedule-card-%s" data-schedule-id="schedule-%04d" data-schedule-enabled="true" data-schedule-next-run="2030-04-05T09:30:00.125Z"><div class="font-semibold">%s</div><span>Next: 2030-04-05 9:30 AM</span></div></div>`, legacyTasks[index].ID, index, legacyTasks[index].Title))
				}
			}
		}
		if body == nil {
			b.Errorf("unexpected benchmark request %s", r.URL.RequestURI())
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
		} else {
			w.Header().Set("Content-Type", "text/html")
		}
		n, _ := w.Write(body)
		responseBytes.Add(int64(n))
	}))
	defer srv.Close()
	c, err := New(srv.URL)
	if err != nil {
		b.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = scheduleDetailConcurrencyLimit + 1
	transport.MaxIdleConnsPerHost = scheduleDetailConcurrencyLimit + 1
	c.http.Transport = transport

	latencies := make([]time.Duration, 0, scheduleListingBenchmarkSamples*b.N)
	var totalRequests, totalBytes int64
	for iteration := 0; iteration < b.N; iteration++ {
		for sample := 0; sample < scheduleListingBenchmarkSamples; sample++ {
			requests.Store(0)
			responseBytes.Store(0)
			started := time.Now()
			if catalog {
				_, _, err = c.GetSchedule(context.Background(), "p1")
			} else {
				err = benchmarkCurrentScheduleListing(c)
			}
			latencies = append(latencies, time.Since(started))
			if err != nil {
				b.Fatal(err)
			}
			gotRequests := requests.Load()
			if catalog && gotRequests > 3 {
				b.Fatalf("catalog profile requests = %d, want at most 3", gotRequests)
			}
			if !catalog && gotRequests != int64(taskCount+2) {
				b.Fatalf("current profile requests = %d, want %d", gotRequests, taskCount+2)
			}
			totalRequests += gotRequests
			totalBytes += responseBytes.Load()
		}
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	samples := len(latencies)
	p50 := percentileScheduleLatency(latencies, 0.50)
	p95 := percentileScheduleLatency(latencies, 0.95)
	rawSamplesMS := make([]float64, len(latencies))
	for i, latency := range latencies {
		rawSamplesMS[i] = float64(latency) / float64(time.Millisecond)
	}
	b.Logf("latency_samples_ms=%v", rawSamplesMS)
	ops := float64(samples)
	b.ReportMetric(float64(totalRequests)/ops, "req/op")
	b.ReportMetric(float64(totalBytes)/ops, "response_bytes/op")
	b.ReportMetric(float64(p50)/float64(time.Millisecond), "p50_ms")
	b.ReportMetric(float64(p95)/float64(time.Millisecond), "p95_ms")
	b.ReportMetric(float64(samples), "samples")
	return scheduleBenchmarkStats{p50: p50, p95: p95}
}

func benchmarkCurrentScheduleListing(c *Client) error {
	entries, _, err := c.getScheduleWeek(context.Background(), "p1", 0)
	if err != nil {
		return err
	}
	tasks, err := c.ListTaskReferences(context.Background(), "p1")
	if err != nil {
		return err
	}
	byTask, err := c.getTaskScheduleEntries(context.Background(), "p1", tasks)
	if err != nil {
		return err
	}
	positions := make(map[string]int, len(entries))
	unique := make([]ScheduleEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.ScheduleID == "" {
			continue
		}
		if position, ok := positions[entry.ScheduleID]; ok {
			unique[position] = mergeScheduleEntry(unique[position], entry)
			continue
		}
		positions[entry.ScheduleID] = len(unique)
		unique = append(unique, entry)
	}
	for _, task := range tasks {
		for _, entry := range byTask[task.ID] {
			if position, ok := positions[entry.ScheduleID]; ok {
				unique[position] = mergeScheduleEntry(unique[position], entry)
				continue
			}
			positions[entry.ScheduleID] = len(unique)
			unique = append(unique, entry)
		}
	}
	return nil
}

func mapValues(values map[string]any, ordered []Task) []any {
	out := make([]any, len(ordered))
	for i, task := range ordered {
		out[i] = values[task.ID]
	}
	return out
}

func scheduleBenchmarkNextRun() *time.Time {
	tm, _ := time.Parse(time.RFC3339Nano, "2030-04-05T09:30:00.125Z")
	return &tm
}

func scheduleBenchmarkEnabled() *bool {
	enabled := true
	return &enabled
}

func percentileScheduleLatency(samples []time.Duration, percentile float64) time.Duration {
	index := int(float64(len(samples))*percentile+0.999999) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(samples) {
		index = len(samples) - 1
	}
	return samples[index]
}
