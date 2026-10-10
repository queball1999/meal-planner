package web

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goeat/db"
	"goeat/middleware"
)

// memberRow is one household member as the preferences page shows it. The
// preset label turns the bare factor into something readable ("Big eater")
// without storing a category the user would then have to keep in sync with the
// number.
type memberRow struct {
	*db.HouseholdMember
	PresetLabel string
}

// portionPreset is one entry in the portion-size picker. The factor is what
// gets stored; the label and hint are what make it choosable by someone who
// has never thought about their family in multiples of a serving.
type portionPreset struct {
	Value float64
	Label string
	Hint  string
}

var portionPresets = []portionPreset{
	{0.4, "Small child", "toddler or preschooler"},
	{0.6, "Child", "roughly half an adult plate"},
	{0.8, "Light eater", "usually leaves some"},
	{1.0, "Standard adult", "one full serving"},
	{1.2, "Hearty eater", "goes back for a bit more"},
	{1.5, "Big eater", "teenager, or seconds every time"},
}

// presetLabel names the closest preset to a stored factor, so a hand-typed
// 1.35 still reads as something rather than as a bare number. Exact equality
// is not required - the presets are guide posts, not an enum.
func presetLabel(factor float64) string {
	best := portionPresets[0]
	bestDiff := factor - best.Value
	if bestDiff < 0 {
		bestDiff = -bestDiff
	}
	for _, p := range portionPresets[1:] {
		d := factor - p.Value
		if d < 0 {
			d = -d
		}
		if d < bestDiff {
			best, bestDiff = p, d
		}
	}
	return best.Label
}

func memberRows(members []*db.HouseholdMember) []memberRow {
	rows := make([]memberRow, len(members))
	for i, m := range members {
		rows[i] = memberRow{HouseholdMember: m, PresetLabel: presetLabel(m.PortionFactor)}
	}
	return rows
}

// totalPortions is what the household's members add up to - the number every
// plan is actually scaled to, shown on the preferences page so the effect of a
// portion factor is visible before a plan is generated.
func totalPortions(members []*db.HouseholdMember) float64 {
	var t float64
	for _, m := range members {
		t += m.PortionFactor
	}
	return t
}

// parseMemberForm pulls a member out of a submitted form. The portion factor
// arrives from either the preset <select> or the "custom" number input, so
// whichever one the user actually touched wins.
func parseMemberForm(r *http.Request) (name string, factor float64, notes string, err error) {
	name = strings.TrimSpace(r.FormValue("name"))
	notes = strings.TrimSpace(r.FormValue("notes"))

	raw := strings.TrimSpace(r.FormValue("portion_factor_custom"))
	if raw == "" {
		raw = strings.TrimSpace(r.FormValue("portion_factor"))
	}
	if raw == "" {
		return name, 0, notes, fmt.Errorf("pick how much %s eats", nameOr(name, "this person"))
	}
	factor, err = strconv.ParseFloat(raw, 64)
	if err != nil {
		return name, 0, notes, fmt.Errorf("%q is not a portion size", raw)
	}
	return name, factor, notes, nil
}

func nameOr(name, fallback string) string {
	if name == "" {
		return fallback
	}
	return name
}

// handleMemberCreate adds a household member (§4.5).
func (s *Server) handleMemberCreate(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	name, factor, notes, err := parseMemberForm(r)
	if err != nil {
		s.setNotify(w, NotifyDanger, err.Error())
		http.Redirect(w, r, "/preferences", http.StatusSeeOther)
		return
	}
	if _, err := s.store.CreateHouseholdMember(r.Context(), db.CreateHouseholdMemberParams{
		HouseholdID:   hh.ID,
		Name:          name,
		PortionFactor: factor,
		Notes:         notes,
	}); err != nil {
		log.Printf("members: create: %v", err)
		s.setNotify(w, NotifyDanger, memberSaveMessage(err, name))
		http.Redirect(w, r, "/preferences", http.StatusSeeOther)
		return
	}
	s.setNotify(w, NotifySuccess, fmt.Sprintf("Added %s.", name))
	http.Redirect(w, r, "/preferences", http.StatusSeeOther)
}

