package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Event is a single server-sent event from GET /events/live.
// The backend multiplexes task, chat, and file-change events on this stream;
// each event's JSON payload carries a "type" discriminator.
type Event struct {
	// Name is the SSE "event:" field (e.g. "task_status_changed",
	// "chat_new_message"); empty for unnamed events.
	Name string
	// Data is the raw JSON payload from the "data:" field.
	Data json.RawMessage
}

// TaskEvent is the payload for task-scoped events (mirrors events.TaskEvent).
type TaskEvent struct {
	Type      string `json:"type"`
	TaskID    string `json:"task_id"`
	TaskName  string `json:"task_name,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	Status    string `json:"status,omitempty"`
	Category  string `json:"category,omitempty"`
	Message   string `json:"message,omitempty"`
}

// ChatEvent is the payload for chat-scoped events (mirrors events.ChatEvent).
type ChatEvent struct {
	Type            string `json:"type"`
	ProjectID       string `json:"project_id"`
	ExecID          string `json:"exec_id"`
	TaskID          string `json:"task_id,omitempty"`
	Message         string `json:"message,omitempty"`
	Source          string `json:"source,omitempty"`
	AgentName       string `json:"agent_name,omitempty"`
	CompletedOutput string `json:"completed_output,omitempty"`
	Queued          bool   `json:"queued,omitempty"`
}

// StreamEvents connects to /events/live and delivers parsed SSE events on the
// returned channel until ctx is cancelled or the connection drops. The channel
// is closed when the stream ends; the returned error channel receives at most
// one terminal error (nil on clean shutdown via ctx).
//
// projectID optionally filters task/chat events server-side.
func (c *Client) StreamEvents(ctx context.Context, projectID string) (<-chan Event, <-chan error) {
	events := make(chan Event, 32)
	errCh := make(chan error, 1)

	go func() {
		defer close(events)
		defer close(errCh)
		sendErr := func(err error) {
			if err == nil {
				return
			}
			select {
			case errCh <- err:
			case <-ctx.Done():
			}
		}

		endpoint := c.baseURL + "/events/live"
		if projectID != "" {
			endpoint += "?project_id=" + url.QueryEscape(projectID)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			sendErr(err)
			return
		}
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Cache-Control", "no-cache")

		// SSE is long-lived: use a dedicated client without the default timeout
		// but reuse the cookie jar for authenticated sessions.
		sseClient := &http.Client{
			Jar: c.http.Jar,
			Transport: &http.Transport{
				ResponseHeaderTimeout: 15 * time.Second,
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		resp, err := sseClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			sendErr(fmt.Errorf("connecting to event stream: %w", err))
			return
		}
		defer resp.Body.Close()

		if isReadAuthResponse(resp) {
			sendErr(newAuthRequiredError(http.MethodGet, "/events/live", resp))
			return
		}
		if resp.StatusCode != http.StatusOK {
			sendErr(fmt.Errorf("event stream returned status %d", resp.StatusCode))
			return
		}
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		var eventName string
		var dataLines []string
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				if len(dataLines) > 0 {
					event := Event{
						Name: eventName,
						Data: json.RawMessage(strings.Join(dataLines, "\n")),
					}
					select {
					case events <- event:
					case <-ctx.Done():
						return
					}
				}
				eventName = ""
				dataLines = nil
			case strings.HasPrefix(line, "event:"):
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			case strings.HasPrefix(line, ":"):
				// comment / keep-alive ping
			}
		}
		if ctx.Err() != nil {
			return
		}
		if err := scanner.Err(); err != nil {
			sendErr(fmt.Errorf("event stream read: %w", err))
			return
		}
		sendErr(ErrEventStreamClosed)
	}()

	return events, errCh
}
