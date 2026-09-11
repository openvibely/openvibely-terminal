package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	Type           string `json:"type"`
	TaskID         string `json:"task_id"`
	TaskName       string `json:"task_name,omitempty"`
	ProjectID      string `json:"project_id,omitempty"`
	ExecID         string `json:"exec_id,omitempty"`
	PendingInputID string `json:"pending_input_id,omitempty"`
	Status         string `json:"status,omitempty"`
	Category       string `json:"category,omitempty"`
	Message        string `json:"message,omitempty"`
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

// ChatOutputEvent is one frame from GET /events/chat/:exec_id. Unnamed
// events carry an output chunk; named "done" and "error" events are terminal
// signals whose Data contains the status or error text.
type ChatOutputEvent struct {
	Name string
	Data string
}

type rawSSEFrame struct {
	eventName string
	dataLines []string
}

func scanSSEFrames(r io.Reader, handle func(rawSSEFrame) bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var frame rawSSEFrame
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if len(frame.dataLines) > 0 && !handle(frame) {
				return nil
			}
			frame = rawSSEFrame{}
		case strings.HasPrefix(line, "event:"):
			frame.eventName = strings.TrimPrefix(line, "event:")
		case strings.HasPrefix(line, "data:"):
			frame.dataLines = append(frame.dataLines, strings.TrimPrefix(line, "data:"))
		case strings.HasPrefix(line, ":"):
			// Comment / keep-alive ping.
		}
	}
	return scanner.Err()
}

const (
	ExecutionDelta = "delta"
	ExecutionDone  = "done"
	ExecutionError = "error"
)

// ExecutionEvent is one incremental or terminal frame from an execution stream.
// Offset is the cumulative UTF-8 byte offset after Data for delta events.
type ExecutionEvent struct {
	Type   string `json:"type"`
	Data   string `json:"data,omitempty"`
	Offset int    `json:"offset"`
}

// StreamChatOutput connects to the backend's execution-specific output stream.
// offset is the number of UTF-8 bytes already received and lets reconnects ask
// the backend to replay only missing durable output.
func (c *Client) StreamChatOutput(ctx context.Context, execID string, offset int) (<-chan ChatOutputEvent, <-chan error) {
	events := make(chan ChatOutputEvent, 32)
	errCh := make(chan error, 1)

	go func() {
		defer close(events)
		defer close(errCh)
		req, err := c.newRequest(ctx, http.MethodGet, "/events/chat/"+url.PathEscape(execID)+"?offset="+fmt.Sprint(offset), nil)
		if err != nil {
			errCh <- err
			return
		}
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Cache-Control", "no-cache")
		transport := &http.Transport{ResponseHeaderTimeout: 15 * time.Second}
		defer transport.CloseIdleConnections()
		streamClient := &http.Client{
			Jar:       c.http.Jar,
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		resp, err := streamClient.Do(req)
		if err != nil {
			if ctx.Err() == nil {
				errCh <- fmt.Errorf("connecting to chat output stream: %w", err)
			}
			return
		}
		defer resp.Body.Close()
		if isReadAuthResponse(resp) {
			errCh <- newAuthRequiredError(http.MethodGet, "/events/chat/:exec_id", resp)
			return
		}
		if resp.StatusCode != http.StatusOK {
			errCh <- fmt.Errorf("chat output stream returned status %d", resp.StatusCode)
			return
		}

		err = scanSSEFrames(resp.Body, func(frame rawSSEFrame) bool {
			dataLines := make([]string, len(frame.dataLines))
			for i, line := range frame.dataLines {
				dataLines[i] = strings.TrimPrefix(line, " ")
			}
			event := ChatOutputEvent{
				Name: strings.TrimSpace(frame.eventName),
				Data: strings.Join(dataLines, "\n"),
			}
			select {
			case events <- event:
				return true
			case <-ctx.Done():
				return false
			}
		})
		if ctx.Err() == nil && err != nil {
			errCh <- fmt.Errorf("chat output stream read: %w", err)
		}
	}()

	return events, errCh
}

// StreamExecution adapts the shared chat-output transport for headless callers,
// adding cumulative UTF-8 byte offsets and explicit clean-disconnect errors.
func (c *Client) StreamExecution(ctx context.Context, execID string, offset int) (<-chan ExecutionEvent, <-chan error) {
	outputEvents, outputErrs := c.StreamChatOutput(ctx, execID, offset)
	events := make(chan ExecutionEvent, 32)
	errCh := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errCh)
		currentOffset := offset
		terminal := false
		for outputEvents != nil || outputErrs != nil {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-outputEvents:
				if !ok {
					outputEvents = nil
					continue
				}
				typeName := event.Name
				if typeName == "" {
					typeName = ExecutionDelta
					currentOffset += len([]byte(event.Data))
				}
				if typeName == ExecutionDone || typeName == ExecutionError {
					terminal = true
				}
				select {
				case events <- ExecutionEvent{Type: typeName, Data: event.Data, Offset: currentOffset}:
				case <-ctx.Done():
					return
				}
			case err, ok := <-outputErrs:
				if !ok {
					outputErrs = nil
					continue
				}
				if err != nil {
					select {
					case errCh <- err:
					case <-ctx.Done():
					}
					return
				}
			}
		}
		if !terminal && ctx.Err() == nil {
			errCh <- ErrEventStreamClosed
		}
	}()
	return events, errCh
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

		endpoint := "/events/live"
		if projectID != "" {
			endpoint += "?project_id=" + url.QueryEscape(projectID)
		}
		req, err := c.newRequest(ctx, http.MethodGet, endpoint, nil)
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
		err = scanSSEFrames(resp.Body, func(frame rawSSEFrame) bool {
			dataLines := make([]string, len(frame.dataLines))
			for i, line := range frame.dataLines {
				dataLines[i] = strings.TrimSpace(line)
			}
			event := Event{
				Name: strings.TrimSpace(frame.eventName),
				Data: json.RawMessage(strings.Join(dataLines, "\n")),
			}
			select {
			case events <- event:
				return true
			case <-ctx.Done():
				return false
			}
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			sendErr(fmt.Errorf("event stream read: %w", err))
			return
		}
		sendErr(ErrEventStreamClosed)
	}()

	return events, errCh
}
