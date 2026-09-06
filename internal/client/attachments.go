package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/html"
)

// Attachment is one file associated with a task. The ID is the stable backend
// identifier used by the delete route; FileName and FileSize are the fields
// rendered by the task detail attachment list.
type Attachment struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	FileName  string `json:"file_name"`
	FilePath  string `json:"file_path,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	FileSize  int64  `json:"file_size"`
}

// PartialAttachmentUploadError reports a successful HTTP upload response that
// did not contain evidence for every requested file in the refreshed list.
// Uploaded contains only newly created attachment records matched to the
// requested filenames; Missing contains each requested filename without a
// matching new record, including repeated filenames when applicable.
type PartialAttachmentUploadError struct {
	Requested []string
	Uploaded  []Attachment
	Missing   []string
}

func (e *PartialAttachmentUploadError) Error() string {
	if e == nil {
		return "partial attachment upload"
	}
	return fmt.Sprintf(
		"partial attachment upload: uploaded %d of %d file(s); successful: %s; not uploaded: %s",
		len(e.Uploaded), len(e.Requested), formatUploadedAttachments(e.Uploaded), strings.Join(e.Missing, ", "),
	)
}

func formatUploadedAttachments(attachments []Attachment) string {
	if len(attachments) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		parts = append(parts, fmt.Sprintf("%s (%d B)", attachment.FileName, attachment.FileSize))
	}
	return strings.Join(parts, ", ")
}

// ListTaskAttachments reads the attachment list embedded in a task detail
// page. The selected project is required because the task page is also the
// backend's attachment read contract.
func (c *Client) ListTaskAttachments(ctx context.Context, taskID, projectID string) ([]Attachment, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("project ID is required for task attachments")
	}
	root, err := c.getHTML(ctx, "/tasks/"+url.PathEscape(taskID)+query("project_id", projectID))
	if err != nil {
		return nil, err
	}
	return parseTaskAttachmentsForProject(root, taskID, projectID)
}

// GetTaskAttachments is an alias for ListTaskAttachments retained as a
// discoverable read name for callers working with task detail resources.
func (c *Client) GetTaskAttachments(ctx context.Context, taskID, projectID string) ([]Attachment, error) {
	return c.ListTaskAttachments(ctx, taskID, projectID)
}

// AddTaskAttachments uploads one or more local files using the same multipart
// field and HTMX response contract as the web task detail form. The response
// is the refreshed attachment-list fragment, not an optimistic local result.
func (c *Client) AddTaskAttachments(ctx context.Context, taskID, projectID string, filePaths []string) ([]Attachment, error) {
	if strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("task ID is required for task attachments")
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("project ID is required for task attachments")
	}
	if len(filePaths) == 0 {
		return nil, fmt.Errorf("at least one attachment file is required")
	}

	requestedNames := make([]string, 0, len(filePaths))
	for _, path := range filePaths {
		requestedNames = append(requestedNames, filepath.Base(strings.TrimSpace(path)))
	}

	before, err := c.ListTaskAttachments(ctx, taskID, projectID)
	if err != nil {
		return nil, fmt.Errorf("checking existing task attachments: %w", err)
	}

	body, err := newAttachmentMultipartBody(filePaths)
	if err != nil {
		return nil, err
	}
	closeDone := make(chan struct{})
	stopClose := context.AfterFunc(ctx, func() {
		_ = body.closeWithError(ctx.Err())
		close(closeDone)
	})
	path := "/tasks/" + url.PathEscape(taskID) + "/attachments" + query("project_id", projectID)
	root, requestErr := c.doMultipartHTML(ctx, http.MethodPost, path, body, body.ContentType())
	if !stopClose() {
		<-closeDone
	}
	closeErr := body.Close()
	if localErr := body.LocalError(); localErr != nil {
		return nil, localErr
	}
	if requestErr != nil {
		return nil, requestErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	attachments, err := parseAttachmentMutationResponse(root, "upload attachments", projectID)
	if err != nil {
		return nil, err
	}
	for i := range attachments {
		attachments[i].TaskID = taskID
	}
	uploaded, missing := matchUploadedAttachments(before, attachments, requestedNames)
	if len(missing) > 0 {
		return attachments, &PartialAttachmentUploadError{
			Requested: append([]string(nil), requestedNames...),
			Uploaded:  uploaded,
			Missing:   missing,
		}
	}
	return attachments, nil
}

func matchUploadedAttachments(before, after []Attachment, requestedNames []string) ([]Attachment, []string) {
	existingIDs := make(map[string]struct{}, len(before))
	for _, attachment := range before {
		if attachment.ID != "" {
			existingIDs[attachment.ID] = struct{}{}
		}
	}

	newByName := make(map[string][]Attachment)
	for _, attachment := range after {
		if attachment.ID == "" {
			continue
		}
		if _, existed := existingIDs[attachment.ID]; existed {
			continue
		}
		newByName[attachment.FileName] = append(newByName[attachment.FileName], attachment)
	}

	uploaded := make([]Attachment, 0, len(requestedNames))
	missing := make([]string, 0)
	for _, requestedName := range requestedNames {
		candidates := newByName[requestedName]
		if len(candidates) == 0 {
			missing = append(missing, requestedName)
			continue
		}
		uploaded = append(uploaded, candidates[0])
		newByName[requestedName] = candidates[1:]
	}
	return uploaded, missing
}

// UploadTaskAttachments is a descriptive alias for AddTaskAttachments.
func (c *Client) UploadTaskAttachments(ctx context.Context, taskID, projectID string, filePaths []string) ([]Attachment, error) {
	return c.AddTaskAttachments(ctx, taskID, projectID, filePaths)
}

// DeleteTaskAttachment deletes one attachment and returns the refreshed list
// from the backend. The project query is mandatory so the server can reject an
// attachment owned by another project.
func (c *Client) DeleteTaskAttachment(ctx context.Context, attachmentID, projectID string) ([]Attachment, error) {
	if strings.TrimSpace(attachmentID) == "" {
		return nil, fmt.Errorf("attachment ID is required")
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("project ID is required for task attachments")
	}
	path := "/attachments/" + url.PathEscape(attachmentID) + query("project_id", projectID)
	root, err := c.doMultipartHTML(ctx, http.MethodDelete, path, nil, "")
	if err != nil {
		return nil, err
	}
	return parseAttachmentMutationResponse(root, "delete attachment", projectID)
}

// DeleteAttachment is the route-oriented alias for DeleteTaskAttachment.
func (c *Client) DeleteAttachment(ctx context.Context, attachmentID, projectID string) ([]Attachment, error) {
	return c.DeleteTaskAttachment(ctx, attachmentID, projectID)
}

type attachmentFile interface {
	io.Reader
	io.Closer
	Stat() (os.FileInfo, error)
}

var openAttachmentFile = func(path string) (attachmentFile, error) {
	return os.Open(path)
}

var statAttachmentFile = os.Stat

type attachmentMultipartBody struct {
	paths         []string
	fileSizes     []int64
	fileInfos     []os.FileInfo
	preflightErrs []error
	headers       [][]byte
	trailer       []byte
	contentType   string
	contentLength int64

	mu            sync.Mutex
	index         int
	headerOffset  int
	trailerOffset int
	remaining     int64
	current       attachmentFile
	openingDone   chan struct{}
	fileCloseDone chan struct{}
	closeDone     chan struct{}
	closed        bool
	terminalErr   error
	localErr      error
}

func newAttachmentMultipartBody(paths []string) (*attachmentMultipartBody, error) {
	body := &attachmentMultipartBody{
		paths:         make([]string, len(paths)),
		fileSizes:     make([]int64, len(paths)),
		fileInfos:     make([]os.FileInfo, len(paths)),
		preflightErrs: make([]error, len(paths)),
		closeDone:     make(chan struct{}),
	}
	for i, rawPath := range paths {
		path := strings.TrimSpace(rawPath)
		if path == "" {
			return nil, fmt.Errorf("attachment file path is required")
		}
		body.paths[i] = path
		info, err := statAttachmentFile(path)
		if err != nil {
			var pathErr *os.PathError
			if errors.As(err, &pathErr) {
				openPathErr := *pathErr
				openPathErr.Op = "open"
				err = &openPathErr
			}
			body.preflightErrs[i] = fmt.Errorf("open attachment %q: %w", path, err)
			continue
		}
		if info.IsDir() {
			body.preflightErrs[i] = fmt.Errorf("attachment %q is a directory", path)
			continue
		}
		body.fileSizes[i] = info.Size()
		body.fileInfos[i] = info
	}

	var encoded bytes.Buffer
	writer := multipart.NewWriter(&encoded)
	body.headers = make([][]byte, 0, len(paths))
	body.contentType = writer.FormDataContentType()
	previous := 0
	for i, path := range body.paths {
		if _, err := writer.CreateFormFile("files", filepath.Base(path)); err != nil {
			return nil, err
		}
		header := append([]byte(nil), encoded.Bytes()[previous:]...)
		body.headers = append(body.headers, header)
		body.contentLength += int64(len(header)) + body.fileSizes[i]
		previous = encoded.Len()
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("closing attachment upload: %w", err)
	}
	body.trailer = append([]byte(nil), encoded.Bytes()[previous:]...)
	body.contentLength += int64(len(body.trailer))
	return body, nil
}

func (b *attachmentMultipartBody) ContentType() string  { return b.contentType }
func (b *attachmentMultipartBody) ContentLength() int64 { return b.contentLength }

func (b *attachmentMultipartBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		b.mu.Lock()
		if b.closed {
			err := b.terminalErr
			b.mu.Unlock()
			if err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		if b.terminalErr != nil {
			err := b.terminalErr
			b.mu.Unlock()
			return 0, err
		}
		if b.index >= len(b.paths) {
			if b.trailerOffset < len(b.trailer) {
				n := copy(p, b.trailer[b.trailerOffset:])
				b.trailerOffset += n
				b.mu.Unlock()
				return n, nil
			}
			b.mu.Unlock()
			return 0, io.EOF
		}
		if b.headerOffset < len(b.headers[b.index]) {
			n := copy(p, b.headers[b.index][b.headerOffset:])
			b.headerOffset += n
			b.mu.Unlock()
			return n, nil
		}

		path := b.paths[b.index]
		index := b.index
		if err := b.preflightErrs[index]; err != nil {
			b.recordLocalErrorLocked(err)
			b.mu.Unlock()
			return 0, err
		}
		file := b.current
		if file == nil {
			openingDone := make(chan struct{})
			b.openingDone = openingDone
			b.mu.Unlock()

			opened, openErr := openAttachmentFile(path)
			var preparationErr error
			if openErr != nil {
				preparationErr = fmt.Errorf("open attachment %q: %w", path, openErr)
			} else {
				openedInfo, statErr := opened.Stat()
				switch {
				case statErr != nil:
					preparationErr = fmt.Errorf("stat attachment %q: %w", path, statErr)
				case openedInfo.IsDir():
					preparationErr = fmt.Errorf("attachment %q is a directory", path)
				case !samePreparedAttachment(b.fileInfos[index], openedInfo):
					preparationErr = fmt.Errorf("attachment %q changed after upload preparation", path)
				}
			}

			b.mu.Lock()
			if b.closed || b.index != index || b.current != nil || preparationErr != nil {
				b.mu.Unlock()
				var closeErr error
				if opened != nil && openErr == nil {
					closeErr = opened.Close()
				}
				b.mu.Lock()
				if preparationErr != nil {
					b.recordLocalErrorLocked(preparationErr)
				} else if closeErr != nil {
					b.recordLocalErrorLocked(fmt.Errorf("close attachment %q: %w", path, closeErr))
				}
				if b.openingDone == openingDone {
					b.openingDone = nil
					close(openingDone)
				}
				closed := b.closed
				err := b.terminalErr
				b.mu.Unlock()
				if err != nil {
					return 0, err
				}
				if closed {
					return 0, io.EOF
				}
				continue
			}
			b.current = opened
			b.remaining = b.fileSizes[index]
			b.openingDone = nil
			close(openingDone)
			file = opened
		}

		remaining := b.remaining
		if remaining == 0 {
			b.mu.Unlock()
			var extra [1]byte
			n, probeErr := file.Read(extra[:])
			b.mu.Lock()
			if b.closed || b.current != file || b.index != index {
				err := b.terminalErr
				b.mu.Unlock()
				if err != nil {
					return 0, err
				}
				return 0, io.EOF
			}
			if n > 0 {
				b.recordLocalErrorLocked(fmt.Errorf("attachment %q changed during upload", path))
			} else if probeErr != nil && !errors.Is(probeErr, io.EOF) {
				b.recordLocalErrorLocked(fmt.Errorf("read attachment %q: %w", path, probeErr))
			} else if probeErr == nil {
				b.mu.Unlock()
				continue
			}
			b.current = nil
			b.index++
			b.headerOffset = 0
			fileCloseDone := make(chan struct{})
			b.fileCloseDone = fileCloseDone
			b.mu.Unlock()
			closeErr := file.Close()
			b.mu.Lock()
			if closeErr != nil && b.localErr == nil {
				b.recordLocalErrorLocked(fmt.Errorf("close attachment %q: %w", path, closeErr))
			}
			err := b.terminalErr
			b.fileCloseDone = nil
			close(fileCloseDone)
			b.mu.Unlock()
			if err != nil {
				return 0, err
			}
			continue
		}
		if int64(len(p)) > remaining {
			p = p[:remaining]
		}
		b.mu.Unlock()

		n, readErr := file.Read(p)
		b.mu.Lock()
		if b.closed || b.current != file || b.index != index {
			err := b.terminalErr
			b.mu.Unlock()
			if n > 0 {
				return n, err
			}
			if err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		b.remaining -= int64(n)
		remaining = b.remaining
		if readErr == nil {
			b.mu.Unlock()
			if n == 0 {
				continue
			}
			return n, nil
		}
		b.current = nil
		b.index++
		b.headerOffset = 0
		fileCloseDone := make(chan struct{})
		b.fileCloseDone = fileCloseDone
		b.mu.Unlock()

		closeErr := file.Close()
		b.mu.Lock()
		if !errors.Is(readErr, io.EOF) {
			b.recordLocalErrorLocked(fmt.Errorf("read attachment %q: %w", path, readErr))
		} else if remaining > 0 {
			b.recordLocalErrorLocked(fmt.Errorf("read attachment %q: %w", path, io.ErrUnexpectedEOF))
		} else if closeErr != nil {
			b.recordLocalErrorLocked(fmt.Errorf("close attachment %q: %w", path, closeErr))
		}
		err := b.terminalErr
		b.fileCloseDone = nil
		close(fileCloseDone)
		b.mu.Unlock()
		if err != nil {
			return n, err
		}
		if n > 0 {
			return n, nil
		}
	}
}

func (b *attachmentMultipartBody) recordTerminalErrorLocked(err error) error {
	if b.terminalErr == nil {
		b.terminalErr = err
	}
	return b.terminalErr
}

func (b *attachmentMultipartBody) recordLocalErrorLocked(err error) error {
	if b.localErr == nil {
		b.localErr = err
	}
	return b.recordTerminalErrorLocked(err)
}

func (b *attachmentMultipartBody) LocalError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.localErr
}

func samePreparedAttachment(prepared, opened os.FileInfo) bool {
	if prepared == nil || opened == nil || prepared.Size() != opened.Size() {
		return false
	}
	if prepared.Sys() == nil || opened.Sys() == nil {
		return true
	}
	return os.SameFile(prepared, opened)
}

func (b *attachmentMultipartBody) closeWithError(err error) error {
	b.mu.Lock()
	if b.terminalErr == nil {
		b.terminalErr = err
	}
	b.mu.Unlock()
	return b.Close()
}

func (b *attachmentMultipartBody) Close() error {
	b.mu.Lock()
	if b.closed {
		done := b.closeDone
		b.mu.Unlock()
		<-done
		b.mu.Lock()
		err := b.terminalErr
		b.mu.Unlock()
		return err
	}
	b.closed = true
	b.mu.Unlock()

	for {
		b.mu.Lock()
		if b.openingDone != nil {
			done := b.openingDone
			b.mu.Unlock()
			<-done
			continue
		}
		if b.fileCloseDone != nil {
			done := b.fileCloseDone
			b.mu.Unlock()
			<-done
			continue
		}
		file := b.current
		path := ""
		if b.index < len(b.paths) {
			path = b.paths[b.index]
		}
		b.current = nil
		b.mu.Unlock()

		var closeErr error
		if file != nil {
			closeErr = file.Close()
		}

		b.mu.Lock()
		if closeErr != nil {
			b.recordLocalErrorLocked(fmt.Errorf("close attachment %q: %w", path, closeErr))
		}
		err := b.terminalErr
		close(b.closeDone)
		b.mu.Unlock()
		return err
	}
}

func (c *Client) doMultipartHTML(ctx context.Context, method, path string, body io.Reader, contentType string) (*html.Node, error) {
	resp, err := c.doHTMXMutation(ctx, method, path, body, contentType, mutationRedirectAPIError)
	if err != nil {
		return nil, err
	}
	defer drainAndClose(resp.Body)

	root, err := html.Parse(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("decoding %s response: %w", path, err)
	}
	return root, nil
}

func parseAttachmentMutationResponse(root *html.Node, operation, projectID string) ([]Attachment, error) {
	if findByID(root, "attachment-list") == nil {
		return nil, fmt.Errorf("%s response did not include the attachment list", operation)
	}
	return parseTaskAttachmentsForProject(root, "", projectID)
}

func parseTaskAttachmentsForProject(root *html.Node, taskID, projectID string) ([]Attachment, error) {
	if strings.TrimSpace(projectID) == "" {
		return parseTaskAttachments(root, taskID, projectID), nil
	}
	if actual := taskDetailProjectID(root); actual != "" && actual != projectID {
		return nil, fmt.Errorf("task attachment response belongs to project %q, not selected project %q", actual, projectID)
	}

	list := findByID(root, "attachment-list")
	if list == nil {
		return make([]Attachment, 0), nil
	}
	for _, control := range findAll(list, func(e *html.Node) bool {
		return attachmentDeleteURL(attr(e, "hx-delete")) != ""
	}) {
		if actual := attachmentDeleteProjectID(attr(control, "hx-delete")); actual != "" && actual != projectID {
			return nil, fmt.Errorf("attachment response belongs to project %q, not selected project %q", actual, projectID)
		}
	}
	return parseTaskAttachments(root, taskID, projectID), nil
}

func taskDetailProjectID(root *html.Node) string {
	if root == nil {
		return ""
	}
	for _, marker := range []string{"data-task-project-id", "data-project-id"} {
		if n := findNode(root, func(e *html.Node) bool { return strings.TrimSpace(attr(e, marker)) != "" }); n != nil {
			return strings.TrimSpace(attr(n, marker))
		}
	}
	return ""
}

// parseTaskAttachments extracts attachment rows from the current backend
// markup. The web template keeps the stable ID in the button's hx-delete URL,
// the filename in the font-medium paragraph, and the human-readable size in
// the following text-xs paragraph.
func parseTaskAttachments(root *html.Node, taskID, projectID string) []Attachment {
	list := findByID(root, "attachment-list")
	if list == nil {
		return make([]Attachment, 0)
	}

	attachments := make([]Attachment, 0)
	seen := make(map[string]bool)
	controls := findAll(list, func(e *html.Node) bool {
		return attachmentDeleteURL(attr(e, "hx-delete")) != ""
	})
	for _, control := range controls {
		id := attachmentDeleteURL(attr(control, "hx-delete"))
		if id == "" || seen[id] {
			continue
		}
		row := attachmentRow(control, list)
		attachment := parseAttachmentRow(row, id, taskID)
		if attachment.FileName == "" {
			continue
		}
		seen[id] = true
		attachments = append(attachments, attachment)
	}

	// Keep the parser compatible with future/fixture markup that places the
	// stable ID directly on the row instead of on a delete control.
	for _, row := range findAll(list, func(e *html.Node) bool {
		return attr(e, "data-attachment-id") != ""
	}) {
		id := strings.TrimSpace(attr(row, "data-attachment-id"))
		if id == "" || seen[id] {
			continue
		}
		attachment := parseAttachmentRow(row, id, taskID)
		if attachment.FileName == "" {
			continue
		}
		seen[id] = true
		attachments = append(attachments, attachment)
	}
	return attachments
}

func attachmentDeleteURL(raw string) string {
	id, _ := attachmentDeleteTarget(raw)
	return id
}

func attachmentDeleteProjectID(raw string) string {
	_, projectID := attachmentDeleteTarget(raw)
	return projectID
}

func attachmentDeleteTarget(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ""
	}
	const prefix = "/attachments/"
	if !strings.HasPrefix(u.Path, prefix) {
		return "", ""
	}
	rest := strings.TrimPrefix(u.Path, prefix)
	if rest == "" || strings.Contains(rest, "/") {
		return "", ""
	}
	id, err := url.PathUnescape(rest)
	if err != nil {
		return "", ""
	}
	return strings.TrimSpace(id), strings.TrimSpace(u.Query().Get("project_id"))
}

func attachmentRow(control, list *html.Node) *html.Node {
	row := control
	for row != nil && row != list {
		if row.Data == "div" && (findNode(row, func(e *html.Node) bool {
			return strings.Contains(attr(e, "class"), "font-medium") || attr(e, "data-attachment-filename") != ""
		}) != nil) {
			return row
		}
		row = row.Parent
	}
	if control.Parent != nil {
		return control.Parent
	}
	return control
}

func parseAttachmentRow(row *html.Node, id, taskID string) Attachment {
	fileName := firstNonEmptyAttr(row,
		"data-attachment-filename", "data-file-name", "data-filename")
	if fileName == "" {
		if name := findNode(row, func(e *html.Node) bool {
			return e.Data == "p" && strings.Contains(attr(e, "class"), "font-medium")
		}); name != nil {
			fileName = strings.TrimSpace(NodeText(name))
		}
	}
	if fileName == "" {
		if name := findNode(row, func(e *html.Node) bool {
			return e.Data == "p" && !strings.Contains(attr(e, "class"), "text-xs")
		}); name != nil {
			fileName = strings.TrimSpace(NodeText(name))
		}
	}

	fileSize := parseAttachmentSize(firstNonEmptyAttr(row,
		"data-attachment-size", "data-file-size", "data-size"))
	if fileSize == 0 {
		if size := findNode(row, func(e *html.Node) bool {
			return e.Data == "p" && strings.Contains(attr(e, "class"), "text-xs")
		}); size != nil {
			fileSize = parseAttachmentSize(NodeText(size))
		}
	}

	return Attachment{
		ID:        id,
		TaskID:    taskID,
		FileName:  fileName,
		FilePath:  attr(row, "data-file-path"),
		MediaType: firstNonEmptyAttr(row, "data-media-type", "data-content-type"),
		FileSize:  fileSize,
	}
}

func firstNonEmptyAttr(n *html.Node, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(attr(n, name)); value != "" {
			return value
		}
	}
	return ""
}

func parseAttachmentSize(value string) int64 {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return 0
	}
	number := strings.TrimSpace(fields[0])
	parsed, err := strconv.ParseFloat(number, 64)
	if err != nil || parsed < 0 {
		return 0
	}
	unit := "B"
	if len(fields) > 1 {
		unit = strings.ToUpper(strings.TrimSpace(fields[1]))
	}
	multipliers := map[string]float64{
		"B":  1,
		"KB": 1 << 10,
		"MB": 1 << 20,
		"GB": 1 << 30,
		"TB": 1 << 40,
		"PB": 1 << 50,
	}
	multiplier, ok := multipliers[unit]
	if !ok {
		return 0
	}
	return int64(math.Round(parsed * multiplier))
}
