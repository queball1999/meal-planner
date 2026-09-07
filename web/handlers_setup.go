package web

import (
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goeat/auth"
	"goeat/catalog"
	"goeat/db"
	"goeat/middleware"
)

// usTimezones is the list offered in the setup wizard (US-only, §4.5).
var usTimezones = []struct{ Label, Value string }{
	{"Eastern (ET)", "America/New_York"},
	{"Central (CT)", "America/Chicago"},
	{"Mountain (MT)", "America/Denver"},
	{"Mountain - Arizona (no DST)", "America/Phoenix"},
	{"Pacific (PT)", "America/Los_Angeles"},
	{"Alaska (AKT)", "America/Anchorage"},
	{"Hawaii (HST)", "Pacific/Honolulu"},
}

type setupPageData struct {
	Timezones      []struct{ Label, Value string }
	Stores         []KnownStore
	DietTagOptions []checkboxOption
	CuisineOptions []checkboxOption
	KrogerReady    bool // true when Kroger API keys are configured
	HasLLM         bool // true when an LLM is configured (free-text parsing)
}

// handleSetupPage renders the first-run setup wizard.
// Redirects away if setup is already complete.
func (s *Server) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh != nil {
		if middleware.UserFromCtx(r) != nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
		} else {
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		}
		return
	}
	dietOpts := make([]checkboxOption, len(dietTagDefs))
	for i, d := range dietTagDefs {
		dietOpts[i] = checkboxOption{Value: d.value, Label: d.label}
	}
	cuisineOpts := make([]checkboxOption, len(cuisineDefs))
	for i, c := range cuisineDefs {
		cuisineOpts[i] = checkboxOption{Value: c, Label: c}
	}

	s.render(w, r, "setup", setupPageData{
		Timezones:      usTimezones,
		Stores:         KnownStores,
		DietTagOptions: dietOpts,
		CuisineOptions: cuisineOpts,
		KrogerReady:    s.cfg.KrogerClientID != "",
		HasLLM:         s.gen != nil,
	})
}

