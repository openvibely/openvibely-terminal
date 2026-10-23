package terminal

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/openvibely/openvibely-terminal/internal/client"
)

func TestTranscriptResizeMatchesCanonicalRefresh(t *testing.T) {
	long := strings.Repeat("ASCII body with wrapping words ", 18)
	seed := transcriptResizeTestModel([]entry{
		{role: "you", text: long},
		{role: "agent", text: "wide text 界🙂 and ANSI \x1b[31mred\x1b[0m\r\nsecond line\nthird line"},
		{role: "result", head: "Result", text: "result body\r\nwith a second line"},
		{role: "event", text: "event entry with enough text to wrap: " + long},
		{role: "system", text: "system entry"},
	})
	got := cloneTranscriptResizeTestModel(seed)
	want := cloneTranscriptResizeTestModel(seed)

	steps := []tea.WindowSizeMsg{
		{Width: 48, Height: 31}, // height-only
		{Width: 48, Height: 31}, // repeated dimensions
		{Width: 48, Height: 37}, // alternating height
		{Width: 72, Height: 37}, // width change
		{Width: 72, Height: 30},
		{Width: 48, Height: 34}, // return to original wrapping width
	}
	for i, msg := range steps {
		got.transcript.GotoTop()
		want.transcript.GotoTop()
		if got.transcript.YOffset == got.transcript.TotalLineCount()-got.transcript.Height {
			t.Fatalf("step %d did not start above bottom", i)
		}
		got, want = updateAndCompareTranscriptResize(t, got, want, msg)
	}
}

func TestTranscriptResizeHandlesEmptyHistoryRolloverAndInvalidCache(t *testing.T) {
	t.Run("empty history", func(t *testing.T) {
		got := transcriptResizeTestModel(nil)
		want := cloneTranscriptResizeTestModel(got)
		got, want = updateAndCompareTranscriptResize(t, got, want, tea.WindowSizeMsg{Width: 48, Height: 39})
		if len(got.log) != 0 || !got.transcriptLineCacheValid() {
			t.Fatalf("empty transcript cache is inconsistent: entries=%d lines=%d", len(got.log), len(got.transcriptLines))
		}
	})

	t.Run("500 entry rollover", func(t *testing.T) {
		entries := make([]entry, maxTranscript)
		for i := range entries {
			entries[i] = entry{role: "agent", text: strings.Repeat("entry body ", 4) + strings.Repeat("x", i%19)}
		}
		got := transcriptResizeTestModel(entries)
		got.appendTranscriptEntry(entry{role: "result", head: "Newest", text: "rollover result"})
		if len(got.log) != maxTranscript || got.log[len(got.log)-1].head != "Newest" {
			t.Fatalf("rollover did not retain the newest %d entries", maxTranscript)
		}
		want := cloneTranscriptResizeTestModel(got)
		got, want = updateAndCompareTranscriptResize(t, got, want, tea.WindowSizeMsg{Width: 48, Height: 35})
	})

	t.Run("invalid cache falls back", func(t *testing.T) {
		got := transcriptResizeTestModel([]entry{{role: "agent", text: strings.Repeat("cached reply ", 20)}})
		got.transcriptBlockLineCounts = nil
		want := cloneTranscriptResizeTestModel(got)
		got, want = updateAndCompareTranscriptResize(t, got, want, tea.WindowSizeMsg{Width: 48, Height: 36})
		if !got.transcriptLineCacheValid() {
			t.Fatal("resize did not repair invalid transcript cache metadata")
		}
	})
}

