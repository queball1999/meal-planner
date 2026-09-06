-- +goose Up
-- Store context: what a retailer needs to know before its search page will
-- show prices at all. Albertsons and Safeway answer a search with skeleton
-- loaders forever until a store is selected, and that selection lives in a
-- cookie, not in the URL - so a scraper that only ever fetches the search URL
-- can never see a price no matter how well its selectors are written.
--
-- context_json is an object:
--   {
--     "cookies":  {"NAME": "value"},   -- set before the first navigation
--     "prewarm":  ["https://…/"],      -- visited in order, same session
--     "wait_for": "[data-qa=prd-itm]"  -- CSS selector to wait for
--   }
ALTER TABLE scrape_configs ADD COLUMN context_json TEXT NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE scrape_configs DROP COLUMN context_json;
