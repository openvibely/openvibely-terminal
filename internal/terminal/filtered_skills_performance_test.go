package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

type filteredSkillsServerOptions struct {
	catalogDelay   time.Duration
	detailDelay    time.Duration
	slowFirst      bool
	duplicateFirst bool
	failHandle     string
	blockDetails   <-chan struct{}
	waitForCancel  bool
	started        chan<- struct{}
}

type filteredSkillsServerStats struct {
	mu               sync.Mutex
	catalogRequests  int
	detailRequests   int
	activeDetails    int
	maxActiveDetails int
	catalogBytes     int64
	detailBytes      int64
	requestedHandles []string
	completedHandles []string
}

func newFilteredSkillsServer(t testing.TB, catalogSize, matchCount int, options filteredSkillsServerOptions) (*client.Client, *filteredSkillsServerStats) {
	t.Helper()
	fixture := filteredSkillsCatalogFixture(catalogSize, matchCount, options.duplicateFirst)
	stats := &filteredSkillsServerStats{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/skills" {
			stats.mu.Lock()
			stats.catalogRequests++
			stats.catalogBytes += int64(len(fixture))
			stats.mu.Unlock()
			if options.catalogDelay > 0 {
				time.Sleep(options.catalogDelay)
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, fixture)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/skills/") || !strings.HasSuffix(r.URL.Path, "/details") {
			http.NotFound(w, r)
			return
		}

		handle := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/skills/"), "/details")
		index, err := filteredSkillIndex(handle)
		if err != nil || index >= matchCount {
			http.Error(w, "unexpected detail request", http.StatusNotFound)
			return
		}
		stats.mu.Lock()
		stats.detailRequests++
		stats.activeDetails++
		if stats.activeDetails > stats.maxActiveDetails {
			stats.maxActiveDetails = stats.activeDetails
		}
		stats.requestedHandles = append(stats.requestedHandles, handle)
		stats.mu.Unlock()
		if options.started != nil {
			select {
			case options.started <- struct{}{}:
			default:
			}
		}
		defer func() {
			stats.mu.Lock()
			stats.activeDetails--
			stats.completedHandles = append(stats.completedHandles, handle)
			stats.mu.Unlock()
		}()
		if options.waitForCancel {
			<-r.Context().Done()
			return
		}
		if options.blockDetails != nil {
			select {
			case <-options.blockDetails:
			case <-r.Context().Done():
				return
			}
		}
		delay := options.detailDelay
		if options.slowFirst && index == 0 {
			delay += 100 * time.Millisecond
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		if handle == options.failHandle {
			http.Error(w, "detail failed", http.StatusBadGateway)
			return
		}
		body := filteredSkillDetailJSON(index)
		stats.mu.Lock()
		stats.detailBytes += int64(len(body))
		stats.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	c, err := client.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c, stats
}

func filteredSkillsCatalogFixture(catalogSize, matchCount int, duplicateFirst bool) string {
	var page strings.Builder
	page.Grow(catalogSize * 200)
	page.WriteString("<div>")
	for i := 0; i < catalogSize; i++ {
		handle := filteredSkillHandle(i, i < matchCount)
		name := "Other skill"
		description := "outside filter"
		if i < matchCount {
			name = fmt.Sprintf("Matching skill %04d", i)
			description = "matched description"
		}
		scope := filteredSkillScope(i)
		fmt.Fprintf(&page, `<div data-skill-handle=%q data-skill-name=%q data-skill-description=%q data-skill-scope=%q data-skill-source=%q data-skill-enabled=%t data-skill-always-use=%t></div>`, handle, name, description, scope, scope, i%2 == 0, i%3 == 0)
	}
	if duplicateFirst && matchCount > 0 {
		page.WriteString(`<div data-skill-handle="match-0000" data-skill-name="Duplicate catalog entry" data-skill-description="matched duplicate" data-skill-scope="project" data-skill-source="project" data-skill-enabled="false"></div>`)
	}
	page.WriteString("</div>")
	return page.String()
}

func filteredSkillHandle(index int, matched bool) string {
	if matched {
		return fmt.Sprintf("match-%04d", index)
	}
	return fmt.Sprintf("other-%04d", index)
}

func filteredSkillIndex(handle string) (int, error) {
	if !strings.HasPrefix(handle, "match-") {
		return 0, fmt.Errorf("unexpected handle %q", handle)
	}
	var index int
	_, err := fmt.Sscanf(strings.TrimPrefix(handle, "match-"), "%d", &index)
	return index, err
}

func filteredSkillScope(index int) string {
	if index%2 == 0 {
		return "project"
	}
	return "global"
}

func filteredSkillDetail(index int) client.Skill {
	handle := filteredSkillHandle(index, true)
	scope := filteredSkillScope(index)
	return client.Skill{
		Handle:      handle,
		Name:        fmt.Sprintf("Matching skill %04d", index),
		Description: "matched description",
		Scope:       scope,
		Source:      scope,
		Content:     fmt.Sprintf("full body %04d", index),
		Enabled:     index%2 == 0,
		AlwaysUse:   index%3 == 0,
	}
}

func filteredSkillDetailJSON(index int) string {
	body, _ := json.Marshal(filteredSkillDetail(index))
	return string(body)
}

func TestListSkillsWithFilteredContentMatchThresholdsAndOutputBytes(t *testing.T) {
	for _, matchCount := range []int{0, 1, 5, 10, 11, 100} {
		t.Run(fmt.Sprintf("matches=%d", matchCount), func(t *testing.T) {
			catalogSize := max(4, matchCount+3)
			c, stats := newFilteredSkillsServer(t, catalogSize, matchCount, filteredSkillsServerOptions{
				duplicateFirst: matchCount == 11,
				detailDelay:    time.Millisecond,
				slowFirst:      matchCount >= 11,
			})
			filter := "match"
			if matchCount == 0 {
				filter = "not-present"
			}
			got, err := listSkillsWithFilteredContent(context.Background(), c, "p1", filter)
			if err != nil {
				t.Fatal(err)
			}
			wantSkills := make([]client.Skill, 0, matchCount)
			for i := 0; i < matchCount; i++ {
				wantSkills = append(wantSkills, filteredSkillDetail(i))
			}
			gotJSON, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, err := json.Marshal(wantSkills)
			if err != nil {
				t.Fatal(err)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("JSON bytes = %s, want %s", gotJSON, wantJSON)
			}
			if len(got) != matchCount {
				t.Fatalf("skills = %d, want %d", len(got), matchCount)
			}
			stats.mu.Lock()
			catalogRequests, detailRequests, maxActive := stats.catalogRequests, stats.detailRequests, stats.maxActiveDetails
			stats.mu.Unlock()
			if catalogRequests != 1 {
				t.Fatalf("catalog requests = %d, want 1", catalogRequests)
			}
			if detailRequests != matchCount {
				t.Fatalf("detail requests = %d, want exactly %d (unmatched skills must not load)", detailRequests, matchCount)
			}
			if matchCount < 11 && maxActive > 1 {
				t.Fatalf("small match set used %d concurrent requests, want serial loading", maxActive)
			}
			if maxActive > clientSkillDetailWorkerLimitForTest {
				t.Fatalf("maximum in-flight details = %d, want at most %d", maxActive, clientSkillDetailWorkerLimitForTest)
			}
		})
	}
}

const clientSkillDetailWorkerLimitForTest = 8

func TestListSkillsWithFilteredContentBoundsWorkersAndPreservesOrder(t *testing.T) {
	const skillCount = 100
	release := make(chan struct{})
	started := make(chan struct{}, skillCount)
	c, stats := newFilteredSkillsServer(t, skillCount+7, skillCount, filteredSkillsServerOptions{
		duplicateFirst: true,
		slowFirst:      true,
		blockDetails:   release,
		started:        started,
	})
	type result struct {
		skills []client.Skill
		err    error
	}
	finished := make(chan result, 1)
	go func() {
		skills, err := listSkillsWithFilteredContent(context.Background(), c, "p1", "match")
		finished <- result{skills: skills, err: err}
	}()
	for i := 0; i < clientSkillDetailWorkerLimitForTest; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("timed out waiting for initial detail wave")
		}
	}
	select {
	case <-started:
		close(release)
		t.Fatal("detail loader started more than eight concurrent requests")
	case <-time.After(20 * time.Millisecond):
	}
	stats.mu.Lock()
	initialDetails, initialMax := stats.detailRequests, stats.maxActiveDetails
	stats.mu.Unlock()
	if initialDetails != clientSkillDetailWorkerLimitForTest || initialMax != clientSkillDetailWorkerLimitForTest {
		close(release)
		t.Fatalf("initial wave details=%d max-in-flight=%d, want %d", initialDetails, initialMax, clientSkillDetailWorkerLimitForTest)
	}
	close(release)
	var loaded []client.Skill
	select {
	case result := <-finished:
		if result.err != nil {
			t.Fatal(result.err)
		}
		loaded = result.skills
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for all filtered skill details")
	}
	if len(loaded) != skillCount {
		t.Fatalf("loaded skills = %d, want %d", len(loaded), skillCount)
	}
	for i, skill := range loaded {
		want := filteredSkillDetail(i)
		if skill != want {
			t.Fatalf("skill[%d] = %+v, want %+v", i, skill, want)
		}
	}
	stats.mu.Lock()
	defer stats.mu.Unlock()
	if stats.detailRequests != skillCount {
		t.Fatalf("detail requests = %d, want %d", stats.detailRequests, skillCount)
	}
	if stats.maxActiveDetails > clientSkillDetailWorkerLimitForTest {
		t.Fatalf("maximum in-flight details = %d, want at most %d", stats.maxActiveDetails, clientSkillDetailWorkerLimitForTest)
	}
	if len(stats.completedHandles) != skillCount || stats.completedHandles[0] == "match-0000" {
		t.Fatalf("detail completion order = %v, want out-of-order completion", stats.completedHandles[:min(10, len(stats.completedHandles))])
	}
}

