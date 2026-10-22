package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	liveSocketPath        = "/events/ws"
	liveSocketSubprotocol = "openvibely-live"
	liveConnectTimeout    = 15 * time.Second
	liveReadLimit         = 16 << 20
	liveFrameBuffer       = 64
)

// Event is one project-scoped event delivered by the backend live WebSocket.
type Event struct {
	Name string
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

// ChatOutputEvent adapts a WebSocket execution frame to the chat stream API.
// Offset is the cumulative UTF-8 byte cursor after Data for output frames.
type ChatOutputEvent struct {
	Name   string
	Data   string
	Offset int
}

const (
	ExecutionDelta = "delta"
	ExecutionReset = "reset"
	ExecutionDone  = "done"
	ExecutionError = "error"
)

// ExecutionEvent is one incremental or terminal frame from an execution stream.
// Offset is the cumulative UTF-8 byte offset after Data for delta and reset events.
type ExecutionEvent struct {
	Type   string `json:"type"`
	Data   string `json:"data,omitempty"`
	Offset int    `json:"offset"`
}

type liveCommand struct {
	Op        string `json:"op"`
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id,omitempty"`
	ExecID    string `json:"exec_id,omitempty"`
	Offset    int    `json:"offset,omitempty"`
}

type liveFrame struct {
	ID     string          `json:"id,omitempty"`
	Event  string          `json:"event"`
	Data   json.RawMessage `json:"data,omitempty"`
	Start  int             `json:"start,omitempty"`
	Offset int             `json:"offset,omitempty"`
}

type liveSubscription struct {
	command       liveCommand
	frames        chan liveFrame
	errs          chan error
	connected     chan struct{}
	connectedOnce sync.Once
	subscribed    bool
	retryDelay    time.Duration
	retryTimer    *time.Timer
}

type liveUpdateKind uint8

const (
	liveUpdateAdd liveUpdateKind = iota
	liveUpdateRemove
	liveUpdateRetry
	liveUpdateOffset
	liveUpdateReconnect
)

type liveUpdate struct {
	kind   liveUpdateKind
	sub    *liveSubscription
	id     string
	offset int
	reset  bool
}

type liveSession struct {
	client    *Client
	ctx       context.Context
	cancel    context.CancelFunc
	updates   chan liveUpdate
	done      chan struct{}
	accepting bool // guarded by client.liveMu
}

type liveReadResult struct {
	frame liveFrame
	err   error
}

func normalizeLiveContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (c *Client) subscribeLive(ctx context.Context, command liveCommand) (*liveSubscription, *liveSession, error) {
	ctx = normalizeLiveContext(ctx)
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	command.ProjectID = strings.TrimSpace(command.ProjectID)
	if command.ProjectID == "" {
		return nil, nil, errors.New("project ID is required for live subscriptions")
	}
	if command.Op == "execution" {
		command.ExecID = strings.TrimSpace(command.ExecID)
		if command.ExecID == "" {
			return nil, nil, errors.New("execution ID is required for live subscriptions")
		}
		if command.Offset < 0 {
			return nil, nil, errors.New("execution offset cannot be negative")
		}
	}

	for {
		c.liveMu.Lock()
		session := c.live
		if session == nil || !session.accepting || session.ctx.Err() != nil {
			sessionCtx, cancel := context.WithCancel(context.Background())
			session = &liveSession{
				client:    c,
				ctx:       sessionCtx,
				cancel:    cancel,
				updates:   make(chan liveUpdate, 256),
				done:      make(chan struct{}),
				accepting: true,
			}
			c.live = session
			go session.run()
		}
		c.liveNextID++
		command.ID = fmt.Sprintf("terminal-%d", c.liveNextID)
		sub := &liveSubscription{
			command:   command,
			frames:    make(chan liveFrame, liveFrameBuffer),
			errs:      make(chan error, 1),
			connected: make(chan struct{}),
		}
		select {
		case session.updates <- liveUpdate{kind: liveUpdateAdd, sub: sub}:
			c.liveMu.Unlock()
			return sub, session, nil
		case <-session.done:
			if c.live == session {
				c.live = nil
			}
			c.liveMu.Unlock()
		default:
			c.liveMu.Unlock()
			return nil, nil, errors.New("live websocket session is busy")
		}
	}
}

func (c *Client) removeLiveSubscription(session *liveSession, id string) {
	if session == nil {
		return
	}
	select {
	case session.updates <- liveUpdate{kind: liveUpdateRemove, id: id}:
	case <-session.done:
	}
}

func (s *liveSession) updateOffset(id string, offset int, reset bool) {
	select {
	case s.updates <- liveUpdate{kind: liveUpdateOffset, id: id, offset: offset, reset: reset}:
	case <-s.done:
	}
}

func (s *liveSession) reconnect() {
	select {
	case s.updates <- liveUpdate{kind: liveUpdateReconnect}:
	case <-s.done:
	}
}

func (s *liveSession) run() {
	subs := make(map[string]*liveSubscription)
	var conn *websocket.Conn
	var readResults chan liveReadResult
	var readCancel context.CancelFunc
	ready := false
	dropLiveConn := func() {
		if readCancel != nil {
			readCancel()
			readCancel = nil
		}
		if conn != nil {
			_ = conn.CloseNow()
			conn = nil
		}
		readResults = nil
		ready = false
	}
	generation := 0
	attempt := 0
	var reconnectDelay time.Duration

	defer func() {
		dropLiveConn()
		for id, sub := range subs {
			delete(subs, id)
			finishLiveSubscription(sub, nil)
		}
		s.cancel()
		s.client.liveMu.Lock()
		if s.client.live == s {
			s.accepting = false
			s.client.live = nil
		}
		close(s.done)
		s.client.liveMu.Unlock()
	}()

	for {
		if s.ctx.Err() != nil {
			return
		}
		if len(subs) == 0 {
			if update, ok := s.stopIfIdle(subs); ok {
				applyLiveUpdate(update, subs, conn, ready, generation)
				continue
			}
			return
		}
		if conn == nil {
			if reconnectDelay > 0 {
				if !s.waitBeforeReconnect(reconnectDelay, subs) {
					return
				}
				reconnectDelay = 0
				if len(subs) == 0 {
					continue
				}
			}
			connectCtx, cancel := context.WithTimeout(s.ctx, liveConnectTimeout)
			opened, _, err := s.client.dialLiveSocket(connectCtx)
			cancel()
			if err != nil {
				if s.ctx.Err() != nil {
					return
				}
				// Like the browser transport, keep active subscriptions through all
				// handshake failures, including authentication and HTTP errors.
				// A malformed local URL cannot become usable by retrying it.
				if IsInvalidServerURL(err) {
					s.failAll(subs, err)
					return
				}
				reconnectDelay = nextLiveReconnectDelay(attempt)
				attempt++
				continue
			}
			conn = opened
			conn.SetReadLimit(liveReadLimit)
			readResults = make(chan liveReadResult, 1)
			readCtx, cancelRead := context.WithCancel(s.ctx)
			readCancel = cancelRead
			go readLiveFrames(readCtx, conn, readResults)
			ready = false
			readyDeadline := time.NewTimer(liveConnectTimeout)
			waitReady := true
			for waitReady {
				select {
				case <-s.ctx.Done():
					readyDeadline.Stop()
					return
				case update := <-s.updates:
					if !applyLiveUpdate(update, subs, conn, ready, generation) {
						dropLiveConn()
						waitReady = false
					} else if len(subs) == 0 {
						dropLiveConn()
						waitReady = false
					}
				case result := <-readResults:
					if result.err != nil {
						dropLiveConn()
						waitReady = false
						break
					}
					if result.frame.Event == "ready" {
						ready = true
						generation++
						attempt = 0
						waitReady = false
						if !s.sendSubscriptions(conn, subs) {
							dropLiveConn()
							reconnectDelay = nextLiveReconnectDelay(attempt)
							attempt++
						}
					}
				case <-readyDeadline.C:
					dropLiveConn()
					waitReady = false
				}
			}
			readyDeadline.Stop()
			if conn == nil {
				if reconnectDelay == 0 {
					reconnectDelay = nextLiveReconnectDelay(attempt)
					attempt++
				}
				continue
			}
		}

		select {
		case <-s.ctx.Done():
			return
		case update := <-s.updates:
			if !applyLiveUpdate(update, subs, conn, ready, generation) {
				dropLiveConn()
				reconnectDelay = nextLiveReconnectDelay(attempt)
				attempt++
			}
		case result := <-readResults:
			if result.err != nil {
				dropLiveConn()
				reconnectDelay = nextLiveReconnectDelay(attempt)
				attempt++
				continue
			}
			if !handleLiveFrame(result.frame, subs, conn, s) {
				dropLiveConn()
				reconnectDelay = nextLiveReconnectDelay(attempt)
				attempt++
			}
		}
	}
}

// stopIfIdle atomically prevents a concurrent subscriber from registering on a
// session that is about to exit. A queued update is returned for the owner loop.
func (s *liveSession) stopIfIdle(subs map[string]*liveSubscription) (liveUpdate, bool) {
	s.client.liveMu.Lock()
	defer s.client.liveMu.Unlock()
	select {
	case update := <-s.updates:
		return update, true
	default:
	}
	if s.client.live == s {
		s.accepting = false
		s.client.live = nil
	}
	return liveUpdate{}, false
}

func (s *liveSession) waitBeforeReconnect(delay time.Duration, subs map[string]*liveSubscription) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return false
		case update := <-s.updates:
			applyLiveUpdate(update, subs, nil, false, 0)
			if len(subs) == 0 {
				return true
			}
		case <-timer.C:
			return true
		}
	}
}

