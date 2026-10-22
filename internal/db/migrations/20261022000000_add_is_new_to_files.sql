-- +goose Up
-- is_new records whether a history version stands for a file that did not
-- exist: 1 means absent, 0 means the file existed, NULL means unknown (rows
-- written before this column). Rewind uses it to delete files a session
-- created instead of truncating them.
ALTER TABLE files ADD COLUMN is_new INTEGER;

-- +goose Down
ALTER TABLE files DROP COLUMN is_new;