// handleMemberUpdate edits one member.
func (s *Server) handleMemberUpdate(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad member id", http.StatusBadRequest)
		return
	}
	name, factor, notes, err := parseMemberForm(r)
	if err != nil {
		s.setNotify(w, NotifyDanger, err.Error())
		http.Redirect(w, r, "/preferences", http.StatusSeeOther)
		return
	}
	if err := s.store.UpdateHouseholdMember(r.Context(), db.UpdateHouseholdMemberParams{
		ID:            id,
		HouseholdID:   hh.ID,
		Name:          name,
		PortionFactor: factor,
		Notes:         notes,
	}); err != nil {
		log.Printf("members: update %d: %v", id, err)
		s.setNotify(w, NotifyDanger, memberSaveMessage(err, name))
		http.Redirect(w, r, "/preferences", http.StatusSeeOther)
		return
	}
	s.setNotify(w, NotifySuccess, fmt.Sprintf("Saved %s.", name)+s.rescalePlansForMembers(r.Context(), hh))
	http.Redirect(w, r, "/preferences", http.StatusSeeOther)
}

// rescalePlansForMembers carries a change to the household's members onto
// the plans still ahead: each day's portion total is recomputed from the
// people eating that day, its meals are rescaled to it, and the shopping list
// follows. Returns what to add to the "saved" notice.
//
// plan_days stores the total rather than recomputing it on read so that a
// plan already cooked is not restated - which is why only plans ending today
// or later are touched. A day with no member selection is a bare headcount
// and has nothing to recompute.
func (s *Server) rescalePlansForMembers(ctx context.Context, hh *db.Household) string {
	plans, err := s.store.ListPlans(ctx, hh.ID)
	if err != nil {
		log.Printf("members: list plans: %v", err)
		return ""
	}
	today := time.Now().Format("2006-01-02")
	var changed int
	for _, p := range plans {
		if p.Canceled || p.WeekEnd < today {
			continue
		}
		days, err := s.store.ListPlanDays(ctx, p.ID)
		if err != nil {
			log.Printf("members: list days of plan %d: %v", p.ID, err)
			continue
		}
		moved := false
		for _, d := range days {
			if len(d.MemberIDs) == 0 {
				continue
			}
			memberPortions, n, err := s.store.SumPortionFactors(ctx, hh.ID, d.MemberIDs)
			if err != nil {
				log.Printf("members: sum factors for %s: %v", d.Date, err)
				continue
			}
			in := dayPortionInput{
				Portions: memberPortions + float64(d.Guests), MemberPortions: memberPortions,
				Headcount: n + d.Guests, Guests: d.Guests, GuestSlots: d.GuestSlots,
			}
			// Nobody left eating (every member on the day was removed): leave
			// the day as it is rather than scale its meals to nothing.
			if in.Portions < 1 || math.Abs(in.Portions-d.Portions) < 1e-9 {
				continue
			}
			if err := s.store.UpsertPlanDay(ctx, db.UpsertPlanDayParams{
				PlanID: p.ID, Date: d.Date, Headcount: in.Headcount, Note: d.Note,
				MemberIDs: d.MemberIDs, Portions: in.Portions,
				Guests: d.Guests, GuestSlots: d.GuestSlots,
			}); err != nil {
				log.Printf("members: save plan day %s: %v", d.Date, err)
				continue
			}
			if _, err := s.scaleDayForInput(ctx, p.ID, d.Date, in); err != nil {
				log.Printf("members: scale meals for %s: %v", d.Date, err)
			}
			moved = true
		}
		if moved {
			changed++
			s.repriceInBackground(p.ID, hh, true)
		}
	}
	if changed == 0 {
		return ""
	}
	return fmt.Sprintf(" %d current %s rescaled to match. The shopping list is updating.",
		changed, pluralize(changed, "plan was", "plans were"))
}

// handleMemberDelete removes one member.
func (s *Server) handleMemberDelete(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad member id", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteHouseholdMember(r.Context(), hh.ID, id); err != nil {
		log.Printf("members: delete %d: %v", id, err)
		s.setNotify(w, NotifyDanger, "Couldn't remove that person. Try again.")
		http.Redirect(w, r, "/preferences", http.StatusSeeOther)
		return
	}
	s.setNotify(w, NotifySuccess, "Removed."+s.rescalePlansForMembers(r.Context(), hh))
	http.Redirect(w, r, "/preferences", http.StatusSeeOther)
}

// memberSaveMessage turns the two failures a user can actually cause into
// something they can act on, and leaves everything else generic.
func memberSaveMessage(err error, name string) string {
	if err == db.ErrInvalidPortionFactor {
		return fmt.Sprintf("Portion size must be between 0 and %g.", db.PortionFactorMax)
	}
	if strings.Contains(strings.ToLower(err.Error()), "unique") {
		return fmt.Sprintf("There's already someone called %s in this household.", name)
	}
	return "Couldn't save that person. Try again."
}
