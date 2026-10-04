package live

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"goeat/scrape"
)

const (
	// idleSessionTimeout closes an abandoned session - a modal the admin
	// closed without clicking Cancel, a browser tab left open - so it cannot
	// leak a Browserless tab forever. Reset on every frame relayed and every
	// input forwarded.
	idleSessionTimeout  = 5 * time.Minute
	connectTimeout      = 20 * time.Second
	screencastMaxWidth  = 1024
	screencastMaxHeight = 768
)

// Frame is one screencast frame, ready to relay to an admin's browser.
type Frame struct {
	JPEGBase64 string
	Width      int // device pixels the frame was captured at - for click scaling
	Height     int
}

// Session is one live, admin-driven CDP session against a single Browserless
// tab - opened when the automated scraping chain hits a bot wall it cannot
// get past on its own (see scrape.IsBotWall), so an admin can solve it by
// hand instead.
type Session struct {
	conn         *conn
	targetID     string
	cdpSessionID string // CDP's own attach session id, not the web layer's registry key

	// Frames is fed by relayFrames; closed once the underlying connection
	// dies (readLoop error, or Close).
	Frames chan Frame

	mu                   sync.Mutex
	viewportW, viewportH float64 // real page viewport, from Page.getLayoutMetrics
	frameW, frameH       float64 // latest screencast frame's device size

	idleTimer *time.Timer
	closeOnce sync.Once
	done      chan struct{}
}

// StartSession opens a fresh Browserless tab navigated to targetURL and
// starts streaming it as a screencast. Only a Browserless renderer works
// here - FlareSolverr manages its own session internally and exposes no CDP
// endpoint a caller can drive by hand.
func StartSession(ctx context.Context, r scrape.Renderer, targetURL string) (*Session, error) {
	if r.Backend != scrape.RendererBrowserless || !r.Enabled() {
		return nil, fmt.Errorf("live: needs a configured Browserless renderer")
	}

	dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	wsURL, err := debuggerWebSocketURL(dialCtx, r)
	if err != nil {
		return nil, err
	}
	c, err := dial(dialCtx, wsURL)
	if err != nil {
		return nil, err
	}

	s := &Session{conn: c, Frames: make(chan Frame, 4), done: make(chan struct{})}
	if err := s.attach(dialCtx, targetURL); err != nil {
		c.close()
		return nil, err
	}

	s.idleTimer = time.AfterFunc(idleSessionTimeout, s.Close)
	go s.relayFrames()
	return s, nil
}

// versionInfo is the subset of Browserless/Chrome's GET /json/version reply
// this package needs.
type versionInfo struct {
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// debuggerWebSocketURL resolves the browser-level CDP websocket to dial.
// Preferred path: GET {base}/json/version, whose webSocketDebuggerUrl is
// authoritative but often carries an internal host (Browserless frequently
// reports "localhost" or "0.0.0.0" regardless of what clients should
// actually dial) - so its host is rewritten to the configured one, keeping
// its path (which encodes the actual browser instance id). Falls back to
// deriving a ws(s):// URL directly from the configured base when that
// endpoint is unreachable or gated, the same way scrape/render.go's
// fetchViaBrowserless derives its own endpoint from cfg.URL.
func debuggerWebSocketURL(ctx context.Context, r scrape.Renderer) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(r.URL), "/")

	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/json/version", nil); err == nil {
		if r.Token != "" {
			req.Header.Set("Authorization", "Bearer "+r.Token)
		}
		if resp, derr := (&http.Client{Timeout: connectTimeout}).Do(req); derr == nil {
			defer resp.Body.Close()
			if resp.StatusCode < 400 {
				var v versionInfo
				if jerr := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&v); jerr == nil && v.WebSocketDebuggerURL != "" {
					if rewritten, rerr := rewriteWSHost(v.WebSocketDebuggerURL, base, r.Token); rerr == nil {
						return rewritten, nil
					}
				}
			}
		}
	}
	return deriveWSBase(base, r.Token)
}

func rewriteWSHost(wsURL, httpBase, token string) (string, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return "", err
	}
	base, err := url.Parse(httpBase)
	if err != nil {
		return "", err
	}
	u.Host = base.Host
	u.Scheme = "ws"
	if base.Scheme == "https" {
		u.Scheme = "wss"
	}
	setToken(u, token)
	return u.String(), nil
}

func deriveWSBase(httpBase, token string) (string, error) {
	u, err := url.Parse(httpBase)
	if err != nil {
		return "", err
	}
	u.Scheme = "ws"
	if u.Scheme == "https" {
		u.Scheme = "wss"
	}
	setToken(u, token)
	return u.String(), nil
}

func setToken(u *url.URL, token string) {
	if token == "" {
		return
	}
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
}

