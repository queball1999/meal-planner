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
var (
	pricingCancelsMu sync.Mutex
	pricingCancels   = map[int64]context.CancelFunc{}
)

// RegisterPricingCancel records cancel as the way to abort planID's in-flight
// background pricing run. Call ClearPricingCancel once that run finishes.
func RegisterPricingCancel(planID int64, cancel context.CancelFunc) {
	pricingCancelsMu.Lock()
	pricingCancels[planID] = cancel
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
func StopPricing(planID int64) bool {
	pricingCancelsMu.Lock()
	cancel, ok := pricingCancels[planID]
	pricingCancelsMu.Unlock()
	if ok {
		cancel()
	}
	return ok
}
