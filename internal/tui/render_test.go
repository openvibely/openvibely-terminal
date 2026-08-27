package tui

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/openvibely/openvibely-tui/internal/client"
)

// Styled cells carry invisible ANSI escapes; measuring them with len() makes
// every column with colour drift out of alignment.
func TestTableAlignsStyledCells(t *testing.T) {
	rows := [][]string{
		{"ID", "STATUS", "TITLE"},
		{"abc12345", statusMark("running"), "First task"},
		{"def67890", statusMark("completed"), "Second task"},
		{"ghi01234", statusMark("failed"), "Third task"},
	}
	out := table(rows)

	// The final column must start at the same display column on every row.
	var starts []int
	for _, line := range strings.Split(out, "\n") {
		plain := stripANSI(line)
		idx := strings.LastIndex(plain, "  ")
		if idx < 0 {
			t.Fatalf("unexpected row layout: %q", plain)
		}
		starts = append(starts, lipgloss.Width(plain[:idx]))
	}
	for i, s := range starts {
		if s != starts[0] {
			t.Errorf("row %d last column starts at %d, want %d (columns misaligned)\n%s",
				i, s, starts[0], out)
		}
	}
}

func TestTablePreservesStyledAndWideCellOutput(t *testing.T) {
	rows := [][]string{
		{"ID", "STATE", "TITLE", "DETAIL"},
		{"task-1", "\x1b[31m失败\x1b[0m", "宽内容：日本語", "first"},
		{"task-2", statusMark("completed"), "café résumé", "second"},
	}

	got := table(rows)
	want := tableWithoutWidthCache(rows)
	if got != want {
		t.Fatalf("cached table output changed\n got: %q\nwant: %q", got, want)
	}
}

func TestTableHandlesEmptyAndRaggedRows(t *testing.T) {
	if got := table(nil); got != "" {
		t.Errorf("table(nil) = %q, want empty output", got)
	}
	if got := table([][]string{}); got != "" {
		t.Errorf("table(empty) = %q, want empty output", got)
	}

	rows := [][]string{
		{"HEADER", "VALUE", "TAIL"},
		{"one"},
		{},
		{"two", "columns"},
	}
	got := table(rows)
	want := tableWithoutWidthCache(rows)
	if got != want {
		t.Fatalf("cached ragged table output changed\n got: %q\nwant: %q", got, want)
	}
	if !strings.Contains(stripANSI(got), "HEADER") {
		t.Fatalf("first-row header content missing: %q", got)
	}
}

