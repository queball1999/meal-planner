package web

import (
	"errors"
	"fmt"
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

// ── Role ceiling rules (QSS security design §8.4) ───────────────────────────
//
// Two questions every membership edit must answer, checked on the server no
// matter what the form offered:
//
//   canAssignHouseholdRole - may the actor hand out the *new* role? Nobody
//     grants a role above their own.
//   canEditMember - may the actor touch this member at all? Nobody edits or
//     removes someone ranked above them, so an owner can't be locked out by
//     an editor.
//
// Only owners reach these handlers (routes.go), so today both reduce to
// "owner may do anything to anyone"; they are kept as real rules so a future
// "editors can invite viewers" route inherits the ceiling instead of
// re-deriving it.

func canAssignHouseholdRole(actorRole, newRole string) bool {
	if !db.ValidHouseholdRole(newRole) {
		return false
	}
	actor := db.HouseholdRoleRank(actorRole)
	return actor >= db.HouseholdRoleRank(db.HouseholdRoleOwner) &&
		db.HouseholdRoleRank(newRole) <= actor
}

func canEditMember(actorRole, targetRole string) bool {
	actor := db.HouseholdRoleRank(actorRole)
	return actor >= db.HouseholdRoleRank(db.HouseholdRoleOwner) &&
		db.HouseholdRoleRank(targetRole) <= actor
}

// ── Page ─────────────────────────────────────────────────────────────────────

type householdRow struct {
	ID     int64
	Name   string
	Role   string // the viewer's role there ("owner" for an admin without a seat)
	Active bool
}

type householdsPageData struct {
	Households []householdRow
	Active     *db.Household
	CanOwn     bool
	IsAdmin    bool
	Members    []*db.HouseholdMembership
	Users      []*db.User // admin only
	Me         int64
	Roles      []string
	Timezones  []struct{ Label, Value string }
}

// handleHouseholdsPage lists the households the user can switch to, the
// active household's people (owners manage them), and - for instance admins -
// every account plus the create-household form.
//
//	GET /households
func (s *Server) handleHouseholdsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := middleware.UserFromCtx(r)
	active := middleware.HouseholdFromCtx(r)

	data := householdsPageData{
		Active:    active,
		CanOwn:    middleware.CanOwn(r),
		IsAdmin:   user.IsAdmin(),
		Me:        user.ID,
		Roles:     []string{db.HouseholdRoleOwner, db.HouseholdRoleEditor, db.HouseholdRoleViewer},
		Timezones: usTimezones,
	}

	seen := map[int64]bool{}
	for _, m := range middleware.MembershipsFromCtx(r) {
		seen[m.HouseholdID] = true
		data.Households = append(data.Households, householdRow{
			ID: m.HouseholdID, Name: m.HouseholdName, Role: m.Role,
			Active: active != nil && active.ID == m.HouseholdID,
		})
	}
	if user.IsAdmin() {
		all, err := s.store.ListHouseholds(ctx)
		if err != nil {
			log.Printf("households page: list households: %v", err)
		}
		for _, h := range all {
			if seen[h.ID] {
				continue
			}
			data.Households = append(data.Households, householdRow{
				ID: h.ID, Name: h.Name, Role: db.HouseholdRoleOwner,
				Active: active != nil && active.ID == h.ID,
			})
		}
		data.Users, _ = s.store.ListUsers(ctx)
	}
	if active != nil {
		data.Members, _ = s.store.ListMembershipsForHousehold(ctx, active.ID)
	}

	s.render(w, r, "households", data)
}

// ── Switch / create / rename / delete ────────────────────────────────────────

// handleHouseholdSwitch points this session at another household the user
// belongs to (or any household, for an instance admin).
//
//	POST /households/switch  {household_id}
func (s *Server) handleHouseholdSwitch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := middleware.UserFromCtx(r)
	sess := middleware.SessionFromCtx(r)
	id, err := strconv.ParseInt(r.FormValue("household_id"), 10, 64)
	if err != nil || sess == nil {
		http.Error(w, "bad household", http.StatusBadRequest)
		return
	}

	allowed := false
	if user.IsAdmin() {
		hh, _ := s.store.GetHousehold(ctx, id)
		allowed = hh != nil
	} else if m, _ := s.store.GetMembership(ctx, id, user.ID); m != nil {
		allowed = true
	}
	if !allowed {
		http.NotFound(w, r)
		return
	}

	if err := s.store.SetSessionHousehold(ctx, sess.ID, id); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleHouseholdCreate makes a new, empty household (admin only), seats the
