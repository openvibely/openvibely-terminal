package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	projectMemoryDirectory           = ".openvibely/memories"
	projectMemoryIndex               = "MEMORIES.md"
	maxMemoryIndexBytes              = 1 << 20
	maxMemoryFileBytes               = 8 << 20
	maxMemoryWarnings                = 64
	maxMemoryWarningRunes            = 240
	maxMemoryAmbiguityCandidates     = 8
	maxMemoryAmbiguityCandidateRunes = 64
	memorySearchChunkBytes           = 64 << 10
)

var (
	errMemoryFileTooLarge     = errors.New("memory file exceeds the read limit")
	errMemoryFileNotRegular   = errors.New("memory path is not a regular file")
	errMemoryPathUnsafe       = errors.New("memory path is unsafe")
	errMemoryFileChanged      = errors.New("memory path changed during read")
	errMemoryIndexUnavailable = errors.New("memory: unable to read project memory index")
)

// Memory is a stable, read-only representation of one indexed project memory
// file. List and search leave Body empty; show populates it with the canonical
// file contents. Snippet is populated only for search results.
type Memory struct {
	File    string `json:"file"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Snippet string `json:"snippet"`
	Body    string `json:"body"`
}

// MemoryList is the machine-readable result for a memory list operation.
// Warnings describe malformed or incomplete index state without making a
// usable partial listing unusable.
type MemoryList struct {
	Memories []Memory `json:"memories"`
	Warnings []string `json:"warnings"`
}

// MemorySearch is the machine-readable result for a memory search operation.
type MemorySearch struct {
	Query    string   `json:"query"`
	Memories []Memory `json:"memories"`
	Warnings []string `json:"warnings"`
}

// MemoryDocument is the machine-readable result for one memory show operation.
type MemoryDocument struct {
	File      string   `json:"file"`
	Title     string   `json:"title"`
	Summary   string   `json:"summary"`
	Snippet   string   `json:"snippet"`
	Body      string   `json:"body"`
	Available bool     `json:"available"`
	Warnings  []string `json:"warnings"`
}

// ListMemories reads the selected project's canonical MEMORIES.md index. It
// intentionally does not enumerate unindexed files: the backend's index is the
// authority for which durable memories are inspectable.
func (c *Client) ListMemories(ctx context.Context, project Project) (MemoryList, error) {
	_ = c
	if ctx == nil {
		ctx = context.Background()
	}
	root, entries, warnings, err := loadMemoryIndex(ctx, project)
	result := MemoryList{
		Memories: make([]Memory, 0, len(entries)),
		Warnings: make([]string, 0, len(warnings)),
	}
	result.Warnings = append(result.Warnings, warnings...)
	if err != nil {
		return result, err
	}

	for _, entry := range entries {
		if warning := inspectIndexedMemory(ctx, root, entry.File); warning != "" {
			appendMemoryWarning(&result.Warnings, warning)
		}
		result.Memories = append(result.Memories, Memory{
			File:    entry.File,
			Title:   entry.Title,
			Summary: entry.Summary,
			Snippet: "",
			Body:    "",
		})
	}
	return result, nil
}

// ShowMemory reads one indexed memory file. A reference may be its indexed
// file handle or an unambiguous title/name reference. MEMORIES.md itself and
// paths outside the project memory directory are never accepted.
func (c *Client) ShowMemory(ctx context.Context, project Project, reference string) (MemoryDocument, error) {
	_ = c
	if ctx == nil {
		ctx = context.Background()
	}
	root, entries, warnings, err := loadMemoryIndex(ctx, project)
	document := MemoryDocument{
		Warnings: make([]string, 0, len(warnings)),
	}
	document.Warnings = append(document.Warnings, warnings...)
	if err != nil {
		return document, err
	}

	entry, err := resolveMemoryEntry(entries, reference)
	if err != nil {
		return document, err
	}
	memory, _, fileWarnings, err := readIndexedMemory(ctx, root, entry)
	document = memoryDocument(memory, document.Warnings, err == nil)
	document.Warnings = append(document.Warnings, fileWarnings...)
	if err != nil {
		return document, err
	}
	return document, nil
}

// SearchMemories searches indexed file metadata and the readable body of each
// indexed file. Unreadable files remain represented by their index metadata and
// add a safe warning rather than exposing an operating-system path or error.
func (c *Client) SearchMemories(ctx context.Context, project Project, query string) (MemorySearch, error) {
	_ = c
	if ctx == nil {
		ctx = context.Background()
	}
	query = strings.TrimSpace(query)
	result := MemorySearch{
		Query:    query,
		Memories: make([]Memory, 0),
		Warnings: make([]string, 0),
	}
	if query == "" {
		return result, errors.New("memory search query is required")
	}

	root, entries, warnings, err := loadMemoryIndex(ctx, project)
	result.Warnings = append(result.Warnings, warnings...)
	if err != nil {
		return result, err
	}

	lowerQuery := strings.ToLower(query)
	lowerQueryBytes := []byte(lowerQuery)
	for _, entry := range entries {
		base := Memory{File: entry.File, Title: entry.Title, Summary: entry.Summary}
		metadataMatch := strings.Contains(strings.ToLower(entry.File), lowerQuery) ||
			strings.Contains(strings.ToLower(entry.Title), lowerQuery) ||
			strings.Contains(strings.ToLower(entry.Summary), lowerQuery)

		memory, bodyMatch, fileWarnings, readErr := readIndexedMemoryForSearch(ctx, root, entry, lowerQueryBytes, metadataMatch)
		for _, warning := range fileWarnings {
			appendMemoryWarning(&result.Warnings, warning)
		}
		if readErr != nil {
			if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
				return result, readErr
			}
			if metadataMatch {
				result.Memories = append(result.Memories, base)
			}
			continue
		}

		if !metadataMatch && !bodyMatch {
			continue
		}
		result.Memories = append(result.Memories, memory)
	}
	return result, nil
}

type memoryIndexEntry struct {
	File    string
	Title   string
	Summary string
}

func loadMemoryIndex(ctx context.Context, project Project) (string, []memoryIndexEntry, []string, error) {
	root, err := projectMemoryRoot(project)
	if err != nil {
		return "", nil, nil, err
	}
	if err := contextErr(ctx); err != nil {
		return root, nil, nil, err
	}

	if _, _, err := safeMemoryRoot(root); err != nil {
		return root, make([]memoryIndexEntry, 0), []string{"project memory directory has an unsafe path"}, errMemoryIndexUnavailable
	}
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return root, make([]memoryIndexEntry, 0), []string{"project memory directory is missing"}, nil
		}
		return root, nil, nil, errors.New("memory: unable to inspect project memory directory")
	}
	if !info.IsDir() {
		return root, nil, nil, errors.New("memory: project memory path is not a directory")
	}

	if _, err := safeMemoryIndexPath(root); err != nil {
		return root, make([]memoryIndexEntry, 0), []string{"project memory index has an unsafe path"}, errMemoryIndexUnavailable
	}
	data, err := readMemoryTarget(ctx, root, projectMemoryIndex, maxMemoryIndexBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return root, make([]memoryIndexEntry, 0), []string{"project memory index is missing"}, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return root, nil, nil, err
		}
		if errors.Is(err, errMemoryFileNotRegular) {
			return root, make([]memoryIndexEntry, 0), []string{"project memory index is not a regular file"}, errMemoryIndexUnavailable
		}
		return root, nil, []string{"project memory index could not be read"}, errMemoryIndexUnavailable
	}
	entries, warnings := parseMemoryIndex(string(data))
	return root, entries, warnings, nil
}

func projectMemoryRoot(project Project) (string, error) {
	repoPath := strings.TrimSpace(project.Path)
	if repoPath == "" {
		return "", errors.New("memory: selected project has no local repository path")
	}
	if strings.ContainsRune(repoPath, '\x00') {
		return "", errors.New("memory: selected project repository path is invalid")
	}
	abs, err := filepath.Abs(filepath.Clean(repoPath))
	if err != nil {
		return "", errors.New("memory: selected project repository path is invalid")
	}
	return filepath.Join(abs, filepath.FromSlash(projectMemoryDirectory)), nil
}

func parseMemoryIndex(index string) ([]memoryIndexEntry, []string) {
	entries := make([]memoryIndexEntry, 0)
	warnings := make([]string, 0)
	seen := map[string]bool{}
	index = strings.ReplaceAll(index, "\r\n", "\n")
	for lineNumber, raw := range strings.Split(index, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line, isEntry := stripMemoryIndexMarker(line)
		if !isEntry {
			line = strings.TrimSpace(raw)
			if !looksLikeUnmarkedMemoryEntry(line) {
				continue
			}
		}
		if !looksLikeMemoryIndexEntry(line) {
			continue
		}

		handle, title, summary, wellFormed := indexedMemoryLine(line)
		if !wellFormed {
			appendMemoryWarning(&warnings, fmt.Sprintf("memory index line %d is malformed", lineNumber+1))
		}
		if handle == "" {
			continue
		}
		handle = normalizeMemoryHandle(handle)
		if handle == "" || strings.EqualFold(path.Base(handle), ".md") {
			appendMemoryWarning(&warnings, fmt.Sprintf("memory index line %d contains an unsafe file reference", lineNumber+1))
			continue
		}
		if seen[strings.ToLower(handle)] {
			appendMemoryWarning(&warnings, fmt.Sprintf("memory index line %d duplicates %q", lineNumber+1, handle))
			continue
		}
		seen[strings.ToLower(handle)] = true
		if strings.TrimSpace(title) == "" {
			title = memoryTitleFromFile(handle)
		}
		entries = append(entries, memoryIndexEntry{
			File:    handle,
			Title:   strings.TrimSpace(title),
			Summary: cleanMemorySummary(summary),
		})
	}
	return entries, warnings
}

func stripMemoryIndexMarker(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", false
	}
	if line[0] == '-' || line[0] == '*' || line[0] == '+' {
		if len(line) == 1 || line[1] == ' ' || line[1] == '\t' {
			return strings.TrimSpace(line[1:]), true
		}
	}
	if dot := strings.Index(line, ". "); dot > 0 && allMemoryIndexDigits(line[:dot]) {
		return strings.TrimSpace(line[dot+2:]), true
	}
	return "", false
}

func allMemoryIndexDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func looksLikeMemoryIndexEntry(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	if strings.HasPrefix(line, "[") && strings.Contains(line, "]") {
		return true
	}
	if strings.HasPrefix(line, "`") {
		return true
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	field := strings.Trim(fields[0], "`<>")
	return hasMemoryFileSuffix(strings.TrimSuffix(field, ":"))
}

