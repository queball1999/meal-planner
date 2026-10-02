package recipes

import (
	"context"
	"errors"
	"testing"

	"goeat/db"
)

func TestSourceKey(t *testing.T) {
	same := [][]string{
		{"https://www.example.com/recipe/pasta/", "http://example.com/recipe/pasta",
			"https://m.example.com/recipe/pasta?utm_source=pin&fbclid=x#comments"},
		{"https://www.tiktok.com/@chef/video/7301234567890?is_from_webapp=1&sender_device=pc",
			"https://tiktok.com/@chef/video/7301234567890"},
		{"https://www.youtube.com/shorts/abc123XYZ?feature=share", "https://youtu.be/abc123XYZ?si=foo",
			"https://www.youtube.com/watch?v=abc123XYZ&pp=bar", "https://m.youtube.com/watch?v=abc123XYZ"},
		{"https://www.instagram.com/reel/C1a2b3/?igsh=xyz", "https://instagram.com/reels/C1a2b3",
			"https://www.instagram.com/chef/reel/C1a2b3/"},
		{"https://example.com/r?id=2&page=1", "https://example.com/r?page=1&id=2"},
	}
	for _, group := range same {
		want := SourceKey(group[0])
		if want == "" {
			t.Fatalf("SourceKey(%q) is empty", group[0])
		}
		for _, u := range group[1:] {
			if got := SourceKey(u); got != want {
				t.Errorf("SourceKey(%q) = %q, want %q (same as %q)", u, got, want, group[0])
			}
		}
	}

	different := [][2]string{
		{"https://example.com/r?id=1", "https://example.com/r?id=2"},
		{"https://example.com/recipe/pasta", "https://other.com/recipe/pasta"},
		{"https://www.youtube.com/watch?v=aaa", "https://www.youtube.com/watch?v=bbb"},
	}
	for _, d := range different {
		if SourceKey(d[0]) == SourceKey(d[1]) {
			t.Errorf("SourceKey(%q) == SourceKey(%q), want different", d[0], d[1])
		}
	}
	for _, bad := range []string{"", "not a url", "ftp://example.com/x", "javascript:alert(1)"} {
		if got := SourceKey(bad); got != "" {
			t.Errorf("SourceKey(%q) = %q, want \"\"", bad, got)
		}
	}
}

func TestImportReturnsDuplicateBeforeFetching(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hh, err := store.CreateHousehold(ctx, db.CreateHouseholdParams{Name: "T", HouseholdSize: 2, Timezone: "UTC"})
	if err != nil {
		t.Fatalf("household: %v", err)
	}
	other, err := store.CreateHousehold(ctx, db.CreateHouseholdParams{Name: "U", HouseholdSize: 2, Timezone: "UTC"})
	if err != nil {
		t.Fatalf("household: %v", err)
	}
	cr, err := store.CreateCatalogRecipe(ctx, db.CreateCatalogRecipeParams{
		HouseholdID: hh.ID, Title: "Pasta", SourceKind: "imported",
		SourceURL: "https://www.example.invalid/recipe/pasta",
	})
	if err != nil {
		t.Fatalf("recipe: %v", err)
	}

	// The .invalid host can't resolve, so reaching the fetch would fail
	// with a fetch error rather than a DuplicateError.
	_, err = Import(ctx, store, hh.ID, "https://example.invalid/recipe/pasta/?utm_source=x", "")
	var dup *DuplicateError
	if !errors.As(err, &dup) || dup.ID != cr.ID {
		t.Fatalf("Import err = %v, want DuplicateError for recipe %d", err, cr.ID)
	}

	// Another household's recipe is not a duplicate.
	if found, err := FindBySource(ctx, store, other.ID, "https://example.invalid/recipe/pasta"); err != nil || found != nil {
		t.Fatalf("FindBySource(other household) = %v, %v; want nil", found, err)
	}
}
