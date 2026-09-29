package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"goeat/llm"
)

// MaxSteps caps how many tool calls one user turn may make.
//
// A model that misreads a tool result can loop on it indefinitely, and every
// step is a real database write against a real household. The cap is generous
// enough for genuine multi-step work ("move three meals and reprice") and hard
// enough that a confused model stops rather than churning.
const MaxSteps = 8

// Message is one turn of conversation as the model sees it.
type Message struct {
	Role    string `json:"role"` // "user" | "assistant"
	Content string `json:"content"`
}

// Reply is the outcome of one user turn.
type Reply struct {
	// Text is what to say back.
	Text string
	// Audit is every tool call made, in order, for the UI to render.
	Audit []AuditEntry
	// Steps is how many tool calls ran, so a caller can tell a plain answer
	// from a piece of work.
	Steps int
	// HitLimit is true when MaxSteps stopped the run rather than the model
	// finishing. The reply is still returned - partial work has been done and
	// hiding it would be worse - but the caller can say so.
	HitLimit bool

	// Pending is set when the run paused on a mutating call awaiting
	// confirmation. Text is empty in that case: the assistant has not finished
	// speaking, it is waiting.
	Pending *Pending
}

// The protocol.
//
// Deliberately a JSON convention rather than provider-native tool calling.
// This app talks to Anthropic and to any OpenAI-compatible endpoint, including
// local reasoning models that the llm package already has to work around
// (llm/openai.go strips <think> blocks and suppresses enable_thinking). Native
// tool-use is spelled differently on each of those and is missing or broken on
// several, whereas "reply with one JSON object" works on all of them and is
// straightforward to swap out for native calling later.
const protocolPrompt = `You control a household meal planner by calling tools.

Reply with exactly ONE JSON object and nothing else - no prose outside it, no
markdown fences.

To call a tool:
  {"tool": "read_plan", "args": {}}

To answer the user, when you have everything you need:
  {"say": "Moved chicken quesadillas to Monday dinner."}

Rules:
- One tool per reply. You will be given the result and can then call another.
- Read before you write. If you need a meal's id or the current list, call a
  read tool first rather than guessing.
- Tools marked [changes data] alter the household's real plan. Only call one
  when the user actually asked for that change.
- If a tool returns an error, read it - it usually says exactly what to fix -
  and either correct the call or explain the problem to the user.
- Never invent ids, prices, or quantities. If you do not know something, use a
  read tool or ask.
- When you are done, say what you did in one or two plain sentences. The user
  can see the list of actions, so do not narrate every step.

Available tools:
`

// Runner executes one user turn against the tool registry.
type Runner struct {
	Gen      llm.Generator
	Registry *Registry
	// MaxSteps overrides the package default when non-zero (tests).
	MaxSteps int

	// ConfirmMutations holds every tool marked Mutates until a person approves
	// it. Off by default so a caller with its own safeguards - or a test - is
	// not forced through the confirmation dance.
	ConfirmMutations bool
}