func looksLikeUnmarkedMemoryEntry(line string) bool {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 {
		return false
	}
	field := strings.Trim(fields[0], "`<>")
	return hasMemoryFileSuffix(strings.TrimSuffix(field, ":"))
}

func indexedMemoryLine(line string) (handle, title, summary string, wellFormed bool) {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "[") {
		if open := strings.Index(line, "]("); open >= 0 {
			start := open + len("](")
			closeOffset := strings.Index(line[start:], ")")
			if closeOffset < 0 {
				return memoryHandleToken(line[start:]), memoryLinkLabel(line[:open]), "", false
			}
			close := start + closeOffset
			target := strings.TrimSpace(line[start:close])
			if target == "" {
				return "", memoryLinkLabel(line[:open]), line[close+1:], false
			}
			return target, memoryLinkLabel(line[:open]), line[close+1:], true
		}
		return "", "", "", false
	}

	return explicitMemoryLine(line)
}

func explicitMemoryLine(line string) (handle, title, summary string, wellFormed bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", "", false
	}
	if line[0] == '`' {
		closeOffset := strings.IndexByte(line[1:], '`')
		if closeOffset < 0 {
			return "", "", "", false
		}
		close := closeOffset + 1
		handle = line[1:close]
		if !hasMemoryFileSuffix(handle) {
			return "", "", "", false
		}
		return handle, "", line[close+1:], true
	}

	fieldEnd := len(line)
	for i, r := range line {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			fieldEnd = i
			break
		}
	}
	if fieldEnd == 0 {
		return "", "", "", false
	}
	rawHandle := line[:fieldEnd]
	handle = strings.TrimSuffix(rawHandle, ":")
	if !hasMemoryFileSuffix(handle) || (!strings.HasSuffix(rawHandle, ":") && !memoryEntryRemainder(line[fieldEnd:])) {
		return "", "", "", false
	}
	return handle, "", line[fieldEnd:], true
}

