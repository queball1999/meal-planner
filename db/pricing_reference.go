package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ── Pricing references (00029_pricing_reference.sql) ─────────────────────────
//
// A reference price is what a model's usage would have cost on a third-party
// provider - a benchmark, not a bill. Prices are stored per-million tokens to
// avoid float underflow. A local model/purpose label is mapped to a reference
// via model_cost_mappings; unmapped labels have no price (they are reported as
// "unpriced", never as $0.00).

func (s *store) ListPricingReferences(ctx context.Context) ([]*PricingReference, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, provider, reference_model, input_price_per_million,
		       output_price_per_million, effective_date, notes, updated_at
		FROM pricing_reference
		ORDER BY provider ASC, reference_model ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*PricingReference
	for rows.Next() {
		var r PricingReference
		var updatedAt string
		if err := rows.Scan(&r.ID, &r.Provider, &r.ReferenceModel,
			&r.InputPricePerMillion, &r.OutputPricePerMillion,
			&r.EffectiveDate, &r.Notes, &updatedAt); err != nil {
			return nil, err
		}
		r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		out = append(out, &r)
	}
	return out, rows.Err()
}

func (s *store) UpsertPricingReference(ctx context.Context, p UpsertPricingReferenceParams) (*PricingReference, error) {
	if p.InputPricePerMillion < 0 || p.OutputPricePerMillion < 0 {
		return nil, fmt.Errorf("prices must be non-negative")
	}
	if p.EffectiveDate == "" {
		p.EffectiveDate = time.Now().UTC().Format("2006-01-02")
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO pricing_reference
			(provider, reference_model, input_price_per_million, output_price_per_million, effective_date, notes)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, reference_model) DO UPDATE SET
			input_price_per_million  = excluded.input_price_per_million,
			output_price_per_million = excluded.output_price_per_million,
			effective_date           = excluded.effective_date,
			notes                    = excluded.notes,
			updated_at               = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`,
		p.Provider, p.ReferenceModel, p.InputPricePerMillion, p.OutputPricePerMillion,
		p.EffectiveDate, p.Notes,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetPricingReference(ctx, id)
}

func (s *store) GetPricingReference(ctx context.Context, id int64) (*PricingReference, error) {
	var r PricingReference
	var updatedAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, provider, reference_model, input_price_per_million,
		       output_price_per_million, effective_date, notes, updated_at
		FROM pricing_reference WHERE id = ?`, id).
		Scan(&r.ID, &r.Provider, &r.ReferenceModel, &r.InputPricePerMillion,
			&r.OutputPricePerMillion, &r.EffectiveDate, &r.Notes, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &r, nil
}

func (s *store) DeletePricingReference(ctx context.Context, id int64) error {
	// Refuse to delete a reference that a mapping still points at - that would
	// silently strip pricing from every mapped model.
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM model_cost_mappings WHERE pricing_reference_id = ?`, id).
		Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("reference is mapped to %d model(s); remove the mapping first", n)
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM pricing_reference WHERE id = ?`, id)
	return err
}

// ── Model cost mappings ───────────────────────────────────────────────────────

func (s *store) ListModelCostMappings(ctx context.Context) ([]*ModelCostMapping, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.local_model, m.pricing_reference_id,
		       COALESCE(r.provider, ''), COALESCE(r.reference_model, ''), m.updated_at
		FROM model_cost_mappings m
		LEFT JOIN pricing_reference r ON r.id = m.pricing_reference_id
		ORDER BY m.local_model ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*ModelCostMapping
	for rows.Next() {
		var m ModelCostMapping
		var updatedAt string
		if err := rows.Scan(&m.LocalModel, &m.PricingReferenceID,
			&m.Provider, &m.ReferenceModel, &updatedAt); err != nil {
			return nil, err
		}
		m.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		out = append(out, &m)
	}
	return out, rows.Err()
}

// SetModelCostMapping points a local model label at a pricing reference.
func (s *store) SetModelCostMapping(ctx context.Context, localModel string, referenceID int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO model_cost_mappings (local_model, pricing_reference_id)
		VALUES (?, ?)
		ON CONFLICT(local_model) DO UPDATE SET
			pricing_reference_id = excluded.pricing_reference_id,
			updated_at           = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`,
		localModel, referenceID)
	return err
}

func (s *store) DeleteModelCostMapping(ctx context.Context, localModel string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM model_cost_mappings WHERE local_model = ?`, localModel)
	return err
}

// PricingForModels resolves a set of local model labels to their reference
// prices. Unmapped labels are simply absent from the returned map - the caller
// reports them as unpriced rather than dropping them.
func (s *store) PricingForModels(ctx context.Context, models []string) (map[string]*PricingReference, error) {
	out := make(map[string]*PricingReference, len(models))
	if len(models) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.local_model, r.id, r.provider, r.reference_model,
		       r.input_price_per_million, r.output_price_per_million,
		       r.effective_date, r.notes, r.updated_at
		FROM model_cost_mappings m
		JOIN pricing_reference r ON r.id = m.pricing_reference_id
		WHERE m.local_model IN (SELECT value FROM json_each(?))`,
		mustJSONArray(models))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var localModel string
		var r PricingReference
		var updatedAt string
		if err := rows.Scan(&localModel, &r.ID, &r.Provider, &r.ReferenceModel,
			&r.InputPricePerMillion, &r.OutputPricePerMillion,
			&r.EffectiveDate, &r.Notes, &updatedAt); err != nil {
			return nil, err
		}
		r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		out[localModel] = &r
	}
	return out, rows.Err()
}

// mustJSONArray marshals a string slice to a JSON array for SQLite's json_each.
func mustJSONArray(vals []string) string {
	b, err := json.Marshal(vals)
	if err != nil {
		return "[]"
	}
	return string(b)
}
