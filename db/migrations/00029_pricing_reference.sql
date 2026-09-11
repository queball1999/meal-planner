-- +goose Up

-- pricing_reference holds a reference price for one (provider, model) pair,
-- stored per-million tokens to avoid float underflow (a $3/M price is
-- 0.000003/token). effective_date + notes are the only "last verified"
-- markers - nothing auto-refreshes prices.
CREATE TABLE IF NOT EXISTS pricing_reference (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    provider                 TEXT    NOT NULL,
    reference_model          TEXT    NOT NULL,
    input_price_per_million  REAL    NOT NULL DEFAULT 0,
    output_price_per_million REAL    NOT NULL DEFAULT 0,
    effective_date           TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%d', 'now')),
    notes                    TEXT    NOT NULL DEFAULT '',
    updated_at               TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (provider, reference_model)
);

-- model_cost_mappings maps a local model/purpose label to a pricing reference.
-- Several local purposes can share one reference price. A mapping is an
-- admin's judgment call, not a default - no rows are seeded.
CREATE TABLE IF NOT EXISTS model_cost_mappings (
    local_model          TEXT    PRIMARY KEY,
    pricing_reference_id INTEGER NOT NULL REFERENCES pricing_reference(id),
    updated_at           TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- +goose Down

DROP TABLE IF EXISTS model_cost_mappings;
DROP TABLE IF EXISTS pricing_reference;
