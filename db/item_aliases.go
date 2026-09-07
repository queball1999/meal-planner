package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ItemAlias is one term the household has said means a particular catalog item.
type ItemAlias struct {
	ID          int64
	HouseholdID int64
	ItemID      int64
	Alias       string // normalized, same shape as items.normalized_term
	Source      string // "manual" | "auto"
	CreatedAt   time.Time
}

// GetItemByAlias resolves a normalized term through the alias table, returning
// nil when nothing claims it. The second lookup catalog.EnsureItem does, after
// an exact normalized_term match and before creating anything.
func (s *store) GetItemByAlias(ctx context.Context, householdID int64, alias string) (*Item, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return nil, nil
	}
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT item_id FROM item_aliases WHERE household_id = ? AND alias = ?`,
		householdID, alias).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.GetItem(ctx, id)
}

// CreateItemAlias records that a term means an item.
//
// An existing alias is repointed rather than rejected: the shopping-list match
// dialog exists precisely so a person can correct a bad guess, and refusing
// the correction because a wrong alias is already on file would make that
// impossible without a delete step first.
func (s *store) CreateItemAlias(ctx context.Context, householdID, itemID int64, alias, source string) error {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return errors.New("alias is required")
	}
	if source != "manual" && source != "auto" {
		source = "manual"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO item_aliases (household_id, item_id, alias, source)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(household_id, alias) DO UPDATE SET
		  item_id = excluded.item_id,
		  source  = excluded.source`,
		householdID, itemID, alias, source)
	return err
}

// ListItemAliases returns the terms pointing at one item, newest last.
func (s *store) ListItemAliases(ctx context.Context, householdID, itemID int64) ([]*ItemAlias, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, household_id, item_id, alias, source, created_at
		FROM item_aliases
		WHERE household_id = ? AND item_id = ?
		ORDER BY id`, householdID, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*ItemAlias
	for rows.Next() {
		var a ItemAlias
		var created string
		if err := rows.Scan(&a.ID, &a.HouseholdID, &a.ItemID, &a.Alias, &a.Source, &created); err != nil {
			return nil, err
		}
		a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, &a)
	}
	return out, rows.Err()
}

// DeleteItemAlias removes one alias.
func (s *store) DeleteItemAlias(ctx context.Context, householdID, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM item_aliases WHERE id = ? AND household_id = ?`, id, householdID)
	return err
}

// MergeItems repoints everything that references `from` at `to` and deletes
// `from`, recording the merged item's term as an alias so the name that
// created it resolves correctly next time.
//
// This is what confirming a match in the shopping list actually does. Without
// the merge, correcting "chicken breasts" -> "Chicken breast" would fix the
// one shopping line while leaving the placeholder item behind, still holding
// its own price history and still collecting future ingredients.
//
// Runs in one transaction: a half-merged item would be referenced from some
// tables and deleted from others.
func (s *store) MergeItems(ctx context.Context, householdID, from, to int64) error {
	if from == to {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Both items must belong to the caller's household, checked inside the
	// transaction so an id from elsewhere cannot be merged by guessing it.
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM items WHERE household_id = ? AND id IN (?, ?)`,
		householdID, from, to).Scan(&n); err != nil {
		return err
	}
	if n != 2 {
		return errors.New("both items must exist in this household")
	}

	var fromTerm string
	if err := tx.QueryRowContext(ctx,
		`SELECT normalized_term FROM items WHERE id = ?`, from).Scan(&fromTerm); err != nil {
		return err
	}

	for _, q := range []string{
		`UPDATE meal_ingredients    SET item_id = ? WHERE item_id = ?`,
		`UPDATE shopping_list_items SET item_id = ? WHERE item_id = ?`,
		`UPDATE price_cache         SET item_id = ? WHERE item_id = ?`,
		`UPDATE manual_prices       SET item_id = ? WHERE item_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, to, from); err != nil {
			return err
		}
	}

	// The pantry has a UNIQUE(household_id, normalized_term), so a pantry row
	// for each item cannot simply both point at `to`. Quantities are added
	// together - the household does own both lots - and the loser is dropped.
	if _, err := tx.ExecContext(ctx, `
		UPDATE pantry_items
		SET quantity_on_hand = quantity_on_hand + COALESCE((
		        SELECT p2.quantity_on_hand FROM pantry_items p2
		        WHERE p2.household_id = ? AND p2.item_id = ?
		    ), 0)
		WHERE household_id = ? AND item_id = ?`,
		householdID, from, householdID, to); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM pantry_items
		WHERE household_id = ? AND item_id = ?
		  AND EXISTS (SELECT 1 FROM pantry_items p2 WHERE p2.household_id = ? AND p2.item_id = ?)`,
		householdID, from, householdID, to); err != nil {
		return err
	}
	// Whatever pantry row is left keeps the surviving item.
	if _, err := tx.ExecContext(ctx,
		`UPDATE pantry_items SET item_id = ? WHERE household_id = ? AND item_id = ?`,
		to, householdID, from); err != nil {
		return err
	}

	// Aliases that pointed at the loser now point at the winner, and the
	// loser's own term becomes an alias so the name that created it resolves
	// correctly next time.
	if _, err := tx.ExecContext(ctx,
		`UPDATE OR REPLACE item_aliases SET item_id = ? WHERE household_id = ? AND item_id = ?`,
		to, householdID, from); err != nil {
		return err
	}
	if fromTerm != "" {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO item_aliases (household_id, item_id, alias, source)
			VALUES (?, ?, ?, 'manual')
			ON CONFLICT(household_id, alias) DO UPDATE SET item_id = excluded.item_id`,
			householdID, to, fromTerm); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM items WHERE id = ? AND household_id = ?`, from, householdID); err != nil {
		return err
	}
	return tx.Commit()
}
