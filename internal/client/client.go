// Package client provides a small REST + SSE client for the OpenVibely
// backend server. It mirrors the JSON API surface exposed by
// internal/handler in the openvibely repo:
//
//   - GET  /api/projects               → project listing
//   - POST /api/chat/message           → async chat send (form encoded)
//   - GET  /api/chat/message/:id       → chat status polling
//   - GET  /api/capacity/global        → worker pool capacity
//   - GET  /api/capacity/projects      → per-project capacity
//   - GET  /auth/me                    → session check
//   - POST /login                      → cookie session login (optional auth)
//   - GET  /events/live                → SSE stream (task/chat/file events)
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// ErrAuthRequired identifies a reachable backend that needs a cookie-session
// login before the requested resource can be used.
var ErrAuthRequired = errors.New("authentication required")

// AuthRequiredError is returned when the backend responds with an
// authentication redirect or HTTP 401. It deliberately contains only the
// request location and status, never request credentials or response bodies.
type AuthRequiredError struct {
	Method     string
	Path       string
	StatusCode int
}

func (e *AuthRequiredError) Error() string {
	return fmt.Sprintf("%s %s: unauthorized (authentication required)", e.Method, e.Path)
}

func (e *AuthRequiredError) Unwrap() error { return ErrAuthRequired }

// IsAuthRequired reports whether err, including a wrapped error, means the
// server was reachable but the current session is not authorized.
func IsAuthRequired(err error) bool {
	return errors.Is(err, ErrAuthRequired)
}

// LoginTransportError identifies a failure to reach or issue the login
// request. It is separate from invalid credentials so callers can retain
// offline recovery state without exposing the submitted form or response body.
type LoginTransportError struct {
	err error
}

func (e *LoginTransportError) Error() string {
	if e == nil || e.err == nil {
		return "login request failed"
	}
	return fmt.Sprintf("login request: %v", e.err)
}

func (e *LoginTransportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// IsLoginTransportError reports whether err, including a wrapped error, means
// the login request could not be issued or completed because of transport.
func IsLoginTransportError(err error) bool {
	var transportErr *LoginTransportError
	return errors.As(err, &transportErr)
}

// IsTransportError reports whether an error came from request construction,
// dialing, timeout, or another network transport rather than an HTTP response.
// HTTP response errors are deliberately not included so callers can distinguish
// offline state from server-side failures and authentication responses.
func IsTransportError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

func newAuthRequiredError(method, path string, resp *http.Response) error {
	return &AuthRequiredError{
		Method:     method,
		Path:       path,
		StatusCode: resp.StatusCode,
	}
}

// isLoginRedirect reports whether a redirect's parsed path is exactly the
// backend login route. Query strings and absolute URLs are allowed, while
// similarly named paths such as /login-help are ordinary redirects.
func isLoginRedirect(resp *http.Response) bool {
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return false
	}
	location := strings.TrimSpace(resp.Header.Get("Location"))
	if location == "" {
		return false
	}
	u, err := url.Parse(location)
	return err == nil && u.Path == "/login"
}

// isAuthResponse recognizes a 401 or a redirect explicitly targeting the
// backend login page. Non-login redirects remain valid for mutation routes
// that intentionally use them.
func isAuthResponse(resp *http.Response) bool {
	return resp.StatusCode == http.StatusUnauthorized || isLoginRedirect(resp)
}

// isReadAuthResponse also preserves the existing read-route behavior where a
// bare 302 is the backend's authentication redirect.
func isReadAuthResponse(resp *http.Response) bool {
	return isAuthResponse(resp) || resp.StatusCode == http.StatusFound
}

// Client talks to an OpenVibely backend over HTTP.
type Client struct {
	baseURL string
	http    *http.Client
}

