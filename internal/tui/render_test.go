package tui

import (
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
