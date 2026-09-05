package db

import (
	"context"
	"time"
)

// Store is the application's complete data-access interface. All SQL lives in
// this package; no handler or service writes its own queries (§10, §12).
//
// Phase 0: Ping, Migrate, Close.
// Phase 1: User, Session, Household, AppEvent methods.
// Later phases add Plan, Meal, Pricing, etc.
type Store interface {
	// ── Lifecycle ──────────────────────────────────────────────────────────

	Ping(ctx context.Context) error
	Migrate() error
	Close() error

	// ── Users (§9.1, §10.1) ───────────────────────────────────────────────

	CreateUser(ctx context.Context, username, passwordHash, role string) (*User, error)
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	GetUserByID(ctx context.Context, id int64) (*User, error)

	// ── Sessions (§9.3, §10.1) ────────────────────────────────────────────

	CreateSession(ctx context.Context, userID int64, tokenHash, ipAddr, userAgent string, expiresAt time.Time) (*Session, error)
	GetSessionByTokenHash(ctx context.Context, hash string) (*Session, error)
	DeleteSession(ctx context.Context, id int64) error
	DeleteUserSessions(ctx context.Context, userID int64) error

	// ── Household (§10.1) ─────────────────────────────────────────────────

	// CreateHousehold creates the single household row.
	CreateHousehold(ctx context.Context, p CreateHouseholdParams) (*Household, error)

	// GetHousehold returns nil, nil when no household exists yet (setup not done).
	GetHousehold(ctx context.Context) (*Household, error)

	// ── Audit log (§9.3) ──────────────────────────────────────────────────

	LogEvent(ctx context.Context, e AppEvent) error
}
