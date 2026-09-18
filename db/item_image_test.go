package db_test

import (
	"context"
	"testing"
)

// TestSetItemImageSource verifies a discovered photo URL can be persisted
// independent of downloading it (the background image backfill scheduler's
// two-step flow: match, then download later), and that it never touches
// ImagePath.
func TestSetItemImageSource(t *testing.T) {
	ctx := context.Background()
	store, hh := newPlansTestStore(t)
	item := newItem(t, store, hh.ID, "Ribeye steak", "ribeye steak", "builtin")

	if err := store.SetItemImageSource(ctx, item.ID, "https://example.com/steak.jpg", "Steak photo by example.com"); err != nil {
		t.Fatalf("SetItemImageSource: %v", err)
	}

	got, err := store.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if got.ImageSourceURL != "https://example.com/steak.jpg" {
		t.Errorf("ImageSourceURL = %q, want the saved URL", got.ImageSourceURL)
	}
	if got.ImageAttribution != "Steak photo by example.com" {
		t.Errorf("ImageAttribution = %q, want the saved credit line", got.ImageAttribution)
	}
	if got.ImagePath != "" {
		t.Errorf("ImagePath = %q, want empty - SetItemImageSource must not touch it", got.ImagePath)
	}

	// A later SetItemImage (the actual download landing) must still work
	// normally on top of this.
	if err := store.SetItemImage(ctx, item.ID, "abc123.jpg", "Steak photo by example.com"); err != nil {
		t.Fatalf("SetItemImage: %v", err)
	}
	got, err = store.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if got.ImagePath != "abc123.jpg" {
		t.Errorf("ImagePath = %q, want abc123.jpg", got.ImagePath)
	}
}
