package terminal

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// BenchmarkTranscriptResize captures the full Model.Update WindowSizeMsg path.
// Each operation alternates geometry so the resize work cannot be optimized away.
func BenchmarkTranscriptResize(b *testing.B) {
	fixtures := []struct {
		name      string
		entries   int
		bodyBytes int
	}{
		{name: "10x1KiB", entries: 10, bodyBytes: 1024},
		{name: "100x16KiB", entries: 100, bodyBytes: 16 * 1024},
		{name: "500x16KiB", entries: 500, bodyBytes: 16 * 1024},
	}
	for _, fixture := range fixtures {
		for _, widthChange := range []bool{false, true} {
			caseName := "height-only"
			if widthChange {
				caseName = "width-change"
			}
			b.Run(fmt.Sprintf("%s/%s", fixture.name, caseName), func(b *testing.B) {
				m := transcriptResizeBenchmarkModel(fixture.entries, fixture.bodyBytes)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					width := 100
					height := 31 + i%2
					if widthChange && i%2 == 0 {
						width = 110
					}
					next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
					m = next.(Model)
				}
			})
		}
	}
}

func transcriptResizeBenchmarkModel(entries, bodyBytes int) Model {
	vp := viewport.New(100, 25)
	input := textinput.New()
	automationEditor := textarea.New()
	personalityEditor := textarea.New()
	skillEditor := textarea.New()
	body := strings.Repeat("x", bodyBytes)
	m := Model{
		transcript:         &vp,
		input:              &input,
		automationEditor:   &automationEditor,
		personalityEditor:  &personalityEditor,
		skillEditor:        &skillEditor,
		chatStreamLogIndex: -1,
		width:              100,
		height:             30,
	}
	for i := 0; i < entries; i++ {
		m.log = append(m.log, entry{role: "agent", text: body})
	}
	m.refreshTranscript()
	return m
}