// tableWithoutWidthCache mirrors the pre-optimization implementation for exact
// output regression tests and before/after benchmarks. It intentionally measures
// every non-final cell again while rendering its padding.
func tableWithoutWidthCache(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	widths := make([]int, cols)
	for _, r := range rows {
		for i, cell := range r {
			if w := lipgloss.Width(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}
	var b strings.Builder
	for ri, r := range rows {
		var line strings.Builder
		for i, cell := range r {
			if i == len(r)-1 {
				line.WriteString(cell)
				break
			}
			line.WriteString(cell)
			line.WriteString(strings.Repeat(" ", widths[i]-lipgloss.Width(cell)+2))
		}
		text := strings.TrimRight(line.String(), " ")
		if ri == 0 {
			b.WriteString(headerStyle.Render(text) + "\n")
		} else {
			b.WriteString(text + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func benchmarkTableRows(rowCount int) [][]string {
	rows := make([][]string, rowCount+1)
	rows[0] = []string{"ID", "STATE", "TITLE", "DETAIL"}
	for i := 1; i <= rowCount; i++ {
		id := "task-" + strconv.Itoa(i)
		state := statusMark("running")
		if i%3 == 0 {
			state = statusMark("completed")
		}
		rows[i] = []string{id, state, "任务 " + id, "wide detail " + id}
	}
	return rows
}

var tableBenchmarkSink string

func BenchmarkTable10KRows4Columns(b *testing.B) {
	rows := benchmarkTableRows(10_000)
	for _, benchmark := range []struct {
		name  string
		table func([][]string) string
	}{
		{name: "cached_widths", table: table},
		{name: "uncached_widths", table: tableWithoutWidthCache},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tableBenchmarkSink = benchmark.table(rows)
			}
		})
	}
}

// Wide/multi-byte titles must not be truncated by byte length.
func TestTruncateMeasuresDisplayWidth(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 20, "short"},
		{"exactly-ten", 11, "exactly-ten"},
		{"truncate me please", 8, "truncat…"},
		// Accented characters are multi-byte but one cell wide: a byte-based
		// truncate would cut this far too early.
		{"café résumé naïve", 20, "café résumé naïve"},
	}
	for _, c := range cases {
		if got := truncate(c.in, c.n); got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
		if w := lipgloss.Width(truncate(c.in, c.n)); w > c.n {
			t.Errorf("truncate(%q, %d) width = %d, exceeds limit", c.in, c.n, w)
		}
	}
}

func TestTruncateNeverCutsMidRune(t *testing.T) {
	got := truncate("ünïcödé strîng that is long", 10)
	if !strings.ContainsRune(got, '…') {
		t.Errorf("expected an ellipsis, got %q", got)
	}
	for _, r := range got {
		if r == '\uFFFD' {
			t.Errorf("truncate produced an invalid rune: %q", got)
		}
	}
}

// The board must render titles, not blanks, and must show its badges.
func TestRenderBoardShowsTitlesAndBadges(t *testing.T) {
	tasks := []client.Task{
		{ID: "t-1", Title: "Refactor the API", Category: "backlog", Status: "pending",
			Badges: []string{"Goal", "Sonnet"}},
		{ID: "t-2", Title: "Write docs", Category: "active", Status: "running"},
	}
	out := stripANSI(renderBoard(tasks, ""))

	for _, want := range []string{"Refactor the API", "Write docs", "Goal", "Sonnet", "Backlog", "Active"} {
		if !strings.Contains(out, want) {
			t.Errorf("board missing %q:\n%s", want, out)
		}
	}
}

// A task with no scrapeable title should be visibly marked, never blank.
func TestRenderBoardMarksUntitledTasks(t *testing.T) {
	out := stripANSI(renderBoard([]client.Task{
		{ID: "t-9", Title: "", Category: "backlog", Status: "pending"},
	}, ""))
	if !strings.Contains(out, "(untitled)") {
		t.Errorf("expected an untitled marker:\n%s", out)
	}
}

func TestRenderBoardEmptyStates(t *testing.T) {
	if out := stripANSI(renderBoard(nil, "")); !strings.Contains(out, "no tasks yet") {
		t.Errorf("empty board = %q", out)
	}
	tasks := []client.Task{{ID: "t-1", Title: "Alpha", Category: "backlog"}}
	if out := stripANSI(renderBoard(tasks, "zzz")); !strings.Contains(out, "no tasks match") {
		t.Errorf("filtered board = %q", out)
	}
}

// renderBadges drops noise and caps the badge count so rows stay readable.
func TestRenderBadges(t *testing.T) {
	if got := renderBadges(nil); got != "" {
		t.Errorf("no badges = %q", got)
	}
	if got := renderBadges([]string{"No Model"}); got != "" {
		t.Errorf("placeholder badge should be dropped, got %q", got)
	}
	got := stripANSI(renderBadges([]string{"a", "b", "c", "d", "e"}))
	if strings.Count(got, "[") != 3 {
		t.Errorf("expected 3 badges, got %q", got)
	}
}

// renderThread falls back to details when a task has no messages yet.
func TestRenderThreadFallsBackToDetails(t *testing.T) {
	withThread := &client.TaskDetail{Thread: "agent: hello"}
	if !strings.Contains(renderThread(withThread), "agent: hello") {
		t.Error("thread body missing")
	}

	noThread := &client.TaskDetail{Details: "the prompt"}
	out := stripANSI(renderThread(noThread))
	if !strings.Contains(out, "the prompt") || !strings.Contains(out, "no messages yet") {
		t.Errorf("fallback = %q", out)
	}

	if out := stripANSI(renderThread(&client.TaskDetail{})); !strings.Contains(out, "no messages") {
		t.Errorf("empty = %q", out)
	}
}

func TestRenderAutomationsShowsStatesAndFilters(t *testing.T) {
	automations := []client.Automation{
		{ID: "automation-active-001", Name: "Native SDLC", State: "active"},
		{ID: "automation-paused-002", Name: "GitHub SDLC", State: "paused"},
	}
	out := stripANSI(renderAutomations(automations, ""))
	for _, want := range []string{"automation-active-001", "Native SDLC", "active", "automation-paused-002", "GitHub SDLC", "paused"} {
		if !strings.Contains(out, want) {
			t.Errorf("automations output missing %q:\n%s", want, out)
		}
	}

	filtered := stripANSI(renderAutomations(automations, "github"))
	if strings.Contains(filtered, "Native SDLC") || !strings.Contains(filtered, "GitHub SDLC") {
		t.Errorf("automation filter output = %q", filtered)
	}
	if got := stripANSI(renderAutomations(automations, "missing")); !strings.Contains(got, "no automations match missing") {
		t.Errorf("filtered empty state = %q", got)
	}
	if got := stripANSI(renderAutomations(nil, "")); !strings.Contains(got, "create one via the web UI") {
		t.Errorf("empty state = %q", got)
	}
}

// Empty-state messages must include actionable slash-command hints (VISION.md "Friendly By Default").
func TestEmptyStateHints(t *testing.T) {
	cases := []struct {
		name string
		out  string
		hint string
	}{
		{
			name: "alerts",
			out:  stripANSI(renderAlerts(nil, "")),
			hint: "/alerts",
		},
		{
			name: "skills",
			out:  stripANSI(renderSkills(nil, "")),
			hint: "/skills",
		},
		{
			name: "agents",
			out:  stripANSI(renderAgents(nil, "")),
			hint: "/agents",
		},
		{
			name: "models",
			out:  stripANSI(renderModels(nil, "")),
			hint: "web UI",
		},
	}
	for _, c := range cases {
		if !strings.Contains(c.out, c.hint) {
			t.Errorf("empty %s state missing hint %q: %q", c.name, c.hint, c.out)
		}
	}
}

func TestRenderModelCapacityWithoutProviderLimits(t *testing.T) {
	caps := []client.ModelCapacity{{Name: "Sonnet", Running: 1, MaxWorkers: 4, AvailableSlots: 3}}
	base := stripANSI(renderModelCapacity(caps))

	for _, usage := range []*client.UsageAnalytics{nil, {}} {
		out := stripANSI(renderModelCapacityWithUsage(caps, usage))
		if !strings.HasPrefix(out, base) {
			t.Errorf("capacity table changed when provider limits are unavailable:\n%s", out)
		}
		if !strings.Contains(out, "provider limits unavailable") || !strings.Contains(out, "/analytics usage") {
			t.Errorf("missing provider-limit hint:\n%s", out)
		}
	}
}

func TestRenderModelCapacityProviderLimits(t *testing.T) {
	secret := "sk-provider-secret"
	out := stripANSI(renderModelCapacityWithUsage(
		[]client.ModelCapacity{{Model: "gpt-4o", Running: 2, MaxWorkers: 4, AvailableSlots: 2}},
		&client.UsageAnalytics{AccountLimits: []client.AccountUsage{
			{
				Provider:      "OpenAI",
				PlanType:      "team",
				StatusLabel:   "healthy",
				AccountDetail: secret,
				Limits: []client.AccountLimit{{
					Label:       "requests",
					UsedPercent: 72.5,
					ResetsAt:    "2026-08-24T00:00:00Z",
				}},
			},
			{
				Provider:    "Anthropic",
				StatusLabel: "blocked",
				PrimaryLimit: &client.AccountLimit{
					Label:       "tokens",
					UsedPercent: 100,
					ResetsAt:    "tomorrow",
				},
				Error: "quota service unavailable",
			},
		}},
	))

	for _, want := range []string{
		"Provider limits", "OpenAI", "team", "healthy", "72.5%", "2026-08-24T00:00:00Z",
		"Anthropic", "blocked", "100.0%", "tomorrow", "error: quota service unavailable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("provider limits missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, secret) {
		t.Errorf("provider/account detail leaked into capacity output:\n%s", out)
	}
}

func TestRenderExecTimesEmptyAndSmallInputs(t *testing.T) {
	if out := stripANSI(renderExecTimes("Execution time", nil)); !strings.Contains(out, "no data") {
		t.Fatalf("empty execution times = %q, want no-data message", out)
	}

	for n := 1; n <= 12; n++ {
		times := make([]client.AvgExecutionTime, n)
		for i := range times {
			times[i] = client.AvgExecutionTime{
				ID:    "row-" + strconv.Itoa(i),
				AvgMs: float64(n - i),
			}
		}
		before := append([]client.AvgExecutionTime(nil), times...)
		out := stripANSI(renderExecTimes("Execution time", times))
		lines := strings.Split(out, "\n")
		if len(lines) != n+1 {
			t.Fatalf("%d execution times rendered %d lines, want %d:\n%s", n, len(lines), n+1, out)
		}
		for i := range times {
			if !strings.Contains(out, "row-"+strconv.Itoa(i)) {
				t.Errorf("%d execution times missing row-%d:\n%s", n, i, out)
			}
		}
		if !reflect.DeepEqual(times, before) {
			t.Errorf("renderExecTimes mutated %d-record input: got %+v, want %+v", n, times, before)
		}
	}
}

func TestRenderExecTimesRendersZeroAndNegativeValues(t *testing.T) {
	out := stripANSI(renderExecTimes("Execution time", []client.AvgExecutionTime{
		{ID: "zero", AvgMs: 0},
		{ID: "negative", AvgMs: -125},
	}))
	for _, want := range []string{"zero", "negative", "0ms", "-125ms"} {
		if !strings.Contains(out, want) {
			t.Fatalf("non-positive execution time output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderExecTimesSelectsTopRowsWithStableTies(t *testing.T) {
	times := []client.AvgExecutionTime{
		{ID: "drop-low-early", AvgMs: 0},
		{ID: "drop-low-late", AvgMs: 0},
		{ID: "tie-early", AvgMs: 500},
		{ID: "top-1", AvgMs: 1000},
		{ID: "tie-middle", AvgMs: 500},
		{ID: "negative-early", AvgMs: -10},
		{ID: "top-2", AvgMs: 900},
		{ID: "tie-late", AvgMs: 500},
		{ID: "top-3", AvgMs: 800},
		{ID: "negative-late", AvgMs: -20},
		{ID: "top-4", AvgMs: 700},
		{ID: "top-5", AvgMs: 600},
		{ID: "top-6", AvgMs: 550},
		{ID: "top-7", AvgMs: 400},
		{ID: "top-8", AvgMs: 300},
	}
	before := append([]client.AvgExecutionTime(nil), times...)
	out := stripANSI(renderExecTimes("Execution time", times))

	wantOrder := []string{
		"top-1", "top-2", "top-3", "top-4", "top-5", "top-6",
		"tie-early", "tie-middle", "tie-late", "top-7", "top-8", "drop-low-early",
	}
	previous := -1
	for _, want := range wantOrder {
		index := strings.Index(out, want)
		if index < 0 {
			t.Fatalf("missing selected row %q:\n%s", want, out)
		}
		if index <= previous {
			t.Fatalf("row %q is out of descending/stable order:\n%s", want, out)
		}
		previous = index
	}
	for _, omitted := range []string{"drop-low-late", "negative-early", "negative-late"} {
		if strings.Contains(out, omitted) {
			t.Errorf("row %q exceeded the top-12 limit:\n%s", omitted, out)
		}
	}
	if !reflect.DeepEqual(times, before) {
		t.Errorf("renderExecTimes mutated input: got %+v, want %+v", times, before)
	}
}

func TestRenderExecTimesUsesLongNamesAndIDsWithoutChangingSelection(t *testing.T) {
	longName := strings.Repeat("long-name-", 8)
	longID := strings.Repeat("long-id-", 8)
	out := stripANSI(renderExecTimes("Execution time", []client.AvgExecutionTime{
		{Name: longName, ID: "named-id", AvgMs: 200},
		{ID: longID, AvgMs: 100},
	}))

	if !strings.Contains(out, "long-name-") || !strings.Contains(out, "long-id-") {
		t.Fatalf("long name/ID prefixes missing:\n%s", out)
	}
	if strings.Count(out, "…") != 2 {
		t.Fatalf("expected both long values to be display-truncated:\n%s", out)
	}
	if strings.Contains(out, longName) || strings.Contains(out, longID) {
		t.Fatalf("long name or ID was rendered in full:\n%s", out)
	}
}

func TestRenderExecTimesMatchesFullSortBaseline(t *testing.T) {
	times := []client.AvgExecutionTime{
		{ID: "value-4", AvgMs: 4},
		{ID: "value-negative", AvgMs: -1},
		{ID: "value-9", AvgMs: 9},
		{ID: "value-zero", AvgMs: 0},
		{ID: "value-2", AvgMs: 2},
		{ID: "value-11", AvgMs: 11},
		{ID: "value-1", AvgMs: 1},
		{ID: "value-8", AvgMs: 8},
		{ID: "value-3", AvgMs: 3},
		{ID: "value-10", AvgMs: 10},
		{ID: "value-5", AvgMs: 5},
		{ID: "value-7", AvgMs: 7},
		{ID: "value-6", AvgMs: 6},
	}
	got := renderExecTimes("Execution time", times)
	want := renderExecTimesFullSort("Execution time", times)
	if got != want {
		t.Fatalf("bounded renderer changed baseline output\n got: %q\nwant: %q", got, want)
	}
}

// renderExecTimesFullSort is the pre-optimization implementation used only for
// output regression and paired benchmark comparisons.
func renderExecTimesFullSort(title string, times []client.AvgExecutionTime) string {
	if len(times) == 0 {
		return sectionStyle.Render(title) + "\n  " + dimStyle.Render("no data")
	}
	sorted := append([]client.AvgExecutionTime(nil), times...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].AvgMs > sorted[j].AvgMs })
	if len(sorted) > 12 {
		sorted = sorted[:12]
	}
	maxMs := sorted[0].AvgMs

	var b strings.Builder
	b.WriteString(sectionStyle.Render(title) + "\n")
	for _, t := range sorted {
		fmt.Fprintf(&b, "  %-26s %s %8s %s\n",
			truncate(firstNonEmpty(t.Name, t.ID), 26), bar(t.AvgMs, maxMs, 18),
			humanMs(t.AvgMs), dimStyle.Render(fmt.Sprintf("n=%d", t.Count)))
	}
	return strings.TrimRight(b.String(), "\n")
}

var renderExecTimesBenchmarkSink string

func benchmarkExecTimesFixture(size int) []client.AvgExecutionTime {
	times := make([]client.AvgExecutionTime, size)
	for i := range times {
		times[i] = client.AvgExecutionTime{
			ID:    "execution-" + strconv.Itoa(i),
			AvgMs: float64((i * 7919) % 1_000_000),
			Count: i % 100,
		}
	}
	return times
}

func BenchmarkRenderExecTimesLargeInput(b *testing.B) {
	fixtures := []struct {
		name  string
		times []client.AvgExecutionTime
	}{
		{name: "10K", times: benchmarkExecTimesFixture(10_000)},
		{name: "100K", times: benchmarkExecTimesFixture(100_000)},
	}

	for _, fixture := range fixtures {
		fixture := fixture
		b.Run(fixture.name+"/full_copy_sort", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				renderExecTimesBenchmarkSink = renderExecTimesFullSort("Execution time", fixture.times)
			}
		})
		b.Run(fixture.name+"/bounded_top_12", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				renderExecTimesBenchmarkSink = renderExecTimes("Execution time", fixture.times)
			}
		})
	}
}