// handleSetup processes the setup wizard form (POST /setup).
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if middleware.HouseholdFromCtx(r) != nil {
		http.Error(w, "setup already complete", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		s.setNotify(w, NotifyDanger, "Invalid form submission")
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	passwordConfirm := r.FormValue("password_confirm")
	householdName := strings.TrimSpace(r.FormValue("household_name"))
	budgetStr := r.FormValue("budget")
	zipCode := strings.TrimSpace(r.FormValue("zip_code"))
	timezone := r.FormValue("timezone")
	sizeStr := r.FormValue("household_size")
	selectedStores := r.Form["stores"] // multi-value checkbox list

	// ── Validate ────────────────────────────────────────────────────────────

	if len(username) < 3 {
		s.setNotify(w, NotifyDanger, "Username must be at least 3 characters")
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if err := auth.ValidatePassword(password); err != nil {
		s.setNotify(w, NotifyDanger, err.Error())
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if password != passwordConfirm {
		s.setNotify(w, NotifyDanger, "Passwords do not match")
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if householdName == "" {
		householdName = "My Household"
	}

	budget, err := strconv.ParseFloat(budgetStr, 64)
	if err != nil || budget <= 0 || budget > 99999 {
		s.setNotify(w, NotifyDanger, "Budget must be a positive dollar amount")
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	budgetCents := int64(math.Round(budget * 100))

	hSize, err := strconv.Atoi(sizeStr)
	if err != nil || hSize < 1 || hSize > 20 {
		s.setNotify(w, NotifyDanger, "Household size must be between 1 and 20")
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	// Validate IANA timezone; fall back to Eastern if bogus.
	if _, err := time.LoadLocation(timezone); err != nil {
		timezone = "America/New_York"
	}

	ctx := r.Context()

	// ── Create account + household ───────────────────────────────────────────

	hash, err := auth.HashPassword(password)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	user, err := s.store.CreateUser(ctx, username, hash, "admin")
	if err != nil {
		s.setNotify(w, NotifyDanger, "Could not create account - username may already be taken")
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	hh, err := s.store.CreateHousehold(ctx, db.CreateHouseholdParams{
		Name:              householdName,
		WeeklyBudgetCents: budgetCents,
		Country:           "US",
		ZIPCode:           zipCode,
		Timezone:          timezone,
		HouseholdSize:     hSize,
	})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// ── Seed the grocery-item catalog for the new household ──────────────────
	_ = catalog.SeedGlobalConversions(ctx, s.store)
	if serr := catalog.SeedHousehold(ctx, s.store, hh.ID); serr != nil {
		log.Printf("setup: seed item catalog: %v", serr)
	}

	// ── Create selected stores ───────────────────────────────────────────────

	hasNonAPIStore := false
	for _, storeName := range selectedStores {
		ks := KnownStoreByName(storeName)
		if ks == nil {
			continue // ignore unknown names (safety)
		}
		created, cerr := s.store.CreateStore(ctx, db.UpsertStoreParams{
			HouseholdID: hh.ID,
			Name:        ks.Name,
			Kind:        ks.Kind,
		})
		if cerr == nil {
			s.ensureScrapeConfig(ctx, created.ID, ks)
		}
		if !ks.HasAPI {
			hasNonAPIStore = true
		}
	}

	// ── Save initial preferences ─────────────────────────────────────────────

	dietTags := r.Form["diet_tags"]
	cuisines := r.Form["cuisines"]
	allergies := splitTrimmed(r.FormValue("allergies"))
	dislikes := splitTrimmed(r.FormValue("dislikes"))
	leftover := r.FormValue("leftover_tolerance") == "1"

	_ = s.store.UpsertPreferences(ctx, db.UpsertPreferencesParams{
		HouseholdID:       hh.ID,
		DietTags:          dietTags,
		Cuisines:          cuisines,
		Dislikes:          dislikes,
		LeftoverTolerance: leftover,
	})
	if len(allergies) > 0 {
		_ = s.store.SetAllergies(ctx, hh.ID, allergies)
	}

	// ── What the household already eats ──────────────────────────────────────
	//
	// Free-text slot descriptions are the planner's anchor: it starts from a
	// household's real habits instead of inventing a diet. The LLM parse is
	// deliberately skipped here - setup should not block on a model call - so
	// the raw text is stored and Preferences parses it on the first save.
	s.saveSetupMealHabits(r, hh.ID)

	// ── Sign in automatically ────────────────────────────────────────────────

	token, err := auth.GenerateToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	expiresAt := time.Now().Add(time.Duration(s.cfg.SessionTTLHours) * time.Hour)
	_, err = s.store.CreateSession(ctx, user.ID, auth.HashToken(token),
		middleware.ClientIP(r), r.UserAgent(), expiresAt)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.setSessionCookie(w, token, expiresAt)

	id := user.ID
	s.logEvent(r, &id, "setup.complete", "household", "1", "")

	if hasNonAPIStore {
		s.setNotify(w, NotifySuccess, "Setup complete! For live prices on your stores, configure scraping in Settings → Scraper.")
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// saveSetupMealHabits stores the wizard's "what you eat now" answers: one
// free-text hint per meal slot, the household's repeat meals, and how many
// meals a week are eaten out.
func (s *Server) saveSetupMealHabits(r *http.Request, householdID int64) {
	ctx := r.Context()

	for _, slot := range []string{"breakfast", "lunch", "dinner"} {
		rawText := strings.TrimSpace(r.FormValue("hint_" + slot))
		effort := r.FormValue("effort_" + slot)
		switch effort {
		case "quick", "standard", "elaborate":
		default:
			effort = "standard"
		}
		if rawText == "" && effort == "standard" {
			continue // nothing the household told us
		}
		if err := s.store.UpsertMealSlotHint(ctx, db.UpsertMealSlotHintParams{
			HouseholdID: householdID,
			Slot:        slot,
			RawText:     rawText,
			ParsedJSON:  "null", // Preferences parses it on the first save
			Effort:      effort,
		}); err != nil {
			log.Printf("setup: slot hint %s: %v", slot, err)
		}
	}

	favorites := splitTrimmed(r.FormValue("favorite_meals"))
	mealsOut, err := strconv.Atoi(strings.TrimSpace(r.FormValue("meals_out")))
	if err != nil || mealsOut < 0 || mealsOut > 21 {
		mealsOut = 0
	}
	if len(favorites) == 0 && mealsOut == 0 {
		return
	}
	if err := s.store.SetSetting(ctx, "favorite_meals", strings.Join(favorites, ", ")); err != nil {
		log.Printf("setup: favorite meals: %v", err)
	}
	if err := s.store.SetSetting(ctx, "meals_out_per_week", strconv.Itoa(mealsOut)); err != nil {
		log.Printf("setup: meals out: %v", err)
	}
}