// attach creates a new target already navigating to targetURL, attaches to
// it in "flattened" mode (every further command for this target rides the
// same websocket, addressed by cdpSessionID - see live.go's conn doc
// comment), enables the domains needed, reads the real viewport for
// click-coordinate scaling, and starts the screencast.
func (s *Session) attach(ctx context.Context, targetURL string) error {
	createRes, err := s.conn.call(ctx, "", "Target.createTarget", map[string]any{"url": targetURL})
	if err != nil {
		return fmt.Errorf("live: create target: %w", err)
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(createRes, &created); err != nil {
		return fmt.Errorf("live: parse createTarget result: %w", err)
	}
	s.targetID = created.TargetID

	attachRes, err := s.conn.call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": s.targetID, "flatten": true,
	})
	if err != nil {
		return fmt.Errorf("live: attach target: %w", err)
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(attachRes, &attached); err != nil {
		return fmt.Errorf("live: parse attachToTarget result: %w", err)
	}
	s.cdpSessionID = attached.SessionID

	for _, method := range []string{"Page.enable", "Network.enable", "Runtime.enable"} {
		if _, err := s.conn.call(ctx, s.cdpSessionID, method, nil); err != nil {
			return fmt.Errorf("live: %s: %w", method, err)
		}
	}

	if metrics, err := s.conn.call(ctx, s.cdpSessionID, "Page.getLayoutMetrics", nil); err == nil {
		var lm struct {
			CSSLayoutViewport struct {
				ClientWidth  float64 `json:"clientWidth"`
				ClientHeight float64 `json:"clientHeight"`
			} `json:"cssLayoutViewport"`
		}
		if json.Unmarshal(metrics, &lm) == nil && lm.CSSLayoutViewport.ClientWidth > 0 {
			s.mu.Lock()
			s.viewportW = lm.CSSLayoutViewport.ClientWidth
			s.viewportH = lm.CSSLayoutViewport.ClientHeight
			s.mu.Unlock()
		}
	}

	_, err = s.conn.call(ctx, s.cdpSessionID, "Page.startScreencast", map[string]any{
		"format": "jpeg", "quality": 60,
		"maxWidth": screencastMaxWidth, "maxHeight": screencastMaxHeight,
		"everyNthFrame": 1,
	})
	if err != nil {
		return fmt.Errorf("live: start screencast: %w", err)
	}
	return nil
}

