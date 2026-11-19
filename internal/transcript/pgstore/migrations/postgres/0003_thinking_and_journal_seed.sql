-- +goose Up
-- Thinking text is kept in full beside the turn's visible text.
ALTER TABLE events ADD COLUMN thinking_redacted text NOT NULL DEFAULT '';

-- The change journal needs its singleton row; the FE writer only updates it.
-- A fresh epoch makes any cursor from a previous corpus generation invalid.
INSERT INTO feed_state (singleton, epoch, head, retained_from)
VALUES (true, replace(gen_random_uuid()::text, '-', ''), 0, 1)
ON CONFLICT (singleton) DO NOTHING;

-- +goose Down
ALTER TABLE events DROP COLUMN thinking_redacted;