// New creates a Client for the given base URL (e.g. "http://localhost:3001").
// A cookie jar is installed so an authenticated session survives across calls.
func New(baseURL string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("server URL is required")
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("creating cookie jar: %w", err)
	}
	return &Client{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: 30 * time.Second,
			Jar:     jar,
			// The backend answers /login with 302 redirects for both success
			// and failure; keep the response so we can inspect Location.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// BaseURL returns the normalized server base URL.
func (c *Client) BaseURL() string { return c.baseURL }

// --- Data models (mirror handler response structs) ---

// Project mirrors handler.ProjectResponse.
type Project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	CreatedAt string `json:"created_at"`
}

type projectsListResponse struct {
	Projects []Project `json:"projects"`
}

// GlobalCapacity mirrors handler.GlobalCapacityResponse.
type GlobalCapacity struct {
	MaxWorkers     int  `json:"max_workers"`
	TotalRunning   int  `json:"total_running"`
	QueueSize      int  `json:"queue_size"`
	HasCapacity    bool `json:"has_capacity"`
	AvailableSlots int  `json:"available_slots"`
}

// ProjectCapacity mirrors handler.ProjectCapacityResponse.
type ProjectCapacity struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Running     int    `json:"running"`
	QueueSize   int    `json:"queue_size"`
	MaxWorkers  *int   `json:"max_workers"`
	HasCapacity bool   `json:"has_capacity"`
}

// ChatAccepted mirrors handler.ChatMessageAcceptedResponse.
type ChatAccepted struct {
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
	StatusURL string `json:"status_url"`
	Queued    bool   `json:"queued,omitempty"`
}

// ChatStatus mirrors handler.ChatMessageStatusResponse.
type ChatStatus struct {
	MessageID  string   `json:"message_id"`
	Status     string   `json:"status"`
	Response   string   `json:"response,omitempty"`
	Error      string   `json:"error,omitempty"`
	TaskIDs    []string `json:"task_ids,omitempty"`
	TokensUsed int      `json:"tokens_used,omitempty"`
	DurationMs int64    `json:"duration_ms,omitempty"`
}