func (s *liveSession) sendSubscriptions(conn *websocket.Conn, subs map[string]*liveSubscription) bool {
	for _, sub := range subs {
		if sub.retryTimer != nil {
			sub.retryTimer.Stop()
			sub.retryTimer = nil
		}
		sub.subscribed = false
		if !writeLiveCommand(s.ctx, conn, sub.command) {
			return false
		}
		markLiveCommandSent(sub)
	}
	return true
}

func markLiveCommandSent(sub *liveSubscription) {
	if sub != nil && sub.command.Op == "execution" {
		sub.subscribed = true
		sub.connectedOnce.Do(func() { close(sub.connected) })
	}
}

func (s *liveSession) failAll(subs map[string]*liveSubscription, err error) {
	// Finish any accepted subscriptions before stopping registration. New
	// registrations fail fast if the bounded update queue is full.
	s.finishQueuedAdds(err)
	s.client.liveMu.Lock()
	s.accepting = false
	if s.client.live == s {
		s.client.live = nil
	}
	s.client.liveMu.Unlock()

	for id, sub := range subs {
		delete(subs, id)
		finishLiveSubscription(sub, err)
	}
	s.finishQueuedAdds(err)
}

func (s *liveSession) finishQueuedAdds(err error) {
	for {
		select {
		case update := <-s.updates:
			if update.kind == liveUpdateAdd && update.sub != nil {
				finishLiveSubscription(update.sub, err)
			}
		default:
			return
		}
	}
}

