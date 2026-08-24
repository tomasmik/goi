-- +goose Up

PRAGMA application_id = 1196378417;

CREATE TABLE vocabulary (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    expression               TEXT NOT NULL CHECK (length(expression) > 0),
    normalized_expression    TEXT NOT NULL CHECK (length(normalized_expression) > 0),
    pronunciation            TEXT NOT NULL DEFAULT '',
    normalized_pronunciation TEXT NOT NULL DEFAULT '',
    status                   TEXT NOT NULL DEFAULT 'unlearned'
                             CHECK (status IN ('unlearned', 'active', 'suspended', 'archived')),
    source_label             TEXT NOT NULL DEFAULT '',
    notes                    TEXT NOT NULL DEFAULT '',
    lesson_completed_at      INTEGER CHECK (lesson_completed_at IS NULL OR lesson_completed_at >= 0),
    known_elsewhere_at       INTEGER CHECK (known_elsewhere_at IS NULL OR known_elsewhere_at >= 0),
    content_revision         INTEGER NOT NULL DEFAULT 1 CHECK (content_revision > 0),
    is_duplicate             INTEGER NOT NULL DEFAULT 0 CHECK (is_duplicate IN (0, 1)),
    created_at               INTEGER NOT NULL CHECK (created_at >= 0),
    updated_at               INTEGER NOT NULL CHECK (updated_at >= 0),
    CHECK ((pronunciation = '') = (normalized_pronunciation = ''))
);

CREATE UNIQUE INDEX vocabulary_live_expression
ON vocabulary (normalized_expression)
WHERE is_duplicate = 0;

CREATE INDEX vocabulary_status_created
ON vocabulary (status, created_at, id);

CREATE INDEX vocabulary_updated
ON vocabulary (updated_at DESC, id DESC);

CREATE INDEX vocabulary_lesson_completed
ON vocabulary (lesson_completed_at)
WHERE lesson_completed_at IS NOT NULL;

CREATE TABLE meanings (
    vocabulary_id   INTEGER NOT NULL REFERENCES vocabulary(id) ON DELETE CASCADE,
    position        INTEGER NOT NULL CHECK (position >= 0),
    text            TEXT NOT NULL CHECK (length(text) > 0),
    normalized_text TEXT NOT NULL CHECK (length(normalized_text) > 0),
    PRIMARY KEY (vocabulary_id, position)
);

CREATE TABLE media (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kind         TEXT NOT NULL CHECK (kind IN ('audio', 'image')),
    mime_type    TEXT NOT NULL CHECK (length(mime_type) > 0),
    sha256       TEXT NOT NULL CHECK (length(sha256) = 64),
    created_at   INTEGER NOT NULL CHECK (created_at >= 0),
    source_name  TEXT NOT NULL DEFAULT '',
    source_url   TEXT NOT NULL DEFAULT '',
    license_name TEXT NOT NULL DEFAULT '',
    license_url  TEXT NOT NULL DEFAULT '',
    UNIQUE (kind, sha256)
);

CREATE TABLE media_content (
    media_id INTEGER PRIMARY KEY REFERENCES media(id) ON DELETE CASCADE,
    content  BLOB NOT NULL CHECK (length(content) > 0)
);

CREATE TABLE vocabulary_media (
    vocabulary_id INTEGER NOT NULL REFERENCES vocabulary(id) ON DELETE CASCADE,
    purpose       TEXT NOT NULL CHECK (purpose IN ('pronunciation', 'picture')),
    media_id      INTEGER NOT NULL REFERENCES media(id) ON DELETE RESTRICT,
    PRIMARY KEY (vocabulary_id, purpose)
);

CREATE INDEX vocabulary_media_media
ON vocabulary_media (media_id);

CREATE TABLE srs_states (
    vocabulary_id    INTEGER PRIMARY KEY REFERENCES vocabulary(id) ON DELETE CASCADE,
    stage            INTEGER NOT NULL CHECK (stage BETWEEN 0 AND 9),
    due_at           INTEGER,
    last_reviewed_at INTEGER,
    suspended_at     INTEGER
);

