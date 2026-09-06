package llm

import (
	"context"
	"sync"
	"time"
)

// DebugEntry records one LLM call for inspection in the admin UI.
type DebugEntry struct {
	At         time.Time
	System     string
	Prompt     string
	Response   string
	Error      string
	DurationMS int64
	InputToks  int
	OutputToks int
	Provider   string
	Model      string
}

const maxDebugEntries = 30

// debugLogger wraps a Generator, recording every call into an in-memory ring.
type debugLogger struct {
	inner Generator
	mu    sync.Mutex
	ring  []DebugEntry
}

// GlobalDebugLog is set by NewDebugLogger and read by handlers.
// nil when debug logging is not wired up.
var GlobalDebugLog *debugLogger

// NewDebugLogger wraps gen with call logging and registers the logger globally.
func NewDebugLogger(gen Generator) Generator {
	dl := &debugLogger{inner: gen}
	GlobalDebugLog = dl
	return dl
}

func (d *debugLogger) ProviderName() string { return d.inner.ProviderName() }
func (d *debugLogger) ModelName() string    { return d.inner.ModelName() }

func (d *debugLogger) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	start := time.Now()
	resp, err := d.inner.Generate(ctx, req)
	ms := time.Since(start).Milliseconds()

	e := DebugEntry{
		At:         start,
		System:     req.System,
		Prompt:     req.Prompt,
		DurationMS: ms,
		Provider:   d.inner.ProviderName(),
		Model:      d.inner.ModelName(),
	}
	if err != nil {
		e.Error = err.Error()
	} else {
		e.Response = resp.Content
		e.InputToks = resp.InputTokens
		e.OutputToks = resp.OutputTokens
		if resp.ModelName != "" {
			e.Model = resp.ModelName
		}
	}

	d.mu.Lock()
	d.ring = append(d.ring, e)
	if len(d.ring) > maxDebugEntries {
		d.ring = d.ring[len(d.ring)-maxDebugEntries:]
	}
	d.mu.Unlock()

	return resp, err
}

// Entries returns a snapshot of the ring, newest last.
func (d *debugLogger) Entries() []DebugEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]DebugEntry, len(d.ring))
	copy(out, d.ring)
	return out
}
