package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goeat/auth"
	"goeat/config"
	"goeat/db"
	"goeat/middleware"
)

// TestRoleCeiling is the (actor role, target/new role, want) table QSS
// security design §8.4 asks for: nobody grants or edits above their own rank,
// and only owners manage people at all.
func TestRoleCeiling(t *testing.T) {
	roles := []string{"", "bogus", db.HouseholdRoleViewer, db.HouseholdRoleEditor, db.HouseholdRoleOwner}
	for _, actor := range roles {
		for _, target := range roles {
			isOwner := actor == db.HouseholdRoleOwner
			wantAssign := isOwner && db.ValidHouseholdRole(target)
			wantEdit := isOwner // an owner outranks or equals every role, including unknown ones (rank 0)
			if got := canAssignHouseholdRole(actor, target); got != wantAssign {
				t.Errorf("canAssignHouseholdRole(%q, %q) = %v, want %v", actor, target, got, wantAssign)
			}
			if got := canEditMember(actor, target); got != wantEdit {
				t.Errorf("canEditMember(%q, %q) = %v, want %v", actor, target, got, wantEdit)
			}
		}
	}
}

type rbacFixture struct {
	h     http.Handler
	store db.Store
	// household A: alice (editor), vera (viewer), olga (owner). B: bob (owner).
	// root is an instance admin with no seats.
	a, b    tenantRows
	cookies map[string]*http.Cookie
}

type tenantRows struct {
	hh    *db.Household
	meal  *db.Meal
	store *db.GroceryStore
}

func newRBACFixture(t *testing.T) *rbacFixture {
	t.Helper()
	return newRBACFixtureWith(t, nil)
}

func newRBACFixtureWith(t *testing.T, tweak func(*config.Config)) *rbacFixture {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	mkTenant := func(name string) tenantRows {
		hh, err := store.CreateHousehold(ctx, db.CreateHouseholdParams{Name: name, WeeklyBudgetCents: 10000, Country: "US", Timezone: "UTC", HouseholdSize: 2})
		if err != nil {
			t.Fatal(err)
		}
		p, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-09-27", WeekEnd: "2026-10-03"})
		if err != nil {
			t.Fatal(err)
		}
		m, err := store.CreateMeal(ctx, db.CreateMealParams{PlanID: p.ID, Day: "2026-09-28", Slot: "dinner", Title: name + " stew", Effort: "standard", Servings: 2, CookedPortions: 2})
		if err != nil {
			t.Fatal(err)
		}
		gs, err := store.CreateStore(ctx, db.UpsertStoreParams{HouseholdID: hh.ID, Name: name + " Mart", Kind: "grocery"})
		if err != nil {
			t.Fatal(err)
		}
		return tenantRows{hh: hh, meal: m, store: gs}
	}

	f := &rbacFixture{store: store, a: mkTenant("Alpha"), b: mkTenant("Bravo"), cookies: map[string]*http.Cookie{}}

	seat := func(name, instanceRole string, hh *db.Household, role string) {
		u, err := store.CreateUser(ctx, name, "unused", instanceRole)
		if err != nil {
			t.Fatal(err)
		}
		if hh != nil {
			if err := store.UpsertMembership(ctx, hh.ID, u.ID, role); err != nil {
				t.Fatal(err)
			}
		}
		token, _ := auth.GenerateToken()
		if _, err := store.CreateSession(ctx, u.ID, auth.HashToken(token), "", "", time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		f.cookies[name] = &http.Cookie{Name: middleware.SessionCookieName, Value: token}
	}
	seat("root", db.InstanceRoleAdmin, nil, "")
	seat("alice", db.InstanceRoleMember, f.a.hh, db.HouseholdRoleEditor)
	seat("vera", db.InstanceRoleMember, f.a.hh, db.HouseholdRoleViewer)
	seat("olga", db.InstanceRoleMember, f.a.hh, db.HouseholdRoleOwner)
	seat("bob", db.InstanceRoleMember, f.b.hh, db.HouseholdRoleOwner)

	cfg := &config.Config{AppName: "test", SessionSecret: strings.Repeat("k", 32), SessionTTLHours: 1, AutoPlanHour: -1, MultiTenant: true}
	if tweak != nil {
		tweak(cfg)
	}
	s := NewServer(cfg, store, nil, "test", nil)
	// The route mux behind LoadSession, without CSRF: these tests are about
	// who may do what, and a CSRF 403 would mask a role 403.
	mux := http.NewServeMux()
	s.routes(mux)
	f.h = middleware.LoadSession(store, middleware.SessionOptions{MultiTenant: cfg.MultiTenant})(mux)
	return f
}

func (f *rbacFixture) do(user, method, path string, form url.Values) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if c := f.cookies[user]; c != nil {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

// TestRBACAndTenancy drives real routes: role gates (who may act) and
// ownership checks (on what) must both hold.
func TestRBACAndTenancy(t *testing.T) {
	f := newRBACFixture(t)
	aMeal, bMeal := f.a.meal.ID, f.b.meal.ID

	cases := []struct {
		name, user, method, path string
		form                     url.Values
		want                     int
	}{
		// Tenancy: another household's ids are 404, read or write.
		{"read own meal", "alice", "GET", fmt.Sprintf("/meals/%d/card", aMeal), nil, http.StatusOK},
		{"read other household's meal", "alice", "GET", fmt.Sprintf("/meals/%d", bMeal), nil, http.StatusNotFound},
		{"lock other household's meal", "alice", "POST", fmt.Sprintf("/meals/%d/lock", bMeal), url.Values{"locked": {"1"}}, http.StatusNotFound},
		{"owner deletes other household's store", "bob", "POST", fmt.Sprintf("/stores/%d/delete", f.a.store.ID), url.Values{}, http.StatusNotFound},
		{"switch into a household you're not in", "alice", "POST", "/households/switch", url.Values{"household_id": {fmt.Sprint(f.b.hh.ID)}}, http.StatusNotFound},

		// Role gates inside the household.
		{"editor locks own meal", "alice", "POST", fmt.Sprintf("/meals/%d/lock", aMeal), url.Values{"locked": {"1"}}, http.StatusSeeOther},
		{"viewer can't write", "vera", "POST", fmt.Sprintf("/meals/%d/lock", aMeal), url.Values{"locked": {"1"}}, http.StatusForbidden},
		{"editor can't manage stores", "alice", "POST", fmt.Sprintf("/stores/%d/delete", f.a.store.ID), url.Values{}, http.StatusForbidden},
		{"editor can't add people", "alice", "POST", "/households/members", url.Values{"username": {"mallory"}, "role": {"owner"}, "password": {"longenough123"}}, http.StatusForbidden},

		// Instance-admin surfaces.
		{"household owner isn't a server admin", "olga", "GET", "/settings", nil, http.StatusForbidden},
		{"member can't create households", "olga", "POST", "/households", url.Values{"name": {"X"}, "budget": {"10"}}, http.StatusForbidden},
		{"member can't promote accounts", "olga", "POST", "/admin/users/1/role", url.Values{"role": {"admin"}}, http.StatusForbidden},

		// Anonymous.
		{"anonymous is sent to login", "", "GET", "/plan", nil, http.StatusSeeOther},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := f.do(c.user, c.method, c.path, c.form).Code; got != c.want {
				t.Errorf("%s %s as %q = %d, want %d", c.method, c.path, c.user, got, c.want)
			}
		})
	}

	// The refused store delete really refused.
	if ok, _ := f.store.HouseholdOwns(context.Background(), f.a.hh.ID, db.ResStore, f.a.store.ID); !ok {
		t.Error("household A's store was deleted by a refused request")
	}
}

