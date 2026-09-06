package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

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
func TestTruncateMatchesCurrentBehavior(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		valid  bool
		limits []int
	}{
		{name: "short ASCII", input: "short", valid: true, limits: []int{1, 5, 20}},
		{name: "exact width ASCII", input: "exact", valid: true, limits: []int{5}},
		{name: "over limit ASCII", input: "truncate me please", valid: true, limits: []int{1, 8, 12}},
		{name: "accented", input: "café résumé naïve", valid: true, limits: []int{4, 10, 20}},
		{name: "wide", input: "日本語の長いタイトル", valid: true, limits: []int{1, 2, 6, 12}},
		{name: "combining", input: "e\u0301e\u0301e\u0301 and more", valid: true, limits: []int{1, 2, 3, 8}},
		{name: "newlines", input: "first\nsecond\nthird", valid: true, limits: []int{5, 6, 12}},
		{name: "spaces", input: "a  b    c", valid: true, limits: []int{1, 2, 5, 8}},
		{name: "ANSI styled", input: "\x1b[31mhello styled text\x1b[0m", valid: true, limits: []int{5, 8, 20}},
		{name: "malformed UTF-8", input: string([]byte{0xff, 'a', 0xc3, 'b', 0xe2, 0x82, 'c'}), valid: false, limits: []int{1, 2, 4, 8}},
		{name: "non-positive limit", input: "line one\nline two", valid: true, limits: []int{0, -1}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, limit := range tc.limits {
				got := truncate(tc.input, limit)
				want := truncateBaseline(tc.input, limit)
				if got != want {
					t.Errorf("truncate(%q, %d) = %q, want current behavior %q", tc.input, limit, got, want)
				}
				if tc.valid && !utf8.ValidString(got) {
					t.Errorf("truncate(%q, %d) returned invalid UTF-8: %q", tc.input, limit, got)
				}
			}
		})
	}
}

func TestTruncateLargeNewlineInputMatchesCurrentBehavior(t *testing.T) {
	input := strings.Repeat("payload\n", 1<<16)
	for _, limit := range []int{24, 70, 96} {
		got := truncate(input, limit)
		want := truncateBaseline(input, limit)
		if got != want {
			t.Errorf("truncate(large newline input, %d) = %q, want %q", limit, got, want)
		}
	}
}

var truncateBenchmarkSink string

func truncateBenchmarkFixture(size int) string {
	return strings.Repeat("x", size)
}

func truncateBenchmarkNewlineFixture(size int) string {
	return strings.Repeat("x\n", size/2)
}

func BenchmarkTruncateLargeFixtures(b *testing.B) {
	fixtures := []struct {
		name  string
		input string
	}{
		{name: "1KiB", input: truncateBenchmarkFixture(1 << 10)},
		{name: "64KiB", input: truncateBenchmarkFixture(64 << 10)},
		{name: "1MiB", input: truncateBenchmarkFixture(1 << 20)},
		{name: "1KiB_newlines", input: truncateBenchmarkNewlineFixture(1 << 10)},
		{name: "64KiB_newlines", input: truncateBenchmarkNewlineFixture(64 << 10)},
		{name: "1MiB_newlines", input: truncateBenchmarkNewlineFixture(1 << 20)},
	}
	limits := []int{24, 70, 96}

	for _, fixture := range fixtures {
		fixture := fixture
		for _, limit := range limits {
			limit := limit
			b.Run(fmt.Sprintf("%s/limit_%d/baseline", fixture.name, limit), func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					truncateBenchmarkSink = truncateBaseline(fixture.input, limit)
				}
			})
			b.Run(fmt.Sprintf("%s/limit_%d/bounded", fixture.name, limit), func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					truncateBenchmarkSink = truncate(fixture.input, limit)
				}
			})
		}
	}
}

var lifecycleBenchmarkSink string

func lifecycleBenchmarkPayload(size int, scalar string) map[string]any {
	return map[string]any{"message": strings.Repeat(scalar, size/len(scalar)), "status": "completed"}
}

func BenchmarkRenderLifecycleEventsLargePayload(b *testing.B) {
	for _, fixture := range []struct {
		name   string
		scalar string
	}{
		{name: "ASCII", scalar: "x"},
		{name: "zero_width", scalar: "\u0301"},
	} {
		for _, size := range []struct {
			name  string
			bytes int
		}{
			{name: "1KiB", bytes: 1 << 10},
			{name: "64KiB", bytes: 64 << 10},
			{name: "1MiB", bytes: 1 << 20},
		} {
			for _, count := range []int{1, 100} {
				payload := lifecycleBenchmarkPayload(size.bytes, fixture.scalar)
				events := make([]client.LifecycleEvent, count)
				for i := range events {
					events[i] = client.LifecycleEvent{
						ID:        fmt.Sprintf("event-%03d", i),
						Seq:       i + 1,
						EventType: "completed",
						Payload:   payload,
					}
				}
				task := client.Task{ID: "task-1", Title: "Large payload task"}
				execution := client.LifecycleExecution{ID: "execution-1", Status: "completed"}
				b.Run(fmt.Sprintf("%s/%s/%d_events", fixture.name, size.name, count), func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(size.bytes * count))
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						lifecycleBenchmarkSink = renderLifecycleEvents(task, execution, events)
					}
				})
			}
		}
	}
}

