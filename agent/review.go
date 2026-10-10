package agent

import (
	"fmt"
	"strings"

	"goeat/plan"
)

// reviewStepsPerIssue is the step allowance each problem adds to a correction
// run: a search, a write, and one retry after a tool error.
const reviewStepsPerIssue = 3

// maxReviewSteps caps a correction run however broken the plan is. An empty
// week is 21 slots; past that the plan wants generating, not correcting.
const maxReviewSteps = 48

// ReviewSteps is the step budget for correcting n problems. MaxSteps is sized
// for one chat request and would stop a week with four empty slots halfway.
func ReviewSteps(n int) int {
	// The fixed part is reading the plan first and answering last, with room
	// for a stray reply: a single problem still has to fit a search, a write
	// and a retry between them.
	steps := 4 + n*reviewStepsPerIssue
	if steps > maxReviewSteps {
		steps = maxReviewSteps
	}
	return steps
}

// ReviewRequest is the message that sets the assistant to correcting a plan.
//
// The problems are found by plan.ReviewPlan and listed here rather than left
// for the model to spot: it is unreliable at noticing that a leftover comes
// before its dinner, and good at choosing what should go there instead.
// leftovers is the household's leftover tolerance - with it off, an empty slot
// is never filled by reheating something.
func ReviewRequest(issues []plan.Issue, leftovers bool) string {
	var b strings.Builder
	b.WriteString("Correct the meal plan. A check of it found these problems:\n")
	for i, is := range issues {
		fmt.Fprintf(&b, "%d. %s\n", i+1, is.String())
	}
	b.WriteString("\nFix every one of them, and change nothing else on the plan.\n")
	b.WriteString("- Call read_plan first, so that nothing you add repeats a dish the week already has.\n")
	if leftovers {
		b.WriteString("- A slot with no meal gets one. For a lunch or dinner, use_leftovers of a meal cooked in the three days before it is a good answer for some of them, not all. Otherwise search_recipes and fill_slot, or create_meal for a dish the week does not have yet.\n")
		b.WriteString("- A leftovers meal with nothing to reheat is replaced in its own slot: use_leftovers of a meal that is cooked before it (the error names what is available if you pick wrong), or a fresh meal as above. Leave the meal it was meant to be leftovers of where it is.\n")
	} else {
		b.WriteString("- A slot with no meal gets one: search_recipes and fill_slot, or create_meal for a dish the week does not have yet. This household does not plan leftovers, so do not use use_leftovers.\n")
		b.WriteString("- A leftovers meal with nothing to reheat is replaced in its own slot with a fresh meal as above.\n")
	}
	b.WriteString("- A locked meal cannot be replaced. Leave it and say so.\n")
	b.WriteString("When every problem is dealt with, say in a sentence or two what you changed and name anything you could not fix.")
	return b.String()
}
