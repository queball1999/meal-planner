package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goeat/agent"
	"goeat/catalog"
	"goeat/db"
	"goeat/middleware"
	"goeat/plan"
)

// chatReviewMessage is the user's side of a "Correct meal plan" turn in the
// conversation. The problem list sent to the model is not what the person
// said, and it is shown anyway as the first step of the reply.
const chatReviewMessage = "Correct the meal plan."

// chatCookMoreStep labels the fix handleChatReview makes itself. chat.js
// knows it as a change (MUTATING), so the page behind the panel reloads.
const chatCookMoreStep = "cook_more"

// chatLeftoversStep labels the other fix it makes itself, a lunch turned into
// the leftovers of the dinner before. The assistant's own tool of that name
// does the same thing, so chat.js already knows it as a change.
const chatLeftoversStep = "use_leftovers"

// chatMergeItemsStep labels shopping items combined into one
// (consolidatePlanItems). Also a change as far as chat.js is concerned.
const chatMergeItemsStep = "merge_items"

// handleChatReview is the plan page's "Correct meal plan": check the plan for
// contradictions, fix them, and say what was done - in the chat panel, as an
// ordinary turn, so the work is visible and can be followed up in words.
//
// The checking is code (plan.ReviewPlan) and so is the one fix that is a sum,
// a meal not cooking enough for its leftovers. What is left needs a dish
// chosen, and that goes to the assistant with the problems already listed.
// Its changes are not held for a yes one at a time as a chat request's are:
// pressing the button is the yes, and the page asked before sending it.
func (s *Server) handleChatReview(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Error(w, "no household", http.StatusUnauthorized)
		return
	}
	planID, err := strconv.ParseInt(r.FormValue("plan_id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "which plan?"})
		return
	}
	if !s.owns(w, r, db.ResPlan, planID) {
		return
	}
	ctx := r.Context()
	p, err := s.store.GetPlanByID(ctx, planID)
	if err != nil || p == nil {
		http.NotFound(w, r)
		return
	}

	// In the household's own day, as the assistant is told it (agent
	// householdContext): near midnight the server's date and theirs differ.
	now := time.Now()
	if loc, lerr := time.LoadLocation(hh.Timezone); lerr == nil && hh.Timezone != "" {
		now = now.In(loc)
	}
	today := now.Format("2006-01-02")
	switch {
	case p.Canceled || p.WeekEnd < today:
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "That plan is kept for reference and can't be changed."})
		return
	case p.Status == "generating":
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "That plan is still being generated. Correct it once it has finished."})
		return
	}

	review := func() ([]plan.Issue, int, error) {
		meals, err := s.store.ListMealsByPlan(ctx, p.ID)
		if err != nil {
			return nil, 0, err
		}
		days, err := s.store.ListPlanDays(ctx, p.ID)
		if err != nil {
			return nil, 0, err
		}
		return plan.ReviewPlan(p, meals, days, today), len(meals), nil
	}
	issues, mealCount, err := review()
	if err != nil {
		log.Printf("chat review: plan %d: %v", p.ID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "couldn't read the plan"})
		return
	}

	send, ok := openChatStream(w)
	if !ok {
		return
	}

	if _, err := s.store.AppendChatMessage(ctx, hh.ID, "user", chatReviewMessage, ""); err != nil {
		log.Printf("chat review: save user turn: %v", err)
	}

	sess := &agent.Session{Store: s.store, Household: hh, HouseholdID: hh.ID, PlanID: p.ID}
	sess.OnStep = func(e agent.AuditEntry) { send("step", e) }

	if len(issues) == 0 {
		text := fmt.Sprintf("I checked the plan's %d meals from today on: no empty slots, and every leftover has a meal cooked before it that makes enough.", mealCount)
		if merged := s.consolidatePlanItems(ctx, hh, p, sess, today); merged != "" {
			text += " " + merged
		} else {
			text += " Nothing to correct."
		}
		s.finishChatTurn(ctx, hh, sess, agent.Reply{Text: text, Audit: sess.Audit}, nil, send)
		return
	}
	sess.Note("plan_check", fmt.Sprintf("Found %d %s: %s.",
		len(issues), pluralize(len(issues), "problem", "problems"), joinIssues(issues)))

	fixed, open, err := plan.FixCookShort(ctx, s.store, issues)
	for _, is := range fixed {
		sess.Note(chatCookMoreStep, fmt.Sprintf("%q on %s now cooks %d more %s to cover its leftovers.",
			is.Meal.Title, dayLabel(is.Day), is.Short, pluralize(is.Short, "portion", "portions")))
	}
	if len(fixed) > 0 {
		// No days named: only the amounts grew, and a rescale from the day's
		// headcount is not what changed (ExtendMealCooked moves the base too).
		sess.PlanChanged = true
	}
	if err != nil {
		s.finishChatTurn(ctx, hh, sess, agent.Reply{}, err, send)
		return
	}

	// A lunch with nothing sound planned has one obvious answer where
	// leftovers are welcome - last night's dinner - so that is code too, not
	// left to the model to think of.
	prefs, _ := s.store.GetPreferences(ctx, hh.ID)
	leftovers := prefs != nil && prefs.LeftoverTolerance
	var reheated []plan.LeftoverFix
	if leftovers {
		reheated, open, err = plan.FixWithLeftovers(ctx, s.store, p.ID, open, float64(hh.HouseholdSize))
		for _, fx := range reheated {
			note := fmt.Sprintf("Put %q on %s %s for %d, from %s dinner", fx.Result.Title,
				dayLabel(fx.Issue.Day), fx.Issue.Slot, fx.Result.Servings, dayLabel(fx.Source.Day))
			if fx.Result.ExtraCooked > 0 {
				note += fmt.Sprintf(", which now cooks %d more", fx.Result.ExtraCooked)
			}
			sess.Note(chatLeftoversStep, note+".")
		}
		if len(reheated) > 0 {
			sess.PlanChanged = true
		}
		if err != nil {
			s.finishChatTurn(ctx, hh, sess, agent.Reply{}, err, send)
			return
		}
	}

	reply := agent.Reply{Audit: sess.Audit}
	switch {
	case len(open) == 0 && len(reheated) == 0:
		reply.Text = "That was all of it - the plan now cooks enough for every leftover on it."
	case len(open) == 0:
		reply.Text = fmt.Sprintf("That was all of it - %d %s now the leftovers of the dinner before, and each of those dinners cooks enough to cover it.",
			len(reheated), pluralize(len(reheated), "lunch is", "lunches are"))
	case s.llmGen() == nil:
		reply.Text = "The rest needs a meal chosen, and no AI provider is configured to choose one."
	default:
		reg := agent.NewRegistry()
		agent.RegisterAll(reg)
		runner := &agent.Runner{Gen: s.llmGen(), Registry: reg, MaxSteps: agent.ReviewSteps(len(open))}
		// The model is told what is left after every change, from the plan
		// itself: left to remember, it refills slots it has already filled
		// and runs out of steps before the last one.
		runner.AfterChange = func() string {
			left, _, rerr := review()
			switch {
			case rerr != nil:
				return ""
			case len(left) == 0:
				return "Every problem is now fixed. Change nothing else - say what you changed."
			}
			return "Still to fix: " + joinIssues(left) + ". Every other slot is done - do not write to it again."
		}
		reply, err = runner.Run(ctx, sess, nil, agent.ReviewRequest(open, leftovers))
		if err != nil {
			s.finishChatTurn(ctx, hh, sess, reply, err, send)
			return
		}
	}

	// Checked again rather than taken on the assistant's word: what it says it
	// fixed and what the plan now holds are not always the same thing.
	if left, _, rerr := review(); rerr == nil && len(left) > 0 {
		reply.Text = strings.TrimSpace(reply.Text) + fmt.Sprintf(" Still not right: %s.", joinIssues(left))
	}
	// Last, so the meals the fixes above put on the plan are looked at too.
	if merged := s.consolidatePlanItems(ctx, hh, p, sess, today); merged != "" {
		reply.Text = strings.TrimSpace(reply.Text) + " " + merged
		reply.Audit = sess.Audit
	}
	s.finishChatTurn(ctx, hh, sess, reply, nil, send)
}

