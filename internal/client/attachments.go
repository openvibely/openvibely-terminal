package client

import (
	"bytes"
	"context"
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

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, path := range filePaths {
		if err := appendMultipartFile(writer, path); err != nil {
			_ = writer.Close()
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("closing attachment upload: %w", err)
	}

	path := "/tasks/" + url.PathEscape(taskID) + "/attachments" + query("project_id", projectID)
	root, err := c.doMultipartHTML(ctx, http.MethodPost, path, &body, writer.FormDataContentType())
	if err != nil {
		return nil, err
	}
	attachments, err := parseAttachmentMutationResponse(root, "upload attachments", projectID)
	if err != nil {
		return nil, err
	}
	for i := range attachments {
		attachments[i].TaskID = taskID
	}
	return attachments, nil
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

func appendMultipartFile(writer *multipart.Writer, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("attachment file path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open attachment %q: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("stat attachment %q: %w", path, err)
	}
	if info.IsDir() {
		_ = file.Close()
		return fmt.Errorf("attachment %q is a directory", path)
	}

	part, err := writer.CreateFormFile("files", filepath.Base(path))
	if err == nil {
		_, err = io.Copy(part, file)
	}
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("read attachment %q: %w", path, err)
	}
	if closeErr != nil {
		return fmt.Errorf("close attachment %q: %w", path, closeErr)
	}
	return nil
}

func (c *Client) doMultipartHTML(ctx context.Context, method, path string, body io.Reader, contentType string) (*html.Node, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Accept", "text/html, application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer drainAndClose(resp.Body)

	if isAuthResponse(resp) {
		return nil, newAuthRequiredError(method, path, resp)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, apiError(resp)
	}
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
