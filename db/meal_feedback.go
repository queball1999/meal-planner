package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

func (s *store) CreateFeedback(ctx context.Context, p CreateFeedbackParams) (*MealFeedback, error) {
	tags, _ := json.Marshal(p.TagsSnapshot)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO meal_feedback (household_id, meal_id, title, rating, tags_snapshot)
		 VALUES (?, ?, ?, ?, ?)`,
		p.HouseholdID, nullInt64(p.MealID), p.Title, p.Rating, string(tags))
	if err != nil {
		return nil, fmt.Errorf("create feedback: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("create feedback last id: %w", err)
	}

	row := s.db.QueryRowContext(ctx,
		`SELECT id, household_id, meal_id, title, rating, tags_snapshot, created_at
		   FROM meal_feedback WHERE id = ?`, id)
	return scanFeedback(row)
}

func (s *store) ListFeedbackDigest(ctx context.Context, householdID int64, limit int) ([]*MealFeedback, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, household_id, meal_id, title, rating, tags_snapshot, created_at
		   FROM meal_feedback WHERE household_id = ?
		   ORDER BY created_at DESC LIMIT ?`,
		householdID, limit)
	if err != nil {
		return nil, fmt.Errorf("list feedback: %w", err)
	}
	defer rows.Close()

	var out []*MealFeedback
	for rows.Next() {
		f, err := scanFeedback(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func scanFeedback(sc storeScanner) (*MealFeedback, error) {
	var f MealFeedback
	var mealID *int64
	var tagsJSON, createdAt string
	if err := sc.Scan(&f.ID, &f.HouseholdID, &mealID, &f.Title, &f.Rating, &tagsJSON, &createdAt); err != nil {
		return nil, fmt.Errorf("scan feedback: %w", err)
	}
	f.MealID = mealID
	_ = json.Unmarshal([]byte(tagsJSON), &f.TagsSnapshot)
	f.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &f, nil
}
