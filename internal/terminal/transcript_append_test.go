package terminal

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestRenderTranscriptEntryMatchesLipglossWrapping(t *testing.T) {
	texts := []struct {
		name string
		text string
	}{
		{name: "short", text: "short reply"},
		{name: "multiline", text: "first line\nsecond line"},
		{name: "ANSI", text: "colored \x1b[31mred text\x1b[0m and link"},
		{name: "CJK and emoji", text: "界🙂 wide text"},
		{name: "combining marks", text: "e\u0301 cafe\u0301"},
		{name: "CRLF", text: "first\r\nsecond\r\n"},
		{name: "empty", text: ""},
		{name: "long unbroken", text: strings.Repeat("x", 16*1024)},
		{name: "exact width", text: strings.Repeat("x", 46)},
		{name: "multiple exact widths", text: strings.Repeat("x", 92)},
	}
	roles := []string{"you", "agent"}
	for _, width := range []int{18, 48, 100} {
		for _, role := range roles {
			for _, tc := range texts {
				t.Run(fmt.Sprintf("%s/%s/width=%d", tc.name, role, width), func(t *testing.T) {
					e := entry{role: role, text: tc.text}
					wrap := transcriptWrap(width)
					got := renderTranscriptEntry(e, wrap)
					want := renderTranscriptEntryWithLipgloss(e, wrap)
					if got != want {
						t.Fatalf("rendered transcript differs from Lipgloss\n got: %q\nwant: %q", got, want)
					}
				})
			}
		}
	}
}

func TestTranscriptAppendMatchesCanonicalRenderAndViewport(t *testing.T) {
	entries := []entry{
		{role: "you", text: strings.Repeat("userreply", 20)},
		{role: "agent", text: strings.Repeat("agentreply", 18)},
		{role: "you", text: "short line\nand another line"},
		{role: "agent", text: "ANSI \x1b[32mgreen\x1b[0m; 界🙂; e\u0301\r\nnext"},
		{role: "agent", text: ""},
		{role: "result", head: "Result", text: "first\r\nsecond\n"},
		{role: "event", text: "event text"},
		{role: "error", text: "error text\ncontinued"},
		{role: "system", text: "unknown role"},
	}
	got := transcriptResizeTestModel(nil)
	want := cloneTranscriptResizeTestModel(got)
	for i, e := range entries {
		got.transcript.GotoTop()
		want.transcript.GotoTop()
		got.appendTranscriptEntry(e)
		want.log = append(want.log, e)
		want.refreshTranscript()
		assertTranscriptResizeMatches(t, got, want)
		assertTranscriptMatchesLipglossBlocks(t, &got)
		if got.transcript.YOffset != got.transcript.TotalLineCount()-got.transcript.Height && got.transcript.TotalLineCount() > got.transcript.Height {
			t.Fatalf("append %d did not move the viewport to the bottom", i)
		}
	}

	got, want = updateAndCompareTranscriptResize(t, got, want, tea.WindowSizeMsg{Width: 32, Height: 34})
	assertTranscriptMatchesLipglossBlocks(t, &got)
	long := entry{role: "agent", text: strings.Repeat("x", 4096)}
	got.appendTranscriptEntry(long)
	want.log = append(want.log, long)
	want.refreshTranscript()
	assertTranscriptResizeMatches(t, got, want)
	assertTranscriptMatchesLipglossBlocks(t, &got)
}

func TestTranscriptAppendInvalidCacheAndRolloverPreserveRenderedTranscript(t *testing.T) {
	invalid := transcriptResizeTestModel([]entry{{role: "agent", text: strings.Repeat("x", 300)}})
	invalid.transcriptBlocks = nil
	wantInvalid := cloneTranscriptResizeTestModel(invalid)
	appended := entry{role: "event", text: "cache was invalid"}
	invalid.appendTranscriptEntry(appended)
	wantInvalid.log = append(wantInvalid.log, appended)
	wantInvalid.refreshTranscript()
	assertTranscriptResizeMatches(t, invalid, wantInvalid)
	assertTranscriptMatchesLipglossBlocks(t, &invalid)

	entries := make([]entry, maxTranscript)
	for i := range entries {
		entries[i] = entry{role: "agent", text: strings.Repeat("x", 20+i%7)}
	}
	got := transcriptResizeTestModel(entries)
	got.appendTranscriptEntry(entry{role: "agent", text: strings.Repeat("y", 400)})
	want := cloneTranscriptResizeTestModel(got)
	want.refreshTranscript()
	assertTranscriptResizeMatches(t, got, want)
	assertTranscriptMatchesLipglossBlocks(t, &got)
	if len(got.log) != maxTranscript || got.log[0].text != entries[1].text || got.log[maxTranscript-1].text != strings.Repeat("y", 400) {
		t.Fatalf("rollover did not retain exactly the newest %d transcript entries", maxTranscript)
	}
}

func assertTranscriptMatchesLipglossBlocks(t *testing.T, m *Model) {
	t.Helper()
	wrap := transcriptWrap(m.effectiveTranscriptWidth())
	var expected strings.Builder
	for _, e := range m.log {
		expected.WriteString(renderTranscriptEntryWithLipgloss(e, wrap))
	}
	m.ensureTranscriptContent()
	if m.transcriptContent != expected.String() {
		t.Fatal("transcript bytes differ from canonical Lipgloss rendering")
	}
}

func renderTranscriptEntryWithLipgloss(e entry, wrap lipgloss.Style) string {
	var b strings.Builder
	switch e.role {
	case "you":
		b.WriteString(chatUserStyle.Render("❯ you") + "\n")
		b.WriteString(wrap.Render(e.text) + "\n\n")
	case "agent":
		b.WriteString(chatAgentStyle.Render("● agent") + "\n")
		b.WriteString(wrap.Render(e.text) + "\n\n")
	case "result":
		if e.head != "" {
			b.WriteString(sectionStyle.Render("▸ "+e.head) + "\n")
		}
		b.WriteString(e.text + "\n\n")
	case "error":
		b.WriteString(statusErrStyle.Render("✗ "+e.text) + "\n\n")
	case "event":
		b.WriteString(dimStyle.Render(e.text) + "\n")
	default:
		b.WriteString(dimStyle.Render("· "+e.text) + "\n\n")
	}
	return b.String()
}
