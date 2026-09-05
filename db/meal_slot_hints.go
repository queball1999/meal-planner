package db

import (
	"context"
	"fmt"
	"time"
)

var slotOrder = []string{"breakfast", "lunch", "dinner"}

func (s *store) UpsertMealSlotHint(ctx context.Context, p UpsertMealSlotHintParams) error {
	parsed := p.ParsedJSON
	if parsed == "" {
		parsed = "null"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO meal_slot_hints (household_id, slot, raw_text, parsed_json, effort, updated_at)
		 VALUES (?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		 ON CONFLICT(household_id, slot) DO UPDATE SET
		     raw_text    = excluded.raw_text,
		     parsed_json = excluded.parsed_json,
		     effort      = excluded.effort,
		     updated_at  = excluded.updated_at`,
		p.HouseholdID, p.Slot, p.RawText, parsed, p.Effort)
	if err != nil {
		return fmt.Errorf("upsert meal slot hint %s: %w", p.Slot, err)
	}
	return nil
}

func (s *store) GetMealSlotHints(ctx context.Context, householdID int64) ([]*MealSlotHint, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, household_id, slot, raw_text, parsed_json, effort, updated_at
		   FROM meal_slot_hints WHERE household_id = ?`, householdID)
	if err != nil {
		return nil, fmt.Errorf("get meal slot hints: %w", err)
	}
	defer rows.Close()

	bySlot := make(map[string]*MealSlotHint)
	for rows.Next() {
		h, err := scanMealSlotHint(rows)
		if err != nil {
			return nil, err
		}
		bySlot[h.Slot] = h
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Return all three slots in fixed order, filling missing ones with defaults.
	out := make([]*MealSlotHint, 0, 3)
	for _, slot := range slotOrder {
		if h, ok := bySlot[slot]; ok {
			out = append(out, h)
		} else {
			out = append(out, &MealSlotHint{
				HouseholdID: householdID,
				Slot:        slot,
				ParsedJSON:  "null",
				Effort:      "standard",
			})
		}
	}
	return out, nil
}

func scanMealSlotHint(sc storeScanner) (*MealSlotHint, error) {
	var h MealSlotHint
	var updatedAt string
	if err := sc.Scan(&h.ID, &h.HouseholdID, &h.Slot, &h.RawText, &h.ParsedJSON, &h.Effort, &updatedAt); err != nil {
		return nil, fmt.Errorf("scan meal slot hint: %w", err)
	}
	h.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &h, nil
}