// Run answers one user message, calling tools as needed.
//
// history is prior turns for context; it is not mutated. The returned Reply
// always carries whatever work was done, even when the model errors partway -
// a half-finished change that is reported is recoverable, one that is silently
// discarded is not.
func (r *Runner) Run(ctx context.Context, s *Session, history []Message, userMsg string) (Reply, error) {
	// The running transcript the model sees. Tool results are appended as
	// plain text turns rather than a provider-specific tool-result type, for
	// the same portability reason the protocol itself is JSON.
	var b strings.Builder
	for _, m := range history {
		b.WriteString(strings.ToUpper(m.Role))
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	b.WriteString("USER: ")
	b.WriteString(userMsg)
	b.WriteString("\n")

	return r.step(ctx, s, b.String(), 0)
}

// Resume continues a run that paused for confirmation.
//
// Approving runs the held call; declining records the refusal in the
// transcript and lets the model carry on, which is what makes "no, the other
// Tuesday" a conversation rather than a dead end.
func (r *Runner) Resume(ctx context.Context, s *Session, p *Pending, approved bool) (Reply, error) {
	if p == nil {
		return Reply{}, fmt.Errorf("nothing was waiting to be confirmed")
	}
	transcript := p.Resume

	if !approved {
		transcript += fmt.Sprintf(
			"SYSTEM: the user declined the %s call. Do not retry it; ask what they want instead.\n", p.Tool)
		return r.step(ctx, s, transcript, 0)
	}

	res, err := r.Registry.Call(ctx, s, p.Tool, p.Args)
	if err != nil {
		transcript += fmt.Sprintf("TOOL %s ERROR: %s\n", p.Tool, err.Error())
	} else {
		transcript += fmt.Sprintf("TOOL %s OK: %s\n", p.Tool, res.Summary)
		if res.Data != nil {
			if blob, mErr := json.Marshal(res.Data); mErr == nil {
				transcript += "DATA: " + string(blob) + "\n"
			}
		}
	}
	// The approved call counts against the budget, so a conversation cannot
	// restart its own step allowance by pausing.
	return r.step(ctx, s, transcript, 1)
}

// step drives the model until it answers, asks for confirmation, errors, or
// runs out of steps. spent is how many steps the run has already used.
func (r *Runner) step(ctx context.Context, s *Session, transcript string, spent int) (Reply, error) {
	limit := r.MaxSteps
	if limit <= 0 {
		limit = MaxSteps
	}

	system := protocolPrompt + r.Registry.Describe() + "\n" + householdContext(s)

	var b strings.Builder
	b.WriteString(transcript)

	reply := Reply{Steps: spent}
	for step := spent; step < limit; step++ {
		resp, err := r.Gen.Generate(llm.WithPurpose(ctx, "chat"), llm.GenerateRequest{
			System: system,
			Prompt: b.String(),
			// Suppressed: the reply is one small JSON object, and a reasoning
			// model left to think freely spends its whole budget before
			// emitting any content - which arrives here as an empty response.
			SuppressReasoning: true,
			MaxTokens:         2048,
		})
		if err != nil {
			reply.Audit = s.Audit
			return reply, fmt.Errorf("assistant unavailable: %w", err)
		}

		action, perr := parseAction(resp.Content)
		if perr != nil {
			// One correction, then give up on the turn. Models do emit stray
			// prose; re-prompting once fixes it far more often than not, and
			// looping on it burns tokens for nothing.
			b.WriteString("SYSTEM: ")
			b.WriteString(perr.Error())
			b.WriteString(" Reply with one JSON object only.\n")
			continue
		}

		if action.Say != "" {
			reply.Text = action.Say
			reply.Audit = s.Audit
			return reply, nil
		}

		// Hold anything that changes data. Per call, not per turn: a run that
		// moves three meals asks three times rather than presenting one opaque
		// "apply 3 changes?", because the whole point is being able to reject
		// one of them.
		if t := r.Registry.Get(action.Tool); r.ConfirmMutations && t != nil && t.Mutates {
			reply.Audit = s.Audit
			reply.Pending = &Pending{
				Tool:        action.Tool,
				Args:        action.Args,
				Description: t.Description,
				Detail:      describeCall(t, action.Args),
				Resume:      b.String(),
			}
			return reply, nil
		}

		res, terr := r.Registry.Call(ctx, s, action.Tool, action.Args)
		reply.Steps++
		if terr != nil {
			// Errors go back to the model, not to the user: most are things it
			// can fix itself ("say which one", "that is not a day").
			b.WriteString(fmt.Sprintf("TOOL %s ERROR: %s\n", action.Tool, terr.Error()))
			continue
		}
		b.WriteString(fmt.Sprintf("TOOL %s OK: %s\n", action.Tool, res.Summary))
		if res.Data != nil {
			if blob, mErr := json.Marshal(res.Data); mErr == nil {
				b.WriteString("DATA: ")
				b.Write(blob)
				b.WriteString("\n")
			}
		}
	}

	// Out of steps. Report the work rather than pretending nothing happened.
	reply.Audit = s.Audit
	reply.HitLimit = true
	if reply.Text == "" {
		reply.Text = summarizeAudit(s.Audit)
	}
	return reply, nil
}

// action is one parsed model reply.
type action struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
	Say  string          `json:"say"`
}

// parseAction pulls the JSON object out of a model reply.
//
// Tolerant on purpose: models wrap JSON in ```json fences, prefix it with "Here
// you go:", and append a trailing sentence, none of which is worth failing a
// user's request over. Anything with no object at all is an error the model is
// told about.
func parseAction(raw string) (action, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return action{}, fmt.Errorf("empty reply")
	}
	s = stripFence(s)

	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return action{}, fmt.Errorf("no JSON object in the reply")
	}

	var a action
	if err := json.Unmarshal([]byte(s[start:end+1]), &a); err != nil {
		return action{}, fmt.Errorf("reply was not valid JSON: %v", err)
	}
	if a.Say == "" && a.Tool == "" {
		return action{}, fmt.Errorf(`reply had neither "tool" nor "say"`)
	}
	if a.Tool != "" && len(a.Args) == 0 {
		// A tool with no args is legitimate (read_plan), and an absent "args"
		// is far commoner than a malformed one.
		a.Args = json.RawMessage("{}")
	}
	return a, nil
}

func stripFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// summarizeAudit is the fallback answer when the model never produced one.
func summarizeAudit(audit []AuditEntry) string {
	var done []string
	for _, e := range audit {
		if e.Error == "" && e.Summary != "" {
			done = append(done, e.Summary)
		}
	}
	if len(done) == 0 {
		return "I couldn't work that one out. Try asking a different way?"
	}
	return "I got partway: " + strings.Join(done, " ") +
		" I stopped there rather than keep going - ask me to continue if that looks right."
}

// householdContext gives the model the handful of facts every request needs,
// so it does not spend a tool call discovering them.
func householdContext(s *Session) string {
	if s.Household == nil {
		return ""
	}
	return fmt.Sprintf("\nHousehold: %s, %d people, weekly budget $%.0f.\n",
		s.Household.Name, s.Household.HouseholdSize, float64(s.Household.WeeklyBudgetCents)/100)
}
