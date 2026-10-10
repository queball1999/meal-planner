package plan

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// JobStatus represents the state of a generation job.
type JobStatus string

const (
	JobPending JobStatus = "pending"
	JobRunning JobStatus = "running"
	JobDone    JobStatus = "done"
	JobFailed  JobStatus = "failed"
)

// JobEvent is one SSE event emitted during plan generation.
type JobEvent struct {
	Type    string // "status" | "plan" | "llm_start" | "llm_delta" | "done" | "error" | "canceled"
	Message string
	PlanID  int64 // set on "done" events
}

// Job tracks a single in-flight or completed generation attempt.
type Job struct {
	HouseholdID int64
	Status      JobStatus
	PlanID      int64  // set on success
	Error       string // set on failure

	mu       sync.Mutex
	events   []JobEvent
	done     chan struct{}
	cancel   context.CancelFunc
	canceled bool
}

func newJob(householdID int64) *Job {
	return &Job{
		HouseholdID: householdID,
		Status:      JobPending,
		done:        make(chan struct{}),
	}
}

// SetCancel registers the function that aborts this job's work. If Cancel was
// already called (the user clicked before the job got this far) it fires
// straight away, so an early click is never lost.
func (j *Job) SetCancel(fn context.CancelFunc) {
	j.mu.Lock()
	j.cancel = fn
	already := j.canceled
	j.mu.Unlock()
	if already {
		fn()
	}
}

// Cancel asks the job to stop. Reports whether the job was still running -
// false once it has finished, when there is nothing left to abort.
func (j *Job) Cancel() bool {
	select {
	case <-j.done:
		return false
	default:
	}
	j.mu.Lock()
	j.canceled = true
	fn := j.cancel
	j.mu.Unlock()
	if fn != nil {
		fn()
	}
	return true
}

// Canceled reports whether Cancel was called.
func (j *Job) Canceled() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.canceled
}

// Done returns a channel closed once the job's function has returned (success,
// failure, or panic) - lets a caller (e.g. a test) block until generation
// finishes instead of polling JobManager.Get.
func (j *Job) Done() <-chan struct{} {
	return j.done
}

func (j *Job) Emit(e JobEvent) {
	j.mu.Lock()
	j.events = append(j.events, e)
	j.mu.Unlock()
}

// EmitStatus is a nil-safe shorthand for Emit(JobEvent{Type: "status", ...}) -
// Generate and Repair take *Job as an optional progress sink, so every call
// site would otherwise need its own "if j != nil" guard.
func (j *Job) EmitStatus(message string) {
	if j == nil {
		return
	}
	j.Emit(JobEvent{Type: "status", Message: message})
}

// EmitPlan is a nil-safe shorthand for Emit(JobEvent{Type: "plan", ...}) -
// tells the generation page which plan row this run is building, which is
// what its debug panel asks the call log for (see llm.WithPlan).
func (j *Job) EmitPlan(planID int64) {
	if j == nil || planID == 0 {
		return
	}
	j.Emit(JobEvent{Type: "plan", Message: strconv.FormatInt(planID, 10)})
}

// EmitLLMStart is a nil-safe shorthand for Emit(JobEvent{Type: "llm_start"}) -
// marks the beginning of a new model call so the generation page's debug
// panel starts a fresh live entry instead of appending onto the previous
// call's text (see EmitDelta).
func (j *Job) EmitLLMStart() {
	if j == nil {
		return
	}
	j.Emit(JobEvent{Type: "llm_start"})
}

// EmitDelta is a nil-safe shorthand for Emit(JobEvent{Type: "llm_delta", ...})
// - appends one streamed chunk of an in-flight model call's reply to the
// generation page's debug panel in real time, the same way slack-llm-proxy
// streams a chat reply to its browser. Generate and Repair pass this as
// llm.GenerateRequest.OnDelta wherever j is in scope.
func (j *Job) EmitDelta(chunk string) {
	if j == nil || chunk == "" {
		return
	}
	j.Emit(JobEvent{Type: "llm_delta", Message: chunk})
}

// Subscribe returns all events emitted so far, then blocks until new ones
// arrive or the job finishes. It writes SSE lines to w, flushing after each.
func (j *Job) Subscribe(w http.ResponseWriter) {
	flusher, _ := w.(http.Flusher)
	cursor := 0

	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

	// Generation can run for minutes with long silent stretches (a costing
	// pass makes one LLM/scrape call per ingredient). Proxies and browsers
	// drop a stream that sends nothing, so emit an SSE comment periodically -
	// comments are ignored by EventSource but keep the connection alive.
	beat := time.NewTicker(15 * time.Second)
	defer beat.Stop()

	for {
		select {
		case <-j.done:
			// Drain remaining events.
			j.mu.Lock()
			remaining := j.events[cursor:]
			j.mu.Unlock()
			for _, ev := range remaining {
				writeSSE(w, ev)
			}
			if flusher != nil {
				flusher.Flush()
			}
			return
		case <-tick.C:
			j.mu.Lock()
			batch := j.events[cursor:]
			j.mu.Unlock()
			for _, ev := range batch {
				writeSSE(w, ev)
				cursor++
			}
			if flusher != nil {
				flusher.Flush()
			}
		case <-beat.C:
			fmt.Fprint(w, ": keepalive\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func writeSSE(w http.ResponseWriter, e JobEvent) {
	fmt.Fprintf(w, "event: %s\n", e.Type)
	if e.Message != "" {
		// One "data:" line per line of the message, per the SSE spec - a
		// single "data:" line would drop everything after the first newline,
		// which an llm_delta chunk (a token boundary can land anywhere,
		// including mid-line) will eventually contain. The browser rejoins
		// multiple data lines with "\n", reproducing the original text.
		for _, line := range strings.Split(e.Message, "\n") {
			fmt.Fprintf(w, "data: %s\n", line)
		}
	}
	if e.PlanID != 0 {
		fmt.Fprintf(w, "data: plan_id=%d\n", e.PlanID)
	}
	fmt.Fprintf(w, "\n")
}

// JobManager holds at most one active job per household (single-flight).
type JobManager struct {
	mu   sync.Mutex
	jobs map[int64]*Job
}

func NewJobManager() *JobManager {
	return &JobManager{jobs: make(map[int64]*Job)}
}

// Start launches a generation job for the household unless one is already running.
// Returns (job, true) when a new job is started; (existing, false) when one is in-flight.
func (m *JobManager) Start(ctx context.Context, householdID int64, fn func(j *Job)) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.jobs[householdID]; ok {
		if existing.Status == JobRunning || existing.Status == JobPending {
			return existing, false
		}
	}

	j := newJob(householdID)
	j.Status = JobRunning
	m.jobs[householdID] = j

	go func() {
		// Cleanup always runs: a panic in fn must still clear the job from
		// the map and close done, or SSE subscribers block forever.
		defer func() {
			if rec := recover(); rec != nil {
				j.Status = JobFailed
				j.Error = fmt.Sprintf("generation panicked: %v", rec)
				j.Emit(JobEvent{Type: "error", Message: j.Error})
			}
			m.mu.Lock()
			if m.jobs[householdID] == j { // only clear if still this job
				delete(m.jobs, householdID)
			}
			m.mu.Unlock()
			close(j.done)
		}()
		fn(j)
	}()

	return j, true
}

// Get returns the current job for the household, or nil.
func (m *JobManager) Get(householdID int64) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[householdID]
}