func memoryEntryRemainder(remainder string) bool {
	remainder = strings.TrimSpace(remainder)
	if remainder == "" {
		return true
	}
	first, _ := utf8.DecodeRuneInString(remainder)
	switch first {
	case ':', '-', '|', '–', '—':
		return true
	default:
		return false
	}
}

func hasMemoryFileSuffix(handle string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(handle)), ".md")
}

func memoryLinkLabel(line string) string {
	open := strings.Index(line, "[")
	if open < 0 {
		return ""
	}
	return strings.TrimSpace(line[open+1:])
}

func memoryHandleToken(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 || !hasMemoryFileSuffix(fields[0]) {
		return ""
	}
	return strings.Trim(fields[0], "`<>")
}

func normalizeMemoryHandle(handle string) string {
	handle = strings.TrimSpace(strings.ReplaceAll(handle, "\\", "/"))
	if handle == "" || strings.ContainsRune(handle, '\x00') || strings.IndexFunc(handle, unicode.IsControl) >= 0 || strings.HasPrefix(handle, "/") ||
		strings.HasPrefix(handle, "./") || strings.Contains(handle, ":") {
		return ""
	}
	for _, part := range strings.Split(handle, "/") {
		if part == ".." || part == "." {
			return ""
		}
	}
	cleaned := path.Clean(handle)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") ||
		strings.HasSuffix(cleaned, "/..") || !strings.HasSuffix(strings.ToLower(cleaned), ".md") {
		return ""
	}
	if strings.EqualFold(path.Base(cleaned), projectMemoryIndex) {
		return ""
	}
	return cleaned
}

func cleanMemorySummary(summary string) string {
	summary = strings.TrimSpace(summary)
	summary = strings.Trim(summary, "`")
	summary = strings.TrimSpace(summary)
	summary = strings.TrimLeft(summary, "-–—:| ")
	summary = strings.Trim(summary, "`")
	return strings.TrimSpace(summary)
}

func memoryTitleFromFile(handle string) string {
	base := path.Base(handle)
	return strings.TrimSuffix(base, path.Ext(base))
}

func readIndexedMemory(ctx context.Context, root string, entry memoryIndexEntry) (Memory, string, []string, error) {
	base := Memory{File: entry.File, Title: entry.Title, Summary: entry.Summary, Snippet: "", Body: ""}
	if _, err := safeMemoryPath(root, entry.File); err != nil {
		return base, "", []string{memoryFileUnsafeWarning(entry.File)}, errors.New("memory: indexed file reference is unsafe")
	}
	data, err := readMemoryTarget(ctx, root, entry.File, maxMemoryFileBytes)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return base, "", nil, err
		}
		if errors.Is(err, errMemoryPathUnsafe) || errors.Is(err, errMemoryFileChanged) {
			return base, "", []string{memoryFileUnsafeWarning(entry.File)}, errors.New("memory: unable to read indexed memory file")
		}
		return base, "", []string{memoryFileReadWarning(entry.File, err)}, errors.New("memory: unable to read indexed memory file")
	}
	memory, content, warnings := parseMemoryDocument(entry, string(data))
	return memory, content, warnings, nil
}

