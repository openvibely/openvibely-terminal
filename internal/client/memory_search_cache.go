package client

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type memorySearchIOCounters struct {
	filesOpened atomic.Int64
	bytesRead   atomic.Int64
}

var memorySearchIOStats memorySearchIOCounters

func (c *memorySearchIOCounters) addOpen()       { c.filesOpened.Add(1) }
func (c *memorySearchIOCounters) addBytes(n int) { c.bytesRead.Add(int64(n)) }

func (c *memorySearchIOCounters) snapshot() (files, bytes int64) {
	return c.filesOpened.Load(), c.bytesRead.Load()
}

type memoryFileFingerprint struct {
	dev, ino     uint64
	size         int64
	mode         os.FileMode
	mtime, ctime int64
}

// This cache retains query-specific, bounded output from the content scanner.
// New queries and invalidated entries use the full scanner.
// Both a byte budget and entry limit bound retention, including negative matches.
const memorySearchCacheBytes = 32 << 20
const memorySearchCacheEntries = 16384

type memorySearchCacheKey struct {
	root, project, query string
	entry                memoryIndexEntry
	metadataMatch        bool
}
type memorySearchCacheValue struct {
	fingerprint memoryFileFingerprint
	memory      Memory
	match       bool
	warnings    []string
}
type memorySearchCache struct {
	mu      sync.Mutex
	entries map[memorySearchCacheKey]memorySearchCacheValue
	bytes   int
}

// Avoid trusting timestamps in the filesystem's current coarse timestamp tick.
// Unsupported platforms deliberately retain the full-scan behavior.
func stableMemoryFingerprint(info os.FileInfo) (memoryFileFingerprint, bool) {
	fp, changed, ok := memoryFileFingerprintOf(info)
	return fp, ok && time.Since(changed) > 2*time.Second && time.Since(info.ModTime()) > 2*time.Second
}
func (c *memorySearchCache) lookup(key memorySearchCacheKey, info os.FileInfo) (memorySearchCacheValue, bool) {
	if c == nil {
		return memorySearchCacheValue{}, false
	}
	fp, stable := stableMemoryFingerprint(info)
	if !stable {
		return memorySearchCacheValue{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.entries[key]
	return value, ok && value.fingerprint == fp
}
func (c *memorySearchCache) store(key memorySearchCacheKey, before, after os.FileInfo, memory Memory, match bool, warnings []string) {
	if c == nil {
		return
	}
	fp, stable := stableMemoryFingerprint(before)
	next, nextStable := stableMemoryFingerprint(after)
	if !stable || !nextStable || fp != next {
		return
	}
	size := 512 + len(key.root) + len(key.project) + len(key.query) + len(key.entry.File) + len(key.entry.Title) + len(key.entry.Summary) + len(memory.File) + len(memory.Title) + len(memory.Summary) + len(memory.Snippet) + len(memory.Body)
	for _, warning := range warnings {
		size += 16 + len(warning)
	}
	if size > memorySearchCacheBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Clearing at the budget avoids retaining stale keys and keeps bookkeeping
	// bounded. Replacements are charged again conservatively.
	if c.bytes+size > memorySearchCacheBytes || len(c.entries) >= memorySearchCacheEntries {
		c.entries = nil
		c.bytes = 0
	}
	if c.entries == nil {
		c.entries = make(map[memorySearchCacheKey]memorySearchCacheValue)
	}
	// Index fields can be substrings of the entire index. Own the retained
	// strings so the byte budget also bounds their backing allocations.
	key.root, key.project, key.query = strings.Clone(key.root), strings.Clone(key.project), strings.Clone(key.query)
	key.entry.File = strings.Clone(key.entry.File)
	key.entry.Title = strings.Clone(key.entry.Title)
	key.entry.Summary = strings.Clone(key.entry.Summary)
	memory.File = strings.Clone(memory.File)
	memory.Title = strings.Clone(memory.Title)
	memory.Summary = strings.Clone(memory.Summary)
	memory.Snippet = strings.Clone(memory.Snippet)
	memory.Body = strings.Clone(memory.Body)
	ownedWarnings := make([]string, len(warnings))
	for i, warning := range warnings {
		ownedWarnings[i] = strings.Clone(warning)
	}
	c.entries[key] = memorySearchCacheValue{fp, memory, match, ownedWarnings}
	c.bytes += size
}