// creator as its owner, seeds its item catalog, and switches into it.
//
//	POST /households  {name, budget, zip_code, timezone}
func (s *Server) handleHouseholdCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := middleware.UserFromCtx(r)

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 80 {
		s.setNotify(w, NotifyDanger, "Give the household a name (up to 80 characters).")
		http.Redirect(w, r, "/households", http.StatusSeeOther)
		return
	}
	budget, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("budget")), 64)
	if err != nil || budget <= 0 || budget > 99999 {
		s.setNotify(w, NotifyDanger, "Budget must be a positive dollar amount.")
		http.Redirect(w, r, "/households", http.StatusSeeOther)
		return
	}
	timezone := r.FormValue("timezone")
	if _, err := time.LoadLocation(timezone); err != nil || timezone == "" {
		timezone = "America/New_York"
	}

	hh, err := s.store.CreateHousehold(ctx, db.CreateHouseholdParams{
		Name:              name,
		WeeklyBudgetCents: int64(math.Round(budget * 100)),
		Country:           "US",
		ZIPCode:           strings.TrimSpace(r.FormValue("zip_code")),
		Timezone:          timezone,
		HouseholdSize:     2,
	})
	if err != nil {
		s.setNotify(w, NotifyDanger, "Could not create the household.")
		http.Redirect(w, r, "/households", http.StatusSeeOther)
		return
	}
	if err := s.store.UpsertMembership(ctx, hh.ID, user.ID, db.HouseholdRoleOwner); err != nil {
		log.Printf("household create: seat creator: %v", err)
	}
	if err := catalog.SeedHousehold(ctx, s.store, hh.ID); err != nil {
		log.Printf("household create: seed catalog: %v", err)
	}
	if sess := middleware.SessionFromCtx(r); sess != nil {
		_ = s.store.SetSessionHousehold(ctx, sess.ID, hh.ID)
	}

	uid := user.ID
	s.logEvent(r, &uid, "household.created", "household", strconv.FormatInt(hh.ID, 10), "")
	s.setNotify(w, NotifySuccess, fmt.Sprintf("Created %q and switched to it. Add stores and people next.", hh.Name))
	http.Redirect(w, r, "/stores", http.StatusSeeOther)
}

// handleHouseholdRename renames the active household (owner).
//
//	POST /households/rename  {name}
func (s *Server) handleHouseholdRename(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 80 {
		s.setNotify(w, NotifyDanger, "Give the household a name (up to 80 characters).")
		http.Redirect(w, r, "/households", http.StatusSeeOther)
		return
	}
	if err := s.store.RenameHousehold(r.Context(), hh.ID, name); err != nil {
		s.setNotify(w, NotifyDanger, "Could not rename the household.")
	} else {
		uid := middleware.UserFromCtx(r).ID
		s.logEvent(r, &uid, "household.renamed", "household", strconv.FormatInt(hh.ID, 10), "")
		s.setNotify(w, NotifySuccess, "Household renamed.")
	}
	http.Redirect(w, r, "/households", http.StatusSeeOther)
}

// handleHouseholdDelete deletes the active household and everything in it
// (admin only). The form must repeat the household's name exactly.
//
//	POST /households/delete  {confirm}
func (s *Server) handleHouseholdDelete(w http.ResponseWriter, r *http.Request) {
	hh := middleware.HouseholdFromCtx(r)
	if hh == nil {
		http.Redirect(w, r, "/households", http.StatusSeeOther)
		return
	}
	if strings.TrimSpace(r.FormValue("confirm")) != hh.Name {
		s.setNotify(w, NotifyDanger, fmt.Sprintf("Type %q exactly to confirm.", hh.Name))
		http.Redirect(w, r, "/households", http.StatusSeeOther)
		return
	}
	if err := s.store.DeleteHousehold(r.Context(), hh.ID); err != nil {
		s.setNotify(w, NotifyDanger, "Could not delete the household.")
		http.Redirect(w, r, "/households", http.StatusSeeOther)
		return
	}
	uid := middleware.UserFromCtx(r).ID
	s.logEvent(r, &uid, "household.deleted", "household", strconv.FormatInt(hh.ID, 10), "")
	s.setNotify(w, NotifySuccess, fmt.Sprintf("Deleted %q.", hh.Name))
	http.Redirect(w, r, "/households", http.StatusSeeOther)
}

