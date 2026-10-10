package plan

import (
	"net/http/httptest"
	"testing"
)

// TestWriteSSEBareEventHasData: EventSource never dispatches an event with no
// data line, so a marker event with no message must still carry an empty one.
func TestWriteSSEBareEventHasData(t *testing.T) {
	for _, tc := range []struct {
		ev   JobEvent
		want string
	}{
		{JobEvent{Type: "llm_start"}, "event: llm_start\ndata:\n\n"},
		{JobEvent{Type: "status", Message: "a\nb"}, "event: status\ndata: a\ndata: b\n\n"},
		{JobEvent{Type: "done", PlanID: 7}, "event: done\ndata: plan_id=7\n\n"},
	} {
		w := httptest.NewRecorder()
		writeSSE(w, tc.ev)
		if got := w.Body.String(); got != tc.want {
			t.Errorf("writeSSE(%+v) = %q, want %q", tc.ev, got, tc.want)
		}
	}
}
