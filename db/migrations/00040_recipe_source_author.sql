-- +goose Up

-- source_author credits who made an imported recipe: a video's creator
-- (yt-dlp's uploader) or a recipe page's JSON-LD author. '' when unknown.
ALTER TABLE catalog_recipes ADD COLUMN source_author TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE catalog_recipes DROP COLUMN source_author;
