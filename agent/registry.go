// Package agent gives the assistant typed, granular control over the site's
// own data: one tool per verb, each household-scoped, each individually
// describable to a model.
//
// Granular on purpose. A single "update_plan" tool taking a free-form patch
// would be shorter to write and far worse to use: the model could not be told
// what it is allowed to change, a bad call could rewrite a whole week, and
// there would be nothing meaningful to show a user before applying it. One
// verb per tool means every call has a narrow blast radius, an argument schema
// the model is held to, and a result specific enough to render.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"goeat/db"
)

// Session is the context one run of the agent operates in. Every tool reads
// its household from here rather than from its arguments, so a model cannot
// reach another household's data by passing an id - the ids it passes are
// always checked against this household.
type Session struct {
	Store       db.Store
	Household   *db.Household
	HouseholdID int64

	// Audit records what the assistant actually did, in order. The UI shows
	// it so a user can see the work rather than trusting a summary.
	Audit []AuditEntry

	// OnStep, when set, is called with each audit entry as it is recorded.
	// A turn can run several tools and take a while, and a UI that says
	// nothing until it finishes is worse than no assistant at all. Called
	// synchronously from the run, so a slow handler slows the run - keep it to
	// writing one event.
	OnStep func(AuditEntry)
}

// AuditEntry is one tool call and how it went.
type AuditEntry struct {
	Tool    string `json:"tool"`
	Args    string `json:"args"`
	Summary string `json:"summary"`
	Error   string `json:"error,omitempty"`
}

// Tool is one thing the assistant can do.
type Tool struct {
	Name        string
	Description string

	// Params is the tool's arguments as JSON Schema properties, with Required
	// naming the mandatory ones. Kept as data rather than reflected off a Go
	// struct so the description a model sees is written deliberately - the
	// wording of a parameter description is most of what makes a tool usable.
	Params   map[string]Param
	Required []string

	// Mutates marks a tool that changes data. Read-only tools can run freely;
	// mutating ones are audited and, where the caller asks, confirmed.
	Mutates bool

	Run func(ctx context.Context, s *Session, args json.RawMessage) (Result, error)
}

// Param is one argument in a tool's schema.
type Param struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Enum        []string `json:"enum,omitempty"`
}

// Result is what a tool hands back: a line for the transcript, and optional
// structured data for the model to reason over.
type Result struct {
	// Summary is one sentence in plain language, shown to the user and fed
	// back to the model. "Moved Chicken Quesadillas to Monday dinner."
	Summary string `json:"summary"`
	// Data is whatever the model needs to continue - a plan, a list of meals.
	// Omitted for a mutation whose summary says everything.
	Data any `json:"data,omitempty"`
}

// Registry is the set of tools available to one agent run.
type Registry struct {
	tools map[string]*Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]*Tool{}}
}

// Register adds a tool. Panics on a duplicate name: two tools answering to one
// name is a wiring mistake that must not survive to runtime, where it would
// silently resolve to whichever registered last.
func (r *Registry) Register(t *Tool) {
	if t == nil || t.Name == "" {
		panic("agent: tool must have a name")
	}
	if _, dup := r.tools[t.Name]; dup {
		panic("agent: duplicate tool " + t.Name)
	}
	r.tools[t.Name] = t
}

// Get returns a tool by name, or nil.
func (r *Registry) Get(name string) *Tool {
	return r.tools[name]
}

// Names lists every tool, sorted, so prompts and tests are deterministic.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.tools))
	for n := range r.tools {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ErrUnknownTool is returned when a model calls something that does not exist.
// Handed back to the model rather than aborting the run: models mistype tool
// names, and one correction is cheaper than a failed conversation.
type ErrUnknownTool struct{ Name string }

func (e ErrUnknownTool) Error() string {
	return fmt.Sprintf("no tool named %q", e.Name)
}

// Call runs one tool by name and records it in the session audit.
//
// A tool's own error is returned, not swallowed - the loop feeds it back to
// the model so it can correct itself - but it is audited either way, because
// "the assistant tried X and it failed" is exactly what a user needs to see
// when the answer looks wrong.
func (r *Registry) Call(ctx context.Context, s *Session, name string, args json.RawMessage) (Result, error) {
	t := r.tools[name]
	if t == nil {
		return Result{}, ErrUnknownTool{Name: name}
	}
	if err := validateArgs(t, args); err != nil {
		s.record(AuditEntry{Tool: name, Args: string(args), Error: err.Error()})
		return Result{}, err
	}

	res, err := t.Run(ctx, s, args)
	entry := AuditEntry{Tool: name, Args: string(args), Summary: res.Summary}
	if err != nil {
		entry.Error = err.Error()
	}
	s.record(entry)
	return res, err
}

// record appends to the audit and notifies any watcher.
func (s *Session) record(e AuditEntry) {
	s.Audit = append(s.Audit, e)
	if s.OnStep != nil {
		s.OnStep(e)
	}
}

// validateArgs checks required parameters are present before a tool runs.
//
// Cheap, and it turns the most common model mistake - omitting an argument -
// into a precise message it can act on, instead of a nil-dereference or a
// silently zero id deep inside a query.
func validateArgs(t *Tool, args json.RawMessage) error {
	if len(t.Required) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if len(args) > 0 {
		if err := json.Unmarshal(args, &m); err != nil {
			return fmt.Errorf("arguments for %s are not a JSON object: %w", t.Name, err)
		}
	}
	var missing []string
	for _, req := range t.Required {
		if v, ok := m[req]; !ok || len(v) == 0 || string(v) == "null" {
			missing = append(missing, req)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s needs %s", t.Name, strings.Join(missing, " and "))
	}
	return nil
}

// Describe renders the whole tool surface for a system prompt.
//
// Written by hand rather than dumped as JSON Schema: a compact, readable list
// costs a fraction of the tokens and, in practice, models follow it more
// reliably than they follow a wall of nested schema objects.
func (r *Registry) Describe() string {
	var b strings.Builder
	for _, name := range r.Names() {
		t := r.tools[name]
		b.WriteString("- ")
		b.WriteString(t.Name)
		b.WriteString("(")
		params := make([]string, 0, len(t.Params))
		for pname := range t.Params {
			params = append(params, pname)
		}
		sort.Strings(params)
		for i, pname := range params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(pname)
			if !contains(t.Required, pname) {
				b.WriteString("?")
			}
		}
		b.WriteString("): ")
		b.WriteString(t.Description)
		if t.Mutates {
			b.WriteString(" [changes data]")
		}
		b.WriteString("\n")
		for _, pname := range params {
			p := t.Params[pname]
			b.WriteString("    ")
			b.WriteString(pname)
			b.WriteString(" (")
			b.WriteString(p.Type)
			if len(p.Enum) > 0 {
				b.WriteString(": ")
				b.WriteString(strings.Join(p.Enum, "|"))
			}
			b.WriteString(") ")
			b.WriteString(p.Description)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