func TestConnectionPresentationAcrossHealthTransitions(t *testing.T) {
	m := newTestModel(t)
	cases := []struct {
		name             string
		check            *connCheckedMsg
		header           string
		statusContains   []string
		statusNotContain []string
		hint             string
	}{
		{
			name:             "initial connecting",
			header:           "● connecting",
			statusContains:   []string{"server", "connecting"},
			statusNotContain: []string{"offline", "health check failed"},
			hint:             "connecting: /help works offline · check -server or OPENVIBELY_SERVER_URL if this stays here",
		},
		{
			name:           "healthy",
			check:          &connCheckedMsg{},
			header:         "● online",
			statusContains: []string{"server", "connected"},
			statusNotContain: []string{
				"connecting", "offline", "health check failed", "start/check your local backend",
			},
			hint: "type to chat · / for commands · ↑↓ history · pgup/pgdn scroll · ctrl+l clear · ctrl+c quit",
		},
		{
			name:           "failed",
			check:          &connCheckedMsg{err: errors.New("health check failed")},
			header:         "● offline",
			statusContains: []string{"server", "offline", "health check failed", "start/check your local backend", "set -server <url> or OPENVIBELY_SERVER_URL"},
			hint:           "offline: start/check backend · set -server or OPENVIBELY_SERVER_URL · /status",
		},
		{
			name:           "healthy again",
			check:          &connCheckedMsg{},
			header:         "● online",
			statusContains: []string{"server", "connected"},
			statusNotContain: []string{
				"connecting", "offline", "health check failed", "start/check your local backend",
			},
			hint: "type to chat · / for commands · ↑↓ history · pgup/pgdn scroll · ctrl+l clear · ctrl+c quit",
		},
		{
			name:           "failed again",
			check:          &connCheckedMsg{err: errors.New("health check failed again")},
			header:         "● offline",
			statusContains: []string{"server", "offline", "health check failed again", "start/check your local backend", "set -server <url> or OPENVIBELY_SERVER_URL"},
			hint:           "offline: start/check backend · set -server or OPENVIBELY_SERVER_URL · /status",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.check != nil {
				next, _ := m.Update(*tc.check)
				m = next.(Model)
			}

			header := stripANSI(m.renderHeader())
			if !strings.Contains(header, tc.header) {
				t.Errorf("header missing %q:\n%s", tc.header, header)
			}

			status := stripANSI(m.renderStatus())
			for _, want := range tc.statusContains {
				if !strings.Contains(status, want) {
					t.Errorf("status missing %q:\n%s", want, status)
				}
			}
			for _, unwanted := range tc.statusNotContain {
				if strings.Contains(status, unwanted) {
					t.Errorf("status unexpectedly contains %q:\n%s", unwanted, status)
				}
			}
			if got := m.hint(); got != tc.hint {
				t.Errorf("hint = %q, want %q", got, tc.hint)
			}
		})
	}
}

func TestConnectionHintPreservesTaskThreadPriority(t *testing.T) {
	m := newTestModel(t)
	m.threadID = "task-1"
	if got, want := m.hint(), "connecting: /help works offline · check -server or OPENVIBELY_SERVER_URL if this stays here"; got != want {
		t.Errorf("initial thread hint = %q, want %q", got, want)
	}

	next, _ := m.Update(connCheckedMsg{})
	m = next.(Model)
	if got, want := m.hint(), "in task thread · messages reply to this task · /chat to exit · / for commands"; got != want {
		t.Errorf("healthy thread hint = %q, want %q", got, want)
	}

	next, _ = m.Update(connCheckedMsg{err: errors.New("health check failed")})
	m = next.(Model)
	if got, want := m.hint(), "offline: start/check backend · set -server or OPENVIBELY_SERVER_URL · /status"; got != want {
		t.Errorf("offline thread hint = %q, want %q", got, want)
	}
}

// stripANSI removes escape sequences so tests can assert on visible text.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++ // skip the 'm'
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
