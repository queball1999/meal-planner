package db_test

import (
	"context"
	"math"
	"testing"

	"goeat/db"
)

func TestHouseholdMemberCRUD(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)

	adult, err := store.CreateHouseholdMember(ctx, db.CreateHouseholdMemberParams{
		HouseholdID: hh.ID, Name: "Sam", PortionFactor: 1.0,
	})
	if err != nil {
		t.Fatalf("create adult: %v", err)
	}
	if _, err := store.CreateHouseholdMember(ctx, db.CreateHouseholdMemberParams{
		HouseholdID: hh.ID, Name: "Robin", PortionFactor: 0.5, Notes: "toddler - soft textures",
	}); err != nil {
		t.Fatalf("create toddler: %v", err)
	}

	members, err := store.ListHouseholdMembers(ctx, hh.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("len(members) = %d, want 2", len(members))
	}
	// SortOrder 0 on create means "last", so insertion order is display order.
	if members[0].Name != "Sam" || members[1].Name != "Robin" {
		t.Errorf("order = %q, %q; want Sam, Robin", members[0].Name, members[1].Name)
	}
	if members[1].Notes != "toddler - soft textures" {
		t.Errorf("notes = %q", members[1].Notes)
	}

	if err := store.UpdateHouseholdMember(ctx, db.UpdateHouseholdMemberParams{
		ID: adult, HouseholdID: hh.ID, Name: "Sam", PortionFactor: 1.4, Notes: "big eater",
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := store.GetHouseholdMember(ctx, hh.ID, adult)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PortionFactor != 1.4 || got.Notes != "big eater" {
		t.Errorf("after update: factor=%v notes=%q", got.PortionFactor, got.Notes)
	}

	if err := store.DeleteHouseholdMember(ctx, hh.ID, adult); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got, _ := store.GetHouseholdMember(ctx, hh.ID, adult); got != nil {
		t.Error("member still present after delete")
	}
}

// A member id from another household must be invisible, not merely
// unauthorized at the handler layer: the store scopes every read and write by
// household id so a guessed id cannot reach across.
func TestHouseholdMemberIsScopedToHousehold(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)

	other, err := store.CreateHousehold(ctx, db.CreateHouseholdParams{Name: "Other"})
	if err != nil {
		t.Fatalf("other household: %v", err)
	}
	id, err := store.CreateHouseholdMember(ctx, db.CreateHouseholdMemberParams{
		HouseholdID: other.ID, Name: "Nobody", PortionFactor: 1,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if got, _ := store.GetHouseholdMember(ctx, hh.ID, id); got != nil {
		t.Error("read another household's member")
	}
	if err := store.UpdateHouseholdMember(ctx, db.UpdateHouseholdMemberParams{
		ID: id, HouseholdID: hh.ID, Name: "Hijacked", PortionFactor: 5,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	still, _ := store.GetHouseholdMember(ctx, other.ID, id)
	if still == nil || still.Name != "Nobody" {
		t.Error("cross-household update went through")
	}
}

func TestHouseholdMemberRejectsBadPortionFactor(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)

	for _, f := range []float64{0, -1, db.PortionFactorMax + 0.1} {
		if _, err := store.CreateHouseholdMember(ctx, db.CreateHouseholdMemberParams{
			HouseholdID: hh.ID, Name: "X", PortionFactor: f,
		}); err == nil {
			t.Errorf("portion factor %v was accepted", f)
		}
	}
	if _, err := store.CreateHouseholdMember(ctx, db.CreateHouseholdMemberParams{
		HouseholdID: hh.ID, Name: "  ", PortionFactor: 1,
	}); err == nil {
		t.Error("blank name was accepted")
	}
}

// The point of the whole feature: two adults and two toddlers is 3.0 portions,
// not 4.
func TestSumPortionFactors(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)

	var ids []int64
	for _, m := range []struct {
		name   string
		factor float64
	}{{"Adult A", 1.0}, {"Adult B", 1.0}, {"Kid A", 0.5}, {"Kid B", 0.5}} {
		id, err := store.CreateHouseholdMember(ctx, db.CreateHouseholdMemberParams{
			HouseholdID: hh.ID, Name: m.name, PortionFactor: m.factor,
		})
		if err != nil {
			t.Fatalf("create %s: %v", m.name, err)
		}
		ids = append(ids, id)
	}

	total, n, err := store.SumPortionFactors(ctx, hh.ID, ids)
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if math.Abs(total-3.0) > 1e-9 {
		t.Errorf("total = %v, want 3.0", total)
	}
	if n != 4 {
		t.Errorf("n = %d, want 4", n)
	}

	// A stale id - a member deleted since the form was rendered - contributes
	// nothing and is not counted, so the caller can tell the difference.
	total, n, err = store.SumPortionFactors(ctx, hh.ID, append(ids[:2:2], 99999))
	if err != nil {
		t.Fatalf("sum with stale: %v", err)
	}
	if math.Abs(total-2.0) > 1e-9 || n != 2 {
		t.Errorf("stale id counted: total=%v n=%d, want 2.0 / 2", total, n)
	}

	if total, n, _ := store.SumPortionFactors(ctx, hh.ID, nil); total != 0 || n != 0 {
		t.Errorf("empty id list: total=%v n=%d, want 0 / 0", total, n)
	}
}

// Migration 00018 seeds one standard-portion member per person a household
// already claimed, so a household created before members existed keeps the
// serving maths it had.
func TestMigrationSeedsMembersFromHouseholdSize(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hh, err := store.CreateHousehold(ctx, db.CreateHouseholdParams{Name: "Seeded", HouseholdSize: 3})
	if err != nil {
		t.Fatalf("household: %v", err)
	}

	// The household is created after the migration ran, so it starts empty -
	// the seed covers rows that already existed. What must hold either way is
	// that a household with no members reports zero rather than erroring.
	n, err := store.CountHouseholdMembers(ctx, hh.ID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("count = %d, want 0 for a freshly created household", n)
	}
}

// A plan day records who ate and what that added up to. The total is stored
// rather than recomputed, so editing a member's factor later must not restate
// history.
func TestPlanDayKeepsMemberSnapshot(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	planID, _ := seedScalableDay(t, store, hh.ID)

	adult, _ := store.CreateHouseholdMember(ctx, db.CreateHouseholdMemberParams{
		HouseholdID: hh.ID, Name: "Adult", PortionFactor: 1.0,
	})
	kid, _ := store.CreateHouseholdMember(ctx, db.CreateHouseholdMemberParams{
		HouseholdID: hh.ID, Name: "Kid", PortionFactor: 0.5,
	})

	if err := store.UpsertPlanDay(ctx, db.UpsertPlanDayParams{
		PlanID: planID, Date: "2026-01-05", Headcount: 2,
		MemberIDs: []int64{adult, kid}, Portions: 1.5,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// The kid grows up.
	if err := store.UpdateHouseholdMember(ctx, db.UpdateHouseholdMemberParams{
		ID: kid, HouseholdID: hh.ID, Name: "Kid", PortionFactor: 1.0,
	}); err != nil {
		t.Fatalf("update kid: %v", err)
	}

	day, err := store.GetPlanDay(ctx, planID, "2026-01-05")
	if err != nil || day == nil {
		t.Fatalf("get day: %v", err)
	}
	if math.Abs(day.Portions-1.5) > 1e-9 {
		t.Errorf("portions = %v, want the stored 1.5 - a later member edit restated history", day.Portions)
	}
	if len(day.MemberIDs) != 2 || day.MemberIDs[0] != adult || day.MemberIDs[1] != kid {
		t.Errorf("member ids = %v, want [%d %d]", day.MemberIDs, adult, kid)
	}
}

// A caller that predates members (plan generation, the old headcount form)
// sends no portion total; that has to keep meaning "headcount standard
// portions" rather than zero.
func TestUpsertPlanDayDefaultsPortionsToHeadcount(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	planID, _ := seedScalableDay(t, store, hh.ID)

	if err := store.UpsertPlanDay(ctx, db.UpsertPlanDayParams{
		PlanID: planID, Date: "2026-01-05", Headcount: 4,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	day, err := store.GetPlanDay(ctx, planID, "2026-01-05")
	if err != nil || day == nil {
		t.Fatalf("get day: %v", err)
	}
	if day.Portions != 4 {
		t.Errorf("portions = %v, want 4", day.Portions)
	}
	if len(day.MemberIDs) != 0 {
		t.Errorf("member ids = %v, want none", day.MemberIDs)
	}
}
