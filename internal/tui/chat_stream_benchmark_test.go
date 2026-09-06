package tui

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely-tui/internal/client"
)

const benchmarkChunksPerCadence = int(chatStreamRenderInterval / time.Millisecond)

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

// BenchmarkChatStreamLegacyMutation reproduces the pre-change production
// mutation path for the acceptance-critical and short-response comparisons.
func BenchmarkChatStreamLegacyMutation(b *testing.B) {
	for _, responseKiB := range []int{4, 64} {
		b.Run(fmt.Sprintf("entries=500/response=%dKiB/chunk=32B", responseKiB), func(b *testing.B) {
			responseBytes := responseKiB * 1024
			chunk := strings.Repeat("x", 32)
			b.ReportAllocs()
			b.SetBytes(int64(responseBytes))
			for i := 0; i < b.N; i++ {
				m := streamBenchmarkModel(500)
				for remaining := responseBytes; remaining > 0; remaining -= len(chunk) {
					legacyUpdateChatStreamOutput(&m, chunk)
				}
			}
		})
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
	m := Model{chatStreamLogIndex: -1}
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

func legacyUpdateChatStreamOutput(m *Model, delta string) {
	m.chatStreamOutput += delta
	m.chatStreamOffset += len(delta)
	if m.chatStreamLogIndex >= 0 && m.chatStreamLogIndex < len(m.log) && m.log[m.chatStreamLogIndex].role == "agent" {
		m.log[m.chatStreamLogIndex].text = m.chatStreamOutput
		m.refreshTranscript()
		return
	}
	m.appendTranscriptEntry(entry{role: "agent", text: m.chatStreamOutput})
	m.chatStreamLogIndex = len(m.log) - 1
}