func readIndexedMemoryForSearch(ctx context.Context, root string, entry memoryIndexEntry, lowerQuery []byte, metadataMatch bool) (Memory, bool, []string, error) {
	base := Memory{File: entry.File, Title: entry.Title, Summary: entry.Summary, Snippet: "", Body: ""}
	// readMemoryTarget validates the target before opening it and revalidates it
	// after statting it, so search does not need a separate stale path check.
	data, err := readMemoryTarget(ctx, root, entry.File, maxMemoryFileBytes)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return base, false, nil, err
		}
		if errors.Is(err, errMemoryPathUnsafe) || errors.Is(err, errMemoryFileChanged) {
			return base, false, []string{memoryFileUnsafeWarning(entry.File)}, errors.New("memory: unable to read indexed memory file")
		}
		return base, false, []string{memoryFileReadWarning(entry.File, err)}, errors.New("memory: unable to read indexed memory file")
	}
	memory, bodyMatch, warnings := parseMemorySearchDocument(entry, data, lowerQuery, metadataMatch)
	return memory, bodyMatch, warnings, nil
}

// parseMemorySearchDocument extracts only the metadata and bounded text that a
// search result can expose. Unlike parseMemoryDocument, it deliberately does
// not materialize a normalized copy of the complete document body.
func parseMemorySearchDocument(entry memoryIndexEntry, raw []byte, lowerQuery []byte, metadataMatch bool) (Memory, bool, []string) {
	metadata, bodyOffset, warnings := parseMemorySearchFrontMatter(raw)
	content := raw[bodyOffset:]
	memory := Memory{
		File:    entry.File,
		Title:   strings.TrimSpace(entry.Title),
		Summary: strings.TrimSpace(entry.Summary),
		Snippet: "",
		Body:    "",
	}
	if value := strings.TrimSpace(metadata["title"]); value != "" {
		memory.Title = value
	}
	if memory.Title == "" {
		memory.Title = strings.TrimSpace(metadata["name"])
	}
	if memory.Title == "" {
		memory.Title = firstMemoryHeadingBytes(content)
	}
	if memory.Title == "" {
		memory.Title = memoryTitleFromFile(entry.File)
	}
	if value := strings.TrimSpace(metadata["summary"]); value != "" {
		memory.Summary = value
	} else if value := strings.TrimSpace(metadata["description"]); value != "" {
		memory.Summary = value
	}
	if memory.Summary == "" {
		memory.Summary = firstMemoryParagraphBytes(content)
	}

	if metadataMatch {
		memory.Snippet = memorySearchSnippetBytes(content, lowerQuery)
		return memory, false, warnings
	}
	bodyMatch := memorySearchBytesContains(content, lowerQuery)
	if bodyMatch {
		memory.Snippet = memorySearchSnippetBytes(content, lowerQuery)
	}
	return memory, bodyMatch, warnings
}

func parseMemorySearchFrontMatter(raw []byte) (map[string]string, int, []string) {
	metadata := map[string]string{}
	warnings := make([]string, 0)
	if len(raw) == 0 {
		return metadata, 0, warnings
	}
	first, offset, _ := memoryRawLine(raw, 0)
	if !memoryFrontMatterDelimiter(first, true) {
		return metadata, 0, warnings
	}

	endOffset := 0
	for scanOffset := offset; scanOffset < len(raw); {
		line, next, _ := memoryRawLine(raw, scanOffset)
		if memoryFrontMatterDelimiter(line, false) {
			endOffset = next
			break
		}
		scanOffset = next
	}
	if endOffset == 0 {
		appendMemoryWarning(&warnings, "memory file front matter is unterminated")
		return metadata, 0, warnings
	}

	for lineNumber, parseOffset := 2, offset; parseOffset < endOffset; lineNumber++ {
		line, next, _ := memoryRawLine(raw, parseOffset)
		if memoryFrontMatterDelimiter(line, false) {
			break
		}
		trimmed := memoryTrimSpaceBytes(line)
		if len(trimmed) != 0 && trimmed[0] != '#' {
			colon := bytes.IndexByte(trimmed, ':')
			if colon <= 0 {
				appendMemoryWarning(&warnings, fmt.Sprintf("memory file front matter line %d is malformed", lineNumber))
			} else if key := memoryFrontMatterKey(memoryTrimSpaceBytes(trimmed[:colon])); key != "" {
				value := strings.TrimSpace(memoryNormalizedString(memoryTrimSpaceBytes(trimmed[colon+1:])))
				metadata[key] = strings.Trim(value, "\"'")
			}
		}
		parseOffset = next
	}
	return metadata, endOffset, warnings
}

func memoryFrontMatterDelimiter(line []byte, first bool) bool {
	if first && bytes.HasPrefix(line, []byte("\ufeff")) {
		line = line[len("\ufeff"):]
	}
	return bytes.Equal(memoryTrimSpaceBytes(line), []byte("---"))
}