// ── People in the active household (owner) ───────────────────────────────────

// handleHouseholdMemberAdd seats a person in the active household. An
// existing username is added as-is; a new one needs a password and becomes a
// plain member account - an owner can create logins, never admins.
//
//	POST /households/members  {username, role, password?}
func (s *Server) handleHouseholdMemberAdd(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hh := middleware.HouseholdFromCtx(r)
	actor := middleware.UserFromCtx(r)
	actorRole := middleware.HouseholdRoleFromCtx(r)

	username := strings.TrimSpace(r.FormValue("username"))
	role := r.FormValue("role")
	if !canAssignHouseholdRole(actorRole, role) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if len(username) < 3 {
		s.setNotify(w, NotifyDanger, "Username must be at least 3 characters.")
		http.Redirect(w, r, "/households", http.StatusSeeOther)
		return
	}

	target, err := s.store.GetUserByUsername(ctx, username)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	created := false
	if target == nil {
		password := r.FormValue("password")
		if password == "" {
			s.setNotify(w, NotifyDanger, fmt.Sprintf("No account called %q yet - set a password to create one.", username))
			http.Redirect(w, r, "/households", http.StatusSeeOther)
			return
		}
		if err := auth.ValidatePassword(password); err != nil {
			s.setNotify(w, NotifyDanger, err.Error())
			http.Redirect(w, r, "/households", http.StatusSeeOther)
			return
		}
		hash, err := auth.HashPassword(password)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		target, err = s.store.CreateUser(ctx, username, hash, db.InstanceRoleMember)
		if err != nil {
			s.setNotify(w, NotifyDanger, "Could not create that account.")
			http.Redirect(w, r, "/households", http.StatusSeeOther)
			return
		}
		created = true
		aid := actor.ID
		s.logEvent(r, &aid, "user.created", "user", strconv.FormatInt(target.ID, 10), "")
	} else if existing, _ := s.store.GetMembership(ctx, hh.ID, target.ID); existing != nil {
		s.setNotify(w, NotifyInfo, fmt.Sprintf("%s is already in this household - change their role below.", target.Username))
		http.Redirect(w, r, "/households", http.StatusSeeOther)
		return
	}

	if err := s.store.UpsertMembership(ctx, hh.ID, target.ID, role); err != nil {
		s.setNotify(w, NotifyDanger, "Could not add them.")
		http.Redirect(w, r, "/households", http.StatusSeeOther)
		return
	}
	aid := actor.ID
	s.logEvent(r, &aid, "role.assigned", "user", strconv.FormatInt(target.ID, 10),
		fmt.Sprintf(`{"household_id":%d,"role":%q}`, hh.ID, role))

	msg := fmt.Sprintf("Added %s as %s.", target.Username, role)
	if created {
		msg = fmt.Sprintf("Created an account for %s and added them as %s.", target.Username, role)
	}
	s.setNotify(w, NotifySuccess, msg)
	http.Redirect(w, r, "/households", http.StatusSeeOther)
}

// handleHouseholdMemberRole changes a person's role in the active household.
// Your own role is off limits here - a slip would demote you out of the page
// you're on.
//
//	POST /households/members/{userID}/role  {role}
func (s *Server) handleHouseholdMemberRole(w http.ResponseWriter, r *http.Request) {
	target, ok := s.editableMember(w, r)
	if !ok {
		return
	}
	role := r.FormValue("role")
	if !canAssignHouseholdRole(middleware.HouseholdRoleFromCtx(r), role) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	hh := middleware.HouseholdFromCtx(r)
	if err := s.store.SetMembershipRole(r.Context(), hh.ID, target.UserID, role); err != nil {
		s.setNotify(w, NotifyDanger, membershipErrMsg(err))
		http.Redirect(w, r, "/households", http.StatusSeeOther)
		return
	}
	aid := middleware.UserFromCtx(r).ID
	s.logEvent(r, &aid, "role.assigned", "user", strconv.FormatInt(target.UserID, 10),
		fmt.Sprintf(`{"household_id":%d,"role":%q,"previous":%q}`, hh.ID, role, target.Role))
	s.setNotify(w, NotifySuccess, fmt.Sprintf("%s is now %s.", target.Username, role))
	http.Redirect(w, r, "/households", http.StatusSeeOther)
}