func applyLiveUpdate(update liveUpdate, subs map[string]*liveSubscription, conn *websocket.Conn, ready bool, generation int) bool {
	switch update.kind {
	case liveUpdateAdd:
		if update.sub == nil {
			return true
		}
		subs[update.sub.command.ID] = update.sub
		if conn != nil && ready {
			if !writeLiveCommand(context.Background(), conn, update.sub.command) {
				return false
			}
			markLiveCommandSent(update.sub)
		}
	case liveUpdateRemove:
		if sub := subs[update.id]; sub != nil {
			if sub.retryTimer != nil {
				sub.retryTimer.Stop()
			}
			if conn != nil && ready {
				_ = writeLiveCommand(context.Background(), conn, liveCommand{Op: "unsubscribe", ID: update.id})
			}
			delete(subs, update.id)
			finishLiveSubscription(sub, nil)
		}
	case liveUpdateRetry:
		if sub := subs[update.id]; sub != nil && conn != nil && ready {
			if sub.retryTimer != nil {
				sub.retryTimer.Stop()
				sub.retryTimer = nil
			}
			if !writeLiveCommand(context.Background(), conn, sub.command) {
				return false
			}
			markLiveCommandSent(sub)
		}
	case liveUpdateOffset:
		if sub := subs[update.id]; sub != nil && (update.reset || update.offset > sub.command.Offset) {
			sub.command.Offset = update.offset
		}
	case liveUpdateReconnect:
		return false
	}
	return true
}