// truncateBaseline mirrors the pre-optimization helper for exact output
// comparisons and paired benchmark measurements.
func truncateBaseline(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	runes := []rune(s)
	width, cut := 0, len(runes)
	for i, r := range runes {
		w := lipgloss.Width(string(r))
		if width+w > n-1 {
			cut = i
			break
		}
		width += w
	}
	return strings.TrimRight(string(runes[:cut]), " ") + "…"
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

func TestRenderLifecycleRenderersShareTaskHeading(t *testing.T) {
	cases := []struct {
		name      string
		task      client.Task
		wantTitle string
	}{
		{
			name:      "titled task",
			task:      client.Task{ID: "task-123456", Title: "Deploy API"},
			wantTitle: "Deploy API",
		},
		{
			name:      "empty title uses short ID",
			task:      client.Task{ID: "task-123456"},
			wantTitle: "task-123",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executionHeading := strings.SplitN(renderLifecycleExecutions(tc.task, nil), "\n", 2)[0]
			eventHeading := strings.SplitN(renderLifecycleEvents(tc.task, client.LifecycleExecution{ID: "execution-1"}, nil), "\n", 2)[0]
			if executionHeading != eventHeading {
				t.Fatalf("lifecycle headings differ\nexecutions: %q\nevents: %q", executionHeading, eventHeading)
			}

			want := fmt.Sprintf("%s  (id %s)", tc.wantTitle, tc.task.ID)
			if got := stripANSI(executionHeading); got != want {
				t.Fatalf("lifecycle heading = %q, want %q", got, want)
			}
		})
	}
}

func TestLifecyclePayloadSummaryMatchesCanonicalJSON(t *testing.T) {
	payloads := []map[string]any{
		{"status": "completed", "attempt": float64(2), "ok": true},
		{"escaping": "<tag> & line\n\t\"quoted\"", "unicode": "café 日本語 e\u0301"},
		{"nested": map[string]any{"z": nil, "a": []any{"one", float64(2)}}},
		{"nil_containers": map[string]any(nil), "nil_items": []any(nil)},
	}
	for _, payload := range payloads {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal fixture: %v", err)
		}
		if got, want := lifecyclePayloadSummary(payload), truncate(string(encoded), 96); got != want {
			t.Errorf("lifecyclePayloadSummary(%v) = %q, want canonical preview %q", payload, got, want)
		}
	}
}

func TestLifecyclePayloadSummaryPreservesMarshalableGoValues(t *testing.T) {
	payloads := []map[string]any{
		{"items": []string{"one", "two"}},
		{"bytes": []byte("ordinary bytes")},
		{"named_string": lifecyclePreviewNamedString("ordinary")},
		{"marshaler": lifecyclePreviewMarshaler("ordinary")},
		{"nested": struct {
			Name string `json:"name"`
		}{Name: "ordinary"}},
		{"named": lifecyclePreviewNamedMap{"b": 2, "a": 1}},
	}
	for _, payload := range payloads {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal fixture: %v", err)
		}
		if got, want := lifecyclePayloadSummary(payload), truncate(string(encoded), 96); got != want {
			t.Errorf("lifecyclePayloadSummary(%v) = %q, want canonical preview %q", payload, got, want)
		}
	}
}

type lifecyclePreviewNamedMap map[string]int
type lifecyclePreviewNamedString string
type lifecyclePreviewMarshaler string

func (value lifecyclePreviewMarshaler) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"custom": string(value)})
}

func TestLifecyclePayloadSummaryBoundsZeroWidthScalar(t *testing.T) {
	for _, value := range []any{
		strings.Repeat("\u0301", 1<<20),
		lifecyclePreviewNamedString(strings.Repeat("\u0301", 1<<20)),
	} {
		payload := map[string]any{"message": value}
		got := lifecyclePayloadSummary(payload)
		if !strings.HasPrefix(got, `{"message":"`) || !strings.HasSuffix(got, "…") {
			t.Fatalf("zero-width scalar preview = %q, want bounded JSON-like prefix", got)
		}
		if len(got) > 8192 {
			t.Fatalf("zero-width scalar preview length = %d, want bounded output", len(got))
		}
		if width := lipgloss.Width(got); width > 96 {
			t.Fatalf("zero-width scalar preview width = %d, want <= 96", width)
		}
	}
}

func TestLifecyclePayloadSummaryValidatesValuesAfterTruncatedPrefix(t *testing.T) {
	for _, bad := range []any{make(chan int), math.Inf(1), math.NaN(), json.Number("invalid")} {
		payload := map[string]any{
			"a-prefix": strings.Repeat("x", 1<<20),
			"z-bad":    bad,
		}
		if got := lifecyclePayloadSummary(payload); got != "<unavailable>" {
			t.Errorf("later invalid value %T preview = %q, want unavailable", bad, got)
		}
	}
}

func TestLifecyclePayloadSummaryBoundsOversizedValues(t *testing.T) {
	large := map[string]any{"message": strings.Repeat("日本語<&\n", 1<<17), "status": "completed"}
	encoded, err := json.Marshal(large)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if got, want := lifecyclePayloadSummary(large), truncate(string(encoded), 96); got != want {
		t.Fatalf("large scalar preview = %q, want %q", got, want)
	}

	var nested any = "leaf"
	for range 8192 {
		nested = []any{nested}
	}
	got := lifecyclePayloadSummary(map[string]any{"nested": nested})
	if !strings.HasPrefix(got, `{"nested":[[[`) || !strings.HasSuffix(got, "…") {
		t.Fatalf("deep nesting preview = %q, want deterministic JSON-like prefix", got)
	}
	if width := lipgloss.Width(got); width > 96 {
		t.Fatalf("deep nesting preview width = %d, want <= 96", width)
	}
}

func TestLifecyclePayloadSummaryFallbacks(t *testing.T) {
	if got := lifecyclePayloadSummary(nil); got != "—" {
		t.Fatalf("empty payload preview = %q, want dash", got)
	}
	if got := lifecyclePayloadSummary(map[string]any{"bad": make(chan int)}); got != "<unavailable>" {
		t.Fatalf("unsupported payload preview = %q, want unavailable fallback", got)
	}
}

