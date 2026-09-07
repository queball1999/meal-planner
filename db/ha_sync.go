package db

import "context"

// HASyncRow maps one household shopping-list term to a Home Assistant to-do item.
type HASyncRow struct {
	ID             int64
	HouseholdID    int64
	ItemID         *int64
	NormalizedTerm string
	HAUID          string
	HASummary      string
	HAStatus       string
	LocalChecked   bool
	LastPushedAt   string
	LastPulledAt   string
}

const haSyncColumns = `id, household_id, item_id, normalized_term, ha_uid,
	ha_summary, ha_status, local_checked, last_pushed_at, last_pulled_at`

// UpsertHASyncRowParams carries the fields written when (re)registering a term.
type UpsertHASyncRowParams struct {
	HouseholdID    int64
	ItemID         *int64
	NormalizedTerm string
	HASummary      string
}

// UpsertHASyncRow inserts or refreshes the mapping row for a term, leaving the
// HA uid / status / timestamps untouched on conflict (those are set by the
// push and pull steps).
func (s *store) UpsertHASyncRow(ctx context.Context, p UpsertHASyncRowParams) (*HASyncRow, error) {
	var itemID interface{}
	if p.ItemID != nil {
		itemID = *p.ItemID
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ha_sync_map (household_id, item_id, normalized_term, ha_summary)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(household_id, normalized_term) DO UPDATE SET
			item_id    = excluded.item_id,
			ha_summary = excluded.ha_summary`,
		p.HouseholdID, itemID, p.NormalizedTerm, p.HASummary)
	if err != nil {
		return nil, err
	}
	return s.GetHASyncByTerm(ctx, p.HouseholdID, p.NormalizedTerm)
}

func (s *store) ListHASyncRows(ctx context.Context, householdID int64) ([]*HASyncRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+haSyncColumns+` FROM ha_sync_map WHERE household_id = ?`, householdID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanHASyncRows(rows)
}

func (s *store) GetHASyncByTerm(ctx context.Context, householdID int64, term string) (*HASyncRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+haSyncColumns+` FROM ha_sync_map WHERE household_id = ? AND normalized_term = ?`,
		householdID, term)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err := scanHASyncRows(rows)
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return out[0], nil
}

// SetHASyncPushed records the uid/status/summary HA returned after a push,
// along with the local checked state that was just mirrored onto HA - so a
// later pull can tell "HA changed since our last push" from "still matches
// what we pushed" regardless of whether the last write to this row was a
// push or a pull.
func (s *store) SetHASyncPushed(ctx context.Context, id int64, haUID, haStatus, summary string, localChecked bool) error {
	v := 0
	if localChecked {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE ha_sync_map
		SET ha_uid = ?, ha_status = ?, ha_summary = ?, local_checked = ?,
		    last_pushed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ?`, haUID, haStatus, summary, v, id)
	return err
}

// SetHASyncPulled records the status observed in HA and the local checked state
// we reconciled it to.
func (s *store) SetHASyncPulled(ctx context.Context, id int64, haStatus string, localChecked bool) error {
	v := 0
	if localChecked {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE ha_sync_map
		SET ha_status = ?, local_checked = ?,
		    last_pulled_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ?`, haStatus, v, id)
	return err
}

func (s *store) DeleteHASyncRow(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM ha_sync_map WHERE id = ?`, id)
	return err
}

func scanHASyncRows(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]*HASyncRow, error) {
	var out []*HASyncRow
	for rows.Next() {
		var r HASyncRow
		var itemID *int64
		var checked int
		var pushedAt, pulledAt *string
		if err := rows.Scan(&r.ID, &r.HouseholdID, &itemID, &r.NormalizedTerm, &r.HAUID,
			&r.HASummary, &r.HAStatus, &checked, &pushedAt, &pulledAt); err != nil {
			return nil, err
		}
		r.ItemID = itemID
		r.LocalChecked = checked != 0
		if pushedAt != nil {
			r.LastPushedAt = *pushedAt
		}
		if pulledAt != nil {
			r.LastPulledAt = *pulledAt
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}