// AuthStatus mirrors the /auth/me response.
type AuthStatus struct {
	Authenticated bool   `json:"authenticated"`
	Username      string `json:"username,omitempty"`
	Display       string `json:"display,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// --- API methods ---

// Login authenticates against the backend's cookie-session login form.
// It is a no-op requirement only when the server has AUTH_ENABLED set;
// on servers without auth the session cookie is simply never needed.
func (c *Client) Login(ctx context.Context, username, password string) error {
	form := url.Values{}
	form.Set("username", username)
	form.Set("password", password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		return &LoginTransportError{err: err}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return &LoginTransportError{err: err}
	}
	defer drainAndClose(resp.Body)

	// Success redirects to the next path ("/"); failure redirects back to /login.
	// Keep the failure text deliberately generic so credentials can never appear
	// in a login error, even if a server returns an unexpected response body.
	if isAuthResponse(resp) {
		return fmt.Errorf("login failed: invalid credentials")
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		location := strings.TrimSpace(resp.Header.Get("Location"))
		if u, err := url.Parse(location); err == nil && u.Path == "/" {
			return nil
		}
	}
	return fmt.Errorf("login failed: unexpected status %d", resp.StatusCode)
}

// AuthMe returns the current session state.
func (c *Client) AuthMe(ctx context.Context) (*AuthStatus, error) {
	var out AuthStatus
	if err := c.getJSON(ctx, "/auth/me", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListProjects fetches all projects.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	var out projectsListResponse
	if err := c.getJSON(ctx, "/api/projects", &out); err != nil {
		return nil, err
	}
	return out.Projects, nil
}

// CreateProject creates a local-path project through the backend's public
// project form route. The HTMX redirect contains the backend-assigned project
// ID, so project state remains owned by the backend rather than this client.
func (c *Client) CreateProject(ctx context.Context, name, path string) (*Project, error) {
	name = strings.TrimSpace(name)
	path = strings.TrimSpace(path)
	if name == "" {
		return nil, fmt.Errorf("project name is required")
	}
	if path == "" {
		return nil, fmt.Errorf("project path is required")
	}

	form := url.Values{}
	form.Set("name", name)
	form.Set("repo_source", "local")
	form.Set("repo_path", path)

	resp, err := c.doFormResponse(ctx, http.MethodPost, "/projects", form)
	if err != nil {
		return nil, err
	}
	defer drainAndClose(resp.Body)

	redirect := resp.Header.Get("HX-Redirect")
	if redirect == "" {
		if message := projectToastMessage(resp.Header.Get("HX-Trigger")); message != "" {
			return nil, fmt.Errorf("create project: %s", message)
		}
		// The backend's HTMX response uses HX-Redirect. Accept Location too so
		// the client remains compatible with the same form route without HTMX.
		redirect = resp.Header.Get("Location")
	}
	projectID, err := projectIDFromRedirect(redirect)
	if err != nil {
		return nil, err
	}
	return &Project{ID: projectID, Name: name, Path: path}, nil
}

func projectIDFromRedirect(redirect string) (string, error) {
	if strings.TrimSpace(redirect) == "" {
		return "", fmt.Errorf("create project: backend response did not include a project ID")
	}
	u, err := url.Parse(redirect)
	if err != nil {
		return "", fmt.Errorf("create project: invalid backend redirect: %w", err)
	}
	projectID := strings.TrimSpace(u.Query().Get("project_id"))
	if projectID == "" {
		return "", fmt.Errorf("create project: backend response did not include a project ID")
	}
	return projectID, nil
}

func projectToastMessage(trigger string) string {
	var payload struct {
		Toast struct {
			Message string `json:"message"`
		} `json:"openvibelyToast"`
	}
	if json.Unmarshal([]byte(trigger), &payload) != nil {
		return ""
	}
	return strings.TrimSpace(payload.Toast.Message)
}

// GetGlobalCapacity fetches worker pool capacity; also used as a health check.
func (c *Client) GetGlobalCapacity(ctx context.Context) (*GlobalCapacity, error) {
	var out GlobalCapacity
	if err := c.getJSON(ctx, "/api/capacity/global", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetProjectCapacities fetches per-project worker capacity.
func (c *Client) GetProjectCapacities(ctx context.Context) ([]ProjectCapacity, error) {
	var out []ProjectCapacity
	if err := c.getJSON(ctx, "/api/capacity/projects", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SendChatMessage submits a chat message for async processing and returns
// the accepted message ID for polling via GetChatStatus.
func (c *Client) SendChatMessage(ctx context.Context, projectID, message string) (*ChatAccepted, error) {
	form := url.Values{}
	form.Set("message", message)
	form.Set("project_id", projectID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat/message", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send chat message: %w", err)
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode != http.StatusCreated {
		if isAuthResponse(resp) {
			return nil, newAuthRequiredError(http.MethodPost, "/api/chat/message", resp)
		}
		return nil, apiError(resp)
	}
	var out ChatAccepted
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding chat response: %w", err)
	}
	return &out, nil
}

// GetChatStatus polls the status of an async chat message.
func (c *Client) GetChatStatus(ctx context.Context, messageID string) (*ChatStatus, error) {
	var out ChatStatus
	if err := c.getJSON(ctx, "/api/chat/message/"+url.PathEscape(messageID), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- helpers ---

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer drainAndClose(resp.Body)

	if isReadAuthResponse(resp) {
		return newAuthRequiredError(http.MethodGet, path, resp)
	}
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding %s response: %w", path, err)
	}
	return nil
}

func apiError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var er errorResponse
	if json.Unmarshal(body, &er) == nil && er.Error != "" {
		return fmt.Errorf("server error (%d): %s", resp.StatusCode, er.Error)
	}
	return fmt.Errorf("server error (%d)", resp.StatusCode)
}

func drainAndClose(rc io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, 64<<10))
	_ = rc.Close()
}
