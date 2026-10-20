-- +goose Up
-- Magic Context ctx_reduce nudge state (ported from cortexkit/magic-context, MIT).

-- +goose StatementBegin
ALTER TABLE mc_session_meta ADD COLUMN last_nudge_undropped INTEGER NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE mc_session_meta ADD COLUMN channel1_nudge_state TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE mc_session_meta ADD COLUMN channel2_nudge_state TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE mc_session_meta DROP COLUMN channel2_nudge_state;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE mc_session_meta DROP COLUMN channel1_nudge_state;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE mc_session_meta DROP COLUMN last_nudge_undropped;
-- +goose StatementEnd
