-- +goose Up

CREATE TABLE wanikani_sync_state (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    user_id         TEXT NOT NULL DEFAULT '',
    username        TEXT NOT NULL DEFAULT '',
    user_level      INTEGER NOT NULL DEFAULT 0 CHECK (user_level BETWEEN 0 AND 60),
    cursor_at       TEXT NOT NULL DEFAULT '',
    last_attempt_at INTEGER CHECK (last_attempt_at IS NULL OR last_attempt_at >= 0),
    last_success_at INTEGER CHECK (last_success_at IS NULL OR last_success_at >= 0),
    last_error      TEXT NOT NULL DEFAULT '' CHECK (length(last_error) <= 500)
);

INSERT INTO wanikani_sync_state (id) VALUES (1);

CREATE TABLE wanikani_subjects (
    subject_id INTEGER PRIMARY KEY CHECK (subject_id > 0),
    expression TEXT NOT NULL CHECK (length(expression) > 0),
    synced_at  INTEGER NOT NULL CHECK (synced_at >= 0)
);

-- +goose Down

DROP TABLE wanikani_subjects;
DROP TABLE wanikani_sync_state;
