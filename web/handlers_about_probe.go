package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"goeat/homeassistant"
	"goeat/llm"
	"goeat/settings"
)

// probeTimeout bounds one connectivity check.
//
// Short on purpose: this answers "is it there", not "is it fast". A service
// that needs more than five seconds to acknowledge a request is not usable for
// a page load anyway, and reporting it as down is closer to the truth than
// making the operator wait to find out.
const probeTimeout = 5 * time.Second

// probeResult is one connectivity row.
type probeResult struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Status string `json:"status"` // "ok" | "down" | "off" | "checking"
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	// Millis is how long the check took, so a working-but-slow service is
	// visible rather than just green.
	Millis int64 `json:"millis,omitempty"`
}

// handleAboutProbe streams live connectivity checks over SSE.
//
// Streamed rather than returned as one JSON body because the checks are
// independent network calls of very different speeds - a local database ping
// answers instantly, a remote LLM endpoint may take seconds - and a single
// response would make every row wait for the slowest. They run concurrently
// and each row updates as its own answer lands.
//
// The page renders its rows as "checking" and this fills them in, so the About
// page is never blocked on a service that is down.
func (s *Server) handleAboutProbe(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout+2*time.Second)
	defer cancel()

	var mu sync.Mutex
	send := func(res probeResult) {
		// Serialised: the probes run concurrently and two interleaved writes
		// would produce one corrupt SSE frame.
		mu.Lock()
		defer mu.Unlock()
		blob, err := json.Marshal(res)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: probe\ndata: %s\n\n", blob)
		flusher.Flush()
	}

	var wg sync.WaitGroup
	for _, row := range probeRows {
		wg.Add(1)
		go func(p func(*Server, context.Context) probeResult) {
			defer wg.Done()
			// A panic in one probe must not take the whole page's status
			// stream down with it.
			defer func() {
				if rec := recover(); rec != nil {
					log.Printf("about probe panicked: %v", rec)
				}
			}()
			send(p(s, ctx))
		}(row.Run)
	}
	wg.Wait()

	mu.Lock()
	fmt.Fprint(w, "event: done\ndata: {}\n\n")
	flusher.Flush()
	mu.Unlock()

}

// probeRows names every connectivity check, in display order, alongside the
// function that performs it.
//
// One list rather than two: the page renders placeholders and the stream fills
// them in by key, so a placeholder whose key the stream never sends would sit
// on "Checking…" forever - reading as a hung page rather than as the wiring
// mistake it is. Deriving both from here makes that impossible.
var probeRows = []struct {
	Key  string
	Name string
	Run  func(*Server, context.Context) probeResult
}{
	{"database", "Database", (*Server).probeDatabase},
	{"llm", "AI provider", (*Server).probeLLM},
	{"home_assistant", "Home Assistant", (*Server).probeHomeAssistant},
	{"renderer", "Scraping renderer", (*Server).probeRenderer},
}

// probePlaceholders is the connectivity strip's initial state: every row that
// will exist, all reading "checking".
func probePlaceholders() []probeResult {
	out := make([]probeResult, 0, len(probeRows))
	for _, r := range probeRows {
		out = append(out, probeResult{Key: r.Key, Name: r.Name, Status: "checking"})
	}
	return out
}

// timed runs a check and reports how long it took.
func timed(fn func() error) (error, int64) {
	start := time.Now()
	err := fn()
	return err, time.Since(start).Milliseconds()
}

func (s *Server) probeDatabase(ctx context.Context) probeResult {
	res := probeResult{Key: "database", Name: "Database"}
	c, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	err, ms := timed(func() error { return s.store.Ping(c) })
	res.Millis = ms
	if err != nil {
		res.Status, res.Label, res.Detail = "down", "Unreachable", err.Error()
		return res
	}
	res.Status, res.Label = "ok", "Reachable"
	return res
}

