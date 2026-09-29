package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"goeat/db"
)

// tenant is one household with one row of every kind HouseholdOwns checks
// that the tests below need.
type tenant struct {
	hh    *db.Household
	plan  *db.Plan
	meal  *db.Meal
	item  *db.Item
	store *db.GroceryStore
}

func newTenant(t *testing.T, store db.Store, name string) tenant {
	t.Helper()
	ctx := context.Background()
	hh, err := store.CreateHousehold(ctx, db.CreateHouseholdParams{
		Name: name, WeeklyBudgetCents: 10000, Country: "US", Timezone: "UTC", HouseholdSize: 2,
	})
	if err != nil {
		t.Fatalf("create household: %v", err)
	}
	p, err := store.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: hh.ID, WeekStart: "2026-09-27", WeekEnd: "2026-10-03"})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	m, err := store.CreateMeal(ctx, db.CreateMealParams{
		PlanID: p.ID, Day: "2026-09-28", Slot: "dinner", Title: name + " stew", Effort: "standard", Servings: 2, CookedPortions: 2,
	})
	if err != nil {
		t.Fatalf("create meal: %v", err)
	}
	it, err := store.CreateItem(ctx, db.CreateItemParams{HouseholdID: hh.ID, Name: "Garlic", NormalizedTerm: "garlic"})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	gs, err := store.CreateStore(ctx, db.UpsertStoreParams{HouseholdID: hh.ID, Name: name + " Mart", Kind: "grocery"})
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return tenant{hh: hh, plan: p, meal: m, item: it, store: gs}
}

func newMigratedStore(t *testing.T) db.Store {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	return store
}

// TestHouseholdOwnsIsolation is the tenancy boundary: every resource kind
// answers yes for its own household and no for the other one.
func TestHouseholdOwnsIsolation(t *testing.T) {
	ctx := context.Background()
	store := newMigratedStore(t)
	a := newTenant(t, store, "Alpha")
	b := newTenant(t, store, "Bravo")

	cases := []struct {
		kind db.ResourceKind
		a, b int64
	}{
		{db.ResPlan, a.plan.ID, b.plan.ID},
		{db.ResMeal, a.meal.ID, b.meal.ID},
		{db.ResItem, a.item.ID, b.item.ID},
		{db.ResStore, a.store.ID, b.store.ID},
	}
	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			for _, tc := range []struct {
				hh, id int64
				want   bool
			}{
				{a.hh.ID, c.a, true},
				{a.hh.ID, c.b, false},
				{b.hh.ID, c.b, true},
				{b.hh.ID, c.a, false},
				{a.hh.ID, 999999, false}, // missing row
			} {
				got, err := store.HouseholdOwns(ctx, tc.hh, c.kind, tc.id)
				if err != nil {
					t.Fatalf("HouseholdOwns(%d, %d): %v", tc.hh, tc.id, err)
				}
				if got != tc.want {
					t.Errorf("HouseholdOwns(hh=%d, id=%d) = %v, want %v", tc.hh, tc.id, got, tc.want)
				}
			}
		})
	}

	if _, err := store.HouseholdOwns(ctx, a.hh.ID, db.ResourceKind("nope"), 1); err == nil {
		t.Error("unknown resource kind: want error")
	}
}

// TestScopedChildDeletes covers QSS security design §17: a nested route's
// child id must belong to its parent, checked in the SQL.
func TestScopedChildDeletes(t *testing.T) {
	ctx := context.Background()
	store := newMigratedStore(t)
	a := newTenant(t, store, "Alpha")
	b := newTenant(t, store, "Bravo")

	// Conversions: B's conversion can't be deleted through A's item, and a
	// global conversion (item_id NULL) can't be deleted through any item.
	if err := store.UpsertUnitConversion(ctx, db.UpsertUnitConversionParams{ItemID: &b.item.ID, FromUnit: "clove", ToUnit: "g", Factor: 3}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertUnitConversion(ctx, db.UpsertUnitConversionParams{FromUnit: "zz_test", ToUnit: "g", Factor: 1}); err != nil {
		t.Fatal(err)
	}
	bConvs, _ := store.ListConversionsForItem(ctx, b.item.ID)
	globals, _ := store.ListGlobalConversions(ctx)
	var bConv, global int64
	for _, c := range bConvs {
		if c.FromUnit == "clove" {
			bConv = c.ID
		}
	}
	for _, c := range globals {
		if c.FromUnit == "zz_test" {
			global = c.ID
		}
	}
	if bConv == 0 || global == 0 {
		t.Fatalf("setup: conversions not found (b=%d global=%d)", bConv, global)
	}
	if err := store.DeleteUnitConversion(ctx, a.item.ID, bConv); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("delete B's conversion via A's item: err = %v, want ErrNotFound", err)
	}
	if err := store.DeleteUnitConversion(ctx, a.item.ID, global); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("delete global conversion via an item: err = %v, want ErrNotFound", err)
	}
	if err := store.DeleteUnitConversion(ctx, b.item.ID, bConv); err != nil {
		t.Errorf("delete own conversion: %v", err)
	}

	// Packages: same rule.
	if err := store.UpsertItemStorePackage(ctx, db.UpsertItemStorePackageParams{
		ItemID: b.item.ID, StoreID: b.store.ID, PurchaseUnit: "each", AmountPerPackage: 1, PriceCents: 99,
	}); err != nil {
		t.Fatal(err)
	}
	pkgs, _ := store.ListPackagesForItem(ctx, b.item.ID)
	if len(pkgs) != 1 {
		t.Fatalf("setup: %d packages", len(pkgs))
	}
	if err := store.DeleteItemStorePackage(ctx, a.item.ID, pkgs[0].ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("delete B's package via A's item: err = %v, want ErrNotFound", err)
	}
	if err := store.DeleteItemStorePackage(ctx, b.item.ID, pkgs[0].ID); err != nil {
		t.Errorf("delete own package: %v", err)
	}
}