func TestListSkillsWithFilteredContentFailureAndCancellation(t *testing.T) {
	t.Run("detail failure propagates", func(t *testing.T) {
		c, stats := newFilteredSkillsServer(t, 18, 11, filteredSkillsServerOptions{failHandle: "match-0005"})
		_, err := listSkillsWithFilteredContent(context.Background(), c, "p1", "match")
		var statusErr *client.HTTPStatusError
		if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusBadGateway {
			t.Fatalf("error = %v, want HTTP 502 detail failure", err)
		}
		stats.mu.Lock()
		defer stats.mu.Unlock()
		for _, handle := range stats.requestedHandles {
			if strings.HasPrefix(handle, "other-") {
				t.Fatalf("unmatched skill detail requested: %q", handle)
			}
		}
	})

	t.Run("cancellation joins detail requests", func(t *testing.T) {
		started := make(chan struct{}, clientSkillDetailWorkerLimitForTest)
		c, stats := newFilteredSkillsServer(t, 110, 100, filteredSkillsServerOptions{
			waitForCancel: true,
			started:       started,
		})
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan error, 1)
		go func() {
			_, err := listSkillsWithFilteredContent(ctx, c, "p1", "match")
			finished <- err
		}()
		select {
		case <-started:
			cancel()
		case <-time.After(2 * time.Second):
			cancel()
			t.Fatal("timed out waiting for filtered detail request")
		}
		select {
		case err := <-finished:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context cancellation", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("filtered detail loading did not return after cancellation")
		}
		deadline := time.Now().Add(time.Second)
		for {
			stats.mu.Lock()
			active := stats.activeDetails
			stats.mu.Unlock()
			if active == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("active details after cancellation = %d, want zero", active)
			}
			time.Sleep(time.Millisecond)
		}
		stats.mu.Lock()
		defer stats.mu.Unlock()
		if stats.maxActiveDetails > clientSkillDetailWorkerLimitForTest {
			t.Fatalf("maximum in-flight details = %d, want at most %d", stats.maxActiveDetails, clientSkillDetailWorkerLimitForTest)
		}
		for _, handle := range stats.requestedHandles {
			if strings.HasPrefix(handle, "other-") {
				t.Fatalf("unmatched skill detail requested: %q", handle)
			}
		}
	})
}

