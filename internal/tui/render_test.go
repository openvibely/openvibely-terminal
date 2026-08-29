package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
var lifecycleJSONBenchmarkSink []byte

func BenchmarkRenderLifecycleEventsLargePayload(b *testing.B) {
	payload := map[string]any{
		"message": strings.Repeat("x", 1<<20),
		"status":  "completed",
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		b.Fatalf("marshal benchmark payload: %v", err)
	}
	encodedString := string(encoded)
	task := client.Task{ID: "task-1", Title: "Large payload task"}
	execution := client.LifecycleExecution{ID: "execution-1", Status: "completed"}
	events := []client.LifecycleEvent{{
		ID:        "event-1",
		Seq:       1,
		EventType: "completed",
		Payload:   payload,
	}}
	precomputedRows := [][]string{
		{"SEQ", "TIMESTAMP", "EVENT TYPE", "PAYLOAD"},
		{"1", "—", "completed", truncate(encodedString, 96)},
	}

	b.Run("end_to_end_json_table_truncate", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			lifecycleBenchmarkSink = renderLifecycleEvents(task, execution, events)
		}
	})
	b.Run("json_only", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			lifecycleJSONBenchmarkSink, _ = json.Marshal(payload)
		}
	})
	b.Run("truncate_only_preencoded", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			lifecycleBenchmarkSink = truncate(encodedString, 96)
		}
	})
	b.Run("table_only_pretruncated", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			lifecycleBenchmarkSink = table(precomputedRows)
		}
	})
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