func TestRenderLifecycleEventsPreservesEventOrdering(t *testing.T) {
	events := []client.LifecycleEvent{
		{ID: "event-c", Seq: 2, CreatedAt: "2026-01-01T00:00:02Z", EventType: "third", Payload: map[string]any{"n": float64(3)}},
		{ID: "event-b", Seq: 1, CreatedAt: "2026-01-01T00:00:01Z", EventType: "second", Payload: map[string]any{"n": float64(2)}},
		{ID: "event-a", Seq: 1, CreatedAt: "2026-01-01T00:00:01Z", EventType: "first", Payload: map[string]any{"n": float64(1)}},
	}
	out := stripANSI(renderLifecycleEvents(client.Task{ID: "task-1"}, client.LifecycleExecution{ID: "exec-1"}, events))
	first, second, third := strings.Index(out, "first"), strings.Index(out, "second"), strings.Index(out, "third")
	if first < 0 || second < first || third < second {
		t.Fatalf("event order is not seq/time/id stable:\n%s", out)
	}
	if events[0].ID != "event-c" || events[1].ID != "event-b" || events[2].ID != "event-a" {
		t.Fatalf("render mutated source event order: %+v", events)
	}
}

func TestRenderLifecycleEventsDoesNotMutateDecodedPayload(t *testing.T) {
	payload := map[string]any{
		"message": strings.Repeat("payload ", 32),
		"nested":  map[string]any{"ok": true},
		"items":   []any{"one", float64(2)},
	}
	before, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload before render: %v", err)
	}

	_ = renderLifecycleEvents(
		client.Task{ID: "task-1", Title: "Task"},
		client.LifecycleExecution{ID: "execution-1"},
		[]client.LifecycleEvent{{ID: "event-1", Seq: 1, EventType: "completed", Payload: payload}},
	)

	after, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload after render: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("renderLifecycleEvents mutated decoded payload\nbefore: %s\nafter:  %s", before, after)
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

func TestRenderProjectsEmptyStateOffersCreationCommand(t *testing.T) {
	out := stripANSI(renderProjects(nil, nil, ""))
	for _, want := range []string{"no projects", "/projects create <name> <path>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("empty project state missing %q:\n%s", want, out)
		}
	}
}

func TestPersonalityKindPreservesPrecedence(t *testing.T) {
	cases := []struct {
		name        string
		personality client.Personality
		want        string
	}{
		{
			name:        "custom takes precedence",
			personality: client.Personality{HasCustom: true},
			want:        "custom",
		},
		{
			name:        "preset override",
			personality: client.Personality{IsPreset: true, HasCustom: true},
			want:        "override",
		},
		{
			name:        "built-in preset",
			personality: client.Personality{IsPreset: true},
			want:        "built-in",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := personalityKind(tc.personality); got != tc.want {
				t.Errorf("personalityKind(%+v) = %q, want %q", tc.personality, got, tc.want)
			}
		})
	}
}