CREATE INDEX srs_states_due
ON srs_states (due_at)
WHERE due_at IS NOT NULL AND suspended_at IS NULL;

CREATE TABLE user_settings (
    id                       INTEGER PRIMARY KEY CHECK (id = 1),
    time_zone                TEXT NOT NULL DEFAULT 'UTC',
    lesson_window_hours      INTEGER NOT NULL DEFAULT 12 CHECK (lesson_window_hours IN (4, 8, 12, 24)),
    extra_study_limit        INTEGER NOT NULL DEFAULT 10 CHECK (extra_study_limit BETWEEN 1 AND 100),
    retry_count              INTEGER NOT NULL DEFAULT 3 CHECK (retry_count BETWEEN 1 AND 5),
    theme                    TEXT NOT NULL DEFAULT 'light' CHECK (theme IN ('system', 'light', 'dark')),
    audio_enabled            INTEGER NOT NULL DEFAULT 1 CHECK (audio_enabled IN (0, 1)),
    leech_failure_threshold  INTEGER NOT NULL DEFAULT 5 CHECK (leech_failure_threshold BETWEEN 1 AND 100),
    leech_suspend_threshold  INTEGER NOT NULL DEFAULT 3 CHECK (leech_suspend_threshold BETWEEN 1 AND 100),
    leech_recovery_streak    INTEGER NOT NULL DEFAULT 3 CHECK (leech_recovery_streak BETWEEN 1 AND 100),
    six_month_review_enabled INTEGER NOT NULL DEFAULT 0 CHECK (six_month_review_enabled IN (0, 1)),
    review_mode              TEXT NOT NULL DEFAULT 'typed' CHECK (review_mode IN ('typed', 'self_grade')),
    review_order             TEXT NOT NULL DEFAULT 'stage_ascending'
                             CHECK (review_order IN ('stage_ascending', 'stage_descending', 'random')),
    review_card_order        TEXT NOT NULL DEFAULT 'together'
                             CHECK (review_card_order IN ('together', 'spaced')),
    review_auto_advance      INTEGER NOT NULL DEFAULT 0 CHECK (review_auto_advance IN (0, 1))
);

CREATE TABLE backup_settings (
    id             INTEGER PRIMARY KEY CHECK (id = 1),
    enabled        INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    hour           INTEGER NOT NULL DEFAULT 3 CHECK (hour BETWEEN 0 AND 23),
    google_drive   INTEGER NOT NULL DEFAULT 0 CHECK (google_drive IN (0, 1)),
    keep_local     INTEGER NOT NULL DEFAULT 1 CHECK (keep_local IN (0, 1)),
    retention_days INTEGER NOT NULL DEFAULT 1 CHECK (retention_days BETWEEN 1 AND 3),
    CHECK (keep_local = 1 OR google_drive = 1)
);

CREATE TABLE backup_state (
    id                  INTEGER PRIMARY KEY CHECK (id = 1),
    status              TEXT NOT NULL DEFAULT 'idle'
                        CHECK (status IN ('idle', 'running', 'success', 'failed')),
    trigger             TEXT NOT NULL DEFAULT ''
                        CHECK (trigger IN ('', 'manual', 'scheduled')),
    last_attempt_at     INTEGER CHECK (last_attempt_at IS NULL OR last_attempt_at >= 0),
    last_success_at     INTEGER CHECK (last_success_at IS NULL OR last_success_at >= 0),
    last_scheduled_date TEXT NOT NULL DEFAULT '',
    local_name          TEXT NOT NULL DEFAULT '',
    remote_id           TEXT NOT NULL DEFAULT '',
    error_message       TEXT NOT NULL DEFAULT ''
);

INSERT INTO backup_settings (id) VALUES (1);
INSERT INTO backup_state (id) VALUES (1);

