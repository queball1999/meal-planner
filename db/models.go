package db

import "time"

// User represents a household member account (§9.1, §10.1).
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string // "admin" | "read_only"
	TOTPEnabled  bool
	CreatedAt    time.Time
}

// Session is an authenticated session token (§9.3, §10.1).
type Session struct {
	ID        int64
	UserID    int64
	TokenHash string
	ExpiresAt time.Time
	IPAddress string
	UserAgent string
	CreatedAt time.Time
}

// Household is the single household row (§10.1). In v1 there is always at
// most one; the table is kept for the multi-tenant migration path.
type Household struct {
	ID                int64
	Name              string
	WeeklyBudgetCents int64
	Country           string
	ZIPCode           string
	RegionLabel       string
	Timezone          string
	HouseholdSize     int
	CreatedAt         time.Time
}

// CreateHouseholdParams bundles the household creation inputs (§4.5, §10.1).
type CreateHouseholdParams struct {
	Name              string
	WeeklyBudgetCents int64
	Country           string
	ZIPCode           string
	Timezone          string
	HouseholdSize     int
}

// AppEvent is a single audit-log entry (§9.3, §10.1).
type AppEvent struct {
	ActorUserID *int64
	ActorLabel  string
	Action      string
	TargetType  string
	TargetID    string
	Metadata    string // JSON blob or empty string
	IPAddress   string
	UserAgent   string
	Status      string // "ok" | "error"
}
