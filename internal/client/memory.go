package client

import (
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
	for _, entry := range entries {
		base := Memory{File: entry.File, Title: entry.Title, Summary: entry.Summary}
		metadataMatch := strings.Contains(strings.ToLower(entry.File), lowerQuery) ||
			strings.Contains(strings.ToLower(entry.Title), lowerQuery) ||
			strings.Contains(strings.ToLower(entry.Summary), lowerQuery)

		memory, content, fileWarnings, readErr := readIndexedMemory(ctx, root, entry)
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

		bodyMatch := strings.Contains(strings.ToLower(content), lowerQuery)
		if !metadataMatch && !bodyMatch {
			continue
		}
		memory.Body = ""
		memory.Snippet = memorySearchSnippet(content, query)
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

func memorySearchSnippet(content, query string) string {
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && strings.Contains(strings.ToLower(line), lowerQuery) {
			return truncateMemoryText(line, 220)
		}
	}
	return firstMemoryParagraph(content)
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