CREATE TABLE lesson_sessions (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    status         TEXT NOT NULL DEFAULT 'active'
                   CHECK (status IN ('active', 'completed', 'abandoned')),
    phase          TEXT NOT NULL DEFAULT 'study' CHECK (phase IN ('study', 'review')),
    current_batch  INTEGER NOT NULL DEFAULT 0 CHECK (current_batch >= 0),
    study_position INTEGER NOT NULL DEFAULT 0 CHECK (study_position >= 0)
);

CREATE UNIQUE INDEX lesson_sessions_one_active
ON lesson_sessions (status)
WHERE status = 'active';

CREATE TABLE lesson_session_items (
    session_id          INTEGER NOT NULL REFERENCES lesson_sessions(id) ON DELETE CASCADE,
    vocabulary_id       INTEGER NOT NULL REFERENCES vocabulary(id) ON DELETE CASCADE,
    position            INTEGER NOT NULL CHECK (position >= 0),
    batch_number        INTEGER NOT NULL DEFAULT 0 CHECK (batch_number >= 0),
    review_completed_at INTEGER,
    study_viewed_at     INTEGER,
    PRIMARY KEY (session_id, vocabulary_id),
    UNIQUE (session_id, position)
);

CREATE INDEX lesson_session_items_batch
ON lesson_session_items (session_id, batch_number, review_completed_at, position);

CREATE INDEX lesson_session_items_vocabulary
ON lesson_session_items (vocabulary_id, session_id);

CREATE INDEX lesson_session_items_review_completed
ON lesson_session_items (review_completed_at)
WHERE review_completed_at IS NOT NULL;

CREATE TABLE review_sessions (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    kind                TEXT NOT NULL CHECK (kind IN ('normal', 'extra')),
    status              TEXT NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active', 'paused', 'completed', 'abandoned')),
    completed_at        INTEGER,
    max_attempts        INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts BETWEEN 1 AND 5),
    last_undo_result_id INTEGER NOT NULL DEFAULT 0 CHECK (last_undo_result_id >= 0),
    lesson_session_id   INTEGER REFERENCES lesson_sessions(id) ON DELETE CASCADE,
    answer_mode         TEXT NOT NULL DEFAULT 'typed' CHECK (answer_mode IN ('typed', 'self_grade')),
    card_order          TEXT NOT NULL DEFAULT 'together' CHECK (card_order IN ('together', 'spaced')),
    CHECK (lesson_session_id IS NULL OR kind = 'extra')
);

CREATE UNIQUE INDEX review_sessions_one_standalone
ON review_sessions (kind)
WHERE lesson_session_id IS NULL AND status IN ('active', 'paused');

CREATE UNIQUE INDEX review_sessions_one_lesson
ON review_sessions (lesson_session_id)
WHERE lesson_session_id IS NOT NULL AND status IN ('active', 'paused');

CREATE INDEX review_sessions_lesson
ON review_sessions (lesson_session_id, id DESC)
WHERE lesson_session_id IS NOT NULL;

CREATE TABLE review_session_items (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id    INTEGER NOT NULL REFERENCES review_sessions(id) ON DELETE CASCADE,
    vocabulary_id INTEGER NOT NULL REFERENCES vocabulary(id) ON DELETE CASCADE,
    position      INTEGER NOT NULL CHECK (position >= 0),
    srs_applied   INTEGER NOT NULL DEFAULT 1 CHECK (srs_applied IN (0, 1)),
    status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'current', 'completed', 'failed')),
    UNIQUE (session_id, position)
);

CREATE INDEX review_session_items_pending
ON review_session_items (session_id, status, position);

CREATE INDEX review_session_items_vocabulary
ON review_session_items (vocabulary_id, session_id);

CREATE UNIQUE INDEX review_session_items_one_current
ON review_session_items (session_id)
WHERE status = 'current';

