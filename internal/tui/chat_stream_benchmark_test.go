package tui

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

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
						for remaining := responseBytes; remaining > 0; remaining -= chunkBytes {
							delta := chunk
							if remaining < chunkBytes {
								delta = chunk[:remaining]
							}
							m.updateChatStreamOutput(delta)
						}
						m.flushChatStreamOutput()
						b.ReportMetric(float64(m.chatStreamRedraws), "redraws/op")
					}
				})
			}
		}
	}
}

func BenchmarkChatStreamUpdateLatencyP95(b *testing.B) {
	const responseBytes = 64 * 1024
	const chunkBytes = 32
	chunk := strings.Repeat("x", chunkBytes)
	latencies := make([]int64, responseBytes/chunkBytes)
	var p95 int64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m := streamBenchmarkModel(500)
		for update := range latencies {
			started := time.Now()
			m.updateChatStreamOutput(chunk)
			latencies[update] = time.Since(started).Nanoseconds()
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
	return m
}