func memoryTrimSpaceBytes(value []byte) []byte {
	start := 0
	for start < len(value) {
		r, size := utf8.DecodeRune(value[start:])
		if !unicode.IsSpace(r) {
			break
		}
		start += size
	}
	end := len(value)
	for end > start {
		r, size := utf8.DecodeLastRune(value[:end])
		if !unicode.IsSpace(r) {
			break
		}
		end -= size
	}
	return value[start:end]
}

func memoryFrontMatterKey(value []byte) string {
	for _, key := range []string{"name", "title", "summary", "description"} {
		if memoryLowerKeyEqual(value, key) {
			return key
		}
	}
	return ""
}

// memoryLowerKeyEqual compares value as strings.ToLower would, but avoids
// materializing a potentially long front-matter key just to compare it with a
// small recognized ASCII key.
func memoryLowerKeyEqual(value []byte, lower string) bool {
	for _, expected := range lower {
		if len(value) == 0 {
			return false
		}
		r, size := utf8.DecodeRune(value)
		if r == utf8.RuneError && size == 1 || unicode.ToLower(r) != expected {
			return false
		}
		value = value[size:]
	}
	return len(value) == 0
}

func memoryRawLine(data []byte, offset int) ([]byte, int, bool) {
	if newline := bytes.IndexByte(data[offset:], '\n'); newline >= 0 {
		end := offset + newline
		line := data[offset:end]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		return line, end + 1, true
	}
	return data[offset:], len(data), false
}

func memoryNormalizedString(data []byte) string {
	return strings.ToValidUTF8(string(data), "\uFFFD")
}

func firstMemoryHeadingBytes(content []byte) string {
	for offset := 0; offset < len(content); {
		line, next, _ := memoryRawLine(content, offset)
		trimmed := bytes.TrimSpace(line)
		if bytes.HasPrefix(trimmed, []byte("#")) {
			lineText := memoryNormalizedString(trimmed)
			heading := strings.TrimSpace(strings.TrimLeft(lineText, "#"))
			heading = strings.TrimSpace(strings.TrimRight(heading, "#"))
			if heading != "" {
				return heading
			}
		}
		offset = next
	}
	return ""
}

func firstMemoryParagraphBytes(content []byte) string {
	truncated := memoryTextTruncator{max: 220}
	started := false
	for offset := 0; offset < len(content); {
		line, next, _ := memoryRawLine(content, offset)
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			if started {
				break
			}
		} else if !bytes.HasPrefix(trimmed, []byte("#")) && !bytes.HasPrefix(trimmed, []byte("```")) {
			if started {
				truncated.appendRune(' ')
			}
			started = true
			truncated.appendBytes(trimmed)
		}
		offset = next
	}
	return truncated.string()
}

func memorySearchSnippetBytes(content []byte, lowerQuery []byte) string {
	for offset := 0; offset < len(content); {
		line, next, _ := memoryRawLine(content, offset)
		if memoryLineHasNonSpace(line) && memorySearchBytesContains(line, lowerQuery) {
			return truncateMemorySearchBytes(line, 220)
		}
		offset = next
	}
	return firstMemoryParagraphBytes(content)
}

func memorySearchBytesContains(content, lowerQuery []byte) bool {
	if len(lowerQuery) == 0 {
		return true
	}
	matcher := memorySearchMatcher{query: lowerQuery}
	for offset := 0; offset < len(content); {
		line, next, hasNewline := memoryRawLine(content, offset)
		memorySearchFoldBytes(line, &matcher)
		if matcher.found {
			return true
		}
		if hasNewline {
			matcher.feed([]byte("\n"))
			if matcher.found {
				return true
			}
		}
		offset = next
	}
	return false
}

func memoryLineHasNonSpace(value []byte) bool {
	for len(value) > 0 {
		r, size := utf8.DecodeRune(value)
		if r == utf8.RuneError && size == 1 {
			return true
		}
		if !unicode.IsSpace(r) {
			return true
		}
		value = value[size:]
	}
	return false
}

func memorySearchFoldBytes(value []byte, matcher *memorySearchMatcher) {
	if len(value) == 0 || matcher.found {
		return
	}
	if !utf8.Valid(value) {
		memorySearchFoldInvalidUTF8(value, matcher)
		return
	}
	for offset := 0; offset < len(value) && !matcher.found; {
		end := min(offset+memorySearchChunkBytes, len(value))
		if end < len(value) {
			for end > offset && !utf8.RuneStart(value[end]) {
				end--
			}
		}
		matcher.feed(bytes.ToLower(value[offset:end]))
		offset = end
	}
}

func memorySearchFoldInvalidUTF8(value []byte, matcher *memorySearchMatcher) {
	normalized := make([]byte, 0, min(len(value), memorySearchChunkBytes))
	invalidRun := false
	flush := func() {
		if len(normalized) == 0 || matcher.found {
			return
		}
		matcher.feed(bytes.ToLower(normalized))
		normalized = normalized[:0]
	}
	for len(value) > 0 && !matcher.found {
		r, size := utf8.DecodeRune(value)
		if r == utf8.RuneError && size == 1 {
			if !invalidRun {
				if len(normalized)+len("\uFFFD") > memorySearchChunkBytes {
					flush()
					if matcher.found {
						break
					}
				}
				normalized = append(normalized, "\uFFFD"...)
				invalidRun = true
			}
			value = value[1:]
			continue
		}
		invalidRun = false
		if len(normalized) > 0 && len(normalized)+size > memorySearchChunkBytes {
			flush()
			if matcher.found {
				break
			}
		}
		normalized = append(normalized, value[:size]...)
		value = value[size:]
		if len(normalized) >= memorySearchChunkBytes {
			flush()
		}
	}
	flush()
}