func TestMembershipGuards(t *testing.T) {
	ctx := context.Background()
	store := newMigratedStore(t)
	a := newTenant(t, store, "Alpha")

	admin, _ := store.CreateUser(ctx, "admin1", "x", db.InstanceRoleAdmin)
	owner, _ := store.CreateUser(ctx, "owner1", "x", db.InstanceRoleMember)
	editor, _ := store.CreateUser(ctx, "editor1", "x", db.InstanceRoleMember)

	if err := store.UpsertMembership(ctx, a.hh.ID, owner.ID, db.HouseholdRoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertMembership(ctx, a.hh.ID, editor.ID, db.HouseholdRoleEditor); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertMembership(ctx, a.hh.ID, editor.ID, "superuser"); err == nil {
		t.Error("invalid role accepted")
	}

	// The only owner can be neither demoted nor removed, nor deleted.
	if err := store.SetMembershipRole(ctx, a.hh.ID, owner.ID, db.HouseholdRoleViewer); !errors.Is(err, db.ErrLastOwner) {
		t.Errorf("demote last owner: err = %v, want ErrLastOwner", err)
	}
	if err := store.DeleteMembership(ctx, a.hh.ID, owner.ID); !errors.Is(err, db.ErrLastOwner) {
		t.Errorf("remove last owner: err = %v, want ErrLastOwner", err)
	}
	if err := store.DeleteUser(ctx, owner.ID); !errors.Is(err, db.ErrLastOwner) {
		t.Errorf("delete last owner's account: err = %v, want ErrLastOwner", err)
	}

	// With a second owner, both succeed.
	if err := store.SetMembershipRole(ctx, a.hh.ID, editor.ID, db.HouseholdRoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := store.SetMembershipRole(ctx, a.hh.ID, owner.ID, db.HouseholdRoleViewer); err != nil {
		t.Errorf("demote an owner when another exists: %v", err)
	}

	// The only admin can't be demoted or deleted.
	if err := store.SetUserRole(ctx, admin.ID, db.InstanceRoleMember); !errors.Is(err, db.ErrLastAdmin) {
		t.Errorf("demote last admin: err = %v, want ErrLastAdmin", err)
	}
	if err := store.DeleteUser(ctx, admin.ID); !errors.Is(err, db.ErrLastAdmin) {
		t.Errorf("delete last admin: err = %v, want ErrLastAdmin", err)
	}

	got, err := store.ListMembershipsForUser(ctx, owner.ID)
	if err != nil || len(got) != 1 || got[0].Role != db.HouseholdRoleViewer || got[0].HouseholdName != "Alpha" {
		t.Errorf("ListMembershipsForUser = %+v, %v", got, err)
	}
}

func timeFarFuture() time.Time { return time.Now().Add(24 * time.Hour) }

// TestSessionActiveHousehold: the active household round-trips, and deleting
// the household clears it (ON DELETE SET NULL) rather than leaving a
// dangling id for middleware to trip over.
func TestSessionActiveHousehold(t *testing.T) {
	ctx := context.Background()
	store := newMigratedStore(t)
	a := newTenant(t, store, "Alpha")
	u, _ := store.CreateUser(ctx, "someone", "x", db.InstanceRoleMember)

	sess, err := store.CreateSession(ctx, u.ID, "hash1", "", "", timeFarFuture())
	if err != nil {
		t.Fatal(err)
	}
	if sess.ActiveHouseholdID != 0 {
		t.Fatalf("new session ActiveHouseholdID = %d, want 0", sess.ActiveHouseholdID)
	}
	if err := store.SetSessionHousehold(ctx, sess.ID, a.hh.ID); err != nil {
		t.Fatal(err)
	}
	sess, _ = store.GetSessionByTokenHash(ctx, "hash1")
	if sess.ActiveHouseholdID != a.hh.ID {
		t.Errorf("ActiveHouseholdID = %d, want %d", sess.ActiveHouseholdID, a.hh.ID)
	}
	if err := store.DeleteHousehold(ctx, a.hh.ID); err != nil {
		t.Fatal(err)
	}
	sess, _ = store.GetSessionByTokenHash(ctx, "hash1")
	if sess == nil || sess.ActiveHouseholdID != 0 {
		t.Errorf("after household delete: session = %+v, want ActiveHouseholdID 0", sess)
	}
}