CREATE TABLE review_prompts (
    id                               INTEGER PRIMARY KEY AUTOINCREMENT,
    session_item_id                  INTEGER NOT NULL REFERENCES review_session_items(id) ON DELETE CASCADE,
    prompt_type                      TEXT NOT NULL CHECK (prompt_type IN ('meaning', 'pronunciation')),
    position                         INTEGER NOT NULL CHECK (position >= 0),
    status                           TEXT NOT NULL DEFAULT 'pending'
                                     CHECK (status IN ('pending', 'current', 'passed', 'failed')),
    attempt_count                    INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    last_incorrect_answer            TEXT NOT NULL DEFAULT '' CHECK (length(last_incorrect_answer) <= 200),
    last_incorrect_content_revision INTEGER NOT NULL DEFAULT 0 CHECK (last_incorrect_content_revision >= 0),
    queue_position                   INTEGER NOT NULL DEFAULT 0 CHECK (queue_position >= 0),
    UNIQUE (session_item_id, position)
);

CREATE UNIQUE INDEX review_prompts_one_current
ON review_prompts (session_item_id)
WHERE status = 'current';

CREATE INDEX review_prompts_queue
ON review_prompts (status, queue_position);

CREATE TABLE review_results (
    id                                INTEGER PRIMARY KEY AUTOINCREMENT,
    session_item_id                   INTEGER NOT NULL UNIQUE REFERENCES review_session_items(id) ON DELETE CASCADE,
    outcome                           TEXT NOT NULL CHECK (outcome IN ('success', 'failure')),
    stage_before                      INTEGER CHECK (stage_before IS NULL OR stage_before BETWEEN 0 AND 9),
    stage_after                       INTEGER CHECK (stage_after IS NULL OR stage_after BETWEEN 0 AND 9),
    due_before                        INTEGER CHECK (due_before IS NULL OR due_before >= 0),
    due_after                         INTEGER CHECK (due_after IS NULL OR due_after >= 0),
    last_reviewed_before              INTEGER CHECK (last_reviewed_before IS NULL OR last_reviewed_before >= 0),
    created_at                        INTEGER NOT NULL CHECK (created_at >= 0),
    voided_at                         INTEGER CHECK (voided_at IS NULL OR voided_at >= created_at),
    srs_applied                       INTEGER NOT NULL DEFAULT 1 CHECK (srs_applied IN (0, 1)),
    first_attempt_correct_count       INTEGER NOT NULL DEFAULT 0 CHECK (first_attempt_correct_count >= 0),
    prompt_count                      INTEGER NOT NULL DEFAULT 0 CHECK (prompt_count >= 0),
    mistake_visibility_existed_before INTEGER CHECK (mistake_visibility_existed_before IN (0, 1)),
    mistake_hidden_before             INTEGER CHECK (mistake_hidden_before IS NULL OR mistake_hidden_before >= 0),
    mistake_leech_hidden_before       INTEGER CHECK (mistake_leech_hidden_before IS NULL OR mistake_leech_hidden_before >= 0),
    CHECK (first_attempt_correct_count <= prompt_count),
    CHECK (
        (srs_applied = 0
         AND stage_before IS NULL
         AND stage_after IS NULL
         AND due_before IS NULL
         AND due_after IS NULL
         AND last_reviewed_before IS NULL)
        OR
        (srs_applied = 1 AND stage_before IS NOT NULL AND stage_after IS NOT NULL)
    ),
    CHECK (
        (srs_applied = 1
         AND outcome = 'failure'
         AND mistake_visibility_existed_before IS NOT NULL)
        OR
        ((srs_applied = 0 OR outcome = 'success')
         AND mistake_visibility_existed_before IS NULL
         AND mistake_hidden_before IS NULL
         AND mistake_leech_hidden_before IS NULL)
    ),
    CHECK (
        mistake_visibility_existed_before IS NULL
        OR mistake_visibility_existed_before = 1
        OR (mistake_hidden_before IS NULL AND mistake_leech_hidden_before IS NULL)
    )
);

CREATE INDEX review_results_learning_created
ON review_results (created_at DESC)
WHERE srs_applied = 1 AND voided_at IS NULL;