func (s *liveSession) scheduleSubscriptionRetry(sub *liveSubscription) {
	if sub.retryTimer != nil {
		sub.retryTimer.Stop()
	}
	if sub.retryDelay == 0 {
		sub.retryDelay = 500 * time.Millisecond
	} else {
		sub.retryDelay *= 2
	}
	if sub.retryDelay > 15*time.Second {
		sub.retryDelay = 15 * time.Second
	}
	delay := sub.retryDelay + time.Duration(rand.Int63n(int64(250*time.Millisecond)+1))
	sub.retryTimer = time.AfterFunc(delay, func() {
		select {
		case s.updates <- liveUpdate{kind: liveUpdateRetry, id: sub.command.ID}:
		case <-s.done:
		}
	})
}

func handleLiveFrame(frame liveFrame, subs map[string]*liveSubscription, conn *websocket.Conn, session *liveSession) bool {
	if frame.Event == "ready" || frame.Event == "system_update" {
		return true
	}
	if frame.Event == "reconcile" {
		for _, sub := range subs {
			if sub.command.Op == "live" && sub.subscribed {
				sendLiveReconcile(sub)
			}
		}
		return true
	}
	sub := subs[frame.ID]
	if sub == nil {
		return true
	}
	switch frame.Event {
	case "subscribed":
		sub.subscribed = true
		sub.connectedOnce.Do(func() { close(sub.connected) })
		if sub.command.Op == "live" {
			// Broadcaster events are not replayed. Refresh authoritative state
			// after subscription acceptance, including every reconnect.
			sendLiveReconcile(sub)
		}
		return true
	case "retry":
		session.scheduleSubscriptionRetry(sub)
		return true
	case "error":
		if !sub.subscribed {
			finishOneLiveSubscription(subs, sub, fmt.Errorf("live websocket subscription failed: %s", liveFrameDataString(frame.Data)))
			return true
		}
	}

	select {
	case sub.frames <- frame:
	default:
		finishOneLiveSubscription(subs, sub, errors.New("live websocket subscriber is not keeping up"))
		if conn != nil {
			_ = writeLiveCommand(context.Background(), conn, liveCommand{Op: "unsubscribe", ID: frame.ID})
		}
		return true
	}
	if sub.command.Op == "execution" && (frame.Event == "done" || frame.Event == "error") {
		finishOneLiveSubscription(subs, sub, nil)
	}
	return true
}

// sendLiveReconcile drops best-effort refresh hints when the queue is full;
// the next periodic signal retries the refresh without delaying project events.
func sendLiveReconcile(sub *liveSubscription) {
	payload, _ := json.Marshal(struct {
		ProjectID string `json:"project_id"`
	}{ProjectID: sub.command.ProjectID})
	select {
	case sub.frames <- liveFrame{ID: sub.command.ID, Event: "reconcile", Data: payload}:
	default:
	}
}

func finishOneLiveSubscription(subs map[string]*liveSubscription, sub *liveSubscription, err error) {
	if sub == nil {
		return
	}
	if sub.retryTimer != nil {
		sub.retryTimer.Stop()
	}
	delete(subs, sub.command.ID)
	finishLiveSubscription(sub, err)
}

func finishLiveSubscription(sub *liveSubscription, err error) {
	if sub == nil {
		return
	}
	if err != nil {
		select {
		case sub.errs <- err:
		default:
		}
	} else {
		sub.connectedOnce.Do(func() { close(sub.connected) })
	}
	close(sub.frames)
}

func readLiveFrames(ctx context.Context, conn *websocket.Conn, results chan<- liveReadResult) {
	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			select {
			case results <- liveReadResult{err: err}:
			case <-ctx.Done():
			}
			return
		}
		var frame liveFrame
		if err := json.Unmarshal(payload, &frame); err != nil {
			select {
			case results <- liveReadResult{err: fmt.Errorf("decode live websocket frame: %w", err)}:
			case <-ctx.Done():
			}
			return
		}
		select {
		case results <- liveReadResult{frame: frame}:
		case <-ctx.Done():
			return
		}
	}
}

