package terminal

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

const transcriptAppendEvidenceRepeats = 7

var transcriptAppendEvidenceString string
var transcriptAppendEvidenceLines []string

func TestTranscriptAppendPerformanceEvidence(t *testing.T) {
	if os.Getenv("OPENVIBELY_PERF_EVIDENCE") != "1" {
		t.Skip("set OPENVIBELY_PERF_EVIDENCE=1 to run repeated transcript performance evidence")
	}

	for _, entries := range []int{100, 500} {
		for _, bodyBytes := range []int{1024, 16 * 1024} {
			name := fmt.Sprintf("entries=%d/body=%dB", entries, bodyBytes)
			t.Run(name, func(t *testing.T) {
				body := strings.Repeat("x", bodyBytes)
				entryValue := entry{role: "agent", text: body}
				full := measureTranscriptAppendBurst(entries, entryValue)
				t.Logf("transcript_append workload=%s repeats=%d median_allocated_bytes=%d median_full_burst=%s median_p95_append=%s", name, transcriptAppendEvidenceRepeats, full.allocatedBytes, full.burst, full.p95Append)

				measureTranscriptAppendPhases(t, name, entries, entryValue)
			})
		}
	}
}

type transcriptAppendMeasurement struct {
	allocatedBytes uint64
	burst          time.Duration
	p95Append      time.Duration
}

func measureTranscriptAppendBurst(entries int, e entry) transcriptAppendMeasurement {
	allocated := make([]uint64, transcriptAppendEvidenceRepeats)
	bursts := make([]time.Duration, transcriptAppendEvidenceRepeats)
	appendLatencies := make([]time.Duration, 0, transcriptAppendEvidenceRepeats*entries)
	for repeat := 0; repeat < transcriptAppendEvidenceRepeats; repeat++ {
		m := transcriptAppendBenchmarkModel(0)
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		started := time.Now()
		for i := 0; i < entries; i++ {
			appendStarted := time.Now()
			m.appendTranscriptEntry(e)
			appendLatencies = append(appendLatencies, time.Since(appendStarted))
		}
		bursts[repeat] = time.Since(started)
		runtime.ReadMemStats(&after)
		allocated[repeat] = after.TotalAlloc - before.TotalAlloc
		transcriptAppendEvidenceString = m.transcriptBlocks[len(m.transcriptBlocks)-1]
	}
	sort.Slice(appendLatencies, func(i, j int) bool { return appendLatencies[i] < appendLatencies[j] })
	return transcriptAppendMeasurement{
		allocatedBytes: medianUint64(allocated),
		burst:          medianDuration(bursts),
		p95Append:      appendLatencies[(len(appendLatencies)*95)/100],
	}
}

func measureTranscriptAppendPhases(t *testing.T, name string, entries int, e entry) {
	t.Helper()
	wrap := transcriptWrap(transcriptAppendBenchmarkWidth)
	block := renderTranscriptEntry(e, wrap)
	blockLines, blockMaxWidth := renderedLines(block)
	for _, phase := range []string{"render", "line-processing", "cache-insertion"} {
		allocated := make([]uint64, transcriptAppendEvidenceRepeats)
		latencies := make([]time.Duration, transcriptAppendEvidenceRepeats)
		for repeat := 0; repeat < transcriptAppendEvidenceRepeats; repeat++ {
			var m Model
			if phase == "cache-insertion" {
				m = transcriptAppendBenchmarkModel(0)
			}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			started := time.Now()
			for i := 0; i < entries; i++ {
				switch phase {
				case "render":
					transcriptAppendEvidenceString = renderTranscriptEntry(e, wrap)
				case "line-processing":
					transcriptAppendEvidenceLines, _ = renderedLines(block)
				case "cache-insertion":
					m.appendRenderedTranscriptLines(block, blockLines, blockMaxWidth)
				}
			}
			latencies[repeat] = time.Since(started)
			runtime.ReadMemStats(&after)
			allocated[repeat] = after.TotalAlloc - before.TotalAlloc
		}
		t.Logf("transcript_append_phase workload=%s phase=%s median_allocated_bytes=%d median_full_phase=%s", name, phase, medianUint64(allocated), medianDuration(latencies))
	}
}

func medianUint64(values []uint64) uint64 {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values[len(values)/2]
}

func medianDuration(values []time.Duration) time.Duration {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values[len(values)/2]
}