func TestRenderPersonalityKinds(t *testing.T) {
	personalities := []client.Personality{
		{Key: "alpha", Name: "Alpha", IsPreset: false, HasCustom: true},
		{Key: "beta", Name: "Beta", IsPreset: true, HasCustom: true},
		{Key: "gamma", Name: "Gamma", IsPreset: true},
	}
	list := stripANSI(renderPersonalities(personalities, ""))
	for _, want := range []string{"custom", "override", "built-in"} {
		if !strings.Contains(list, want) {
			t.Errorf("personality list missing %q:\n%s", want, list)
		}
	}

	for _, personality := range personalities {
		detail := stripANSI(renderPersonalityDetail(personality))
		want := fmt.Sprintf("key %s · %s", personality.Key, personalityKind(personality))
		if !strings.Contains(detail, want) {
			t.Errorf("personality detail missing %q:\n%s", want, detail)
		}
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
func TestRenderAutomationDetailShowsGraphRuntimeResourcesAndExternalState(t *testing.T) {
	detail := client.AutomationDetail{
		Automation: client.AutomationMetadata{
			ID:             "au-1",
			ProjectID:      "p1",
			Name:           "Nightly review",
			LifecycleState: "active",
			HealthState:    "healthy",
		},
		Version: client.AutomationVersion{ID: "v1", Version: 4, State: "published"},
		Nodes: []client.AutomationLiveNode{
			{AutomationNode: client.AutomationNode{ID: "n2", NodeKey: "review", Name: "Review"}, DisplayState: "waiting_human", Counts: client.AutomationNodeCounts{Running: 2, Waiting: 1}},
			{AutomationNode: client.AutomationNode{ID: "n1", NodeKey: "start", Name: "Start"}, DisplayState: "running", Counts: client.AutomationNodeCounts{CompletedRecently: 3}},
		},
		Edges: []client.AutomationLiveEdge{
			{AutomationEdge: client.AutomationEdge{ID: "e1", SourceNodeID: "n1", TargetNodeID: "n2", Label: "approved"}, TransitionCount: 8, RecentTransitionCount: 2, SourceName: "Start", TargetName: "Review"},
		},
		Resources:                  []client.AutomationResourceSummary{{NodeKey: "review", ResourceType: "repository", ResourceID: "repo1", Name: "openvibely", Relation: "input", Status: "ready"}},
		ActiveInvocations:          3,
		ActiveWorkItems:            5,
		ExternalState:              client.AutomationExternalState{TrackedResources: 1, TrackedResourcesAvailable: true, StaleAvailable: true, Status: "fresh", LastUpdatedAt: "2025-01-02T03:04:05Z"},
		GraphAvailable:             true,
		NodesAvailable:             true,
		EdgesAvailable:             true,
		NodeCountsAvailable:        true,
		EdgeCountsAvailable:        true,
		ActiveInvocationsAvailable: true,
		ActiveWorkItemsAvailable:   true,
		CountsAvailable:            true,
		ResourcesAvailable:         true,
		ExternalStateAvailable:     true,
	}
	out := stripANSI(renderAutomationDetail(detail))
	for _, want := range []string{
		"Automation: Nightly review", "ID au-1", "project:           p1", "published", "Graph", "Nodes", "Review", "waiting human", "RUN", "Edges", "Start", "approved", "8", "Runtime", "active invocations: 3", "active work items: 5", "Resources", "openvibely", "External state", "fresh", "tracked resources: 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("detail output missing %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "start  running") > strings.Index(out, "review  waiting") {
		t.Errorf("nodes are not rendered deterministically:\n%s", out)
	}
	if strings.Contains(out, "optional section unavailable") || strings.Contains(out, "partial detail") {
		t.Errorf("complete detail unexpectedly reports unavailable optional data:\n%s", out)
	}
}

func TestRenderAutomationDetailSanitizesBackendFieldsWithoutMutatingJSONData(t *testing.T) {
	const hostile = "safe\x1b[2J\nFORGED\tROW\u202e"
	detail := client.AutomationDetail{
		Automation: client.AutomationMetadata{
			ID: hostile, ProjectID: hostile, Name: hostile, Description: hostile,
			LifecycleState: "active", HealthState: hostile,
		},
		Version: client.AutomationVersion{ID: hostile, State: "published", Source: hostile},
		Nodes: []client.AutomationLiveNode{{
			AutomationNode: client.AutomationNode{ID: hostile, Name: hostile}, DisplayState: hostile,
		}},
		Edges: []client.AutomationLiveEdge{{
			AutomationEdge: client.AutomationEdge{SourceNodeID: hostile, TargetNodeID: hostile, Label: hostile},
		}},
		Resources:      []client.AutomationResourceSummary{{NodeKey: hostile, ResourceType: hostile, Name: hostile, Relation: hostile, Status: hostile}},
		ExternalState:  client.AutomationExternalState{Status: hostile, LastUpdatedAt: hostile},
		GraphAvailable: true, NodesAvailable: true, EdgesAvailable: true,
		ResourcesAvailable: true, ExternalStateAvailable: true,
		Warnings: []string{hostile},
	}

	out := renderAutomationDetail(detail)
	plain := stripANSI(out)
	if strings.Contains(out, "\x1b[2J") || strings.Contains(plain, "\nFORGED") || strings.ContainsAny(plain, "\t\r") || strings.ContainsRune(plain, '\u202e') {
		t.Fatalf("automation detail contains backend terminal controls: %q", out)
	}
	if !strings.Contains(plain, "safe FORGED ROW") {
		t.Fatalf("sanitized automation detail lost readable text: %q", plain)
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `safe\u001b[2J\nFORGED\tROW`) {
		t.Fatalf("terminal rendering changed machine-readable data: %s", encoded)
	}
}

func TestRenderAutomationDetailSeparatesUnmatchedEdgeEvidence(t *testing.T) {
	detail := client.AutomationDetail{
		Automation:     client.AutomationMetadata{ID: "au-edge", Name: "Edge evidence", LifecycleState: "active"},
		GraphAvailable: true,
		NodesAvailable: true,
		EdgesAvailable: true,
		Edges: []client.AutomationLiveEdge{{
			AutomationEdge: client.AutomationEdge{Label: "approved"}, TransitionCount: 2, TransitionCountAvailable: true,
		}},
		UnmatchedEdgeDetails: []client.AutomationLiveEdge{{
			AutomationEdge: client.AutomationEdge{EdgeKey: "e1"}, SourceName: "Start", TargetName: "Review",
		}},
		Partial: true,
	}
	out := stripANSI(renderAutomationDetail(detail))
	for _, want := range []string{"0 nodes · 1 edges", "Unmatched edge details", "correlation unavailable", "Start", "Review"} {
		if !strings.Contains(out, want) {
			t.Fatalf("edge evidence output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderAutomationDetailDoesNotClaimDraftGraphWasLoaded(t *testing.T) {
	detail := client.AutomationDetail{
		Automation:     client.AutomationMetadata{ID: "au-draft", ProjectID: "p1", Name: "Draft flow", LifecycleState: "draft"},
		Version:        client.AutomationVersion{State: "draft"},
		GraphAvailable: false,
		NodesAvailable: false,
		EdgesAvailable: false,
		Warnings:       []string{"live graph unavailable: automation is draft"},
	}
	out := stripANSI(renderAutomationDetail(detail))
	for _, want := range []string{"Automation: Draft flow", "lifecycle:         draft", "Graph", "unavailable", "draft automation has no live graph", "nodes: unavailable", "edges: unavailable", "not reported"} {
		if !strings.Contains(out, want) {
			t.Errorf("draft output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "NODE   STATE") || strings.Contains(out, "live graph was returned") {
		t.Errorf("draft output claims graph data was loaded:\n%s", out)
	}
}

func TestRenderAutomationDetailMarksPartialOptionalSections(t *testing.T) {
	detail := client.AutomationDetail{
		Automation:          client.AutomationMetadata{ID: "au-partial", Name: "Partial flow", LifecycleState: "active"},
		Version:             client.AutomationVersion{State: "published"},
		GraphAvailable:      true,
		NodesAvailable:      true,
		EdgesAvailable:      true,
		NodeCountsAvailable: true,
		Edges:               make([]client.AutomationLiveEdge, 0),
		Nodes:               make([]client.AutomationLiveNode, 0),
		Partial:             true,
	}
	out := stripANSI(renderAutomationDetail(detail))
	for _, want := range []string{"Graph", "(empty)", "active invocations: not reported", "not reported — optional section unavailable", "partial detail"} {
		if !strings.Contains(out, want) {
			t.Errorf("partial output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderAutomationDetailDoesNotInventExternalFreshness(t *testing.T) {
	detail := client.AutomationDetail{
		Automation:             client.AutomationMetadata{ID: "au-external", Name: "External flow", LifecycleState: "active"},
		GraphAvailable:         true,
		NodesAvailable:         true,
		EdgesAvailable:         true,
		ExternalState:          client.AutomationExternalState{},
		ExternalStateAvailable: true,
		Partial:                true,
	}
	out := stripANSI(renderAutomationDetail(detail))
	if !strings.Contains(out, "status:            not reported") {
		t.Fatalf("malformed external freshness should be unavailable:\n%s", out)
	}
	if strings.Contains(out, "status:            fresh") || strings.Contains(out, "status:            stale") {
		t.Fatalf("malformed external freshness was invented:\n%s", out)
	}
}

func TestRenderAutomationDetailPreservesFieldAvailabilityAndRecentState(t *testing.T) {
	detail := client.AutomationDetail{
		Automation: client.AutomationMetadata{ID: "au-mixed", Name: "Mixed flow", LifecycleState: "active"},
		Nodes: []client.AutomationLiveNode{{
			AutomationNode: client.AutomationNode{Name: "Recently done"},
			DisplayState:   "completed",
			Counts: client.AutomationNodeCounts{
				Running:          0,
				RunningAvailable: true,
				Failed:           9,
				FailedAvailable:  false,
			},
		}},
		Edges: []client.AutomationLiveEdge{{
			AutomationEdge:                 client.AutomationEdge{SourceNodeID: "Source node", TargetNodeID: "Target node", Label: "approved"},
			TransitionCount:                0,
			TransitionCountAvailable:       true,
			RecentTransitionCount:          7,
			RecentTransitionCountAvailable: false,
		}},
		GraphAvailable:      true,
		NodesAvailable:      true,
		EdgesAvailable:      true,
		NodeCountsAvailable: true,
		EdgeCountsAvailable: true,
	}
	out := stripANSI(renderAutomationDetail(detail))
	if !strings.Contains(out, "recently completed") {
		t.Fatalf("recent state missing from output:\n%s", out)
	}
	var nodeLine, edgeLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Recently done") {
			nodeLine = line
		}
		if strings.Contains(line, "Source node") {
			edgeLine = line
		}
	}
	if nodeLine == "" || !strings.Contains(nodeLine, "0") || !strings.Contains(nodeLine, "—") {
		t.Fatalf("node availability line = %q\nfull output:\n%s", nodeLine, out)
	}
	if edgeLine == "" || !strings.Contains(edgeLine, "0") || !strings.Contains(edgeLine, "—") {
		t.Fatalf("edge availability line = %q\nfull output:\n%s", edgeLine, out)
	}
	if strings.Contains(nodeLine, "9") || strings.Contains(edgeLine, "7") {
		t.Fatalf("unavailable values were rendered:\nnode=%q\nedge=%q", nodeLine, edgeLine)
	}
}

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

func TestRenderVoteRecordsIsDeterministicAndExplicit(t *testing.T) {
	records := []client.VoteRecord{
		{ID: "vote-b", StepExecutionID: "step-1", AgentConfigID: "agent-b", Vote: "reject", Confidence: 0.42, Reasoning: "needs more evidence"},
		{ID: "vote-a", StepExecutionID: "step-1", AgentConfigID: "agent-a", Vote: "approve", Confidence: 0.91, Reasoning: "safe\nbecause the checks passed"},
		{ID: "vote-empty", StepExecutionID: "step-1", AgentConfigID: "agent-c", Vote: "abstain"},
	}
	original := append([]client.VoteRecord(nil), records...)
	out := stripANSI(renderVoteRecords("step-1", records))

	for _, want := range []string{
		"Workflow votes", "step execution: step-1", "AGENT", "VOTE", "CONFIDENCE", "REASONING",
		"agent-a", "approve", "0.91", "safe because the checks passed",
		"agent-b", "reject", "0.42", "needs more evidence", "agent-c", "abstain", "0.00", "—",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("vote output missing %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "agent-a") > strings.Index(out, "agent-b") || strings.Index(out, "agent-b") > strings.Index(out, "agent-c") {
		t.Errorf("votes were not rendered in deterministic agent order:\n%s", out)
	}
	if strings.Contains(out, "safe\nbecause") {
		t.Errorf("reasoning was rendered across multiple lines:\n%s", out)
	}
	if !reflect.DeepEqual(records, original) {
		t.Fatalf("renderVoteRecords mutated its input: got %+v, want %+v", records, original)
	}

	empty := stripANSI(renderVoteRecords("step-empty", nil))
	for _, want := range []string{"step execution: step-empty", "no vote records available for this step execution"} {
		if !strings.Contains(empty, want) {
			t.Errorf("empty vote output missing %q:\n%s", want, empty)
		}
	}
}

func TestRenderWorkersTable(t *testing.T) {
	unlimited := 0
	limited := 2
	overview := workersOverview{
		Global:          &client.GlobalCapacity{MaxWorkers: 4, TotalRunning: 2, QueueSize: 3},
		ModelsAvailable: true,
		Projects: []client.ProjectCapacity{
			{ID: "p-z", Name: "Zulu", Running: 2, QueueSize: 1, MaxWorkers: &limited},
			{ID: "p-a", Name: "Alpha", Running: 0, MaxWorkers: &unlimited},
		},
		Models: []client.ModelCapacity{
			{Name: "Sonnet", Model: "claude-sonnet", Running: 1, MaxWorkers: 3},
		},
	}

	out := stripANSI(renderWorkers(overview))
	for _, want := range []string{
		"Worker capacity", "SCOPE", "NAME", "RUNNING", "QUEUE", "LIMIT", "STATUS",
		"Global", "All Projects", "2 / 4", "3", "At capacity", "Zulu", "2 / 2",
		"Alpha", "No limit", "Idle", "Per-model worker pools", "MODEL", "Sonnet (claude-sonnet)", "1 / 3", "Active",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("worker table missing %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "All Projects") > strings.Index(out, "Zulu") || strings.Index(out, "Zulu") > strings.Index(out, "Alpha") {
		t.Fatalf("worker rows did not preserve global-first backend order:\n%s", out)
	}
}

func TestRenderWorkersEmptyAndLongValues(t *testing.T) {
	longName := strings.Repeat("project-name-", 8)
	longModel := strings.Repeat("model-id-", 10)
	overview := workersOverview{
		Global:          &client.GlobalCapacity{},
		Projects:        []client.ProjectCapacity{{Name: longName}},
		Models:          []client.ModelCapacity{{Name: "Long model", Model: longModel, MaxWorkers: 1}},
		ModelsAvailable: true,
	}
	out := stripANSI(renderWorkers(overview))
	if strings.Contains(out, longName) || strings.Contains(out, longModel) || !strings.Contains(out, "…") {
		t.Fatalf("long worker values were not bounded:\n%s", out)
	}

	empty := stripANSI(renderWorkers(workersOverview{Global: &client.GlobalCapacity{}, Projects: []client.ProjectCapacity{}, Models: []client.ModelCapacity{}, ModelsAvailable: true}))
	if !strings.Contains(empty, "Global") || !strings.Contains(empty, "All Projects") {
		t.Fatalf("empty project result lost the canonical global row:\n%s", empty)
	}
	if !strings.Contains(empty, "no dedicated model worker pools") {
		t.Fatalf("empty model result missing explicit state:\n%s", empty)
	}
}

func TestRenderWorkersUnavailableModels(t *testing.T) {
	overview := newWorkersOverview(
		&client.GlobalCapacity{MaxWorkers: 4},
		[]client.ProjectCapacity{}, nil,
		[]string{"model worker capacity unavailable"}, false,
	)
	out := stripANSI(renderWorkers(overview))
	if !strings.Contains(out, "model worker capacity unavailable") {
		t.Fatalf("unavailable model result missing explicit state:\n%s", out)
	}
	if strings.Contains(out, "no dedicated model worker pools") {
		t.Fatalf("unavailable model result was misrepresented as empty:\n%s", out)
	}

	encoded, err := json.Marshal(overview)
	if err != nil {
		t.Fatal(err)
	}
	got := string(encoded)
	if !strings.Contains(got, `"models":[]`) || !strings.Contains(got, `"models_available":false`) {
		t.Fatalf("unavailable model JSON lost availability state: %s", got)
	}
}

func TestWorkersOverviewJSONCompatibility(t *testing.T) {
	overview := newWorkersOverview(
		&client.GlobalCapacity{MaxWorkers: 5, TotalRunning: 1, QueueSize: 2, AvailableSlots: 4, HasCapacity: true},
		[]client.ProjectCapacity{}, []client.ModelCapacity{}, []string{}, true,
	)
	encoded, err := json.Marshal(overview)
	if err != nil {
		t.Fatal(err)
	}
	got := string(encoded)
	for _, want := range []string{`"workers":[`, `"scope":"global"`, `"name":"All Projects"`, `"running":1`, `"queue":2`, `"limit":5`, `"status":"active"`, `"models":[]`, `"models_available":true`, `"warnings":[]`} {
		if !strings.Contains(got, want) {
			t.Errorf("workers JSON missing %s: %s", want, got)
		}
	}
	for _, unwanted := range []string{"available_slots", "has_capacity", "MaxWorkers"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("workers JSON exposed unrelated field %q: %s", unwanted, got)
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

func TestRenderAnalyticsEmptyStates(t *testing.T) {
	cases := []struct {
		name   string
		title  string
		render func() string
	}{
		{
			name:   "rates",
			title:  "Success / failure",
			render: func() string { return renderRates(nil) },
		},
		{
			name:   "execution times",
			title:  "Avg execution time by task",
			render: func() string { return renderExecTimes("Avg execution time by task", nil) },
		},
		{
			name:   "frequent tasks",
			title:  "Most frequent tasks",
			render: func() string { return renderFrequent(nil) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.render()
			want := sectionStyle.Render(tc.title) + "\n  " + dimStyle.Render("no data")
			if got != want {
				t.Fatalf("empty analytics output changed\n got: %q\nwant: %q", got, want)
			}
			if plain := stripANSI(got); plain != tc.title+"\n  no data" {
				t.Fatalf("ANSI-stripped empty analytics output = %q, want %q", plain, tc.title+"\n  no data")
			}
		})
	}
}

func TestRenderFrequentDrawsBars(t *testing.T) {
	out := stripANSI(renderFrequent([]client.TaskFrequency{
		{TaskTitle: "build", ExecutionCount: 3},
		{TaskTitle: "deploy", ExecutionCount: 1},
	}))
	for _, want := range []string{"Most frequent tasks", "build", "deploy", "3", "1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("frequent-task render missing %q:\n%s", want, out)
		}
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
	m.projects = []client.Project{{ID: "p1", Name: "demo"}}
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
			name:             "failed",
			check:            &connCheckedMsg{err: errors.New("health check failed")},
			header:           "● backend error",
			statusContains:   []string{"server", "backend error (unhealthy)", "health check failed", "check backend logs", "set -server <url> or OPENVIBELY_SERVER_URL"},
			statusNotContain: []string{"offline", "start/check your local backend"},
			hint:             "backend error: backend responded but is unhealthy · check backend logs or /status",
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
			name:             "failed again",
			check:            &connCheckedMsg{err: errors.New("health check failed again")},
			header:           "● backend error",
			statusContains:   []string{"server", "backend error (unhealthy)", "health check failed again", "check backend logs", "set -server <url> or OPENVIBELY_SERVER_URL"},
			statusNotContain: []string{"offline", "start/check your local backend"},
			hint:             "backend error: backend responded but is unhealthy · check backend logs or /status",
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
	if got, want := m.hint(), "backend error: backend responded but is unhealthy · check backend logs or /status"; got != want {
		t.Errorf("unhealthy thread hint = %q, want %q", got, want)
	}
}

func TestRenderAutomationDetailDoesNotUseInvalidStatusFreshness(t *testing.T) {
	detail := client.AutomationDetail{
		Automation:             client.AutomationMetadata{ID: "au-invalid-status", Name: "Invalid status", LifecycleState: "active"},
		GraphAvailable:         true,
		NodesAvailable:         true,
		EdgesAvailable:         true,
		ExternalState:          client.AutomationExternalState{Stale: true, StaleAvailable: true, StatusInvalid: true},
		ExternalStateAvailable: true,
	}
	out := stripANSI(renderAutomationDetail(detail))
	if !strings.Contains(out, "status:            not reported") {
		t.Fatalf("invalid status should render unavailable:\n%s", out)
	}
	if strings.Contains(out, "status:            fresh") || strings.Contains(out, "status:            stale") {
		t.Fatalf("invalid status was replaced by freshness:\n%s", out)
	}
}

func TestRenderAutomationDetailShowsUnmatchedNodeDetails(t *testing.T) {
	detail := client.AutomationDetail{
		Automation:           client.AutomationMetadata{ID: "au-unmatched", Name: "Unmatched", LifecycleState: "active"},
		Nodes:                []client.AutomationLiveNode{{AutomationNode: client.AutomationNode{ID: "n1", Name: "Graph node"}}},
		UnmatchedNodeDetails: []client.AutomationLiveNode{{AutomationNode: client.AutomationNode{NodeKey: "review", Name: "Review", Role: "task"}}, {AutomationNode: client.AutomationNode{NodeKey: "start", Name: "Start", Role: "trigger"}}},
		GraphAvailable:       true,
		NodesAvailable:       true,
		EdgesAvailable:       true,
		Partial:              true,
	}
	out := stripANSI(renderAutomationDetail(detail))
	for _, want := range []string{"Unmatched node details", "correlation unavailable", "Review", "Start"} {
		if !strings.Contains(out, want) {
			t.Errorf("unmatched detail output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderAutomationDetailSortsResourcesByRelation(t *testing.T) {
	detail := client.AutomationDetail{
		Automation:         client.AutomationMetadata{ID: "au-resources", Name: "Resources", LifecycleState: "active"},
		Resources:          []client.AutomationResourceSummary{{NodeKey: "shared", ResourceType: "task", ResourceID: "task-1", Relation: "output", Name: "Task"}, {NodeKey: "shared", ResourceType: "task", ResourceID: "task-1", Relation: "input", Name: "Task"}},
		ResourcesAvailable: true,
	}
	out := stripANSI(renderAutomationDetail(detail))
	input := strings.Index(out, "input")
	output := strings.Index(out, "output")
	if input < 0 || output < 0 || input > output {
		t.Fatalf("resources were not sorted by relation: input=%d output=%d\n%s", input, output, out)
	}
}

func TestRenderAutomationDetailDoesNotUseUnmatchedNodeCountsForGraphRows(t *testing.T) {
	detail := client.AutomationDetail{
		Automation:           client.AutomationMetadata{ID: "au-unmatched-counts", Name: "Unmatched counts", LifecycleState: "active"},
		Nodes:                []client.AutomationLiveNode{{AutomationNode: client.AutomationNode{Name: "Graph node"}}},
		UnmatchedNodeDetails: []client.AutomationLiveNode{{AutomationNode: client.AutomationNode{NodeKey: "detail-only", Name: "Detail only"}, Counts: client.AutomationNodeCounts{Running: 5, RunningAvailable: true}}},
		GraphAvailable:       true,
		NodesAvailable:       true,
		EdgesAvailable:       true,
		NodeCountsAvailable:  true,
		Partial:              true,
	}
	out := stripANSI(renderAutomationDetail(detail))
	var graphLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Graph node") {
			graphLine = line
			break
		}
	}
	if graphLine == "" || !strings.Contains(graphLine, "—") || strings.Contains(graphLine, "0") || strings.Contains(graphLine, "5") {
		t.Fatalf("unmatched detail count affected graph row: %q\n%s", graphLine, out)
	}
	if !strings.Contains(out, "Detail only") || !strings.Contains(out, "5") {
		t.Fatalf("retained detail count was not rendered separately:\n%s", out)
	}
}

func TestRenderAutomationDetailShowsUnmatchedDetailsWhenGraphNodesAreEmpty(t *testing.T) {
	detail := client.AutomationDetail{
		Automation:           client.AutomationMetadata{ID: "au-empty-graph-unmatched", Name: "Empty graph", LifecycleState: "active"},
		Nodes:                make([]client.AutomationLiveNode, 0),
		UnmatchedNodeDetails: []client.AutomationLiveNode{{AutomationNode: client.AutomationNode{NodeKey: "detail-only", Name: "Detail only", Role: "task"}}},
		GraphAvailable:       true,
		NodesAvailable:       true,
		EdgesAvailable:       true,
		Partial:              true,
	}
	out := stripANSI(renderAutomationDetail(detail))
	for _, want := range []string{"Unmatched node details", "correlation unavailable", "Detail only"} {
		if !strings.Contains(out, want) {
			t.Errorf("empty-graph unmatched output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderAutomationDetailShowsDetailOnlyNodesWhenGraphUnavailable(t *testing.T) {
	detail := client.AutomationDetail{
		Automation:           client.AutomationMetadata{ID: "au-unavailable-details", Name: "Unavailable graph", LifecycleState: "active"},
		Nodes:                []client.AutomationLiveNode{{AutomationNode: client.AutomationNode{NodeKey: "detail-only", Name: "Detail only"}, Counts: client.AutomationNodeCounts{Running: 7, RunningAvailable: true}}},
		UnmatchedNodeDetails: []client.AutomationLiveNode{{AutomationNode: client.AutomationNode{NodeKey: "unmatched", Name: "Unmatched only"}}},
		GraphAvailable:       false,
		NodesAvailable:       true,
		Partial:              true,
	}
	out := stripANSI(renderAutomationDetail(detail))
	for _, want := range []string{"nodes: unavailable", "Detail-only nodes", "Detail only", "7", "Unmatched node details", "correlation unavailable", "Unmatched only"} {
		if !strings.Contains(out, want) {
			t.Errorf("unavailable graph detail output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderAutomationDetailDoesNotRelabelRetainedGraphNodesAsDetailOnly(t *testing.T) {
	detail := client.AutomationDetail{
		Automation:        client.AutomationMetadata{ID: "au-retained-graph", Name: "Retained graph", LifecycleState: "draft"},
		Nodes:             []client.AutomationLiveNode{{AutomationNode: client.AutomationNode{ID: "n1", NodeKey: "start", Name: "Start"}}},
		GraphAvailable:    false,
		NodesAvailable:    true,
		GraphNodesPresent: true,
		Partial:           true,
	}
	out := stripANSI(renderAutomationDetail(detail))
	if !strings.Contains(out, "Retained graph nodes") {
		t.Fatalf("retained graph rows were not identified as graph-origin diagnostics:\n%s", out)
	}
	if strings.Contains(out, "Detail-only nodes") {
		t.Fatalf("authoritative graph row was relabeled as detail-only:\n%s", out)
	}
}

func TestRenderAutomationDetailSortsDuplicateNodeRowsIndependentlyOfInputOrder(t *testing.T) {
	first := client.AutomationLiveNode{
		AutomationNode: client.AutomationNode{ID: "same", NodeKey: "same", Name: "Same"},
		DisplayState:   "running",
		Counts:         client.AutomationNodeCounts{Running: 1, RunningAvailable: true},
	}
	second := client.AutomationLiveNode{
		AutomationNode: client.AutomationNode{ID: "same", NodeKey: "same", Name: "Same"},
		DisplayState:   "waiting",
		Counts:         client.AutomationNodeCounts{Waiting: 2, WaitingAvailable: true},
	}
	base := client.AutomationDetail{
		Automation:     client.AutomationMetadata{ID: "au-node-order", Name: "Node order", LifecycleState: "active"},
		GraphAvailable: true,
		NodesAvailable: true,
		EdgesAvailable: true,
	}
	left := base
	left.Nodes = []client.AutomationLiveNode{first, second}
	right := base
	right.Nodes = []client.AutomationLiveNode{second, first}
	if got, want := stripANSI(renderAutomationDetail(left)), stripANSI(renderAutomationDetail(right)); got != want {
		t.Fatalf("duplicate node output depends on input order:\nleft:\n%s\nright:\n%s", got, want)
	}
}

func TestRenderAutomationDetailSortsDuplicateEdgeRowsIndependentlyOfInputOrder(t *testing.T) {
	first := client.AutomationLiveEdge{
		AutomationEdge:           client.AutomationEdge{ID: "same", EdgeKey: "same", SourceNodeID: "source", TargetNodeID: "target", Label: "approved"},
		SourceName:               "Source",
		TargetName:               "Target",
		TransitionCount:          1,
		TransitionCountAvailable: true,
	}
	second := client.AutomationLiveEdge{
		AutomationEdge:           client.AutomationEdge{ID: "same", EdgeKey: "same", SourceNodeID: "source", TargetNodeID: "target", Label: "approved"},
		SourceName:               "Source",
		TargetName:               "Target",
		TransitionCount:          2,
		TransitionCountAvailable: true,
	}
	base := client.AutomationDetail{
		Automation:     client.AutomationMetadata{ID: "au-edge-order", Name: "Edge order", LifecycleState: "active"},
		GraphAvailable: true,
		NodesAvailable: true,
		EdgesAvailable: true,
	}
	left := base
	left.Edges = []client.AutomationLiveEdge{first, second}
	right := base
	right.Edges = []client.AutomationLiveEdge{second, first}
	if got, want := stripANSI(renderAutomationDetail(left)), stripANSI(renderAutomationDetail(right)); got != want {
		t.Fatalf("duplicate edge output depends on input order:\nleft:\n%s\nright:\n%s", got, want)
	}
}

func TestRenderAutomationDetailSortsWarningsDeterministically(t *testing.T) {
	base := client.AutomationDetail{
		Automation:     client.AutomationMetadata{ID: "au-warning-order", Name: "Warning order"},
		GraphAvailable: false,
		Partial:        true,
	}
	first := base
	first.Warnings = []string{"zeta warning", "alpha warning"}
	second := base
	second.Warnings = []string{"alpha warning", "zeta warning"}
	if got, want := stripANSI(renderAutomationDetail(first)), stripANSI(renderAutomationDetail(second)); got != want {
		t.Fatalf("warning output depends on input order:\nfirst:\n%s\nsecond:\n%s", got, want)
	}
}

func TestRenderAutomationDetailSortsCaseOnlyResourceTiesDeterministically(t *testing.T) {
	base := client.AutomationDetail{
		Automation:         client.AutomationMetadata{ID: "au-resource-order", Name: "Resource order"},
		Resources:          make([]client.AutomationResourceSummary, 0),
		ResourcesAvailable: true,
	}
	upper := client.AutomationResourceSummary{NodeKey: "Node", ResourceType: "Task", ResourceID: "ID", Relation: "Input", Name: "Name", Status: "Ready"}
	lower := client.AutomationResourceSummary{NodeKey: "node", ResourceType: "task", ResourceID: "id", Relation: "input", Name: "name", Status: "ready"}
	first := base
	first.Resources = []client.AutomationResourceSummary{upper, lower}
	second := base
	second.Resources = []client.AutomationResourceSummary{lower, upper}
	if got, want := stripANSI(renderAutomationDetail(first)), stripANSI(renderAutomationDetail(second)); got != want {
		t.Fatalf("case-only resource output depends on input order:\nfirst:\n%s\nsecond:\n%s", got, want)
	}
}

func TestRenderAutomationDetailShowsRetainedEdgesWhenGraphUnavailable(t *testing.T) {
	detail := client.AutomationDetail{
		Automation: client.AutomationMetadata{ID: "au-retained-edges", Name: "Retained edges", LifecycleState: "active"},
		Edges: []client.AutomationLiveEdge{{
			AutomationEdge:                 client.AutomationEdge{EdgeKey: "approval", SourceNodeID: "start", TargetNodeID: "review", Label: "approved"},
			TransitionCount:                4,
			RecentTransitionCount:          1,
			TransitionCountAvailable:       true,
			RecentTransitionCountAvailable: true,
		}},
		GraphAvailable: false,
		EdgesAvailable: true,
		Partial:        true,
	}
	out := stripANSI(renderAutomationDetail(detail))
	for _, want := range []string{"edges: unavailable", "Retained edge records", "records retained for diagnostics", "start", "review", "approved", "4", "1"} {
		if !strings.Contains(out, want) {
			t.Errorf("unavailable graph retained-edge output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "0 nodes · 1 edges") {
		t.Fatalf("retained edge diagnostics were presented as a loaded graph:\n%s", out)
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