// probeLLM asks the configured provider for one token.
//
// The page used to report "configured" - that s.llmGen() was non-nil - which says
// nothing about whether the endpoint answers. A wrong API key, an unreachable
// local model server, and a working provider all looked identical, which is
// precisely the case an operator opens this page to distinguish.
func (s *Server) probeLLM(ctx context.Context) probeResult {
	res := probeResult{Key: "llm", Name: "AI provider"}
	if s.llmGen() == nil {
		res.Status = "off"
		res.Label = "Not configured"
		res.Detail = "set a provider in Settings → AI Provider"
		return res
	}
	res.Detail = s.llmGen().ProviderName() + " · " + s.llmGen().ModelName()

	c, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	err, ms := timed(func() error {
		_, gerr := s.llmGen().Generate(c, llm.GenerateRequest{
			System: "Reply with the single word: ok",
			Prompt: "ping",
			// Deliberately tiny. This is a reachability check, and a reasoning
			// model given room to think would spend the whole budget before
			// emitting anything - which reads as a failure when the endpoint
			// is fine.
			MaxTokens:         16,
			SuppressReasoning: true,
		})
		return gerr
	})
	res.Millis = ms
	if err != nil {
		res.Status, res.Label = "down", "Not responding"
		res.Detail = truncateErr(err.Error())
		return res
	}
	res.Status, res.Label = "ok", "Responding"
	return res
}

func (s *Server) probeHomeAssistant(ctx context.Context) probeResult {
	res := probeResult{Key: "home_assistant", Name: "Home Assistant"}
	hc := settings.LiveHAConfig(ctx, s.store, s.cfg, s.box)
	if !hc.Configured() {
		res.Status = "off"
		res.Label = "Not configured"
		res.Detail = "no base URL or token set"
		return res
	}
	res.Detail = hostOf(hc.BaseURL)

	c, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	client := homeassistant.NewClient(hc.BaseURL, hc.Token)
	err, ms := timed(func() error { return client.Ping(c) })
	res.Millis = ms
	if err != nil {
		res.Status, res.Label = "down", "Unreachable"
		res.Detail = truncateErr(err.Error())
		return res
	}
	res.Status, res.Label = "ok", "Reachable"
	return res
}

// probeRenderer checks the headless-browser service, when one is configured.
//
// A plain GET on the base URL rather than a real render: both FlareSolverr and
// Browserless answer *something* on their root, and a full render costs
// seconds and a browser tab. This distinguishes "the service is there" from
// "nothing is listening", which is what the row claims.
func (s *Server) probeRenderer(ctx context.Context) probeResult {
	res := probeResult{Key: "renderer", Name: "Scraping renderer"}
	rc := settings.LiveRenderConfig(ctx, s.store, s.cfg)
	if !rc.Enabled() {
		res.Status = "off"
		res.Label = "Not configured"
		res.Detail = "no FlareSolverr or Browserless URL set"
		return res
	}

	rs := rc.Renderers()
	first := rs[0]
	res.Detail = first.Label() + " · " + hostOf(first.URL)

	c, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	var status int
	err, ms := timed(func() error {
		req, rerr := http.NewRequestWithContext(c, http.MethodGet, first.URL, nil)
		if rerr != nil {
			return rerr
		}
		resp, rerr := http.DefaultClient.Do(req)
		if rerr != nil {
			return rerr
		}
		defer resp.Body.Close()
		status = resp.StatusCode
		return nil
	})
	res.Millis = ms
	if err != nil {
		res.Status, res.Label = "down", "Unreachable"
		res.Detail = truncateErr(err.Error())
		return res
	}
	// Any answer at all means something is listening. These services return
	// 404 or 405 on their root as readily as 200, and treating those as
	// failures would report a working renderer as down.
	res.Status, res.Label = "ok", "Reachable"
	if status >= 500 {
		res.Status, res.Label = "down", fmt.Sprintf("HTTP %d", status)
	}
	return res
}

// hostOf reduces a URL to host:port for display - the full URL can carry a
// token, and this text goes on a page an operator may screenshot.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
}

// truncateErr keeps a connection error to one readable line. Transport errors
// nest several layers of context and the tail is rarely the useful part.
func truncateErr(s string) string {
	const max = 140
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
