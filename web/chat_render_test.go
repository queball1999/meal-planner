package web

import (
	"encoding/json"
	"strings"
	"testing"

	"goeat/agent"
	"goeat/db"
)

// The widget is rendered from layout.html, so it has to appear on an ordinary
// page - that is what "available on every page" means.
func TestChatWidgetRendersForSignedInUser(t *testing.T) {
	out := renderPage(t, "plan", pageData{
		AppName:   "Go Eat",
		Page:      "plan",
		User:      &db.User{Username: "sam"},
		CSRFToken: "tok-123",
		Data:      planPageData{HasPlan: false, Tab: "plan"},
	})

	for _, want := range []string{
		`id="chat-launcher"`,
		`id="chat-panel"`,
		`id="chat-log"`,
		`id="chat-input"`,
		`/static/js/chat.js`,
		// fetch() calls cannot post a form field, so the raw token has to be
		// somewhere readable. Nothing provided this before.
		`id="csrf-token"`,
		`value="tok-123"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

// A signed-out page (the login screen) must not offer an assistant that can
// rewrite a household's plan.
func TestChatWidgetHiddenWhenSignedOut(t *testing.T) {
	out := renderPage(t, "plan", pageData{
		AppName: "Go Eat",
		Page:    "plan",
		Data:    planPageData{HasPlan: false, Tab: "plan"},
	})

	for _, unwanted := range []string{`id="chat-launcher"`, `/static/js/chat.js`, `id="csrf-token"`} {
		if strings.Contains(out, unwanted) {
			t.Errorf("signed-out page contains %q", unwanted)
		}
	}
}

func TestToTurnsDecodesAudit(t *testing.T) {
	audit := []agent.AuditEntry{
		{Tool: "move_meal", Summary: "Moved it."},
		{Tool: "read_plan", Error: "no plan"},
	}
	blob, _ := json.Marshal(audit)

	turns := toTurns([]*db.ChatMessage{
		{Role: "user", Content: "move it", AuditJSON: "[]"},
		{Role: "assistant", Content: "Done.", AuditJSON: string(blob)},
	})

	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2", len(turns))
	}
	if len(turns[0].Audit) != 0 {
		t.Errorf("a user turn should carry no audit, got %+v", turns[0].Audit)
	}
	if len(turns[1].Audit) != 2 {
		t.Fatalf("assistant audit = %+v, want 2 entries", turns[1].Audit)
	}
	if turns[1].Audit[0].Tool != "move_meal" || turns[1].Audit[1].Error != "no plan" {
		t.Errorf("audit did not round-trip: %+v", turns[1].Audit)
	}
}

// Unparseable audit JSON must not lose the message it belongs to - the text is
// the part the user actually needs.
func TestToTurnsSurvivesBadAudit(t *testing.T) {
	turns := toTurns([]*db.ChatMessage{
		{Role: "assistant", Content: "Done.", AuditJSON: "{not json"},
	})
	if len(turns) != 1 || turns[0].Content != "Done." {
		t.Fatalf("turns = %+v, want the message kept", turns)
	}
	if len(turns[0].Audit) != 0 {
		t.Errorf("audit = %+v, want none", turns[0].Audit)
	}
}

// A held call is not a finished turn, so nothing is written to the
// conversation until it is answered - recording "the assistant did X" before X
// was agreed to would be a lie in the transcript.
func TestPendingIsHeldOncePerHousehold(t *testing.T) {
	srv := &Server{pendingChat: map[int64]*agent.Pending{}}

	if got := srv.takePending(1); got != nil {
		t.Errorf("takePending on an empty server = %+v, want nil", got)
	}

	first := &agent.Pending{Tool: "move_meal"}
	srv.putPending(1, first)

	// A newer question supersedes an unanswered prompt: a stack of stale
	// "apply?" boxes is worse than losing one.
	second := &agent.Pending{Tool: "delete_meal"}
	srv.putPending(1, second)

	got := srv.takePending(1)
	if got == nil || got.Tool != "delete_meal" {
		t.Fatalf("takePending = %+v, want the newer held call", got)
	}
	// Taken means taken: two tabs racing must not apply the same change twice.
	if again := srv.takePending(1); again != nil {
		t.Errorf("a held call was answerable twice: %+v", again)
	}
}

func TestPendingIsScopedToHousehold(t *testing.T) {
	srv := &Server{pendingChat: map[int64]*agent.Pending{}}
	srv.putPending(1, &agent.Pending{Tool: "move_meal"})

	if got := srv.takePending(2); got != nil {
		t.Errorf("household 2 could take household 1's pending call: %+v", got)
	}
	if got := srv.takePending(1); got == nil {
		t.Error("household 1 lost its own pending call")
	}
}