// relayFrames reads Page.screencastFrame events off the shared conn.events
// stream, acks each immediately (CDP stalls the screencast until a frame is
// acked), and forwards it to Frames for the web layer to relay onward to the
// admin's browser. Exits (closing Frames) once conn.events closes, i.e. once
// the underlying websocket dies.
func (s *Session) relayFrames() {
	defer close(s.Frames)
	for msg := range s.conn.events {
		if msg.Method != "Page.screencastFrame" {
			continue
		}
		var params struct {
			Data      string `json:"data"`
			SessionID int64  `json:"sessionId"` // CDP's screencast frame id, unrelated to cdpSessionID
			Metadata  struct {
				DeviceWidth  float64 `json:"deviceWidth"`
				DeviceHeight float64 `json:"deviceHeight"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			continue
		}

		ackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = s.conn.call(ackCtx, s.cdpSessionID, "Page.screencastFrameAck",
			map[string]any{"sessionId": params.SessionID})
		cancel()

		if params.Metadata.DeviceWidth > 0 {
			s.mu.Lock()
			s.frameW = params.Metadata.DeviceWidth
			s.frameH = params.Metadata.DeviceHeight
			s.mu.Unlock()
		}

		select {
		case s.Frames <- Frame{
			JPEGBase64: params.Data,
			Width:      int(params.Metadata.DeviceWidth),
			Height:     int(params.Metadata.DeviceHeight),
		}:
		default:
			// A slow viewer shouldn't stall frame acking above - the next
			// frame supersedes whatever this one would have shown anyway.
		}
		s.resetIdle()
	}
}

// scale converts a coordinate relative to the streamed frame (screencastMaxWidth
// x screencastMaxHeight, or smaller if the real page is smaller) into a
// coordinate on the actual page, so a click on the admin's <canvas> lands
// where they saw it rather than where that fraction of the page happens to be.
func (s *Session) scale(x, y float64) (float64, float64) {
	s.mu.Lock()
	vw, vh, fw, fh := s.viewportW, s.viewportH, s.frameW, s.frameH
	s.mu.Unlock()
	if vw <= 0 || vh <= 0 || fw <= 0 || fh <= 0 {
		return x, y
	}
	return x * (vw / fw), y * (vh / fh)
}

func (s *Session) resetIdle() {
	if s.idleTimer != nil {
		s.idleTimer.Reset(idleSessionTimeout)
	}
}

// Click dispatches a press-then-release at (x, y), given in the streamed
// frame's coordinate space.
func (s *Session) Click(ctx context.Context, x, y float64) error {
	rx, ry := s.scale(x, y)
	if err := s.dispatchMouse(ctx, "mousePressed", rx, ry); err != nil {
		return err
	}
	time.Sleep(30 * time.Millisecond) // real browsers separate press/release by a beat too
	return s.dispatchMouse(ctx, "mouseReleased", rx, ry)
}

// MouseMove dispatches a hover at (x, y), given in the streamed frame's
// coordinate space - some challenge widgets check for movement before a click.
func (s *Session) MouseMove(ctx context.Context, x, y float64) error {
	rx, ry := s.scale(x, y)
	return s.dispatchMouse(ctx, "mouseMoved", rx, ry)
}

func (s *Session) dispatchMouse(ctx context.Context, kind string, x, y float64) error {
	_, err := s.conn.call(ctx, s.cdpSessionID, "Input.dispatchMouseEvent", map[string]any{
		"type": kind, "x": x, "y": y, "button": "left", "clickCount": 1,
	})
	s.resetIdle()
	return err
}

// nonPrintableKeys maps a JS KeyboardEvent.key to CDP's windowsVirtualKeyCode
// for the handful of non-printable keys a challenge UI plausibly needs.
// Everything else goes through KeyEvent as a "char" event instead, which is
// both simpler and correct for anything that actually inserts text.
var nonPrintableKeys = map[string]int{
	"Enter": 13, "Tab": 9, "Backspace": 8, "Escape": 27,
	"ArrowLeft": 37, "ArrowUp": 38, "ArrowRight": 39, "ArrowDown": 40,
}

// KeyEvent forwards one keystroke from the admin's browser. key is a JS
// KeyboardEvent.key value; text is what it would insert, for a printable key.
func (s *Session) KeyEvent(ctx context.Context, key, text string) error {
	defer s.resetIdle()
	if vk, ok := nonPrintableKeys[key]; ok {
		for _, kind := range []string{"keyDown", "keyUp"} {
			if _, err := s.conn.call(ctx, s.cdpSessionID, "Input.dispatchKeyEvent", map[string]any{
				"type": kind, "key": key, "windowsVirtualKeyCode": vk, "nativeVirtualKeyCode": vk,
			}); err != nil {
				return err
			}
		}
		return nil
	}
	if text == "" {
		text = key
	}
	_, err := s.conn.call(ctx, s.cdpSessionID, "Input.dispatchKeyEvent", map[string]any{
		"type": "char", "text": text, "key": key,
	})
	return err
}

// Finish harvests the cookies and user agent the session's page now carries
// - call it once the admin has solved whatever was blocking it - shaped to
// match scrape.Clearance so the caller can persist and reuse it exactly like
// one FlareSolverr obtains. Returns an error (rather than closing the
// session) so a caller can retry Finish if the admin needs another moment.
func (s *Session) Finish(ctx context.Context) (*scrape.Clearance, error) {
	cookiesRaw, err := s.conn.call(ctx, s.cdpSessionID, "Network.getAllCookies", nil)
	if err != nil {
		return nil, fmt.Errorf("live: get cookies: %w", err)
	}
	var cookiesResp struct {
		Cookies []scrape.Cookie `json:"cookies"`
	}
	if err := json.Unmarshal(cookiesRaw, &cookiesResp); err != nil {
		return nil, fmt.Errorf("live: parse cookies: %w", err)
	}
	if len(cookiesResp.Cookies) == 0 {
		return nil, fmt.Errorf("live: no cookies to hand over yet")
	}

	var ua string
	if uaRaw, err := s.conn.call(ctx, s.cdpSessionID, "Runtime.evaluate", map[string]any{
		"expression": "navigator.userAgent", "returnByValue": true,
	}); err == nil {
		var evalRes struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		if json.Unmarshal(uaRaw, &evalRes) == nil {
			ua = evalRes.Result.Value
		}
	}

	return &scrape.Clearance{Cookies: cookiesResp.Cookies, UserAgent: ua}, nil
}

// Done reports when the session has ended (Close called, or the underlying
// connection died) - a caller (the web layer's registry) can select on it to
// deregister the session without polling.
func (s *Session) Done() <-chan struct{} { return s.done }

// Close tears the session down: closes the Browserless target and the CDP
// websocket. Idempotent - safe to call from both an explicit "cancel"/
// "finish" request and the idle timer without coordination.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		if s.targetID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = s.conn.call(ctx, "", "Target.closeTarget", map[string]any{"targetId": s.targetID})
			cancel()
		}
		s.conn.close()
		close(s.done)
	})
}
