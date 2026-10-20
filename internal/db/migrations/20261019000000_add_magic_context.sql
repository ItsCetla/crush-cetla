-- +goose Up
-- Magic Context (opt-in context manager, ported from cortexkit/magic-context,
-- MIT). Every table is prefixed mc_ and is unused unless
-- options.magic_context.enabled is set. Full-text tables are created lazily
-- at runtime so builds without FTS5 still migrate cleanly.

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_ordinals (
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    message_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    PRIMARY KEY (session_id, message_id),
    UNIQUE (session_id, ordinal)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_tags (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    tag_number INTEGER NOT NULL,
    message_id TEXT NOT NULL,
    part_key TEXT NOT NULL,
    type TEXT NOT NULL,
    tool_name TEXT NOT NULL DEFAULT '',
    tool_call_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active',
    byte_size INTEGER NOT NULL DEFAULT 0,
    input_byte_size INTEGER NOT NULL DEFAULT 0,
    drop_mode TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    UNIQUE (session_id, tag_number),
    UNIQUE (session_id, part_key)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_mc_tags_session_message ON mc_tags (session_id, message_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_pending_ops (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES mc_tags (id) ON DELETE CASCADE,
    operation TEXT NOT NULL DEFAULT 'drop',
    queued_at INTEGER NOT NULL,
    UNIQUE (session_id, tag_id, operation)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_compartments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    start_ordinal INTEGER NOT NULL,
    end_ordinal INTEGER NOT NULL,
    start_message_id TEXT NOT NULL DEFAULT '',
    end_message_id TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL DEFAULT '',
    p1 TEXT NOT NULL DEFAULT '',
    p2 TEXT NOT NULL DEFAULT '',
    p3 TEXT NOT NULL DEFAULT '',
    p4 TEXT NOT NULL DEFAULT '',
    importance INTEGER,
    episode_type TEXT NOT NULL DEFAULT '',
    start_time INTEGER NOT NULL DEFAULT 0,
    end_time INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    UNIQUE (session_id, sequence)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_recomp_compartments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    start_ordinal INTEGER NOT NULL,
    end_ordinal INTEGER NOT NULL,
    start_message_id TEXT NOT NULL DEFAULT '',
    end_message_id TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL DEFAULT '',
    p1 TEXT NOT NULL DEFAULT '',
    p2 TEXT NOT NULL DEFAULT '',
    p3 TEXT NOT NULL DEFAULT '',
    p4 TEXT NOT NULL DEFAULT '',
    importance INTEGER,
    episode_type TEXT NOT NULL DEFAULT '',
    start_time INTEGER NOT NULL DEFAULT 0,
    end_time INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    UNIQUE (session_id, sequence)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_session_facts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    category TEXT NOT NULL,
    content TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    compartment_id INTEGER REFERENCES mc_compartments (id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    fields_json TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_session_meta (
    session_id TEXT PRIMARY KEY REFERENCES sessions (id) ON DELETE CASCADE,
    last_response_time INTEGER NOT NULL DEFAULT 0,
    last_input_tokens INTEGER NOT NULL DEFAULT 0,
    last_context_percentage REAL NOT NULL DEFAULT 0,
    last_nudge_tokens INTEGER NOT NULL DEFAULT 0,
    last_nudge_band TEXT NOT NULL DEFAULT '',
    last_nudge_turn INTEGER NOT NULL DEFAULT 0,
    compartment_in_progress INTEGER NOT NULL DEFAULT 0,
    historian_failure_count INTEGER NOT NULL DEFAULT 0,
    historian_last_error TEXT NOT NULL DEFAULT '',
    lease_holder TEXT NOT NULL DEFAULT '',
    lease_until INTEGER NOT NULL DEFAULT 0,
    m0_floor INTEGER NOT NULL DEFAULT 0,
    cached_m0 TEXT NOT NULL DEFAULT '',
    cached_m1 TEXT NOT NULL DEFAULT '',
    cached_m0_compartment_seq INTEGER NOT NULL DEFAULT 0,
    cached_m0_memory_mutation INTEGER NOT NULL DEFAULT 0,
    cached_m0_max_memory_id INTEGER NOT NULL DEFAULT 0,
    cached_m0_profile_version INTEGER NOT NULL DEFAULT 0,
    cached_m0_budget INTEGER NOT NULL DEFAULT 0,
    render_epoch INTEGER NOT NULL DEFAULT 0,
    cached_render_epoch INTEGER NOT NULL DEFAULT 0,
    materialize_pending TEXT NOT NULL DEFAULT '',
    system_prompt_hash TEXT NOT NULL DEFAULT '',
    last_model_key TEXT NOT NULL DEFAULT '',
    emergency_latched INTEGER NOT NULL DEFAULT 0,
    prior_boundary_ordinal INTEGER NOT NULL DEFAULT 0,
    stripped_reduce_ids TEXT NOT NULL DEFAULT '[]',
    note_nudge_trigger TEXT NOT NULL DEFAULT '',
    note_nudge_anchor_id TEXT NOT NULL DEFAULT '',
    note_nudge_text TEXT NOT NULL DEFAULT '',
    note_nudge_last_at INTEGER NOT NULL DEFAULT 0,
    last_note_read_at INTEGER NOT NULL DEFAULT 0,
    search_hint_anchor_id TEXT NOT NULL DEFAULT '',
    search_hint_text TEXT NOT NULL DEFAULT '',
    user_turns INTEGER NOT NULL DEFAULT 0,
    cached_m0_memory_ids TEXT NOT NULL DEFAULT '[]',
    cached_m0_materialized_at INTEGER NOT NULL DEFAULT 0,
    cached_m0_boundary TEXT NOT NULL DEFAULT '',
    cached_m0_memory_enabled INTEGER NOT NULL DEFAULT 0,
    cached_m0_budget_key TEXT NOT NULL DEFAULT '',
    recomp_partial_range TEXT NOT NULL DEFAULT '',
    recomp_pass_count INTEGER NOT NULL DEFAULT 0
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_path TEXT NOT NULL,
    category TEXT NOT NULL,
    content TEXT NOT NULL,
    normalized_hash TEXT NOT NULL,
    importance INTEGER,
    scope TEXT NOT NULL DEFAULT 'project',
    source_session_id TEXT NOT NULL DEFAULT '',
    source_type TEXT NOT NULL DEFAULT 'historian',
    seen_count INTEGER NOT NULL DEFAULT 1,
    retrieval_count INTEGER NOT NULL DEFAULT 0,
    first_seen_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    last_retrieved_at INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'active',
    expires_at INTEGER NOT NULL DEFAULT 0,
    verification_status TEXT NOT NULL DEFAULT 'unverified',
    verified_at INTEGER NOT NULL DEFAULT 0,
    classified_at INTEGER NOT NULL DEFAULT 0,
    superseded_by_memory_id INTEGER NOT NULL DEFAULT 0,
    merged_from TEXT NOT NULL DEFAULT '',
    UNIQUE (project_path, category, normalized_hash)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_mc_memories_project_status ON mc_memories (project_path, status);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_memory_embeddings (
    memory_id INTEGER NOT NULL REFERENCES mc_memories (id) ON DELETE CASCADE,
    model_id TEXT NOT NULL,
    embedding BLOB NOT NULL,
    PRIMARY KEY (memory_id, model_id)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_memory_verifications (
    memory_id INTEGER NOT NULL REFERENCES mc_memories (id) ON DELETE CASCADE,
    file_path TEXT NOT NULL,
    verified_at INTEGER NOT NULL DEFAULT 0,
    mapped_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (memory_id, file_path)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_memory_mutation_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_path TEXT NOT NULL,
    mutation_type TEXT NOT NULL CHECK (mutation_type IN ('archive', 'delete', 'update', 'superseded')),
    target_memory_id INTEGER NOT NULL,
    superseded_by_id INTEGER NOT NULL DEFAULT 0,
    category TEXT NOT NULL DEFAULT '',
    new_content TEXT NOT NULL DEFAULT '',
    queued_at INTEGER NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_message_index (
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    message_id TEXT NOT NULL,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    created_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (session_id, ordinal)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_notes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    type TEXT NOT NULL DEFAULT 'session',
    status TEXT NOT NULL DEFAULT 'active',
    content TEXT NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    project_path TEXT NOT NULL DEFAULT '',
    surface_condition TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    last_checked_at INTEGER NOT NULL DEFAULT 0,
    ready_at INTEGER NOT NULL DEFAULT 0,
    ready_reason TEXT NOT NULL DEFAULT '',
    check_status TEXT NOT NULL DEFAULT 'uncompiled',
    check_next_due_at INTEGER NOT NULL DEFAULT 0,
    check_failure_count INTEGER NOT NULL DEFAULT 0,
    anchor_ordinal INTEGER NOT NULL DEFAULT 0
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_mc_notes_session_status ON mc_notes (session_id, status);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_mc_notes_project_status ON mc_notes (project_path, status);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_user_memory_candidates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    content TEXT NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    source_compartment_start INTEGER NOT NULL DEFAULT 0,
    source_compartment_end INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_user_memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    content TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    promoted_at INTEGER NOT NULL DEFAULT 0,
    source_candidate_ids TEXT NOT NULL DEFAULT '[]',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_primer_candidates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_path TEXT NOT NULL,
    session_id TEXT NOT NULL,
    question TEXT NOT NULL,
    normalized_question TEXT NOT NULL,
    source_compartment_start INTEGER NOT NULL DEFAULT 0,
    source_compartment_end INTEGER NOT NULL DEFAULT 0,
    source_message_time INTEGER NOT NULL DEFAULT 0,
    question_embedding BLOB,
    question_embedding_model_id TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    UNIQUE (project_path, session_id, source_compartment_start, source_compartment_end)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_primers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_path TEXT NOT NULL,
    question TEXT NOT NULL,
    question_embedding BLOB,
    question_embedding_model_id TEXT NOT NULL DEFAULT '',
    answer TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    total_support INTEGER NOT NULL DEFAULT 0,
    last_observed_at INTEGER NOT NULL DEFAULT 0,
    answer_refreshed_at INTEGER NOT NULL DEFAULT 0,
    source_candidate_ids TEXT NOT NULL DEFAULT '[]',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_git_commits (
    sha TEXT PRIMARY KEY,
    project_path TEXT NOT NULL,
    short_sha TEXT NOT NULL,
    message TEXT NOT NULL,
    author TEXT NOT NULL DEFAULT '',
    committed_at INTEGER NOT NULL DEFAULT 0,
    indexed_at INTEGER NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_git_commit_embeddings (
    sha TEXT NOT NULL REFERENCES mc_git_commits (sha) ON DELETE CASCADE,
    model_id TEXT NOT NULL,
    embedding BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (sha, model_id)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_dream_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_dream_queue (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_path TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    enqueued_at INTEGER NOT NULL,
    started_at INTEGER NOT NULL DEFAULT 0,
    retry_count INTEGER NOT NULL DEFAULT 0
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_dream_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_path TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    finished_at INTEGER NOT NULL DEFAULT 0,
    holder_id TEXT NOT NULL DEFAULT '',
    tasks_json TEXT NOT NULL DEFAULT '[]',
    tasks_succeeded INTEGER NOT NULL DEFAULT 0,
    tasks_failed INTEGER NOT NULL DEFAULT 0,
    smart_notes_surfaced INTEGER NOT NULL DEFAULT 0,
    smart_notes_pending INTEGER NOT NULL DEFAULT 0,
    memory_changes_json TEXT NOT NULL DEFAULT '{}'
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mc_task_schedule_state (
    project_path TEXT NOT NULL,
    task TEXT NOT NULL,
    last_run_at INTEGER NOT NULL DEFAULT 0,
    next_due_at INTEGER NOT NULL DEFAULT 0,
    schedule TEXT NOT NULL DEFAULT '',
    last_status TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    last_checked_commit TEXT NOT NULL DEFAULT '',
    retrospective_watermark_ms INTEGER NOT NULL DEFAULT 0,
    retry_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (project_path, task)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS mc_task_schedule_state;
DROP TABLE IF EXISTS mc_dream_runs;
DROP TABLE IF EXISTS mc_dream_queue;
DROP TABLE IF EXISTS mc_dream_state;
DROP TABLE IF EXISTS mc_git_commit_embeddings;
DROP TABLE IF EXISTS mc_git_commits;
DROP TABLE IF EXISTS mc_primers;
DROP TABLE IF EXISTS mc_primer_candidates;
DROP TABLE IF EXISTS mc_user_memories;
DROP TABLE IF EXISTS mc_user_memory_candidates;
DROP TABLE IF EXISTS mc_notes;
DROP TABLE IF EXISTS mc_message_index;
DROP TABLE IF EXISTS mc_memory_mutation_log;
DROP TABLE IF EXISTS mc_memory_verifications;
DROP TABLE IF EXISTS mc_memory_embeddings;
DROP TABLE IF EXISTS mc_memories;
DROP TABLE IF EXISTS mc_session_meta;
DROP TABLE IF EXISTS mc_events;
DROP TABLE IF EXISTS mc_session_facts;
DROP TABLE IF EXISTS mc_recomp_compartments;
DROP TABLE IF EXISTS mc_compartments;
DROP TABLE IF EXISTS mc_pending_ops;
DROP TABLE IF EXISTS mc_tags;
DROP TABLE IF EXISTS mc_ordinals;
-- +goose StatementEnd
