package terminal

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"

	"github.com/openvibely/openvibely-terminal/internal/client"
)

const benchmarkChunksPerCadence = int(chatStreamRenderInterval / time.Millisecond)

func BenchmarkTranscriptAppend(b *testing.B) {
	for _, retained := range []int{499, 500} {
		name := fmt.Sprintf("retained=%d", retained)
		b.Run(name, func(b *testing.B) {
			template := transcriptAppendBenchmarkModel(retained)
			m := template
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				m = cloneTranscriptAppendBenchmarkModel(template)
				b.StartTimer()
				m.appendTranscriptEntry(entry{role: "event", text: "benchmark append entry"})
			}
		})
	}
}

func BenchmarkTranscriptCumulativeAppend(b *testing.B) {
	for _, entries := range []int{100, 500} {
		for _, bodyBytes := range []int{1024, 16 * 1024} {
			name := fmt.Sprintf("entries=%d/body=%dB", entries, bodyBytes)
			b.Run(name, func(b *testing.B) {
				body := strings.Repeat("x", bodyBytes)
				latencies := make([]int64, b.N*entries)
				latencyIndex := 0
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					m := transcriptAppendBenchmarkModel(0)
					b.StartTimer()
					for j := 0; j < entries; j++ {
						started := time.Now()
						m.appendTranscriptEntry(entry{role: "agent", text: body})
						latencies[latencyIndex] = time.Since(started).Nanoseconds()
						latencyIndex++
					}
				}
				b.StopTimer()
				sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
				p95 := latencies[(len(latencies)*95)/100]
				b.ReportMetric(float64(p95), "p95-ns/append")
			})
		}
	}
}

func transcriptAppendBenchmarkModel(retained int) Model {
	vp := viewport.New(100, 25)
	input := textinput.New()
	editor := textarea.New()
	m := Model{transcript: &vp, input: &input, automationEditor: &editor}
	for i := 0; i < retained; i++ {
		m.log = append(m.log, entry{role: "event", text: fmt.Sprintf("history entry %03d", i)})
	}
	m.refreshTranscript()
	return m
}

func cloneTranscriptAppendBenchmarkModel(template Model) Model {
	m := template
	m.log = append([]entry(nil), template.log...)
	m.transcriptBlocks = append([]string(nil), template.transcriptBlocks...)
	m.transcriptBlockLineCounts = append([]int(nil), template.transcriptBlockLineCounts...)
	m.transcriptBlockMaxWidths = append([]int(nil), template.transcriptBlockMaxWidths...)
	m.transcriptLines = append([]string(nil), template.transcriptLines...)
	viewportCopy := *template.transcript
	m.transcript = &viewportCopy
	return m
}

// BenchmarkChatStreamMutation exercises the production Update path while
// simulating one chunk arriving per millisecond. A real render message is
// delivered at the 33 ms cadence and a terminal event forces the final flush.
func BenchmarkChatStreamMutation(b *testing.B) {
	for _, entries := range []int{10, 100, 500} {
		for _, responseKiB := range []int{4, 64, 256} {
			for _, chunkBytes := range []int{32, 1024} {
				name := fmt.Sprintf("entries=%d/response=%dKiB/chunk=%dB", entries, responseKiB, chunkBytes)
				b.Run(name, func(b *testing.B) {
					responseBytes := responseKiB * 1024
					chunk := strings.Repeat("x", chunkBytes)
					b.ReportAllocs()
					b.SetBytes(int64(responseBytes))
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						m := streamBenchmarkModel(entries)
						chunks := 0
						for remaining := responseBytes; remaining > 0; remaining -= chunkBytes {
							delta := chunk
							if remaining < chunkBytes {
								delta = chunk[:remaining]
							}
							m = updateBenchmarkStream(m, client.ChatOutputEvent{Data: delta})
							chunks++
							if chunks%benchmarkChunksPerCadence == 0 {
								m = renderBenchmarkStream(m)
							}
						}
						m = updateBenchmarkStream(m, client.ChatOutputEvent{Name: "done"})
						b.ReportMetric(float64(m.chatStreamRedraws), "redraws/op")
					}
				})
			}
		}
	}
}

// BenchmarkChatStreamUpdateLatencyP95 measures the user-visible transcript
// update itself. Delta ingestion is intentionally outside each sample; every
// recorded duration includes Model.Update, styling/wrapping, cache replacement,
// viewport content replacement, and bottom scrolling for one cadence tick.
func BenchmarkChatStreamUpdateLatencyP95(b *testing.B) {
	const responseBytes = 64 * 1024
	const chunkBytes = 32
	chunk := strings.Repeat("x", chunkBytes)
	latencies := make([]int64, 0, responseBytes/chunkBytes/benchmarkChunksPerCadence+1)
	var p95 int64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m := streamBenchmarkModel(500)
		latencies = latencies[:0]
		chunks := 0
		for remaining := responseBytes; remaining > 0; remaining -= chunkBytes {
			m = updateBenchmarkStream(m, client.ChatOutputEvent{Data: chunk})
			chunks++
			if chunks%benchmarkChunksPerCadence != 0 {
				continue
			}
			started := time.Now()
			m = renderBenchmarkStream(m)
			latencies = append(latencies, time.Since(started).Nanoseconds())
		}
		if m.chatStreamRenderQueued {
			started := time.Now()
			m = renderBenchmarkStream(m)
			latencies = append(latencies, time.Since(started).Nanoseconds())
		}
		b.StopTimer()
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		p95 = latencies[(len(latencies)*95)/100]
		b.StartTimer()
	}
	b.ReportMetric(float64(p95)/1e6, "p95-ms/update")
}

func streamBenchmarkModel(entries int) Model {
	vp := viewport.New(0, 0)
	input := textinput.New()
	editor := textarea.New()
	m := Model{chatStreamLogIndex: -1, transcript: &vp, input: &input, automationEditor: &editor}
	m.transcript.Width = 100
	m.transcript.Height = 25
	for i := 0; i < entries-1; i++ {
		m.log = append(m.log, entry{role: "system", text: fmt.Sprintf("history entry %03d", i)})
	}
	m.refreshTranscript()
	m.selectedID = "project-A"
	m.pendingMsgID = "exec-1"
	m.pendingMsgExecutionID = "exec-1"
	m.pendingMsgProjectID = "project-A"
	m.chatSubmissionPending = true
	m.chatSubmissionID = 1
	m.chatStreamGeneration = 1
	m.chatStreamExecID = "exec-1"
	return m
}

func updateBenchmarkStream(m Model, event client.ChatOutputEvent) Model {
	next, _ := m.Update(chatStreamEventMsg{
		generation:   1,
		submissionID: 1,
		projectID:    "project-A",
		execID:       "exec-1",
		event:        event,
	})
	return next.(Model)
}

func renderBenchmarkStream(m Model) Model {
	next, _ := m.Update(chatStreamRenderMsg{
		generation:       1,
		renderGeneration: m.chatStreamRenderGeneration,
		submissionID:     1,
		projectID:        "project-A",
		execID:           "exec-1",
	})
	return next.(Model)
}