func TestTranscriptResizeKeepsDirtyAndReplacedEntriesCurrent(t *testing.T) {
	got := transcriptResizeTestModel([]entry{{role: "agent", text: "original"}})
	got.appendTranscriptEntry(entry{role: "event", text: "dirty joined content"})
	if !got.transcriptContentDirty {
		t.Fatal("append should defer joining transcript content")
	}
	got.log[len(got.log)-1].text = "replaced event content"
	got.replaceTranscriptBlock(len(got.log) - 1)
	if !got.transcriptContentDirty {
		t.Fatal("block replacement should leave joined content dirty")
	}
	want := cloneTranscriptResizeTestModel(got)
	got, want = updateAndCompareTranscriptResize(t, got, want, tea.WindowSizeMsg{Width: 48, Height: 34})
	if !got.transcriptContentDirty {
		t.Fatal("height-only resize eagerly joined dirty transcript content")
	}
	got.ensureTranscriptContent()
	if !strings.Contains(stripANSI(got.transcriptContent), "replaced event content") {
		t.Fatalf("joined content omitted replacement: log=%+v blocks=%q dirty=%v content=%q", got.log, got.transcriptBlocks, got.transcriptContentDirty, got.transcriptContent)
	}
}

func TestTranscriptResizeEditorOpenAndClose(t *testing.T) {
	got := transcriptResizeTestModel([]entry{{role: "agent", text: strings.Repeat("editor resize reply ", 12)}})
	got.selectedID = "project-A"
	got.skillEditorRequestID = 7
	want := cloneTranscriptResizeTestModel(got)

	open := skillEditorLoadedMsg{
		projectID: "project-A",
		requestID: 7,
		skill:     client.Skill{Handle: "guide", Scope: "project", Content: "editor content"},
	}
	got, want = updateAndCompareTranscriptResize(t, got, want, open)
	if !got.skillEditorActive || got.transcript.Height != got.transcriptHeight() {
		t.Fatal("skill editor did not open and resize the transcript")
	}

	close := skillEditorSavedMsg{projectID: "project-A", requestID: 7, name: "guide"}
	got, want = updateAndCompareTranscriptResize(t, got, want, close)
	if got.skillEditorActive || got.transcript.Height != got.transcriptHeight() {
		t.Fatal("skill editor did not close and resize the transcript")
	}
}

func TestTranscriptResizeFlushesQueuedStreamAndHandlesWidthChanges(t *testing.T) {
	t.Run("height-only flushes complete queued bytes", func(t *testing.T) {
		got := transcriptResizeStreamTestModel()
		want := cloneTranscriptResizeTestModel(got)
		response := "queued stream bytes with 界 and words " + strings.Repeat("reply ", 8)
		got, want = updateAndCompareStreamEvent(t, got, want, client.ChatOutputEvent{Data: response})
		if !got.chatStreamRenderQueued || string(got.chatStreamBuffer) != response {
			t.Fatal("stream fixture did not retain queued bytes")
		}
		got, want = updateAndCompareTranscriptResize(t, got, want, tea.WindowSizeMsg{Width: 48, Height: 38})
		got.ensureTranscriptContent()
		if got.chatStreamRenderedOffset != len(response) || !strings.Contains(stripANSI(got.transcriptContent), "queued") || !strings.Contains(stripANSI(got.transcriptContent), "bytes") {
			t.Fatalf("resize lost queued stream bytes: rendered=%d total=%d", got.chatStreamRenderedOffset, len(got.chatStreamBuffer))
		}
		got, want = updateAndCompareStreamEvent(t, got, want, client.ChatOutputEvent{Name: "done"})
		got.ensureTranscriptContent()
		if !strings.Contains(stripANSI(got.transcriptContent), "queued") {
			t.Fatal("stream completion lost accepted output")
		}
	})

	t.Run("incomplete UTF-8 suffix remains pending", func(t *testing.T) {
		got := transcriptResizeStreamTestModel()
		want := cloneTranscriptResizeTestModel(got)
		glyph := "界"
		got, want = updateAndCompareStreamEvent(t, got, want, client.ChatOutputEvent{Data: glyph[:1]})
		got, want = updateAndCompareTranscriptResize(t, got, want, tea.WindowSizeMsg{Width: 48, Height: 37})
		if got.chatStreamRenderedOffset != 0 || string(got.chatStreamBuffer) != glyph[:1] {
			t.Fatal("resize consumed or lost an incomplete UTF-8 suffix")
		}
		got, want = updateAndCompareStreamEvent(t, got, want, client.ChatOutputEvent{Data: glyph[1:]})
		got, want = updateAndCompareStreamRender(t, got, want)
		got.ensureTranscriptContent()
		if !strings.Contains(got.transcriptContent, glyph) || got.chatStreamRenderedOffset != len(glyph) {
			t.Fatal("stream did not render the completed UTF-8 rune after resize")
		}
	})

	t.Run("width change during stream", func(t *testing.T) {
		got := transcriptResizeStreamTestModel()
		want := cloneTranscriptResizeTestModel(got)
		response := strings.Repeat("stream reflow words ", 14)
		got, want = updateAndCompareStreamEvent(t, got, want, client.ChatOutputEvent{Data: response})
		got, want = updateAndCompareTranscriptResize(t, got, want, tea.WindowSizeMsg{Width: 64, Height: 37})
		if string(got.chatStreamBuffer) != response || got.chatStreamRenderedOffset != 0 {
			t.Fatal("width-changing resize lost or prematurely consumed queued stream output")
		}
		got, want = updateAndCompareStreamRender(t, got, want)
		got.ensureTranscriptContent()
		if !strings.Contains(stripANSI(got.transcriptContent), "stream") || !got.transcriptLineCacheValid() {
			t.Fatal("stream output or cache became stale after width change")
		}
	})
}