func writeLiveCommand(ctx context.Context, conn *websocket.Conn, command liveCommand) bool {
	if conn == nil {
		return false
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return false
	}
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, payload) == nil
}

func (c *Client) dialLiveSocket(ctx context.Context) (*websocket.Conn, *http.Response, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, nil, &InvalidServerURLError{}
	}
	wsURL := *base
	if wsURL.Scheme == "https" {
		wsURL.Scheme = "wss"
	} else {
		wsURL.Scheme = "ws"
	}
	wsURL.Path = strings.TrimRight(wsURL.Path, "/") + liveSocketPath
	wsURL.RawPath = ""
	wsURL.RawQuery = ""
	wsURL.Fragment = ""

	origin := base.Scheme + "://" + base.Host
	httpClient := &http.Client{
		Transport: c.http.Transport,
		Jar:       c.http.Jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	conn, resp, err := websocket.Dial(ctx, wsURL.String(), &websocket.DialOptions{
		HTTPClient:   httpClient,
		HTTPHeader:   http.Header{"Origin": []string{origin}},
		Subprotocols: []string{liveSocketSubprotocol},
	})
	if err != nil {
		if resp != nil && isReadAuthResponse(resp) {
			return nil, resp, newAuthRequiredError(http.MethodGet, liveSocketPath, resp)
		}
		if resp != nil {
			return nil, resp, fmt.Errorf("live websocket returned status %d", resp.StatusCode)
		}
		return nil, nil, fmt.Errorf("connecting to live websocket: %w", err)
	}
	return conn, resp, nil
}

func nextLiveReconnectDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := 250 * time.Millisecond
	for i := 0; i < attempt && delay < 15*time.Second; i++ {
		delay *= 2
	}
	if delay > 15*time.Second {
		delay = 15 * time.Second
	}
	return delay + time.Duration(rand.Int63n(int64(250*time.Millisecond)+1))
}

func liveFrameDataString(data json.RawMessage) string {
	if len(data) == 0 || string(data) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(data, &text) == nil {
		return text
	}
	return string(data)
}

func (c *Client) StreamEvents(ctx context.Context, projectID string) (<-chan Event, <-chan error) {
	ctx = normalizeLiveContext(ctx)
	events := make(chan Event, 32)
	errs := make(chan error, 1)
	sub, session, err := c.subscribeLive(ctx, liveCommand{Op: "live", ProjectID: projectID})
	if err != nil {
		errs <- err
		close(events)
		close(errs)
		return events, errs
	}
	go func() {
		defer close(events)
		defer close(errs)
		defer c.removeLiveSubscription(session, sub.command.ID)
		for {
			select {
			case <-ctx.Done():
				return
			case frame, ok := <-sub.frames:
				if !ok {
					select {
					case err, ok := <-sub.errs:
						if ok && err != nil {
							errs <- err
						}
					default:
					}
					return
				}
				event := Event{Name: frame.Event, Data: append(json.RawMessage(nil), frame.Data...)}
				select {
				case events <- event:
				case <-ctx.Done():
					return
				}
			case err, ok := <-sub.errs:
				if ok && err != nil {
					errs <- err
				}
				return
			}
		}
	}()
	return events, errs
}

// StreamChatOutput retains the legacy method shape. New callers should use
// StreamChatOutputForProject so the backend can authorize the subscription.
func (c *Client) StreamChatOutput(ctx context.Context, execID string, offset int) (<-chan ChatOutputEvent, <-chan error) {
	events, errs, _ := c.StreamChatOutputWithConnect(ctx, execID, offset)
	return events, errs
}

// StreamChatOutputForProject connects to one execution on the shared live socket.
func (c *Client) StreamChatOutputForProject(ctx context.Context, projectID, execID string, offset int) (<-chan ChatOutputEvent, <-chan error) {
	events, errs, _ := c.StreamChatOutputWithConnectForProject(ctx, projectID, execID, offset)
	return events, errs
}

