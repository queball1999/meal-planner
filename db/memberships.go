package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// UpsertMembership gives userID the role in householdID, replacing any role
// they already had there.
func (s *store) UpsertMembership(ctx context.Context, householdID, userID int64, role string) error {
	if !ValidHouseholdRole(role) {
		return fmt.Errorf("upsert membership: invalid role %q", role)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO household_memberships (household_id, user_id, role) VALUES (?, ?, ?)
		 ON CONFLICT(household_id, user_id) DO UPDATE SET role = excluded.role`,
		householdID, userID, role)
	if err != nil {
		return fmt.Errorf("upsert membership: %w", err)
	}
	return nil
}

// DeleteMembership removes userID from householdID. Removing the last owner
// is refused: a household nobody can administer is only recoverable by an
// instance admin, and an owner locking themselves out by accident is the
// likelier story than one doing it on purpose.
func (s *store) DeleteMembership(ctx context.Context, householdID, userID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete membership: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var role string
	err = tx.QueryRowContext(ctx,
		`SELECT role FROM household_memberships WHERE household_id = ? AND user_id = ?`,
		householdID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete membership: %w", err)
	}
	if role == HouseholdRoleOwner {
		if err := ensureAnotherOwner(ctx, tx, householdID, userID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM household_memberships WHERE household_id = ? AND user_id = ?`,
		householdID, userID); err != nil {
		return fmt.Errorf("delete membership: %w", err)
	}
	return tx.Commit()
}

