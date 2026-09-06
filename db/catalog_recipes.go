package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func (s *store) CreateCatalogRecipe(ctx context.Context, p CreateCatalogRecipeParams) (*CatalogRecipe, error) {
	tags, _ := json.Marshal(p.Tags)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO catalog_recipes
			(household_id, title, source_kind, source_url, source_site,
			 image_path, servings, prep_minutes, cook_minutes, tags)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.HouseholdID, p.Title, p.SourceKind, p.SourceURL, p.SourceSite,
		p.ImagePath, p.Servings, p.PrepMinutes, p.CookMinutes, string(tags),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetCatalogRecipe(ctx, id)
}

func (s *store) GetCatalogRecipe(ctx context.Context, id int64) (*CatalogRecipe, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, household_id, title, source_kind, source_url, source_site,
		       image_path, servings, prep_minutes, cook_minutes, tags, created_at
		FROM catalog_recipes WHERE id = ?`, id)
	return scanCatalogRecipe(row)
}

func (s *store) ListCatalogRecipes(ctx context.Context, householdID int64) ([]*CatalogRecipe, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, household_id, title, source_kind, source_url, source_site,
		       image_path, servings, prep_minutes, cook_minutes, tags, created_at
		FROM catalog_recipes
		WHERE household_id = ?
		ORDER BY created_at DESC`, householdID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CatalogRecipe
	for rows.Next() {
		r, err := scanCatalogRecipeRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *store) DeleteCatalogRecipe(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM catalog_recipes WHERE id = ?`, id)
	return err
}

func (s *store) AddCatalogRecipeIngredient(ctx context.Context, catalogRecipeID int64, name, quantity, unit string, position int) error {
	normalized := normalizeTerm(name)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO catalog_recipe_ingredients
			(catalog_recipe_id, name, quantity, unit, normalized_term, position)
		VALUES (?, ?, ?, ?, ?, ?)`,
		catalogRecipeID, name, quantity, unit, normalized, position,
	)
	return err
}

func (s *store) ListCatalogRecipeIngredients(ctx context.Context, catalogRecipeID int64) ([]*CatalogRecipeIngredient, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, catalog_recipe_id, name, quantity, unit, normalized_term, position
		FROM catalog_recipe_ingredients
		WHERE catalog_recipe_id = ?
		ORDER BY position`, catalogRecipeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CatalogRecipeIngredient
	for rows.Next() {
		var i CatalogRecipeIngredient
		if err := rows.Scan(&i.ID, &i.CatalogRecipeID, &i.Name, &i.Quantity, &i.Unit, &i.NormalizedTerm, &i.Position); err != nil {
			return nil, err
		}
		out = append(out, &i)
	}
	return out, rows.Err()
}

func (s *store) AddCatalogRecipeStep(ctx context.Context, catalogRecipeID int64, position int, text string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO catalog_recipe_steps (catalog_recipe_id, position, text)
		VALUES (?, ?, ?)`, catalogRecipeID, position, text)
	return err
}

func (s *store) ListCatalogRecipeSteps(ctx context.Context, catalogRecipeID int64) ([]*CatalogRecipeStep, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, catalog_recipe_id, position, text
		FROM catalog_recipe_steps
		WHERE catalog_recipe_id = ?
		ORDER BY position`, catalogRecipeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CatalogRecipeStep
	for rows.Next() {
		var step CatalogRecipeStep
		if err := rows.Scan(&step.ID, &step.CatalogRecipeID, &step.Position, &step.Text); err != nil {
			return nil, err
		}
		out = append(out, &step)
	}
	return out, rows.Err()
}

// UpdateCatalogRecipe rewrites the editable fields of a catalog recipe.
// ImagePath is only written when non-empty so an edit that leaves the image
// alone does not clear it; use ClearCatalogRecipeImage to drop one.
func (s *store) UpdateCatalogRecipe(ctx context.Context, p UpdateCatalogRecipeParams) error {
	tags, _ := json.Marshal(p.Tags)
	if p.ImagePath != "" {
		_, err := s.db.ExecContext(ctx, `
			UPDATE catalog_recipes
			SET title = ?, servings = ?, prep_minutes = ?, cook_minutes = ?,
			    tags = ?, image_path = ?
			WHERE id = ?`,
			p.Title, p.Servings, p.PrepMinutes, p.CookMinutes, string(tags), p.ImagePath, p.ID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE catalog_recipes
		SET title = ?, servings = ?, prep_minutes = ?, cook_minutes = ?, tags = ?
		WHERE id = ?`,
		p.Title, p.Servings, p.PrepMinutes, p.CookMinutes, string(tags), p.ID)
	return err
}

// ClearCatalogRecipeImage drops the stored image reference.
func (s *store) ClearCatalogRecipeImage(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE catalog_recipes SET image_path = '' WHERE id = ?`, id)
	return err
}

// DeleteCatalogRecipeIngredients removes every ingredient line so the caller
// can re-add the full set (edit and re-import both replace wholesale).
func (s *store) DeleteCatalogRecipeIngredients(ctx context.Context, catalogRecipeID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM catalog_recipe_ingredients WHERE catalog_recipe_id = ?`, catalogRecipeID)
	return err
}

// DeleteCatalogRecipeSteps removes every step so the caller can re-add them.
func (s *store) DeleteCatalogRecipeSteps(ctx context.Context, catalogRecipeID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM catalog_recipe_steps WHERE catalog_recipe_id = ?`, catalogRecipeID)
	return err
}

// ── scan helpers ──────────────────────────────────────────────────────────────

func scanCatalogRecipe(row *sql.Row) (*CatalogRecipe, error) {
	var r CatalogRecipe
	var tagsJSON, createdAt string
	err := row.Scan(
		&r.ID, &r.HouseholdID, &r.Title, &r.SourceKind, &r.SourceURL, &r.SourceSite,
		&r.ImagePath, &r.Servings, &r.PrepMinutes, &r.CookMinutes, &tagsJSON, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(tagsJSON), &r.Tags)
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &r, nil
}

func scanCatalogRecipeRow(rows *sql.Rows) (*CatalogRecipe, error) {
	var r CatalogRecipe
	var tagsJSON, createdAt string
	err := rows.Scan(
		&r.ID, &r.HouseholdID, &r.Title, &r.SourceKind, &r.SourceURL, &r.SourceSite,
		&r.ImagePath, &r.Servings, &r.PrepMinutes, &r.CookMinutes, &tagsJSON, &createdAt,
	)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(tagsJSON), &r.Tags)
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &r, nil
}

// normalizeTerm is a minimal in-package normalizer for ingredient terms.
// The canonical normalizer lives in pricing; this avoids an import cycle
// while keeping db/ self-contained.
func normalizeTerm(name string) string {
	return name // pricing.Normalize is called by the import layer; db stores what it gets
}