CREATE TABLE mistake_visibility (
    vocabulary_id   INTEGER PRIMARY KEY REFERENCES vocabulary(id) ON DELETE CASCADE,
    hidden_at       INTEGER,
    leech_hidden_at INTEGER
);

CREATE TABLE import_runs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    filename     TEXT NOT NULL CHECK (length(filename) > 0),
    archive_path TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'previewed'
                 CHECK (status IN ('previewed', 'applied', 'failed')),
    created_at   INTEGER NOT NULL CHECK (created_at >= 0),
    completed_at INTEGER
);

CREATE TABLE import_notes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id        INTEGER NOT NULL REFERENCES import_runs(id) ON DELETE CASCADE,
    action        TEXT NOT NULL CHECK (action IN ('created', 'skipped', 'failed')),
    error_message TEXT
);

CREATE INDEX import_notes_run
ON import_notes (run_id, id);

CREATE TABLE web_sessions (
    token     TEXT PRIMARY KEY,
    data      BLOB NOT NULL,
    expiry_at INTEGER NOT NULL
);

CREATE INDEX web_sessions_expiry
ON web_sessions (expiry_at);

CREATE TABLE mining_captures (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    raw_text                 TEXT NOT NULL CHECK (length(raw_text) > 0),
    expression               TEXT NOT NULL CHECK (length(expression) > 0),
    normalized_expression    TEXT NOT NULL CHECK (length(normalized_expression) > 0),
    context_text             TEXT NOT NULL DEFAULT '',
    source_kind              TEXT NOT NULL CHECK (source_kind IN ('manual', 'web', 'video', 'ebook', 'other')),
    source_title             TEXT NOT NULL DEFAULT '',
    source_url               TEXT NOT NULL DEFAULT '',
    source_position_ms       INTEGER CHECK (source_position_ms IS NULL OR source_position_ms >= 0),
    capture_nonce            TEXT NOT NULL UNIQUE CHECK (length(capture_nonce) = 32),
    request_hash             TEXT NOT NULL CHECK (length(request_hash) = 64),
    revision                 INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
    status                   TEXT NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending', 'accepted', 'discarded')),
    vocabulary_id            INTEGER REFERENCES vocabulary(id) ON DELETE SET NULL,
    created_at               INTEGER NOT NULL CHECK (created_at >= 0),
    suggested_entry_sequence INTEGER CHECK (suggested_entry_sequence IS NULL OR suggested_entry_sequence > 0),
    CHECK (status = 'accepted' OR vocabulary_id IS NULL)
);

CREATE INDEX mining_captures_status_created
ON mining_captures (status, created_at DESC, id DESC);

CREATE TABLE mining_capture_tombstones (
    capture_nonce TEXT PRIMARY KEY CHECK (length(capture_nonce) = 32),
    deleted_at    INTEGER NOT NULL CHECK (deleted_at >= 0)
);

-- A deleted capture nonce remains spent so a delayed extension retry cannot
-- recreate work the user deliberately removed.
-- +goose StatementBegin
CREATE TRIGGER mining_capture_reject_deleted_nonce
BEFORE INSERT ON mining_captures
WHEN EXISTS (
    SELECT 1 FROM mining_capture_tombstones
    WHERE capture_nonce = NEW.capture_nonce
)
BEGIN
    SELECT RAISE(ABORT, 'capture nonce was deleted');
END;
-- +goose StatementEnd

CREATE TABLE mining_capture_media (
    capture_id INTEGER NOT NULL REFERENCES mining_captures(id) ON DELETE CASCADE,
    purpose    TEXT NOT NULL CHECK (purpose IN ('sentence_audio', 'video_frame', 'pronunciation')),
    position   INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    media_id   INTEGER NOT NULL REFERENCES media(id) ON DELETE RESTRICT,
    PRIMARY KEY (capture_id, purpose, position),
    UNIQUE (capture_id, purpose, media_id)
);

