package plan

import (
	"context"

	"goeat/db"
)

// Resolve composes the PreferenceProfile from all four capture mechanisms
// in the fixed order specified in §4.6:
// hard constraints (allergies) → diet → structured prefs → free-text hints → feedback digest.
func Resolve(ctx context.Context, store db.Store, householdID int64) (*PreferenceProfile, error) {
	prefs, err := store.GetPreferences(ctx, householdID)
	if err != nil {
		return nil, err
	}

	allergies, err := store.ListAllergies(ctx, householdID)
	if err != nil {
		return nil, err
	}

	hints, err := store.GetMealSlotHints(ctx, householdID)
	if err != nil {
		return nil, err
	}

	// Map hints by slot for easy lookup.
	hintMap := make(map[string]*db.MealSlotHint, 3)
	for _, h := range hints {
		hintMap[h.Slot] = h
	}
	slotText := func(slot string) string {
		if h, ok := hintMap[slot]; ok {
			return h.RawText
		}
		return ""
	}
	slotEffort := func(slot string) string {
		if h, ok := hintMap[slot]; ok && h.Effort != "" {
			return h.Effort
		}
		return "standard"
	}

	// Who the household actually cooks for. A household that has never set
	// members up gets none, and the prompt falls back to household size with
	// everyone eating a standard portion.
	members, err := store.ListHouseholdMembers(ctx, householdID)
	if err != nil {
		return nil, err
	}

	// Feedback digest: up to 20 most recent entries.
	feedback, err := store.ListFeedbackDigest(ctx, householdID, 20)
	if err != nil {
		return nil, err
	}
	var liked, disliked []string
	for _, f := range feedback {
		if f.Rating == 1 {
			liked = append(liked, f.Title)
		} else {
			disliked = append(disliked, f.Title)
		}
	}

	return &PreferenceProfile{
		Allergies:         allergies,
		DietTags:          prefs.DietTags,
		Cuisines:          prefs.Cuisines,
		Dislikes:          prefs.Dislikes,
		LeftoverTolerance: prefs.LeftoverTolerance,
		HintsBreakfast:    slotText("breakfast"),
		HintsLunch:        slotText("lunch"),
		HintsDinner:       slotText("dinner"),
		EffortBreakfast:   slotEffort("breakfast"),
		EffortLunch:       slotEffort("lunch"),
		EffortDinner:      slotEffort("dinner"),
		FeedbackLiked:     liked,
		FeedbackDisliked:  disliked,
		Members:           members,
	}, nil
}
