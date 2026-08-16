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
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

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
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	defer drainAndClose(resp.Body)

	// Success redirects to the next path ("/"); failure redirects back to /login.
	if resp.StatusCode == http.StatusFound {
		loc := resp.Header.Get("Location")
		if strings.HasPrefix(loc, "/login") {
			return fmt.Errorf("login failed: invalid credentials")
		}
		return nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return nil
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

	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("GET %s: unauthorized (server auth enabled; provide credentials)", path)
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