CREATE INDEX mining_capture_media_media
ON mining_capture_media (media_id);

CREATE TABLE extension_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    token_hash   BLOB NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    token_prefix TEXT NOT NULL CHECK (length(token_prefix) = 19),
    created_at   INTEGER NOT NULL CHECK (created_at >= 0),
    last_used_at INTEGER CHECK (last_used_at IS NULL OR last_used_at >= created_at)
);

CREATE INDEX extension_tokens_list
ON extension_tokens (created_at DESC, id DESC);

CREATE TABLE vocabulary_examples (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    vocabulary_id      INTEGER NOT NULL REFERENCES vocabulary(id) ON DELETE CASCADE,
    mining_capture_id  INTEGER UNIQUE REFERENCES mining_captures(id) ON DELETE CASCADE,
    origin             TEXT NOT NULL CHECK (origin IN ('manual', 'mined', 'generated')),
    sentence           TEXT NOT NULL CHECK (length(sentence) > 0),
    translation        TEXT NOT NULL DEFAULT '',
    target_surface     TEXT NOT NULL DEFAULT '',
    source_title       TEXT NOT NULL DEFAULT '',
    source_url         TEXT NOT NULL DEFAULT '',
    source_position_ms INTEGER CHECK (source_position_ms IS NULL OR source_position_ms >= 0),
    provider           TEXT NOT NULL DEFAULT '',
    model              TEXT NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL CHECK (created_at >= 0),
    updated_at         INTEGER NOT NULL CHECK (updated_at >= 0),
    CHECK ((origin = 'mined') = (mining_capture_id IS NOT NULL))
);

CREATE INDEX vocabulary_examples_vocabulary
ON vocabulary_examples (vocabulary_id, created_at DESC, id DESC);

CREATE TABLE leech_states (
    vocabulary_id         INTEGER PRIMARY KEY REFERENCES vocabulary(id) ON DELETE CASCADE,
    failures_toward_leech INTEGER NOT NULL DEFAULT 0 CHECK (failures_toward_leech >= 0),
    active                INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0, 1)),
    ever_leech            INTEGER NOT NULL DEFAULT 0 CHECK (ever_leech IN (0, 1)),
    marked_at             INTEGER,
    failures_since_mark   INTEGER NOT NULL DEFAULT 0 CHECK (failures_since_mark >= 0),
    correct_streak        INTEGER NOT NULL DEFAULT 0 CHECK (correct_streak >= 0),
    auto_suspended_at     INTEGER,
    cleared_at            INTEGER,
    reset_after_result_id INTEGER NOT NULL DEFAULT 0 CHECK (reset_after_result_id >= 0),
    CHECK (active = 0 OR (ever_leech = 1 AND marked_at IS NOT NULL)),
    CHECK (active = 1 OR auto_suspended_at IS NULL)
);

CREATE INDEX leech_states_active
ON leech_states (active, auto_suspended_at, marked_at)
WHERE active = 1;

-- +goose Down

DROP TABLE leech_states;
DROP TABLE vocabulary_examples;
DROP TABLE extension_tokens;
DROP TABLE mining_capture_media;
DROP TRIGGER mining_capture_reject_deleted_nonce;
DROP TABLE mining_captures;
DROP TABLE mining_capture_tombstones;
DROP TABLE web_sessions;
DROP TABLE import_notes;
DROP TABLE import_runs;
DROP TABLE mistake_visibility;
DROP TABLE review_results;
DROP TABLE review_prompts;
DROP TABLE review_session_items;
DROP TABLE review_sessions;
DROP TABLE lesson_session_items;
DROP TABLE lesson_sessions;
DROP TABLE backup_state;
DROP TABLE backup_settings;
DROP TABLE user_settings;
DROP TABLE srs_states;
DROP TABLE vocabulary_media;
DROP TABLE media_content;
DROP TABLE media;
DROP TABLE meanings;
DROP TABLE vocabulary;

PRAGMA application_id = 0;