// consolidatePlanItems is the shopping-list half of "Correct meal plan": items
// on the plan that are one thing to buy under several names become one item,
// renamed in the plan's meals and the saved recipes
// (catalog.ConsolidatePlanItems). Each set is noted as a step, and the return
// is the sentence for the reply, "" when nothing was combined. It needs a
// model to say which names match, so without one it does nothing; a failure
// is logged and likewise changes nothing - the plan's own fixes still stand.
func (s *Server) consolidatePlanItems(ctx context.Context, hh *db.Household, p *db.Plan, sess *agent.Session, today string) string {
	gen := s.llmGen()
	if gen == nil {
		return ""
	}
	sets, err := catalog.ConsolidatePlanItems(ctx, s.store, gen, hh.ID, p.ID, today)
	if err != nil {
		log.Printf("chat review: consolidate items for plan %d: %v", p.ID, err)
		return ""
	}
	merged := 0
	var kept []string
	for _, c := range sets {
		if len(c.Merged) > 0 {
			merged += len(c.Merged)
			kept = append(kept, c.Kept.Name)
			sess.Note(chatMergeItemsStep, fmt.Sprintf("%s %s now %q, on the shopping list, in this plan's meals and in your saved recipes.",
				quotedList(c.Merged), pluralize(len(c.Merged), "is", "are"), c.Kept.Name))
		}
		if len(c.Skipped) > 0 {
			sess.Note("item_check", fmt.Sprintf("%s %s like %q too, but %s a catalog item of its own rather than a placeholder, so I left it.",
				quotedList(c.Skipped), pluralize(len(c.Skipped), "looks", "look"), c.Kept.Name, pluralize(len(c.Skipped), "it is", "each is")))
		}
	}
	if merged == 0 {
		return ""
	}
	// The list has to be rebuilt for the merged lines to become one.
	sess.PlanChanged = true
	return fmt.Sprintf("I also combined %d shopping %s that %s the same thing under another name, now %s.",
		merged, pluralize(merged, "item", "items"), pluralize(merged, "was", "were"), quotedList(kept))
}

// quotedList is `"a", "b" and "c"`.
func quotedList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = strconv.Quote(n)
	}
	if len(q) < 2 {
		return strings.Join(q, "")
	}
	return strings.Join(q[:len(q)-1], ", ") + " and " + q[len(q)-1]
}

func joinIssues(issues []plan.Issue) string {
	parts := make([]string, 0, len(issues))
	for _, is := range issues {
		parts = append(parts, is.String())
	}
	return strings.Join(parts, "; ")
}
