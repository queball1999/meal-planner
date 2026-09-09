package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

func (s *store) UpsertPreferences(ctx context.Context, p UpsertPreferencesParams) error {
	diet, _ := json.Marshal(p.DietTags)
	cuisines, _ := json.Marshal(p.Cuisines)
	dislikes, _ := json.Marshal(p.Dislikes)
	lt := 0
	if p.LeftoverTolerance {
		lt = 1
	}
	unitSystem := p.UnitSystem
	if unitSystem == "" {
		unitSystem = "as-is"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO preferences (household_id, diet_tags, cuisines, dislikes, leftover_tolerance, unit_system, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		 ON CONFLICT(household_id) DO UPDATE SET
		     diet_tags          = excluded.diet_tags,
		     cuisines           = excluded.cuisines,
		     dislikes           = excluded.dislikes,
		     leftover_tolerance = excluded.leftover_tolerance,
		     unit_system        = excluded.unit_system,
		     updated_at         = excluded.updated_at`,
		p.HouseholdID, string(diet), string(cuisines), string(dislikes), lt, unitSystem)
	if err != nil {
		return fmt.Errorf("upsert preferences: %w", err)
	}
	return nil
}

func (s *store) GetPreferences(ctx context.Context, householdID int64) (*Preferences, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, household_id, diet_tags, cuisines, dislikes, leftover_tolerance, unit_system, updated_at
		   FROM preferences WHERE household_id = ?`, householdID)

	var p Preferences
	var dietJSON, cuisinesJSON, dislikesJSON, unitSystem, updatedAt string
	var lt int
	err := row.Scan(&p.ID, &p.HouseholdID, &dietJSON, &cuisinesJSON, &dislikesJSON, &lt, &unitSystem, &updatedAt)
	if err != nil {
		// No row → return zero-value defaults.
		p.HouseholdID = householdID
		p.LeftoverTolerance = true
		p.UnitSystem = "as-is"
		return &p, nil
	}

	_ = json.Unmarshal([]byte(dietJSON), &p.DietTags)
	_ = json.Unmarshal([]byte(cuisinesJSON), &p.Cuisines)
	_ = json.Unmarshal([]byte(dislikesJSON), &p.Dislikes)
	p.LeftoverTolerance = lt != 0
	p.UnitSystem = unitSystem
	if p.UnitSystem == "" {
		p.UnitSystem = "as-is"
	}
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &p, nil
}
