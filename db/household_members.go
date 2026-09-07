package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PortionFactorMax mirrors the CHECK constraint in
// migrations/00018_household_members.sql. Validated here as well so a bad
// value comes back as a message the form can show rather than as a raw
// SQLite constraint error.
const PortionFactorMax = 5.0

// ErrInvalidPortionFactor is returned for a factor outside (0, PortionFactorMax].
var ErrInvalidPortionFactor = fmt.Errorf("portion factor must be greater than 0 and at most %g", PortionFactorMax)

func validMember(name string, factor float64) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("member name is required")
	}
	if factor <= 0 || factor > PortionFactorMax {
		return ErrInvalidPortionFactor
	}
	return nil
}

// ListHouseholdMembers returns every member of a household in display order.
func (s *store) ListHouseholdMembers(ctx context.Context, householdID int64) ([]*HouseholdMember, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, household_id, name, portion_factor, notes, sort_order, created_at
		FROM household_members
		WHERE household_id = ?
		ORDER BY sort_order, id`, householdID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*HouseholdMember
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetHouseholdMember returns one member, or nil when it does not exist or
// belongs to another household.
func (s *store) GetHouseholdMember(ctx context.Context, householdID, id int64) (*HouseholdMember, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, household_id, name, portion_factor, notes, sort_order, created_at
		FROM household_members
		WHERE household_id = ? AND id = ?`, householdID, id)
	m, err := scanMember(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// CreateHouseholdMember adds a member and returns its id. SortOrder 0 means
// "put them last", which is what an add form always wants.
func (s *store) CreateHouseholdMember(ctx context.Context, p CreateHouseholdMemberParams) (int64, error) {
	if err := validMember(p.Name, p.PortionFactor); err != nil {
		return 0, err
	}
	order := p.SortOrder
	if order == 0 {
		if err := s.db.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(sort_order), 0) + 1 FROM household_members WHERE household_id = ?`,
			p.HouseholdID).Scan(&order); err != nil {
			return 0, err
		}
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO household_members (household_id, name, portion_factor, notes, sort_order)
		VALUES (?, ?, ?, ?, ?)`,
		p.HouseholdID, strings.TrimSpace(p.Name), p.PortionFactor, p.Notes, order)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateHouseholdMember edits a member. The household id is part of the WHERE
// clause, so an id belonging to another household updates nothing.
func (s *store) UpdateHouseholdMember(ctx context.Context, p UpdateHouseholdMemberParams) error {
	if err := validMember(p.Name, p.PortionFactor); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE household_members
		SET name = ?, portion_factor = ?, notes = ?
		WHERE id = ? AND household_id = ?`,
		strings.TrimSpace(p.Name), p.PortionFactor, p.Notes, p.ID, p.HouseholdID)
	return err
}

// DeleteHouseholdMember removes a member.
//
// Past plan days keep the deleted id in their member_ids and their stored
// portion total is untouched: a plan records who was fed that week, and
// removing someone from the household today does not change what was cooked.
func (s *store) DeleteHouseholdMember(ctx context.Context, householdID, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM household_members WHERE id = ? AND household_id = ?`, householdID, id)
	return err
}

// SumPortionFactors adds up the factors for a set of member ids, and reports
// how many of them actually resolved. Ids that are unknown or belong to
// another household contribute nothing and are excluded from the count, so a
// caller can tell "3 people eating" from "3 ids sent, 1 of them stale".
func (s *store) SumPortionFactors(ctx context.Context, householdID int64, ids []int64) (float64, int, error) {
	if len(ids) == 0 {
		return 0, 0, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, householdID)
	placeholders := make([]string, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	var total sql.NullFloat64
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(portion_factor), 0), COUNT(*)
		FROM household_members
		WHERE household_id = ? AND id IN (`+strings.Join(placeholders, ",")+`)`,
		args...).Scan(&total, &n)
	if err != nil {
		return 0, 0, err
	}
	return total.Float64, n, nil
}

// CountHouseholdMembers is the cheap check for "has this household set up
// members yet", used to decide whether the plan UI offers a member picker or
// the plain headcount it has always had.
func (s *store) CountHouseholdMembers(ctx context.Context, householdID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM household_members WHERE household_id = ?`, householdID).Scan(&n)
	return n, err
}

func scanMember(sc scanner) (*HouseholdMember, error) {
	var m HouseholdMember
	var created string
	if err := sc.Scan(&m.ID, &m.HouseholdID, &m.Name, &m.PortionFactor, &m.Notes, &m.SortOrder, &created); err != nil {
		return nil, err
	}
	m.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return &m, nil
}
