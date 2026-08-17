package tui

import (
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