// SetMembershipRole changes an existing member's role. Demoting the last
// owner is refused for the same reason DeleteMembership refuses removing them.
func (s *store) SetMembershipRole(ctx context.Context, householdID, userID int64, role string) error {
	if !ValidHouseholdRole(role) {
		return fmt.Errorf("set membership role: invalid role %q", role)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("set membership role: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var current string
	err = tx.QueryRowContext(ctx,
		`SELECT role FROM household_memberships WHERE household_id = ? AND user_id = ?`,
		householdID, userID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("set membership role: user %d is not in household %d", userID, householdID)
	}
	if err != nil {
		return fmt.Errorf("set membership role: %w", err)
	}
	if current == HouseholdRoleOwner && role != HouseholdRoleOwner {
		if err := ensureAnotherOwner(ctx, tx, householdID, userID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE household_memberships SET role = ? WHERE household_id = ? AND user_id = ?`,
		role, householdID, userID); err != nil {
		return fmt.Errorf("set membership role: %w", err)
	}
	return tx.Commit()
}

// ErrLastOwner is returned when a change would leave a household with no owner.
var ErrLastOwner = errors.New("a household needs at least one owner")

func ensureAnotherOwner(ctx context.Context, tx *sql.Tx, householdID, exceptUserID int64) error {
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM household_memberships
		  WHERE household_id = ? AND role = 'owner' AND user_id <> ?`,
		householdID, exceptUserID).Scan(&n); err != nil {
		return fmt.Errorf("count owners: %w", err)
	}
	if n == 0 {
		return ErrLastOwner
	}
	return nil
}

// GetMembership returns userID's seat in householdID, or nil, nil if none.
func (s *store) GetMembership(ctx context.Context, householdID, userID int64) (*HouseholdMembership, error) {
	rows, err := s.queryMemberships(ctx,
		`WHERE m.household_id = ? AND m.user_id = ?`, householdID, userID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

// ListMembershipsForUser returns every household userID belongs to, by
// household name.
func (s *store) ListMembershipsForUser(ctx context.Context, userID int64) ([]*HouseholdMembership, error) {
	return s.queryMemberships(ctx, `WHERE m.user_id = ? ORDER BY h.name COLLATE NOCASE, h.id`, userID)
}

// ListMembershipsForHousehold returns everyone in householdID, owners first.
func (s *store) ListMembershipsForHousehold(ctx context.Context, householdID int64) ([]*HouseholdMembership, error) {
	return s.queryMemberships(ctx,
		`WHERE m.household_id = ?
		 ORDER BY CASE m.role WHEN 'owner' THEN 0 WHEN 'editor' THEN 1 ELSE 2 END,
		          u.username COLLATE NOCASE`, householdID)
}

func (s *store) queryMemberships(ctx context.Context, where string, args ...any) ([]*HouseholdMembership, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.household_id, h.name, m.user_id, u.username, m.role, m.created_at
		   FROM household_memberships m
		   JOIN households h ON h.id = m.household_id
		   JOIN users u      ON u.id = m.user_id `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("list memberships: %w", err)
	}
	defer rows.Close()
	var out []*HouseholdMembership
	for rows.Next() {
		var m HouseholdMembership
		var createdAt string
		if err := rows.Scan(&m.HouseholdID, &m.HouseholdName, &m.UserID, &m.Username, &m.Role, &createdAt); err != nil {
			return nil, fmt.Errorf("scan membership: %w", err)
		}
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, &m)
	}
	return out, rows.Err()
}

// ErrNotFound is returned by scoped writes whose WHERE clause (id plus its
// parent) matched no row.
var ErrNotFound = errors.New("not found")

// oneRow turns "the scoped UPDATE/DELETE touched nothing" into ErrNotFound.
func oneRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Resource kinds HouseholdOwns can check. Each maps to one SQL query that
// walks from the row to its household.
type ResourceKind string

const (
	ResPlan         ResourceKind = "plan"
	ResMeal         ResourceKind = "meal"
	ResShoppingItem ResourceKind = "shopping_item"
	ResPantryItem   ResourceKind = "pantry_item"
	ResItem         ResourceKind = "item"
	ResRecipe       ResourceKind = "recipe"
	ResStore        ResourceKind = "store"
	ResManualPrice  ResourceKind = "manual_price"
	ResItemPackage  ResourceKind = "item_package"
	ResConversion   ResourceKind = "conversion"
)

// ownershipQueries resolve an id to the household that owns it. A global
// unit conversion (item_id NULL) has no household and is never "owned".
var ownershipQueries = map[ResourceKind]string{
	ResPlan:         `SELECT household_id FROM plans WHERE id = ?`,
	ResMeal:         `SELECT p.household_id FROM meals m JOIN plans p ON p.id = m.plan_id WHERE m.id = ?`,
	ResShoppingItem: `SELECT p.household_id FROM shopping_list_items s JOIN plans p ON p.id = s.plan_id WHERE s.id = ?`,
	ResPantryItem:   `SELECT household_id FROM pantry_items WHERE id = ?`,
	ResItem:         `SELECT household_id FROM items WHERE id = ?`,
	ResRecipe:       `SELECT household_id FROM catalog_recipes WHERE id = ?`,
	ResStore:        `SELECT household_id FROM stores WHERE id = ?`,
	ResManualPrice:  `SELECT st.household_id FROM manual_prices mp JOIN stores st ON st.id = mp.store_id WHERE mp.id = ?`,
	ResItemPackage:  `SELECT i.household_id FROM item_store_packages k JOIN items i ON i.id = k.item_id WHERE k.id = ?`,
	ResConversion:   `SELECT i.household_id FROM unit_conversions c JOIN items i ON i.id = c.item_id WHERE c.id = ?`,
}

// HouseholdOwns reports whether the row (kind, id) belongs to householdID.
// This is the tenancy boundary for every handler that takes an id from a
// request: a false here must end the request. A missing row is false, nil.
func (s *store) HouseholdOwns(ctx context.Context, householdID int64, kind ResourceKind, id int64) (bool, error) {
	q, ok := ownershipQueries[kind]
	if !ok {
		return false, fmt.Errorf("household owns: unknown resource kind %q", kind)
	}
	var owner int64
	err := s.db.QueryRowContext(ctx, q, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("household owns %s %d: %w", kind, id, err)
	}
	return owner == householdID, nil
}

// Image kinds HouseholdUsesImage can check: the two upload directories.
type ImageKind string

const (
	ImageRecipe ImageKind = "recipe"
	ImageItem   ImageKind = "item"
)

var imageQueries = map[ImageKind]string{
	ImageRecipe: `SELECT EXISTS (SELECT 1 FROM catalog_recipes WHERE household_id = ? AND image_path = ?)`,
	ImageItem:   `SELECT EXISTS (SELECT 1 FROM items WHERE household_id = ? AND image_path = ?)`,
}

// HouseholdUsesImage reports whether one of householdID's recipes or items
// points at the stored image file name - the ownership check for serving
// /recipe-images/{name} and /item-images/{name}. Files are shared on disk, so
// ownership is "referenced by one of your rows", not "created by you".
func (s *store) HouseholdUsesImage(ctx context.Context, householdID int64, kind ImageKind, name string) (bool, error) {
	q, ok := imageQueries[kind]
	if !ok {
		return false, fmt.Errorf("household uses image: unknown kind %q", kind)
	}
	var used bool
	if err := s.db.QueryRowContext(ctx, q, householdID, name).Scan(&used); err != nil {
		return false, fmt.Errorf("household uses image: %w", err)
	}
	return used, nil
}
