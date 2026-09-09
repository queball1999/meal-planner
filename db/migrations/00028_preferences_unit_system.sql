-- +goose Up

-- unit_system is the household's preferred measurement system for weights and
-- volumes on the shopping list. 'as-is' (the default) leaves every quantity in
-- the unit it was stored in; 'metric' and 'imperial' re-express weight/volume
-- lines into that system and promote to the next unit up once the number would
-- otherwise read awkwardly large (1200 g -> 1.2 kg, 20 oz -> 1.25 lb). Countable
-- units (each, can, bunch, clove, ...) are never converted.
ALTER TABLE preferences ADD COLUMN unit_system TEXT NOT NULL DEFAULT 'as-is';

-- +goose Down

ALTER TABLE preferences DROP COLUMN unit_system;