// StreamChatOutputWithConnect retains the legacy method shape. New callers
// should use StreamChatOutputWithConnectForProject.
func (c *Client) StreamChatOutputWithConnect(ctx context.Context, execID string, offset int) (<-chan ChatOutputEvent, <-chan error, <-chan struct{}) {
	return c.StreamChatOutputWithConnectForProject(ctx, "", execID, offset)
}

// StreamChatOutputWithConnectForProject adds a channel closed after the backend
// acknowledges the execution subscription.
func (c *Client) StreamChatOutputWithConnectForProject(ctx context.Context, projectID, execID string, offset int) (<-chan ChatOutputEvent, <-chan error, <-chan struct{}) {
	ctx = normalizeLiveContext(ctx)
	events := make(chan ChatOutputEvent, 32)
	errs := make(chan error, 1)
	connected := make(chan struct{})
	sub, session, err := c.subscribeLive(ctx, liveCommand{Op: "execution", ProjectID: projectID, ExecID: execID, Offset: offset})
	if err != nil {
		errs <- err
		close(events)
		close(errs)
		return events, errs, connected
	}
	go func() {
		defer close(events)
		defer close(errs)
		defer c.removeLiveSubscription(session, sub.command.ID)
		var connectedOnce sync.Once
		signalConnected := func() { connectedOnce.Do(func() { close(connected) }) }
		currentOffset := offset
		connectedSource := (<-chan struct{})(sub.connected)
		for {
			select {
			case <-ctx.Done():
				return
			case <-connectedSource:
				signalConnected()
				connectedSource = nil
			case frame, ok := <-sub.frames:
				if !ok {
					select {
					case err, ok := <-sub.errs:
						if ok && err != nil {
							errs <- err
						}
					default:
					}
					return
				}
				if frame.Event == "message" {
					if frame.Offset <= currentOffset {
						continue
					}
					start := frame.Start
					if start > currentOffset {
						session.reconnect()
						continue
					}
					data := liveFrameDataString(frame.Data)
					trim := currentOffset - start
					if trim > 0 {
						if trim >= len(data) {
							continue
						}
						data = data[trim:]
					}
					event := ChatOutputEvent{Data: data, Offset: frame.Offset}
					select {
					case events <- event:
						currentOffset = frame.Offset
						session.updateOffset(sub.command.ID, currentOffset, false)
					case <-ctx.Done():
						return
					}
					continue
				}
				if frame.Event == "reset" {
					data := liveFrameDataString(frame.Data)
					event := ChatOutputEvent{Name: "reset", Data: data, Offset: frame.Offset}
					select {
					case events <- event:
						currentOffset = frame.Offset
						session.updateOffset(sub.command.ID, currentOffset, true)
					case <-ctx.Done():
						return
					}
					continue
				}
				event := ChatOutputEvent{Name: frame.Event, Data: liveFrameDataString(frame.Data), Offset: frame.Offset}
				select {
				case events <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return events, errs, connected
}

// StreamExecution retains the legacy method shape. New callers should use
// StreamExecutionForProject so the backend can authorize the subscription.
func (c *Client) StreamExecution(ctx context.Context, execID string, offset int) (<-chan ExecutionEvent, <-chan error) {
	return c.StreamExecutionForProject(ctx, "", execID, offset)
}

// StreamExecutionForProject adapts the shared WebSocket execution subscription
// for headless callers, adding cumulative UTF-8 byte offsets.
func (c *Client) StreamExecutionForProject(ctx context.Context, projectID, execID string, offset int) (<-chan ExecutionEvent, <-chan error) {
	ctx = normalizeLiveContext(ctx)
	outputEvents, outputErrs := c.StreamChatOutputForProject(ctx, projectID, execID, offset)
	events := make(chan ExecutionEvent, 32)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
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
				switch typeName {
				case "":
					typeName = ExecutionDelta
					if event.Offset > 0 {
						currentOffset = event.Offset
					} else {
						currentOffset += len([]byte(event.Data))
					}
				case "reset":
					typeName = ExecutionReset
					currentOffset = event.Offset
				case ExecutionDone, ExecutionError:
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
					case errs <- err:
					case <-ctx.Done():
					}
					return
				}
			}
		}
		if !terminal && ctx.Err() == nil {
			errs <- ErrEventStreamClosed
		}
	}()
	return events, errs
}
