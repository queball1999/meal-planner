package web

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goeat/auth"
	"goeat/db"
	"goeat/middleware"
)

// usTimezones is the list offered in the setup wizard (US-only, §4.5).
var usTimezones = []struct{ Label, Value string }{
	{"Eastern (ET)", "America/New_York"},
	{"Central (CT)", "America/Chicago"},
	{"Mountain (MT)", "America/Denver"},
	{"Mountain – Arizona (no DST)", "America/Phoenix"},
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
	})
}

// handleSetup processes the setup wizard form (POST /setup).
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if middleware.HouseholdFromCtx(r) != nil {
		http.Error(w, "setup already complete", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		s.setFlash(w, "Invalid form submission")
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
		s.setFlash(w, "Username must be at least 3 characters")
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if err := auth.ValidatePassword(password); err != nil {
		s.setFlash(w, err.Error())
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if password != passwordConfirm {
		s.setFlash(w, "Passwords do not match")
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if householdName == "" {
		householdName = "My Household"
	}

	budget, err := strconv.ParseFloat(budgetStr, 64)
	if err != nil || budget <= 0 || budget > 99999 {
		s.setFlash(w, "Budget must be a positive dollar amount")
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	budgetCents := int64(math.Round(budget * 100))

	hSize, err := strconv.Atoi(sizeStr)
	if err != nil || hSize < 1 || hSize > 20 {
		s.setFlash(w, "Household size must be between 1 and 20")
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
		s.setFlash(w, "Could not create account — username may already be taken")
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

	// ── Create selected stores ───────────────────────────────────────────────

	hasNonAPIStore := false
	for _, storeName := range selectedStores {
		ks := KnownStoreByName(storeName)
		if ks == nil {
			continue // ignore unknown names (safety)
		}
		_, _ = s.store.CreateStore(ctx, db.UpsertStoreParams{
			HouseholdID: hh.ID,
			Name:        ks.Name,
			Kind:        ks.Kind,
		})
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
		s.setFlash(w, "Setup complete! For live prices on your stores, configure scraping in Settings → Scraper.")
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}
