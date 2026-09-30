-- +goose Up

-- Brings stored normalized terms in line with two pricing.Normalize changes,
-- and cleans up what the old behaviour linked wrongly:
--
--   * "canned" is kept ("Canned diced tomatoes" -> "canned tomato"). It used
--     to be stripped, which made canned tomatoes and a fresh tomato one item.
--   * "-oes" plurals singularize properly ("tomatoes" -> "tomato", not
--     "tomatoe").
--
-- Every table keyed by a term is renamed through one old -> new map, so a
-- price, pantry row or HA mapping keeps following its item. UPDATE OR IGNORE
-- skips a row whose new term is already taken rather than failing the boot.

CREATE TEMP TABLE term_map (household_id INTEGER NOT NULL, old TEXT NOT NULL, new TEXT NOT NULL);

-- Canned items (the built-in catalog names them "Canned ...").
INSERT INTO term_map (household_id, old, new)
SELECT household_id, normalized_term,
       'canned ' || CASE WHEN normalized_term LIKE '%toe'
                         THEN substr(normalized_term, 1, length(normalized_term) - 1)
                         ELSE normalized_term END
FROM items
WHERE lower(name) LIKE 'canned %' AND normalized_term NOT LIKE 'canned %';

-- Everything else left with a "-toe" stem (tomatoe, cherry tomatoe, potatoe).
INSERT INTO term_map (household_id, old, new)
SELECT household_id, normalized_term, substr(normalized_term, 1, length(normalized_term) - 1)
FROM items
WHERE normalized_term LIKE '%toe'
  AND NOT EXISTS (SELECT 1 FROM term_map t
                  WHERE t.household_id = items.household_id AND t.old = items.normalized_term);

-- Aliases the fuzzy matcher recorded because it ignored "canned" - the one
-- that sent a recipe's fresh "tomato" to a can of diced tomatoes. Only 'auto'
-- ones: an alias a person confirmed stays.
DELETE FROM item_aliases
WHERE source = 'auto'
  AND alias NOT LIKE 'canned %'
  AND item_id IN (SELECT id FROM items WHERE lower(name) LIKE 'canned %');

UPDATE OR IGNORE items SET normalized_term =
    (SELECT new FROM term_map t WHERE t.household_id = items.household_id AND t.old = items.normalized_term)
WHERE EXISTS (SELECT 1 FROM term_map t WHERE t.household_id = items.household_id AND t.old = items.normalized_term);

UPDATE OR IGNORE item_aliases SET alias =
    (SELECT new FROM term_map t WHERE t.household_id = item_aliases.household_id AND t.old = item_aliases.alias)
WHERE EXISTS (SELECT 1 FROM term_map t WHERE t.household_id = item_aliases.household_id AND t.old = item_aliases.alias);

UPDATE OR IGNORE pantry_items SET normalized_term =
    (SELECT new FROM term_map t WHERE t.household_id = pantry_items.household_id AND t.old = pantry_items.normalized_term)
WHERE EXISTS (SELECT 1 FROM term_map t WHERE t.household_id = pantry_items.household_id AND t.old = pantry_items.normalized_term);

UPDATE OR IGNORE ha_sync_map SET normalized_term =
    (SELECT new FROM term_map t WHERE t.household_id = ha_sync_map.household_id AND t.old = ha_sync_map.normalized_term)
WHERE EXISTS (SELECT 1 FROM term_map t WHERE t.household_id = ha_sync_map.household_id AND t.old = ha_sync_map.normalized_term);

-- Store-keyed prices, mapped through the store's household.
UPDATE OR IGNORE price_cache SET normalized_term =
    (SELECT t.new FROM term_map t JOIN stores s ON s.household_id = t.household_id
     WHERE s.id = price_cache.store_id AND t.old = price_cache.normalized_term)
WHERE EXISTS (SELECT 1 FROM term_map t JOIN stores s ON s.household_id = t.household_id
              WHERE s.id = price_cache.store_id AND t.old = price_cache.normalized_term);

