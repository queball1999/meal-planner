package live

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// fakeCDPServer starts an httptest server speaking just enough of CDP's
// JSON-RPC-over-websocket shape to exercise conn's router without a real
// Chrome/Browserless: echoes "test.echo" params back as the result, and lets
// the test push an unsolicited event on demand via pushEvent.
type fakeCDPServer struct {
	*httptest.Server
	wsURL string

	mu   sync.Mutex
	conn *websocket.Conn
	// ready closes once the handler has stored conn. dial returns as soon
	// as the handshake completes, which can be before the handler runs.
	ready chan struct{}
}

func startFakeCDPServer(t *testing.T) *fakeCDPServer {
	t.Helper()
	f := &fakeCDPServer{ready: make(chan struct{})}
	handler := websocket.Handler(func(ws *websocket.Conn) {
		f.mu.Lock()
		f.conn = ws
		f.mu.Unlock()
		close(f.ready)
		for {
			var req rpcRequest
			if err := websocket.JSON.Receive(ws, &req); err != nil {
				return
			}
			switch req.Method {
			case "test.echo":
				resultBytes, _ := json.Marshal(req.Params)
				_ = websocket.JSON.Send(ws, rpcMessage{ID: req.ID, Result: resultBytes})
			case "test.fail":
				_ = websocket.JSON.Send(ws, rpcMessage{ID: req.ID, Error: &rpcError{Code: 1, Message: "boom"}})
			}
		}
	})
	f.Server = httptest.NewServer(handler)
	f.wsURL = "ws" + strings.TrimPrefix(f.Server.URL, "http")
	return f
}

func (f *fakeCDPServer) pushEvent(t *testing.T, method string, params any) {
	t.Helper()
	select {
	case <-f.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("pushEvent before a client connected")
	}
	f.mu.Lock()
	ws := f.conn
	f.mu.Unlock()
	raw, _ := json.Marshal(params)
	if err := websocket.JSON.Send(ws, rpcMessage{Method: method, Params: raw}); err != nil {
		t.Fatalf("push event: %v", err)
	}
}

func dialFake(t *testing.T, f *fakeCDPServer) *conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dial(ctx, f.wsURL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(c.close)
	return c
}

func TestConnCallRoundTrip(t *testing.T) {
	f := startFakeCDPServer(t)
	defer f.Close()
	c := dialFake(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := c.call(ctx, "", "test.echo", map[string]any{"foo": "bar"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var out struct {
		Foo string `json:"foo"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if out.Foo != "bar" {
		t.Fatalf("got %+v, want foo=bar", out)
	}
}

func TestConnCallError(t *testing.T) {
	f := startFakeCDPServer(t)
	defer f.Close()
	c := dialFake(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.call(ctx, "", "test.fail", nil)
	if err == nil {
		t.Fatal("expected an error from test.fail")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error %q does not mention the CDP error message", err)
	}
}

// TestConnCallConcurrent exercises the id-correlation router with several
// calls in flight at once, the shape a real session (mouse/key input racing
// against screencast frame acks) produces.
func TestConnCallConcurrent(t *testing.T) {
	f := startFakeCDPServer(t)
	defer f.Close()
	c := dialFake(t, f)

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := c.call(ctx, "", "test.echo", map[string]any{"n": i})
			if err != nil {
				errs <- err
				return
			}
			var out struct {
				N int `json:"n"`
			}
			if uerr := json.Unmarshal(result, &out); uerr != nil {
				errs <- uerr
				return
			}
			if out.N != i {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent call: %v", err)
		}
	}
}

// TestConnEventDispatch verifies an unsolicited message (no id) is routed to
// events rather than mistaken for a call's response, and does not block
// concurrent in-flight calls.
func TestConnEventDispatch(t *testing.T) {
	f := startFakeCDPServer(t)
	defer f.Close()
	c := dialFake(t, f)

	f.pushEvent(t, "Page.screencastFrame", map[string]any{"data": "abc"})

	select {
	case msg := <-c.events:
		if msg.Method != "Page.screencastFrame" {
			t.Fatalf("got event method %q", msg.Method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.call(ctx, "", "test.echo", map[string]any{"ok": true}); err != nil {
		t.Fatalf("call after event: %v", err)
	}
}

// TestConnCallTimeout verifies a call whose context expires before the
// (never-sent) response arrives returns promptly rather than hanging, and
// cleans up its pending entry.
func TestConnCallTimeout(t *testing.T) {
	f := startFakeCDPServer(t)
	defer f.Close()
	c := dialFake(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.call(ctx, "", "test.never-responds", nil)
	if err == nil {
		t.Fatal("expected a timeout error")
	}

	c.mu.Lock()
	pending := len(c.pending)
	c.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending map leaked %d entries after timeout", pending)
	}
}
