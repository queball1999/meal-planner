package web

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"goeat/db"
	"goeat/llm"
	"goeat/middleware"
)

// checkboxOption holds a single checkbox state for the template.
type checkboxOption struct {
	Value   string
	Label   string
	Checked bool
}

// hintRow extends MealSlotHint with a display label for the template.
type hintRow struct {
	*db.MealSlotHint
	SlotLabel string
}

type preferencesPageData struct {
	Members           []memberRow
	PortionPresets    []portionPreset
	TotalPortions     float64
	Hints             []hintRow
	DietTagOptions    []checkboxOption
	CuisineOptions    []checkboxOption
	AllergiesCSV      string
	DislikesCSV       string
	LeftoverTolerance bool
	UnitSystem        string // "as-is" | "metric" | "imperial"
	HasLLM            bool
}

var dietTagDefs = []struct{ value, label string }{
	{"vegetarian", "Vegetarian"},
	{"vegan", "Vegan"},
	{"pescatarian", "Pescatarian"},
	{"keto", "Keto"},
	{"low-carb", "Low-carb"},
	{"gluten-free", "Gluten-free"},
}

var cuisineDefs = []string{
	"Italian", "Mexican", "Thai", "Chinese", "Japanese",
	"Indian", "Mediterranean", "American", "French", "Korean",
}

var slotLabels = map[string]string{
	"breakfast": "Breakfast",
	"lunch":     "Lunch",
	"dinner":    "Dinner",
}

func buildPreferencesPageData(prefs *db.Preferences, allergies []string, hints []*db.MealSlotHint, members []*db.HouseholdMember, hasLLM bool) preferencesPageData {
	// Diet tag checkboxes
	tagSet := sliceToSet(prefs.DietTags)
	tagOpts := make([]checkboxOption, len(dietTagDefs))
	for i, d := range dietTagDefs {
		tagOpts[i] = checkboxOption{Value: d.value, Label: d.label, Checked: tagSet[d.value]}
	}

	// Cuisine checkboxes
	cuisineSet := sliceToSet(prefs.Cuisines)
	cuisineOpts := make([]checkboxOption, len(cuisineDefs))
	for i, c := range cuisineDefs {
		cuisineOpts[i] = checkboxOption{Value: c, Label: c, Checked: cuisineSet[c]}
	}

	// Hint rows with display labels
	rows := make([]hintRow, len(hints))
	for i, h := range hints {
		rows[i] = hintRow{MealSlotHint: h, SlotLabel: slotLabels[h.Slot]}
	}

	return preferencesPageData{
		Members:           memberRows(members),
		PortionPresets:    portionPresets,
		TotalPortions:     totalPortions(members),
		Hints:             rows,
		DietTagOptions:    tagOpts,
		CuisineOptions:    cuisineOpts,
		AllergiesCSV:      strings.Join(allergies, ", "),
		DislikesCSV:       strings.Join(prefs.Dislikes, ", "),
		LeftoverTolerance: prefs.LeftoverTolerance,
		UnitSystem:        prefs.UnitSystem,
		HasLLM:            hasLLM,
	}
}

func sliceToSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

func (s *Server) handlePreferencesPage(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()

	prefs, _ := s.store.GetPreferences(ctx, hh.ID)
	allergies, _ := s.store.ListAllergies(ctx, hh.ID)
	hints, _ := s.store.GetMealSlotHints(ctx, hh.ID)
	members, _ := s.store.ListHouseholdMembers(ctx, hh.ID)

	s.render(w, r, "preferences", buildPreferencesPageData(prefs, allergies, hints, members, s.llmGen() != nil))
}

// handlePreferences saves all preference data and optionally parses free-text
// descriptions via the configured LLM (§4.1).
func (s *Server) handlePreferences(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	u := middleware.UserFromCtx(r)

	if err := r.ParseForm(); err != nil {
		s.setNotify(w, NotifyDanger, "Could not read form data.")
		http.Redirect(w, r, "/preferences", http.StatusSeeOther)
		return
	}

	// ── Structured preferences ──────────────────────────────────────────────
	dietTags := r.Form["diet_tags"]
	cuisines := r.Form["cuisines"]
	dislikes := splitTrimmed(r.FormValue("dislikes"))
	leftover := r.FormValue("leftover_tolerance") == "1"

	unitSystem := r.FormValue("unit_system")
	switch unitSystem {
	case "metric", "imperial", "as-is":
	default:
		unitSystem = "as-is"
	}

	if err := s.store.UpsertPreferences(ctx, db.UpsertPreferencesParams{
		HouseholdID:       hh.ID,
		DietTags:          dietTags,
		Cuisines:          cuisines,
		Dislikes:          dislikes,
		LeftoverTolerance: leftover,
		UnitSystem:        unitSystem,
	}); err != nil {
		log.Printf("preferences: upsert: %v", err)
		s.setNotify(w, NotifyDanger, "Could not save preferences. Please try again.")
		http.Redirect(w, r, "/preferences", http.StatusSeeOther)
		return
	}

	// ── Allergies ───────────────────────────────────────────────────────────
	allergyTerms := splitTrimmed(r.FormValue("allergies"))
	if err := s.store.SetAllergies(ctx, hh.ID, allergyTerms); err != nil {
		log.Printf("preferences: set allergies: %v", err)
	}

	// ── Meal slot hints (free-text + effort) ────────────────────────────────
	parsed := false
	for _, slot := range []string{"breakfast", "lunch", "dinner"} {
		rawText := strings.TrimSpace(r.FormValue("hint_" + slot))
		effort := r.FormValue("effort_" + slot)
		if effort == "" {
			effort = "standard"
		}

		parsedJSON := "null"
		var inToks, outToks int

		if rawText != "" && s.llmGen() != nil {
			var err error
			parsedJSON, inToks, outToks, err = llm.ParseMealDescription(ctx, s.llmGen(), slot, rawText)
			if err != nil {
				log.Printf("preferences: parse %s: %v", slot, err)
			} else if parsedJSON != "null" {
				parsed = true
				if u != nil {
					id := u.ID
					s.logEvent(r, &id, "parse_meal_description", "meal_slot_hint", slot, "")
				}
				if _, err := s.store.CreateAIRun(ctx, db.CreateAIRunParams{
					HouseholdID:      hh.ID,
					Purpose:          "free_text_parse",
					Provider:         s.llmGen().ProviderName(),
					Model:            s.llmGen().ModelName(),
					PromptTokens:     inToks,
					CompletionTokens: outToks,
					Status:           "ok",
				}); err != nil {
					log.Printf("preferences: create ai run: %v", err)
				}
			}
		}

		if err := s.store.UpsertMealSlotHint(ctx, db.UpsertMealSlotHintParams{
			HouseholdID: hh.ID,
			Slot:        slot,
			RawText:     rawText,
			ParsedJSON:  parsedJSON,
			Effort:      effort,
		}); err != nil {
			log.Printf("preferences: upsert slot hint %s: %v", slot, err)
		}
	}

	msg := "Preferences saved."
	if parsed {
		msg = "Preferences saved and meal descriptions parsed."
	}
	s.setNotify(w, NotifySuccess, msg)
	if u != nil {
		id := u.ID
		s.logEvent(r, &id, "update_preferences", "household", fmt.Sprintf("%d", hh.ID), "")
	}
	http.Redirect(w, r, "/preferences", http.StatusSeeOther)
}

// splitTrimmed splits a comma-separated string and trims whitespace.
func splitTrimmed(s string) []string {
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