// TestRemovalTakesEffectImmediately: roles are re-read per request (QSS
// security design §1.4, §8.5), so removing someone locks their existing
// session out on the next click, without waiting for it to expire.
func TestRemovalTakesEffectImmediately(t *testing.T) {
	f := newRBACFixture(t)
	path := fmt.Sprintf("/meals/%d/lock", f.a.meal.ID)
	form := url.Values{"locked": {"1"}}

	if got := f.do("alice", "POST", path, form).Code; got != http.StatusSeeOther {
		t.Fatalf("before removal: %d, want 303", got)
	}
	alice, _ := f.store.GetUserByUsername(context.Background(), "alice")
	if err := f.store.DeleteMembership(context.Background(), f.a.hh.ID, alice.ID); err != nil {
		t.Fatal(err)
	}
	// No household left: bounced to /households, and certainly not allowed through.
	w := f.do("alice", "POST", path, form)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/households" {
		t.Errorf("after removal: %d -> %q, want 303 -> /households", w.Code, w.Header().Get("Location"))
	}
}

// TestOwnerManagesPeople: an owner adds, re-roles and removes people, can't
// edit themselves, and the last-owner guard holds through the handlers.
func TestOwnerManagesPeople(t *testing.T) {
	f := newRBACFixture(t)
	ctx := context.Background()

	// The page itself renders for each kind of viewer (templates only fail at run time).
	for _, u := range []string{"olga", "alice", "vera", "root"} {
		if w := f.do(u, "GET", "/households", nil); w.Code != http.StatusOK {
			t.Fatalf("GET /households as %s: %d\n%s", u, w.Code, w.Body.String())
		}
	}

	w := f.do("olga", "POST", "/households/members", url.Values{"username": {"newbie"}, "role": {"viewer"}, "password": {"longenough123"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("add person: %d", w.Code)
	}
	newbie, _ := f.store.GetUserByUsername(ctx, "newbie")
	if newbie == nil || newbie.Role != db.InstanceRoleMember {
		t.Fatalf("new account = %+v, want a member-role account", newbie)
	}
	if m, _ := f.store.GetMembership(ctx, f.a.hh.ID, newbie.ID); m == nil || m.Role != db.HouseholdRoleViewer {
		t.Fatalf("membership = %+v, want viewer", m)
	}

	f.do("olga", "POST", fmt.Sprintf("/households/members/%d/role", newbie.ID), url.Values{"role": {"editor"}})
	if m, _ := f.store.GetMembership(ctx, f.a.hh.ID, newbie.ID); m == nil || m.Role != db.HouseholdRoleEditor {
		t.Errorf("after role change = %+v, want editor", m)
	}

	olga, _ := f.store.GetUserByUsername(ctx, "olga")
	f.do("olga", "POST", fmt.Sprintf("/households/members/%d/role", olga.ID), url.Values{"role": {"viewer"}})
	if m, _ := f.store.GetMembership(ctx, f.a.hh.ID, olga.ID); m.Role != db.HouseholdRoleOwner {
		t.Errorf("owner demoted themselves: role = %s", m.Role)
	}

	f.do("olga", "POST", fmt.Sprintf("/households/members/%d/remove", newbie.ID), url.Values{})
	if m, _ := f.store.GetMembership(ctx, f.a.hh.ID, newbie.ID); m != nil {
		t.Errorf("still a member after removal: %+v", m)
	}

	// Bob owns B; he can't touch A's people even by id.
	w = f.do("bob", "POST", fmt.Sprintf("/households/members/%d/remove", olga.ID), url.Values{})
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-household member removal: %d, want 404", w.Code)
	}
}

// TestImagesScopedToHousehold: a recipe photo is served only to households
// whose recipes reference it, so a leaked or guessed file name from another
// household 404s.
func TestImagesScopedToHousehold(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha.jpg"), []byte("\xff\xd8\xff\xe0 fake jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newRBACFixtureWith(t, func(c *config.Config) { c.RecipeImageDir = dir })
	if _, err := f.store.CreateCatalogRecipe(ctx, db.CreateCatalogRecipeParams{
		HouseholdID: f.a.hh.ID, Title: "Alpha stew", SourceKind: "manual", ImagePath: "alpha.jpg", Servings: 2,
	}); err != nil {
		t.Fatal(err)
	}

	if w := f.do("vera", "GET", "/recipe-images/alpha.jpg", nil); w.Code != http.StatusOK {
		t.Errorf("own household's photo: %d, want 200", w.Code)
	} else if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("image served without nosniff")
	}
	if w := f.do("bob", "GET", "/recipe-images/alpha.jpg", nil); w.Code != http.StatusNotFound {
		t.Errorf("other household's photo: %d, want 404", w.Code)
	}
	if w := f.do("alice", "GET", "/recipe-images/..%2Fsecret", nil); w.Code == http.StatusOK {
		t.Errorf("path traversal served: %d", w.Code)
	}
}

