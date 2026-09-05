package plan

import (
	"context"
	"fmt"
	"net/http"
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
	Type    string // "status" | "done" | "error"
	Message string
	PlanID  int64  // set on "done" events
}

// Job tracks a single in-flight or completed generation attempt.
type Job struct {
	HouseholdID int64
	Status      JobStatus
	PlanID      int64  // set on success
	Error       string // set on failure

	mu     sync.Mutex
	events []JobEvent
	done   chan struct{}
}

func newJob(householdID int64) *Job {
	return &Job{
		HouseholdID: householdID,
		Status:      JobPending,
		done:        make(chan struct{}),
	}
}

func (j *Job) Emit(e JobEvent) {
	j.mu.Lock()
	j.events = append(j.events, e)
	j.mu.Unlock()
}

// Subscribe returns all events emitted so far, then blocks until new ones
// arrive or the job finishes. It writes SSE lines to w, flushing after each.
func (j *Job) Subscribe(w http.ResponseWriter) {
	flusher, _ := w.(http.Flusher)
	cursor := 0

	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

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
		}
	}
}

func writeSSE(w http.ResponseWriter, e JobEvent) {
	fmt.Fprintf(w, "event: %s\n", e.Type)
	if e.Message != "" {
		fmt.Fprintf(w, "data: %s\n", e.Message)
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
		fn(j)
		m.mu.Lock()
		// Only clear if it's still this job.
		if m.jobs[householdID] == j {
			delete(m.jobs, householdID)
		}
		m.mu.Unlock()
		close(j.done)
	}()

	return j, true
}

// Get returns the current job for the household, or nil.
func (m *JobManager) Get(householdID int64) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[householdID]
}
