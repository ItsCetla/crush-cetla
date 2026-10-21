-- +goose Up
ALTER TABLE sessions ADD COLUMN working_dir TEXT;

-- +goose Down
ALTER TABLE sessions DROP COLUMN working_dir;