type memorySearchMatcher struct {
	query []byte
	tail  []byte
	found bool
}

func (matcher *memorySearchMatcher) feed(value []byte) {
	if matcher.found || len(value) == 0 {
		return
	}
	if bytes.Contains(value, matcher.query) {
		matcher.found = true
		return
	}
	limit := len(matcher.query) - 1
	if limit <= 0 {
		return
	}
	if len(matcher.tail) > 0 {
		prefix := value[:min(len(value), limit)]
		boundary := make([]byte, 0, len(matcher.tail)+len(prefix))
		boundary = append(boundary, matcher.tail...)
		boundary = append(boundary, prefix...)
		if bytes.Contains(boundary, matcher.query) {
			matcher.found = true
			return
		}
	}
	if len(value) >= limit {
		matcher.tail = append(matcher.tail[:0], value[len(value)-limit:]...)
		return
	}
	matcher.tail = append(matcher.tail, value...)
	if len(matcher.tail) > limit {
		copy(matcher.tail, matcher.tail[len(matcher.tail)-limit:])
		matcher.tail = matcher.tail[:limit]
	}
}

func memoryFileUnsafeWarning(handle string) string {
	return sanitizeMemoryWarning(fmt.Sprintf("memory file %q has an unsafe path", handle))
}

func memoryFileReadWarning(handle string, err error) string {
	var warning string
	if errors.Is(err, os.ErrNotExist) {
		warning = fmt.Sprintf("memory file %q is missing", handle)
	} else if errors.Is(err, errMemoryFileNotRegular) {
		warning = fmt.Sprintf("memory file %q is not a regular file", handle)
	} else {
		warning = fmt.Sprintf("memory file %q could not be read", handle)
	}
	return sanitizeMemoryWarning(warning)
}

func parseMemoryDocument(entry memoryIndexEntry, raw string) (Memory, string, []string) {
	raw = strings.ToValidUTF8(strings.ReplaceAll(raw, "\r\n", "\n"), "\uFFFD")
	metadata, content, warnings := parseMemoryFrontMatter(raw)
	memory := Memory{
		File:    entry.File,
		Title:   strings.TrimSpace(entry.Title),
		Summary: strings.TrimSpace(entry.Summary),
		Snippet: "",
		Body:    raw,
	}
	if value := strings.TrimSpace(metadata["title"]); value != "" {
		memory.Title = value
	}
	if memory.Title == "" {
		memory.Title = strings.TrimSpace(metadata["name"])
	}
	if memory.Title == "" {
		memory.Title = firstMemoryHeading(content)
	}
	if memory.Title == "" {
		memory.Title = memoryTitleFromFile(entry.File)
	}
	if value := strings.TrimSpace(metadata["summary"]); value != "" {
		memory.Summary = value
	} else if value := strings.TrimSpace(metadata["description"]); value != "" {
		memory.Summary = value
	}
	if memory.Summary == "" {
		memory.Summary = firstMemoryParagraph(content)
	}
	return memory, content, warnings
}

func parseMemoryFrontMatter(raw string) (map[string]string, string, []string) {
	metadata := map[string]string{}
	warnings := make([]string, 0)
	lines := strings.Split(raw, "\n")
	if len(lines) == 0 || strings.TrimSpace(strings.TrimPrefix(lines[0], "\ufeff")) != "---" {
		return metadata, raw, warnings
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		appendMemoryWarning(&warnings, "memory file front matter is unterminated")
		return metadata, raw, warnings
	}
	for i, line := range lines[1:end] {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		colon := strings.Index(line, ":")
		if colon <= 0 {
			appendMemoryWarning(&warnings, fmt.Sprintf("memory file front matter line %d is malformed", i+2))
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:colon]))
		if key != "name" && key != "title" && key != "summary" && key != "description" {
			continue
		}
		value := strings.TrimSpace(line[colon+1:])
		value = strings.Trim(value, "\"'")
		metadata[key] = value
	}
	return metadata, strings.Join(lines[end+1:], "\n"), warnings
}

func firstMemoryHeading(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			heading := strings.TrimSpace(strings.TrimLeft(line, "#"))
			heading = strings.TrimSpace(strings.TrimRight(heading, "#"))
			if heading != "" {
				return heading
			}
		}
	}
	return ""
}

func firstMemoryParagraph(content string) string {
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(lines) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "```") {
			continue
		}
		lines = append(lines, line)
	}
	return truncateMemoryText(strings.Join(lines, " "), 220)
}

type memorySearchText struct {
	original   string
	normalized string
}

func newMemorySearchText(content string) memorySearchText {
	return memorySearchText{
		original:   content,
		normalized: strings.ToLower(content),
	}
}

func (text memorySearchText) contains(normalizedQuery string) bool {
	return strings.Contains(text.normalized, normalizedQuery)
}

