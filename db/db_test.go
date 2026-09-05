package db_test

import (
	"context"
	"testing"

	"goeat/db"
)

func TestOpenAndMigrate(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if err := store.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	// Running migrations twice should be a no-op, not an error.
	if err := store.Migrate(); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}
