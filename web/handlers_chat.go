package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"goeat/agent"
	"goeat/db"
	"goeat/middleware"
)

// chatHistoryTurns is how much conversation the model is given as context.
//
// Enough for a follow-up ("move it to Wednesday instead") to make sense,
// short enough that a long-running conversation does not push the tool
// descriptions - which are what actually steer the model - out of the window.
const chatHistoryTurns = 12

// chatTurn is one message as the widget renders it.
type chatTurn struct {
	Role    string             `json:"role"`
	Content string             `json:"content"`
	Audit   []agent.AuditEntry `json:"audit,omitempty"`
}

// handleChatHistory returns the household's conversation so a reload does not
// lose it. The assistant makes real changes; what it was asked to do has to
// outlive a refresh.
func (s *Server) handleChatHistory(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "no household"})
		return
	}
	msgs, err := s.store.ListChatMessages(r.Context(), hh.ID, 50)
	if err != nil {
		log.Printf("chat history: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "couldn't load the conversation"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"messages": toTurns(msgs),
		"enabled":  s.gen != nil,
	})
}

func toTurns(msgs []*db.ChatMessage) []chatTurn {
	out := make([]chatTurn, 0, len(msgs))
	for _, m := range msgs {
		t := chatTurn{Role: m.Role, Content: m.Content}
		if m.AuditJSON != "" && m.AuditJSON != "[]" {
			_ = json.Unmarshal([]byte(m.AuditJSON), &t.Audit)
		}
		out = append(out, t)
	}
	return out
}

// handleChatClear wipes the conversation.
func (s *Server) handleChatClear(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "no household"})
		return
	}
	if err := s.store.ClearChatMessages(r.Context(), hh.ID); err != nil {
		log.Printf("chat clear: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "couldn't clear it"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleChatSend runs one user turn and streams progress over SSE.
//
// SSE rather than a plain JSON response because a turn can take a while: the
// model may call several tools, each doing real work, and a spinner that says
// nothing for twenty seconds while the plan is being rewritten is worse than
// no assistant at all. What streams is *progress* - one event per tool call as
// it completes - not tokens; the agent loop makes whole Generate calls, and
// pretending to stream text it does not have would be a lie in the UI.
func (s *Server) handleChatSend(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Error(w, "no household", http.StatusUnauthorized)
		return
	}
	if s.gen == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":    false,
			"error": "No AI provider is configured, so the assistant is off. Set one up in Settings.",
		})
		return
	}
	msg := strings.TrimSpace(r.FormValue("message"))
	if msg == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "say something first"})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Nothing between here and the browser may buffer, or the whole point of
	// streaming progress is lost.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(event string, payload any) {
		blob, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, blob)
		flusher.Flush()
	}

	ctx := r.Context()
	if _, err := s.store.AppendChatMessage(ctx, hh.ID, "user", msg, ""); err != nil {
		log.Printf("chat: save user turn: %v", err)
	}

	history, _ := s.store.ListChatMessages(ctx, hh.ID, chatHistoryTurns+1)
	// The turn just saved is the prompt, not context for itself.
	if n := len(history); n > 0 && history[n-1].Role == "user" {
		history = history[:n-1]
	}
	prior := make([]agent.Message, 0, len(history))
	for _, m := range history {
		prior = append(prior, agent.Message{Role: m.Role, Content: m.Content})
	}

	reg := agent.NewRegistry()
	agent.RegisterAll(reg)
	sess := &agent.Session{Store: s.store, Household: hh, HouseholdID: hh.ID}

	// Progress goes out as each tool completes, via the session's OnStep hook.
	// A turn can run several tools doing real work, and a spinner that says
	// nothing for twenty seconds while the plan is being rewritten is worse
	// than no assistant at all. Run is synchronous, so writing the event
	// straight from the callback needs no goroutine and no ordering care.
	sess.OnStep = func(e agent.AuditEntry) { send("step", e) }

	// Every tool that changes data is held for a yes. The assistant edits a
	// real household's real plan, and a silent write is the one thing about it
	// that is not recoverable by asking again.
	runner := &agent.Runner{Gen: s.gen, Registry: reg, ConfirmMutations: true}
	reply, err := runner.Run(ctx, sess, prior, msg)

	s.finishChatTurn(ctx, hh, sess, reply, err, send)
}