func (text memorySearchText) snippet(normalizedQuery string) string {
	originalOffset, normalizedOffset := 0, 0
	for originalOffset <= len(text.original) && normalizedOffset <= len(text.normalized) {
		originalLine, nextOriginal := memorySearchLine(text.original, originalOffset)
		normalizedLine, nextNormalized := memorySearchLine(text.normalized, normalizedOffset)
		line := strings.TrimSpace(originalLine)
		if line != "" && strings.Contains(strings.TrimSpace(normalizedLine), normalizedQuery) {
			return truncateMemoryText(line, 220)
		}
		originalOffset, normalizedOffset = nextOriginal, nextNormalized
	}
	return firstMemoryParagraph(text.original)
}

func memorySearchLine(value string, offset int) (string, int) {
	if newline := strings.IndexByte(value[offset:], '\n'); newline >= 0 {
		return value[offset : offset+newline], offset + newline + 1
	}
	return value[offset:], len(value) + 1
}

type memoryTextTruncator struct {
	max          int
	runes        []rune
	pendingSpace bool
	overflow     bool
}

func (truncated *memoryTextTruncator) appendBytes(value []byte) {
	for len(value) > 0 {
		r, size := utf8.DecodeRune(value)
		if r == utf8.RuneError && size == 1 {
			truncated.appendTextRune(utf8.RuneError)
			value = value[1:]
			for len(value) > 0 {
				r, size = utf8.DecodeRune(value)
				if r != utf8.RuneError || size != 1 {
					break
				}
				value = value[1:]
			}
			continue
		}
		truncated.appendTextRune(r)
		value = value[size:]
	}
}

func (truncated *memoryTextTruncator) appendTextRune(r rune) {
	if unicode.IsSpace(r) {
		if len(truncated.runes) > 0 {
			truncated.pendingSpace = true
		}
		return
	}
	if truncated.pendingSpace {
		truncated.appendRune(' ')
		truncated.pendingSpace = false
	}
	truncated.appendRune(r)
}

func (truncated *memoryTextTruncator) appendRune(r rune) {
	if len(truncated.runes) >= truncated.max {
		truncated.overflow = true
		return
	}
	truncated.runes = append(truncated.runes, r)
}

func (truncated memoryTextTruncator) string() string {
	if truncated.max <= 0 || len(truncated.runes) == 0 {
		return ""
	}
	if truncated.overflow {
		return string(truncated.runes[:truncated.max-1]) + "…"
	}
	return string(truncated.runes)
}

func truncateMemorySearchBytes(value []byte, max int) string {
	if max <= 0 {
		return ""
	}
	truncated := memoryTextTruncator{max: max}
	truncated.appendBytes(value)
	return truncated.string()
}

func truncateMemoryText(value string, max int) string {
	value = strings.Join(strings.Fields(value), " ")
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max-1]) + "…"
}

func resolveMemoryEntry(entries []memoryIndexEntry, reference string) (memoryIndexEntry, error) {
	var zero memoryIndexEntry
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return zero, errors.New("memory: a memory file or title is required")
	}
	normalized := normalizeMemoryHandle(reference)
	if normalized != "" {
		var exact []memoryIndexEntry
		for _, entry := range entries {
			if strings.EqualFold(entry.File, normalized) {
				exact = append(exact, entry)
			}
		}
		if len(exact) == 1 {
			return exact[0], nil
		}
	}

	lower := strings.ToLower(reference)
	exactTitles := memoryEntryMatches(entries, func(entry memoryIndexEntry) bool {
		return strings.EqualFold(entry.Title, reference)
	})
	if len(exactTitles) == 1 {
		return exactTitles[0], nil
	}
	if len(exactTitles) > 1 {
		return zero, memoryAmbiguousReferenceError(exactTitles)
	}
	for _, tier := range []func(memoryIndexEntry) bool{
		func(entry memoryIndexEntry) bool {
			return strings.HasPrefix(strings.ToLower(entry.File), lower) || strings.HasPrefix(strings.ToLower(entry.Title), lower)
		},
		func(entry memoryIndexEntry) bool {
			return strings.Contains(strings.ToLower(entry.Title), lower) || strings.Contains(strings.ToLower(entry.File), lower)
		},
	} {
		matches := memoryEntryMatches(entries, tier)
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return zero, memoryAmbiguousReferenceError(matches)
		}
	}
	return zero, errors.New("memory: requested memory is not indexed; use memory list")
}

func memoryAmbiguousReferenceError(matches []memoryIndexEntry) error {
	candidates := make([]string, 0, min(len(matches), maxMemoryAmbiguityCandidates))
	for i, match := range matches {
		if i >= maxMemoryAmbiguityCandidates {
			break
		}
		candidate := sanitizeMemoryWarning(match.File)
		if candidate == "" {
			candidate = "(unnamed)"
		}
		candidates = append(candidates, truncateMemoryText(candidate, maxMemoryAmbiguityCandidateRunes))
	}
	if len(matches) > maxMemoryAmbiguityCandidates {
		candidates = append(candidates, fmt.Sprintf("… %d more", len(matches)-maxMemoryAmbiguityCandidates))
	}
	message := fmt.Sprintf("memory: reference is ambiguous; choose one of %s", strings.Join(candidates, ", "))
	return errors.New(sanitizeMemoryWarning(message))
}

