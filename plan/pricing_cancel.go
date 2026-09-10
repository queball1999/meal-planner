package plan

import (
	"context"
	"sync"
)

// pricingCancels tracks the cancel func for whichever background pricing run
// (fresh-plan costing via priceInBackground, or an explicit reprice via
// web.repriceInBackground) is currently in flight for a plan, so the shopping
// list's "stop pricing" control has something to call. At most one entry per
// plan: neither caller starts a second background run for the same plan while
// one is already active.
//
// stoppedPlans remembers which plans had their pricing run stopped, so a page
// visited after the run has already ended - or one that was still winding
// down when the user navigated away and back - still reports "stopped"
// instead of "still pricing" just because a slow provider call left a line
// or two genuinely pending. Cleared the next time a background run actually
// starts for that plan (RegisterPricingCancel), so a later regenerate/reprice
// isn't permanently suppressed by an old stop.
var (
	pricingCancelsMu sync.Mutex
	pricingCancels   = map[int64]context.CancelFunc{}
	stoppedPlans     = map[int64]bool{}
)

// RegisterPricingCancel records cancel as the way to abort planID's in-flight
// background pricing run, and clears any earlier stopped state for it - a new
// run means pricing is genuinely in progress again. Call ClearPricingCancel
// once that run finishes.
func RegisterPricingCancel(planID int64, cancel context.CancelFunc) {
	pricingCancelsMu.Lock()
	pricingCancels[planID] = cancel
	delete(stoppedPlans, planID)
	pricingCancelsMu.Unlock()
}

// ClearPricingCancel removes planID's registered cancel func once its
// background pricing run has finished.
func ClearPricingCancel(planID int64) {
	pricingCancelsMu.Lock()
	delete(pricingCancels, planID)
	pricingCancelsMu.Unlock()
}

// StopPricing cancels the in-flight background pricing run for planID, if
// any, and reports whether one was found. Canceling lets whatever pricing
// already resolved land (ResolvePricing's finalize pass is detached from the
// canceled context) while aborting any resolve/AI-estimate call still in
// flight and skipping every call after it.
//
// Also marks planID as stopped (see IsPricingStopped) even when no cancel
// func was found - the run may have already finished, or may still be
// winding down a slow provider call the canceled context can't interrupt any
// faster - so a later page view still knows to report "stopped" rather than
// "still pricing".
func StopPricing(planID int64) bool {
	pricingCancelsMu.Lock()
	cancel, ok := pricingCancels[planID]
	stoppedPlans[planID] = true
	pricingCancelsMu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// IsPricingStopped reports whether planID's pricing was last stopped by the
// user rather than left to finish or never started. The shopping list view
// uses this to keep showing "stopped" instead of "still pricing" for any
// line a stopped-but-slow-to-abort provider call left pending.
func IsPricingStopped(planID int64) bool {
	pricingCancelsMu.Lock()
	defer pricingCancelsMu.Unlock()
	return stoppedPlans[planID]
}
