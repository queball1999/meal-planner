// Package live drives one Browserless-hosted Chrome tab over the raw Chrome
// DevTools Protocol (CDP) so an admin can watch it live and solve a CAPTCHA
// themselves - the hybrid fallback for when the fully automated
// FlareSolverr/Browserless chain in package scrape still comes back
// bot-walled (see scrape.IsBotWall).
//
// This is a minimal hand-rolled CDP client, not chromedp: the app never
// embeds a browser or a heavy scraping framework (see scrape/render.go's
// package doc comment), and golang.org/x/net/websocket is already a
// dependency - talking CDP's JSON-RPC-over-websocket directly needs no new
// one.
package live

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

// idleReadDeadline bounds how long conn.readLoop waits for the *next* frame
// before deciding the connection is dead. Reset after every successful read,
// so a live screencast (frames arrive continuously) acts as its own
// heartbeat; this only fires when Browserless has gone silent entirely.
const idleReadDeadline = 90 * time.Second

// rpcRequest is one outgoing CDP command.
type rpcRequest struct {
	ID        int64  `json:"id"`
	SessionID string `json:"sessionId,omitempty"`
	Method    string `json:"method"`
	Params    any    `json:"params,omitempty"`
}

// rpcMessage decodes anything incoming: either a response to a call we made
// (ID set) or an unsolicited event (Method set, ID zero).
type rpcMessage struct {
	ID        int64           `json:"id"`
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("cdp: %s (code %d)", e.Message, e.Code) }

// conn is one CDP websocket connection, multiplexing request/response
// correlation (call) with unsolicited event dispatch (events) over a single
// reader goroutine - CDP's "flattened" mode lets every target/session share
// one websocket rather than one per attached target.
type conn struct {
	ws *websocket.Conn

	nextID int64

	writeMu sync.Mutex // serializes Send calls: x/net/websocket requires this

	mu       sync.Mutex
	pending  map[int64]chan rpcMessage
	closed   chan struct{}
	closeMu  sync.Once
	closeErr error

	// events carries every unsolicited message (Method set, ID zero) - most
	// of the volume here is Page.screencastFrame. Buffered and lossy by
	// design: session.go's frame relay drops rather than blocks when a
	// viewer's own connection is slow, since a screencast frame missed is a
	// frame the next one supersedes.
	events chan rpcMessage
}

func dial(ctx context.Context, wsURL string) (*conn, error) {
	cfg, err := websocket.NewConfig(wsURL, "http://localhost")
	if err != nil {
		return nil, fmt.Errorf("live: bad websocket url %q: %w", wsURL, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		cfg.Dialer = &net.Dialer{Deadline: dl}
	}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("live: dial %s: %w", wsURL, err)
	}

	c := &conn{
		ws:      ws,
		pending: make(map[int64]chan rpcMessage),
		closed:  make(chan struct{}),
		events:  make(chan rpcMessage, 8),
	}
	go c.readLoop()
	return c, nil
}

// readLoop is the sole reader of c.ws - x/net/websocket's Conn is not safe
// for concurrent Receive calls, so every other method only ever writes.
func (c *conn) readLoop() {
	defer close(c.events)
	for {
		_ = c.ws.SetReadDeadline(time.Now().Add(idleReadDeadline))
		var msg rpcMessage
		if err := websocket.JSON.Receive(c.ws, &msg); err != nil {
			c.fail(fmt.Errorf("live: read: %w", err))
			return
		}
		if msg.ID != 0 {
			c.mu.Lock()
			ch, ok := c.pending[msg.ID]
			delete(c.pending, msg.ID)
			c.mu.Unlock()
			if ok {
				ch <- msg
			}
			continue
		}
		if msg.Method == "" {
			continue
		}
		select {
		case c.events <- msg:
		default:
			// A slow consumer of a high-rate event (screencast frames) - drop
			// rather than stall the only reader goroutine, which would wedge
			// every in-flight call too.
		}
	}
}

// fail unblocks every pending call and marks the connection dead. Called at
// most once in effect (closeMu), whether the cause was a read error or an
// explicit close.
func (c *conn) fail(err error) {
	c.closeMu.Do(func() {
		c.closeErr = err
		c.mu.Lock()
		for id, ch := range c.pending {
			delete(c.pending, id)
			close(ch)
		}
		c.mu.Unlock()
		close(c.closed)
	})
}

// call issues one CDP command and waits for its matching response. sessionID
// is empty for browser-level commands (Target.createTarget) and set for
// everything sent to an attached page (flattened mode - see dial's doc
// comment on package live).
func (c *conn) call(ctx context.Context, sessionID, method string, params any) (json.RawMessage, error) {
	id := atomic.AddInt64(&c.nextID, 1)
	ch := make(chan rpcMessage, 1)

	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		return nil, fmt.Errorf("live: connection closed: %w", c.closeErr)
	default:
	}
	c.pending[id] = ch
	c.mu.Unlock()

	req := rpcRequest{ID: id, SessionID: sessionID, Method: method, Params: params}

	c.writeMu.Lock()
	err := websocket.JSON.Send(c.ws, req)
	c.writeMu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("live: send %s: %w", method, err)
	}

	select {
	case msg, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("live: connection closed waiting for %s: %w", method, c.closeErr)
		}
		if msg.Error != nil {
			return nil, msg.Error
		}
		return msg.Result, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-c.closed:
		return nil, fmt.Errorf("live: connection closed waiting for %s: %w", method, c.closeErr)
	}
}

func (c *conn) close() {
	c.fail(fmt.Errorf("live: closed"))
	_ = c.ws.Close()
}