func updateAndCompareTranscriptResize(t *testing.T, got, want Model, msg tea.Msg) (Model, Model) {
	t.Helper()
	gotNext, _ := got.Update(msg)
	wantNext, _ := want.Update(msg)
	got = gotNext.(Model)
	want = wantNext.(Model)
	want.refreshTranscript()
	assertTranscriptResizeMatches(t, got, want)
	return got, want
}

func updateAndCompareStreamEvent(t *testing.T, got, want Model, event client.ChatOutputEvent) (Model, Model) {
	t.Helper()
	msgFor := func(m Model) chatStreamEventMsg {
		return chatStreamEventMsg{
			generation:   m.chatStreamGeneration,
			submissionID: m.chatSubmissionID,
			projectID:    m.selectedID,
			execID:       m.chatStreamExecID,
			event:        event,
		}
	}
	gotNext, _ := got.Update(msgFor(got))
	wantNext, _ := want.Update(msgFor(want))
	got = gotNext.(Model)
	want = wantNext.(Model)
	want.refreshTranscript()
	assertTranscriptResizeMatches(t, got, want)
	return got, want
}

func updateAndCompareStreamRender(t *testing.T, got, want Model) (Model, Model) {
	t.Helper()
	msgFor := func(m Model) chatStreamRenderMsg {
		return chatStreamRenderMsg{
			generation:       m.chatStreamGeneration,
			renderGeneration: m.chatStreamRenderGeneration,
			submissionID:     m.chatSubmissionID,
			projectID:        m.pendingMsgProjectID,
			execID:           m.chatStreamExecID,
		}
	}
	gotNext, _ := got.Update(msgFor(got))
	wantNext, _ := want.Update(msgFor(want))
	got = gotNext.(Model)
	want = wantNext.(Model)
	want.refreshTranscript()
	assertTranscriptResizeMatches(t, got, want)
	return got, want
}