// TestControlsFollowRole: pages hide what the role can't use (the gates
// still enforce; this is so nobody is offered a button that 403s).
func TestControlsFollowRole(t *testing.T) {
	f := newRBACFixture(t)

	prefs := func(user string) string {
		w := f.do(user, "GET", "/preferences", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /preferences as %s: %d", user, w.Code)
		}
		return w.Body.String()
	}
	if body := prefs("olga"); !strings.Contains(body, "Save preferences") || strings.Contains(body, `class="fieldset-plain" disabled`) {
		t.Error("owner: preferences should be editable")
	}
	for _, u := range []string{"alice", "vera"} {
		if body := prefs(u); strings.Contains(body, "Save preferences") || !strings.Contains(body, `class="fieldset-plain" disabled`) {
			t.Errorf("%s: preferences should render read-only", u)
		}
	}

	w := f.do("vera", "GET", "/plan", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /plan as viewer: %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, `data-role="viewer"`) {
		t.Error(`viewer's page is missing <body data-role="viewer">`)
	}
}

// TestSingleTenant: with ENABLE_MULTI_TENANT off, the oldest household
// (Alpha) is the only one. Switching, creating and deleting households 404;
// Alpha's members work as usual; Bravo's owner - a leftover from a
// multi-tenant install - has no household at all; and the admin lands in
// Alpha even after asking for Bravo.
func TestSingleTenant(t *testing.T) {
	f := newRBACFixtureWith(t, func(c *config.Config) { c.MultiTenant = false })

	for _, path := range []string{"/households/switch", "/households", "/households/delete"} {
		if w := f.do("root", "POST", path, url.Values{"household_id": {fmt.Sprint(f.b.hh.ID)}, "name": {"X"}, "budget": {"10"}}); w.Code != http.StatusNotFound {
			t.Errorf("POST %s: %d, want 404", path, w.Code)
		}
	}

	lock := func(mealID int64) string { return fmt.Sprintf("/meals/%d/lock", mealID) }
	if got := f.do("alice", "POST", lock(f.a.meal.ID), url.Values{"locked": {"1"}}).Code; got != http.StatusSeeOther {
		t.Errorf("alice in Alpha: %d, want 303", got)
	}
	w := f.do("bob", "POST", lock(f.b.meal.ID), url.Values{"locked": {"1"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/households" {
		t.Errorf("bob (Bravo only): %d -> %q, want 303 -> /households", w.Code, w.Header().Get("Location"))
	}
	if got := f.do("root", "POST", lock(f.b.meal.ID), url.Values{"locked": {"1"}}).Code; got != http.StatusNotFound {
		t.Errorf("admin touching Bravo's meal: %d, want 404 (acting in Alpha)", got)
	}
}