func memoryEntryMatches(entries []memoryIndexEntry, predicate func(memoryIndexEntry) bool) []memoryIndexEntry {
	matches := make([]memoryIndexEntry, 0)
	for _, entry := range entries {
		if predicate(entry) {
			matches = append(matches, entry)
		}
	}
	return matches
}

func memoryDocument(memory Memory, warnings []string, available bool) MemoryDocument {
	return MemoryDocument{
		File:      memory.File,
		Title:     memory.Title,
		Summary:   memory.Summary,
		Snippet:   memory.Snippet,
		Body:      memory.Body,
		Available: available,
		Warnings:  append(make([]string, 0, len(warnings)), warnings...),
	}
}

func inspectIndexedMemory(ctx context.Context, root, handle string) string {
	if err := contextErr(ctx); err != nil {
		return ""
	}
	filePath, err := safeMemoryPath(root, handle)
	if err != nil {
		return memoryFileUnsafeWarning(handle)
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return memoryFileReadWarning(handle, err)
	}
	if !info.Mode().IsRegular() {
		return memoryFileReadWarning(handle, errMemoryFileNotRegular)
	}
	return ""
}

func safeMemoryPath(root, handle string) (string, error) {
	handle = normalizeMemoryHandle(handle)
	if handle == "" {
		return "", errors.New("unsafe memory handle")
	}
	return safeMemoryTarget(root, handle)
}

func safeMemoryIndexPath(root string) (string, error) {
	return safeMemoryTarget(root, projectMemoryIndex)
}

func safeMemoryTarget(root, handle string) (string, error) {
	root, resolvedRoot, err := safeMemoryRoot(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.FromSlash(handle))
	if !memoryPathWithin(root, target) {
		return "", errors.New("memory path escapes project directory")
	}

	resolvedTarget, err := evalMemoryPathLenient(target)
	if err != nil {
		return "", err
	}
	if !memoryPathWithin(resolvedRoot, resolvedTarget) {
		return "", errors.New("memory path escapes project directory")
	}
	return target, nil
}

// safeMemoryRoot verifies both the lexical memory directory and its resolved
// location. The repository itself may be a symlink, but the memory directory
// must remain below that canonical repository root.
func safeMemoryRoot(root string) (string, string, error) {
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", "", err
	}
	repoRoot := filepath.Dir(filepath.Dir(root))
	resolvedRepo, err := evalMemoryPathLenient(repoRoot)
	if err != nil {
		return "", "", err
	}
	resolvedRoot, err := evalMemoryPathLenient(root)
	if err != nil {
		return "", "", err
	}
	if !memoryPathWithin(resolvedRepo, resolvedRoot) {
		return "", "", errors.New("memory directory escapes repository")
	}
	return root, resolvedRoot, nil
}

func memoryPathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func evalMemoryPathLenient(value string) (string, error) {
	value = filepath.Clean(value)
	var suffix []string
	cur := value
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return value, nil
		}
		suffix = append(suffix, filepath.Base(cur))
		cur = parent
	}
	resolved, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return "", err
	}
	for i := len(suffix) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, suffix[i])
	}
	return filepath.Clean(resolved), nil
}

func readMemoryTarget(ctx context.Context, root, handle string, maxBytes int64) ([]byte, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	filePath, err := safeMemoryTarget(root, handle)
	if err != nil {
		return nil, errMemoryPathUnsafe
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errMemoryFileNotRegular
	}
	// Re-check the trust boundary after the path stat. If the memory directory
	// was replaced while it was being inspected, do not open the replacement.
	checkedPath, err := safeMemoryTarget(root, handle)
	if err != nil {
		return nil, errMemoryPathUnsafe
	}
	if checkedPath != filePath {
		return nil, errMemoryFileChanged
	}
	return readMemoryFile(ctx, filePath, info, maxBytes)
}

func readMemoryFile(ctx context.Context, filePath string, expectedInfo os.FileInfo, maxBytes int64) ([]byte, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	file, err := openMemoryFile(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, errMemoryFileNotRegular
	}
	if !os.SameFile(expectedInfo, openedInfo) {
		return nil, errMemoryFileChanged
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errMemoryFileTooLarge
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	return data, nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func appendMemoryWarning(warnings *[]string, warning string) {
	warning = sanitizeMemoryWarning(warning)
	if warning == "" {
		return
	}
	for _, existing := range *warnings {
		if existing == warning {
			return
		}
	}
	if len(*warnings) >= maxMemoryWarnings {
		const omitted = "additional memory warnings omitted"
		for _, existing := range *warnings {
			if existing == omitted {
				return
			}
		}
		(*warnings)[maxMemoryWarnings-1] = omitted
		return
	}
	*warnings = append(*warnings, warning)
}

func sanitizeMemoryWarning(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case unicode.IsControl(r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	value = strings.Join(strings.Fields(b.String()), " ")
	runes := []rune(value)
	if len(runes) <= maxMemoryWarningRunes {
		return value
	}
	return string(runes[:maxMemoryWarningRunes-1]) + "…"
}

// Keep the public result fields explicit and stable; all collection fields are
// initialized by the public methods, including empty results.