UPDATE OR IGNORE manual_prices SET normalized_term =
    (SELECT t.new FROM term_map t JOIN stores s ON s.household_id = t.household_id
     WHERE s.id = manual_prices.store_id AND t.old = manual_prices.normalized_term)
WHERE EXISTS (SELECT 1 FROM term_map t JOIN stores s ON s.household_id = t.household_id
              WHERE s.id = manual_prices.store_id AND t.old = manual_prices.normalized_term);

UPDATE OR IGNORE item_product_map SET normalized_term =
    (SELECT t.new FROM term_map t JOIN stores s ON s.household_id = t.household_id
     WHERE s.id = item_product_map.store_id AND t.old = item_product_map.normalized_term)
WHERE EXISTS (SELECT 1 FROM term_map t JOIN stores s ON s.household_id = t.household_id
              WHERE s.id = item_product_map.store_id AND t.old = item_product_map.normalized_term);

-- Cached lookups that could not move (the new term was already cached) would
-- otherwise answer for the old term - a canned price for a fresh tomato. They
-- are only a cache; drop them.
DELETE FROM price_cache WHERE EXISTS (
    SELECT 1 FROM term_map t JOIN stores s ON s.household_id = t.household_id
    WHERE s.id = price_cache.store_id AND t.old = price_cache.normalized_term);
DELETE FROM item_product_map WHERE EXISTS (
    SELECT 1 FROM term_map t JOIN stores s ON s.household_id = t.household_id
    WHERE s.id = item_product_map.store_id AND t.old = item_product_map.normalized_term);

-- A recipe's fresh ingredient linked to a canned item through that alias:
-- not measured in cans and not called canned. Unlinked, so the boot-time
-- catalog backfill (catalog.BackfillHousehold) links it again under the fixed
-- rules.
UPDATE meal_ingredients SET item_id = NULL
WHERE item_id IN (SELECT id FROM items WHERE normalized_term LIKE 'canned %')
  AND lower(name) NOT LIKE '%canned%'
  AND lower(trim(unit)) NOT IN ('can', 'cans', 'tin', 'tins');

-- An ingredient's term is always its item's (persistPlan, LinkPlanIngredients).
UPDATE meal_ingredients SET normalized_term =
    (SELECT normalized_term FROM items WHERE items.id = meal_ingredients.item_id)
WHERE item_id IS NOT NULL;
UPDATE meal_ingredients SET normalized_term = substr(normalized_term, 1, length(normalized_term) - 1)
WHERE item_id IS NULL AND normalized_term LIKE '%toe';
UPDATE catalog_recipe_ingredients SET normalized_term = substr(normalized_term, 1, length(normalized_term) - 1)
WHERE normalized_term LIKE '%toe';

-- Auto-created items stocked by a store container (catalog.containerUnits):
-- "6 tortillas" read as "0.6 packages". Restocked by "each" wherever the
-- recipe generator left the each -> container edge to size the pack with.
-- The derived conversion rows are dropped; the item page rebuilds them, and
-- pricing reads the raw edges directly.
CREATE TEMP TABLE restock AS
SELECT i.id AS item_id, 1.0 / c.factor AS per_pack
FROM items i
JOIN unit_conversions c ON c.item_id = i.id AND c.derived = 0
                        AND lower(c.from_unit) IN ('each', 'ea', 'count', 'piece')
                        AND lower(c.to_unit) = i.stock_unit
WHERE i.source = 'auto'
  AND i.stock_unit IN ('package', 'bag', 'box', 'carton')
  AND c.factor > 0;

DELETE FROM unit_conversions WHERE derived = 1 AND item_id IN (SELECT item_id FROM restock);
UPDATE items SET stock_unit = 'each',
                 default_purchase_qty = (SELECT per_pack FROM restock r WHERE r.item_id = items.id)
WHERE id IN (SELECT item_id FROM restock);

-- Their shopping lines' recorded need was in containers; re-derived on the
-- next view (pricing.BackfillNeed).
UPDATE shopping_list_items SET need_quantity = 0
WHERE item_id IN (SELECT item_id FROM restock);

DROP TABLE restock;
DROP TABLE term_map;

-- +goose Down
-- Data cleanup; nothing to undo structurally.
SELECT 1;