// handleChatConfirm applies or discards a held tool call and continues the
// conversation, streaming the rest of it exactly as the original turn did.
func (s *Server) handleChatConfirm(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Error(w, "no household", http.StatusUnauthorized)
		return
	}
	if s.gen == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "the assistant is off"})
		return
	}

	pending := s.takePending(hh.ID)
	if pending == nil {
		// The window has closed - a restart, or a second tab already answered.
		// Saying so is better than silently applying nothing.
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":    false,
			"error": "That request expired. Ask again and I'll redo it.",
		})
		return
	}
	approved := r.FormValue("approve") == "1"

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

	send := func(event string, payload any) {
		blob, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, blob)
		flusher.Flush()
	}

	ctx := r.Context()
	reg := agent.NewRegistry()
	agent.RegisterAll(reg)
	sess := &agent.Session{Store: s.store, Household: hh, HouseholdID: hh.ID}
	sess.OnStep = func(e agent.AuditEntry) { send("step", e) }

	runner := &agent.Runner{Gen: s.gen, Registry: reg, ConfirmMutations: true}
	reply, err := runner.Resume(ctx, sess, pending, approved)

	s.finishChatTurn(ctx, hh, sess, reply, err, send)
}

// finishChatTurn is the tail every assistant turn shares: report a failure,
// hold a pending call, or save and send the answer.
func (s *Server) finishChatTurn(
	ctx context.Context,
	hh *db.Household,
	sess *agent.Session,
	reply agent.Reply,
	err error,
	send func(string, any),
) {
	if err != nil {
		log.Printf("chat: run: %v", err)
		send("error", map[string]any{"error": err.Error()})
		// Whatever ran is still recorded: partial work that is visible is
		// recoverable, work that is silently discarded is not.
		if len(sess.Audit) > 0 {
			s.saveAssistantTurn(ctx, hh.ID, "I ran into a problem partway through.", sess.Audit)
		}
		return
	}

	// Paused on a change. Nothing is saved to the conversation yet - the turn
	// is not over, and recording "the assistant did X" before X has been
	// agreed to would be a lie in the transcript.
	if reply.Pending != nil {
		s.putPending(hh.ID, reply.Pending)
		send("confirm", map[string]any{
			"tool":        reply.Pending.Tool,
			"description": reply.Pending.Description,
			"detail":      reply.Pending.Detail,
		})
		return
	}

	text := reply.Text
	if reply.HitLimit {
		text += " (I stopped after several steps rather than keep going.)"
	}
	s.saveAssistantTurn(ctx, hh.ID, text, reply.Audit)

	send("done", chatTurn{Role: "assistant", Content: text, Audit: reply.Audit})
}

// putPending parks a held call, replacing any earlier one for the household.
//
// Replacing rather than queueing: a new question supersedes an unanswered
// prompt, and a stack of stale "apply?" boxes is worse than losing one.
func (s *Server) putPending(householdID int64, p *agent.Pending) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	s.pendingChat[householdID] = p
}

// takePending removes and returns a held call, so it can only be answered
// once - two tabs racing must not apply the same change twice.
func (s *Server) takePending(householdID int64) *agent.Pending {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	p := s.pendingChat[householdID]
	delete(s.pendingChat, householdID)
	return p
}

func (s *Server) saveAssistantTurn(ctx context.Context, householdID int64, text string, audit []agent.AuditEntry) {
	blob, err := json.Marshal(audit)
	if err != nil {
		blob = []byte("[]")
	}
	// Detached from the request: the browser closing the SSE connection the
	// moment it has the answer must not lose the record of what was done.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := s.store.AppendChatMessage(saveCtx, householdID, "assistant", text, string(blob)); err != nil {
		log.Printf("chat: save assistant turn: %v", err)
	}
}
