package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
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

type sseDataLineMode uint8

const (
	sseDataLineChatOutput sseDataLineMode = iota
	sseDataLineLiveEvent
)

type rawSSEFrame struct {
	eventName     string
	payload       []byte
	dataLineCount int
}

type sseStreamConfig struct {
	endpoint           string
	authPath           string
	connectErrorPrefix string
	statusErrorPrefix  string
}

type sseStreamBody struct {
	io.ReadCloser
	transport *http.Transport
}

func (b *sseStreamBody) Close() error {
	err := b.ReadCloser.Close()
	b.transport.CloseIdleConnections()
	return err
}

func (c *Client) openSSEStream(ctx context.Context, cfg sseStreamConfig) (*http.Response, error) {
	req, err := c.newRequest(ctx, http.MethodGet, cfg.endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	transport := &http.Transport{ResponseHeaderTimeout: 15 * time.Second}
	streamClient := &http.Client{
		Jar:       c.http.Jar,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := streamClient.Do(req)
	if err != nil {
		transport.CloseIdleConnections()
		if ctx.Err() != nil {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", cfg.connectErrorPrefix, err)
	}
	if isReadAuthResponse(resp) {
		resp.Body.Close()
		transport.CloseIdleConnections()
		return nil, newAuthRequiredError(http.MethodGet, cfg.authPath, resp)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("%s %d", cfg.statusErrorPrefix, resp.StatusCode)
	}
	resp.Body = &sseStreamBody{ReadCloser: resp.Body, transport: transport}
	return resp, nil
}

func scanSSEFrames(r io.Reader, dataLineMode sseDataLineMode, handle func(rawSSEFrame) bool) error {
	reader := bufio.NewReaderSize(r, 64*1024)
	var frame rawSSEFrame
	var lineBuffer []byte

	for {
		line, err := readSSELine(reader, &lineBuffer)
		if len(line) > 0 {
			if bytes.HasSuffix(line, []byte{'\n'}) {
				line = line[:len(line)-1]
				line = bytes.TrimSuffix(line, []byte{'\r'})
			}
			switch {
			case len(line) == 0:
				if frame.dataLineCount > 0 && !handle(frame) {
					return nil
				}
				frame = rawSSEFrame{}
			case bytes.HasPrefix(line, []byte("event:")):
				frame.eventName = string(line[len("event:"):])
			case bytes.HasPrefix(line, []byte("data:")):
				data := line[len("data:"):]
				switch dataLineMode {
				case sseDataLineChatOutput:
					if len(data) > 0 && data[0] == ' ' {
						data = data[1:]
					}
				case sseDataLineLiveEvent:
					data = bytes.TrimSpace(data)
				}
				if frame.dataLineCount == 0 {
					if len(data) > 0 && len(data) <= 128 {
						// Short multiline frames commonly split into similarly sized lines.
						// Reserve for the next line and separator to avoid growing twice.
						frame.payload = make([]byte, 0, 2*len(data)+1)
					}
					if len(lineBuffer) > 0 {
						// A long line already owns its storage; keep the payload slice
						// instead of copying the full line into a second allocation.
						frame.payload = data
					} else {
						frame.payload = append(frame.payload, data...)
					}
				} else {
					extra := len(data) + 1
					if extra > cap(frame.payload)-len(frame.payload) {
						frame.payload = slices.Grow(frame.payload, extra)
					}
					frame.payload = append(frame.payload, '\n')
					frame.payload = append(frame.payload, data...)
				}
				frame.dataLineCount++
			case len(line) > 0 && line[0] == ':':
				// Comment / keep-alive ping.
			}
			// A long line's buffer is either retained by the frame or no longer
			// needed, so allocate fresh scratch storage for the next line.
			if len(lineBuffer) > 0 {
				lineBuffer = nil
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// readSSELine joins ReadSlice fragments without imposing a token-size limit.
// The returned bytes are only valid until the next call when no scratch buffer
// was needed; callers process each line before reading again.
func readSSELine(reader *bufio.Reader, scratch *[]byte) ([]byte, error) {
	*scratch = (*scratch)[:0]
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(*scratch) == 0 && err != bufio.ErrBufferFull {
			return fragment, err
		}
		*scratch = append(*scratch, fragment...)
		if err != bufio.ErrBufferFull {
			return *scratch, err
		}
	}
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
		resp, err := c.openSSEStream(ctx, sseStreamConfig{
			endpoint:           "/events/chat/" + url.PathEscape(execID) + "?offset=" + fmt.Sprint(offset),
			authPath:           "/events/chat/:exec_id",
			connectErrorPrefix: "connecting to chat output stream",
			statusErrorPrefix:  "chat output stream returned status",
		})
		if err != nil {
			errCh <- err
			return
		}
		if resp == nil {
			return
		}
		defer resp.Body.Close()

		err = scanSSEFrames(resp.Body, sseDataLineChatOutput, func(frame rawSSEFrame) bool {
			event := ChatOutputEvent{
				Name: strings.TrimSpace(frame.eventName),
				Data: string(frame.payload),
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
		resp, err := c.openSSEStream(ctx, sseStreamConfig{
			endpoint:           endpoint,
			authPath:           "/events/live",
			connectErrorPrefix: "connecting to event stream",
			statusErrorPrefix:  "event stream returned status",
		})
		if err != nil {
			sendErr(err)
			return
		}
		if resp == nil {
			return
		}
		defer resp.Body.Close()

		err = scanSSEFrames(resp.Body, sseDataLineLiveEvent, func(frame rawSSEFrame) bool {
			event := Event{
				Name: strings.TrimSpace(frame.eventName),
				Data: json.RawMessage(frame.payload),
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