func TestFilteredSkillsSmallSetLatencyEvidence(t *testing.T) {
	if os.Getenv("OPENVIBELY_PERF_EVIDENCE") != "1" {
		t.Skip("set OPENVIBELY_PERF_EVIDENCE=1 to run repeated latency and allocation evidence")
	}
	for _, matchCount := range []int{0, 1, 5, 10} {
		t.Run(fmt.Sprintf("matches=%d", matchCount), func(t *testing.T) {
			c, _ := newFilteredSkillsServer(t, 100, matchCount, filteredSkillsServerOptions{})
			const runs = 21
			serialDurations := make([]time.Duration, 0, runs)
			boundedDurations := make([]time.Duration, 0, runs)
			measure := func(serial bool) time.Duration {
				started := time.Now()
				var skills []client.Skill
				var err error
				if serial {
					skills, err = listSkillsWithFilteredContentSerial(context.Background(), c, "p1", "match")
				} else {
					skills, err = listSkillsWithFilteredContent(context.Background(), c, "p1", "match")
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, err := json.Marshal(skills); err != nil {
					t.Fatal(err)
				}
				return time.Since(started)
			}
			for i := 0; i < runs; i++ {
				if i%2 == 0 {
					serialDurations = append(serialDurations, measure(true))
					boundedDurations = append(boundedDurations, measure(false))
				} else {
					boundedDurations = append(boundedDurations, measure(false))
					serialDurations = append(serialDurations, measure(true))
				}
			}
			serialSorted := slices.Clone(serialDurations)
			boundedSorted := slices.Clone(boundedDurations)
			slices.Sort(serialSorted)
			slices.Sort(boundedSorted)
			serialMedian := skillMedianDuration(serialSorted)
			boundedMedian := skillMedianDuration(boundedSorted)

			t.Logf("runs=%d serial-median=%s bounded-median=%s serial-samples=%v bounded-samples=%v",
				runs, serialMedian, boundedMedian, serialSorted, boundedSorted)
			if float64(boundedMedian) > float64(serialMedian)*1.10 {
				t.Errorf("bounded median %s exceeds serial median %s by more than 10%%", boundedMedian, serialMedian)
			}
		})
	}
}

func BenchmarkListSkillsWithFilteredContentSerialVsBounded(b *testing.B) {
	cases := []struct {
		catalogSize int
		matchCount  int
		detailDelay time.Duration
	}{
		{catalogSize: 100, matchCount: 0},
		{catalogSize: 100, matchCount: 1},
		{catalogSize: 100, matchCount: 5},
		{catalogSize: 100, matchCount: 10},
		{catalogSize: 100, matchCount: 11},
		{catalogSize: 100, matchCount: 100},
		{catalogSize: 100, matchCount: 100, detailDelay: 25 * time.Millisecond},
	}
	for _, tc := range cases {
		name := fmt.Sprintf("catalog=%d/matches=%d/detail-delay=%dms", tc.catalogSize, tc.matchCount, tc.detailDelay/time.Millisecond)
		var serialJSON, boundedJSON string
		b.Run(name+"/serial", func(b *testing.B) {
			serialJSON = benchmarkFilteredSkillsPath(b, tc, true)
		})
		b.Run(name+"/bounded", func(b *testing.B) {
			boundedJSON = benchmarkFilteredSkillsPath(b, tc, false)
		})
		if serialJSON != boundedJSON {
			b.Fatalf("serial JSON = %s, bounded JSON = %s", serialJSON, boundedJSON)
		}
	}
}

func benchmarkFilteredSkillsPath(b *testing.B, tc struct {
	catalogSize int
	matchCount  int
	detailDelay time.Duration
}, serial bool) string {
	b.Helper()
	catalogDelay := time.Duration(0)
	if tc.detailDelay > 0 {
		catalogDelay = time.Millisecond
	}
	c, stats := newFilteredSkillsServer(b, tc.catalogSize, tc.matchCount, filteredSkillsServerOptions{
		catalogDelay: catalogDelay,
		detailDelay:  tc.detailDelay,
	})
	b.ReportAllocs()
	b.ResetTimer()
	durations := make([]time.Duration, 0, b.N)
	var output string
	for i := 0; i < b.N; i++ {
		started := time.Now()
		var skills []client.Skill
		var err error
		if serial {
			skills, err = listSkillsWithFilteredContentSerial(context.Background(), c, "p1", "match")
		} else {
			skills, err = listSkillsWithFilteredContent(context.Background(), c, "p1", "match")
		}
		if err != nil {
			b.Fatal(err)
		}
		encoded, err := json.Marshal(skills)
		if err != nil {
			b.Fatal(err)
		}
		output = string(encoded)
		durations = append(durations, time.Since(started))
		if len(skills) != tc.matchCount {
			b.Fatalf("skills = %d, want %d", len(skills), tc.matchCount)
		}
	}
	b.StopTimer()
	slices.Sort(durations)
	stats.mu.Lock()
	catalogRequests, detailRequests := stats.catalogRequests, stats.detailRequests
	maxActive := stats.maxActiveDetails
	catalogBytes, detailBytes := stats.catalogBytes, stats.detailBytes
	stats.mu.Unlock()
	wantDetails := b.N * tc.matchCount
	if catalogRequests != b.N || detailRequests != wantDetails {
		b.Fatalf("requests: catalog=%d details=%d, want catalog=%d details=%d", catalogRequests, detailRequests, b.N, wantDetails)
	}
	wantMaxActive := 1
	if !serial && tc.matchCount >= 11 && tc.detailDelay > 0 {
		wantMaxActive = clientSkillDetailWorkerLimitForTest
	}
	if maxActive > clientSkillDetailWorkerLimitForTest || (serial && tc.matchCount >= 11 && maxActive != wantMaxActive) || (!serial && tc.matchCount >= 11 && tc.detailDelay > 0 && maxActive != wantMaxActive) {
		b.Fatalf("maximum in-flight details = %d, want %d under this workload (cap %d)", maxActive, wantMaxActive, clientSkillDetailWorkerLimitForTest)
	}
	b.ReportMetric(float64(skillMedianDuration(durations)), "median-wall-ns/op")
	b.ReportMetric(float64(len(durations)), "samples")
	b.ReportMetric(float64(catalogRequests+detailRequests)/float64(max(1, b.N)), "requests/op")
	b.ReportMetric(float64(catalogBytes+detailBytes)/float64(max(1, b.N)), "response-B/op")
	b.ReportMetric(float64(maxActive), "max-inflight")
	if len(durations) > 1 && len(durations) <= 20 {
		b.Logf("wall-samples-ns=%v", durations)
	}
	return output
}

func listSkillsWithFilteredContentSerial(ctx context.Context, c *client.Client, projectID, filter string) ([]client.Skill, error) {
	if filter == "" {
		return c.ListSkillsWithContent(ctx, projectID)
	}
	skills, err := c.ListSkills(ctx, projectID)
	if err != nil {
		return nil, err
	}
	filtered := make([]client.Skill, 0, len(skills))
	for _, skill := range skills {
		if filterMatch(filter, skill.Handle, skill.Name, skill.Description) {
			filtered = append(filtered, skill)
		}
	}
	for i, skill := range filtered {
		detail, err := c.GetSkillDetail(ctx, projectID, skill.Handle, skill.Scope)
		if err != nil {
			return nil, err
		}
		filtered[i] = detail
	}
	return filtered, nil
}

// skillMedianDuration expects sorted samples and averages the two middle values
// for even sample counts.
func skillMedianDuration(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	middle := len(durations) / 2
	if len(durations)%2 == 1 {
		return durations[middle]
	}
	return durations[middle-1] + (durations[middle]-durations[middle-1])/2
}
