package client

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const benchmarkWarmQueries = 30

var benchmarkMemorySearchQueries = []struct {
	query   string
	matches int
}{
	{query: "absent search term", matches: 0},
	{query: "ünicode benchmark needle", matches: 1},
	{query: "fixture-0007.md", matches: 1},
}

// BenchmarkSearchMemoriesCorpus measures local fallback search on generated
// corpora. "cold" uses a fresh client per query; "warm" primes one client and
// then issues benchmarkWarmQueries repeated queries per iteration.
func BenchmarkSearchMemoriesCorpus(b *testing.B) {
	for _, fixture := range []struct {
		documents     int
		documentBytes int
	}{
		{100, 64 << 10},
		{100, 1 << 20},
		{1000, 64 << 10},
		{1000, 1 << 20},
	} {
		name := fmt.Sprintf("%dx%dKiB", fixture.documents, fixture.documentBytes>>10)
		b.Run(name, func(b *testing.B) {
			repo := b.TempDir()
			writeBenchmarkMemoryCorpus(b, repo, fixture.documents, fixture.documentBytes)
			project := Project{ID: "bench", Path: repo}

			b.Run("cold", func(b *testing.B) {
				latencies := make([]time.Duration, 0, b.N)
				files0, bytes0 := memorySearchIOStats.snapshot()
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					q := benchmarkMemorySearchQueries[i%len(benchmarkMemorySearchQueries)]
					start := time.Now()
					runBenchmarkMemorySearch(b, &Client{}, project, q.query, q.matches)
					latencies = append(latencies, time.Since(start))
				}
				b.StopTimer()
				reportBenchmarkMemorySearch(b, latencies, files0, bytes0)
			})

			b.Run("warm", func(b *testing.B) {
				client := &Client{}
				for _, q := range benchmarkMemorySearchQueries {
					runBenchmarkMemorySearch(b, client, project, q.query, q.matches)
				}
				latencies := make([]time.Duration, 0, b.N*benchmarkWarmQueries)
				files0, bytes0 := memorySearchIOStats.snapshot()
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					for j := 0; j < benchmarkWarmQueries; j++ {
						q := benchmarkMemorySearchQueries[j%len(benchmarkMemorySearchQueries)]
						start := time.Now()
						runBenchmarkMemorySearch(b, client, project, q.query, q.matches)
						latencies = append(latencies, time.Since(start))
					}
				}
				b.StopTimer()
				reportBenchmarkMemorySearch(b, latencies, files0, bytes0)
			})
		})
	}
}

func runBenchmarkMemorySearch(b *testing.B, client *Client, project Project, query string, matches int) {
	var result MemorySearch
	var err error
	if os.Getenv("MEMORY_SEARCH_BASELINE") == "1" {
		result, err = searchLocalMemories(context.Background(), project, query)
	} else {
		result, err = client.SearchMemories(context.Background(), project, query)
	}
	if err != nil {
		b.Fatalf("SearchMemories(%q): %v", query, err)
	}
	if len(result.Memories) != matches {
		b.Fatalf("SearchMemories(%q) matches = %d, want %d", query, len(result.Memories), matches)
	}
	benchmarkMemorySearchFound = len(result.Memories) > 0
}

func reportBenchmarkMemorySearch(b *testing.B, latencies []time.Duration, files0, bytes0 int64) {
	files1, bytes1 := memorySearchIOStats.snapshot()
	queries := float64(len(latencies))
	slices.Sort(latencies)
	p95 := latencies[min(len(latencies)-1, (len(latencies)*95+99)/100-1)]
	b.ReportMetric(float64(latencies[len(latencies)/2].Nanoseconds()), "p50-ns/query")
	b.ReportMetric(float64(p95.Nanoseconds()), "p95-ns/query")
	b.ReportMetric(float64(bytes1-bytes0)/queries, "read-B/query")
	b.ReportMetric(float64(files1-files0)/queries, "opened/query")
}

func writeBenchmarkMemoryCorpus(b *testing.B, repo string, documents, documentBytes int) {
	b.Helper()
	dir := filepath.Join(repo, ".openvibely", "memories")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatalf("MkdirAll: %v", err)
	}
	line := []byte("Ordinary project memory content without the requested phrase\n")
	filler := bytes.Repeat(line, documentBytes/len(line)+1)
	var index strings.Builder
	for i := 0; i < documents; i++ {
		name := fmt.Sprintf("fixture-%04d.md", i)
		prefix := fmt.Sprintf("---\ntitle: Fixture %04d Ünicode\nsummary: Representative MiXeD-case fixture\n---\n\n# MiXeD Ünicode Fixture %04d\n\nMiXeD Ünicode content paragraph.\n\n", i, i)
		suffix := "\n"
		if i == documents-1 {
			suffix = "\nMiXeD ÜNICODE Benchmark Needle\n"
		}
		remaining := documentBytes - len(prefix) - len(suffix)
		content := make([]byte, 0, documentBytes)
		content = append(content, prefix...)
		content = append(content, filler[:remaining]...)
		content = append(content, suffix...)
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			b.Fatalf("WriteFile: %v", err)
		}
		fmt.Fprintf(&index, "- [Fixture %04d](%s) - benchmark corpus entry\n", i, name)
	}
	if err := os.WriteFile(filepath.Join(dir, projectMemoryIndex), []byte(index.String()), 0o644); err != nil {
		b.Fatalf("WriteFile index: %v", err)
	}
	// Let the corpus settle past the cache's racy-timestamp window.
	time.Sleep(2100 * time.Millisecond)
}