func assertTranscriptResizeMatches(t *testing.T, got, want Model) {
	t.Helper()
	got.ensureTranscriptContent()
	want.ensureTranscriptContent()
	if got.transcript.View() != want.transcript.View() {
		t.Fatalf("viewport output differs from canonical refresh\n got: %q\nwant: %q", got.transcript.View(), want.transcript.View())
	}
	if got.transcript.YOffset != want.transcript.YOffset || got.transcript.Width != want.transcript.Width || got.transcript.Height != want.transcript.Height {
		t.Fatalf("viewport geometry/scroll differs: got=(%d,%d,%d) want=(%d,%d,%d)", got.transcript.Width, got.transcript.Height, got.transcript.YOffset, want.transcript.Width, want.transcript.Height, want.transcript.YOffset)
	}
	if got.transcriptContent != want.transcriptContent || !reflect.DeepEqual(got.log, want.log) {
		t.Fatal("transcript content or log differs from canonical refresh")
	}
	if !reflect.DeepEqual(got.transcriptLines, want.transcriptLines) || !reflect.DeepEqual(got.transcriptBlockLineCounts, want.transcriptBlockLineCounts) ||
		!reflect.DeepEqual(got.transcriptBlockMaxWidths, want.transcriptBlockMaxWidths) || got.transcriptMaxLineWidth != want.transcriptMaxLineWidth {
		t.Fatal("transcript line cache metadata differs from canonical refresh")
	}
	if len(got.chatStreamBuffer) == 0 && !reflect.DeepEqual(got.transcriptBlocks, want.transcriptBlocks) {
		t.Fatal("transcript block cache differs from canonical refresh")
	}
	if !got.transcriptLineCacheValid() || !want.transcriptLineCacheValid() || !got.transcriptReady ||
		got.transcriptRenderWidth != got.effectiveTranscriptWidth() || got.transcriptRenderWidth != want.transcriptRenderWidth {
		t.Fatal("transcript cache is invalid after resize")
	}
	if !reflect.DeepEqual(got.chatStreamBuffer, want.chatStreamBuffer) || got.chatStreamRenderedOffset > len(got.chatStreamBuffer) {
		t.Fatal("stream buffer or rendered offset is inconsistent after resize")
	}
}

func transcriptResizeTestModel(entries []entry) Model {
	vp := viewport.New(48, 25)
	input := textinput.New()
	automationEditor := textarea.New()
	personalityEditor := textarea.New()
	skillEditor := textarea.New()
	m := Model{
		transcript:         &vp,
		input:              &input,
		automationEditor:   &automationEditor,
		personalityEditor:  &personalityEditor,
		skillEditor:        &skillEditor,
		chatStreamLogIndex: -1,
		width:              48,
		height:             30,
		log:                append([]entry(nil), entries...),
	}
	m.refreshTranscript()
	return m
}

func transcriptResizeStreamTestModel() Model {
	m := transcriptResizeTestModel([]entry{{role: "agent", text: "previous response"}})
	m.selectedID = "project-A"
	m.pendingMsgID = "execution-A"
	m.pendingMsgExecutionID = "execution-A"
	m.pendingMsgProjectID = "project-A"
	m.chatSubmissionPending = true
	m.chatSubmissionID = 1
	m.chatStreamGeneration = 1
	m.chatStreamExecID = "execution-A"
	return m
}

func cloneTranscriptResizeTestModel(template Model) Model {
	m := template
	m.log = append([]entry(nil), template.log...)
	m.transcriptBlocks = append([]string(nil), template.transcriptBlocks...)
	m.transcriptBlockLineCounts = append([]int(nil), template.transcriptBlockLineCounts...)
	m.transcriptBlockMaxWidths = append([]int(nil), template.transcriptBlockMaxWidths...)
	m.transcriptLines = append([]string(nil), template.transcriptLines...)
	m.chatStreamBuffer = append([]byte(nil), template.chatStreamBuffer...)
	m.chatStreamWrappedLines = append([]string(nil), template.chatStreamWrappedLines...)
	m.chatStreamWrapBaseLines = append([]string(nil), template.chatStreamWrapBaseLines...)
	m.chatStreamWrapWord = append([]byte(nil), template.chatStreamWrapWord...)
	m.chatStreamWrapSpace = append([]byte(nil), template.chatStreamWrapSpace...)
	viewportCopy := *template.transcript
	m.transcript = &viewportCopy
	inputCopy := *template.input
	m.input = &inputCopy
	automationCopy := *template.automationEditor
	m.automationEditor = &automationCopy
	personalityCopy := *template.personalityEditor
	m.personalityEditor = &personalityCopy
	skillCopy := *template.skillEditor
	m.skillEditor = &skillCopy
	return m
}
