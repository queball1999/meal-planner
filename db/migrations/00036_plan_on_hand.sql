-- +goose Up

-- on_hand is the generate form's "food we already have" list (JSON array of
-- names), kept with the plan so building its shopping list can mark those
-- lines "I already have this" (pricing.ApplyOnHand). Before this it only went
-- into the prompt, and the model then listed those ingredients as normal - so
-- they landed on the list to be bought again.
ALTER TABLE plans ADD COLUMN on_hand TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE plans DROP COLUMN on_hand;