// handleHouseholdMemberRemove takes a person out of the active household.
// Their account stays; they just lose access here.
//
//	POST /households/members/{userID}/remove
func (s *Server) handleHouseholdMemberRemove(w http.ResponseWriter, r *http.Request) {
	target, ok := s.editableMember(w, r)
	if !ok {
		return
	}
	hh := middleware.HouseholdFromCtx(r)
	if err := s.store.DeleteMembership(r.Context(), hh.ID, target.UserID); err != nil {
		s.setNotify(w, NotifyDanger, membershipErrMsg(err))
		http.Redirect(w, r, "/households", http.StatusSeeOther)
		return
	}
	aid := middleware.UserFromCtx(r).ID
	s.logEvent(r, &aid, "role.revoked", "user", strconv.FormatInt(target.UserID, 10),
		fmt.Sprintf(`{"household_id":%d,"previous":%q}`, hh.ID, target.Role))
	s.setNotify(w, NotifySuccess, fmt.Sprintf("Removed %s from this household.", target.Username))
	http.Redirect(w, r, "/households", http.StatusSeeOther)
}

// editableMember loads the {userID} member of the active household and checks
// the actor may edit them. Writes the response itself on false.
func (s *Server) editableMember(w http.ResponseWriter, r *http.Request) (*db.HouseholdMembership, bool) {
	hh := middleware.HouseholdFromCtx(r)
	userID, err := strconv.ParseInt(r.PathValue("userID"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return nil, false
	}
	if userID == middleware.UserFromCtx(r).ID {
		s.setNotify(w, NotifyDanger, "You can't change your own role here - ask another owner.")
		http.Redirect(w, r, "/households", http.StatusSeeOther)
		return nil, false
	}
	target, err := s.store.GetMembership(r.Context(), hh.ID, userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	if target == nil {
		http.NotFound(w, r)
		return nil, false
	}
	if !canEditMember(middleware.HouseholdRoleFromCtx(r), target.Role) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return nil, false
	}
	return target, true
}

func membershipErrMsg(err error) string {
	switch {
	case errors.Is(err, db.ErrLastOwner):
		return "A household needs at least one owner - make someone else owner first."
	case errors.Is(err, db.ErrLastAdmin):
		return "The server needs at least one admin - make someone else admin first."
	}
	return "Could not save that."
}

// ── Accounts (instance admin) ────────────────────────────────────────────────

// handleUserRole promotes or demotes an account's instance role.
//
//	POST /admin/users/{userID}/role  {role: admin|member}
func (s *Server) handleUserRole(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(r.PathValue("userID"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	actor := middleware.UserFromCtx(r)
	if userID == actor.ID {
		s.setNotify(w, NotifyDanger, "You can't change your own server role - ask another admin.")
		http.Redirect(w, r, "/households", http.StatusSeeOther)
		return
	}
	role := r.FormValue("role")
	if err := s.store.SetUserRole(r.Context(), userID, role); err != nil {
		s.setNotify(w, NotifyDanger, membershipErrMsg(err))
		http.Redirect(w, r, "/households", http.StatusSeeOther)
		return
	}
	aid := actor.ID
	s.logEvent(r, &aid, "user.updated", "user", strconv.FormatInt(userID, 10), fmt.Sprintf(`{"instance_role":%q}`, role))
	s.setNotify(w, NotifySuccess, "Server role updated.")
	http.Redirect(w, r, "/households", http.StatusSeeOther)
}

// handleUserDelete deletes an account outright (sessions and memberships go
// with it).
//
//	POST /admin/users/{userID}/delete
func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(r.PathValue("userID"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	actor := middleware.UserFromCtx(r)
	if userID == actor.ID {
		s.setNotify(w, NotifyDanger, "You can't delete your own account here.")
		http.Redirect(w, r, "/households", http.StatusSeeOther)
		return
	}
	if err := s.store.DeleteUser(r.Context(), userID); err != nil {
		s.setNotify(w, NotifyDanger, membershipErrMsg(err))
		http.Redirect(w, r, "/households", http.StatusSeeOther)
		return
	}
	aid := actor.ID
	s.logEvent(r, &aid, "user.deleted", "user", strconv.FormatInt(userID, 10), "")
	s.setNotify(w, NotifySuccess, "Account deleted.")
	http.Redirect(w, r, "/households", http.StatusSeeOther)
}
