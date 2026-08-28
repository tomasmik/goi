package database

import (
	"database/sql"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestInitSchemaObjects(t *testing.T) {
	db, _ := openTestDatabase(t)

	assertSchemaNames(t, db, "table", []string{
		"backup_settings",
		"backup_state",
		"extension_tokens",
		"goose_db_version",
		"import_notes",
		"import_runs",
		"leech_states",
		"lesson_session_items",
		"lesson_sessions",
		"meanings",
		"media",
		"media_content",
		"mining_capture_media",
		"mining_capture_tombstones",
		"mining_captures",
		"mistake_visibility",
		"review_prompts",
		"review_results",
		"review_session_items",
		"review_sessions",
		"srs_states",
		"user_settings",
		"vocabulary",
		"vocabulary_examples",
		"vocabulary_media",
		"wanikani_subjects",
		"wanikani_sync_state",
		"web_sessions",
	})
	assertSchemaNames(t, db, "index", []string{
		"extension_tokens_list",
		"import_notes_run",
		"leech_states_active",
		"lesson_session_items_batch",
		"lesson_session_items_review_completed",
		"lesson_session_items_vocabulary",
		"lesson_sessions_one_active",
		"mining_capture_media_media",
		"mining_captures_status_created",
		"review_prompts_one_current",
		"review_prompts_queue",
		"review_results_learning_created",
		"review_session_items_one_current",
		"review_session_items_pending",
		"review_session_items_vocabulary",
		"review_sessions_lesson",
		"review_sessions_one_lesson",
		"review_sessions_one_standalone",
		"srs_states_due",
		"vocabulary_examples_vocabulary",
		"vocabulary_lesson_completed",
		"vocabulary_live_expression",
		"vocabulary_media_media",
		"vocabulary_status_created",
		"vocabulary_updated",
		"web_sessions_expiry",
	})
	assertSchemaNames(t, db, "trigger", []string{
		"mining_capture_reject_deleted_nonce",
	})
}

func TestInitSchemaColumns(t *testing.T) {
	db, _ := openTestDatabase(t)
	expected := map[string][]string{
		"backup_settings": {"id", "enabled", "hour", "google_drive", "keep_local", "retention_days"},
		"backup_state": {
			"id", "status", "trigger", "last_attempt_at", "last_success_at", "last_scheduled_date",
			"local_name", "remote_id", "error_message",
		},
		"extension_tokens": {
			"id", "name", "token_hash", "token_prefix", "created_at", "last_used_at",
		},
		"import_notes": {"id", "run_id", "action", "error_message"},
		"import_runs":  {"id", "filename", "archive_path", "status", "created_at", "completed_at"},
		"lesson_session_items": {
			"session_id", "vocabulary_id", "position", "batch_number", "review_completed_at", "study_viewed_at",
		},
		"lesson_sessions": {"id", "status", "phase", "current_batch", "study_position"},
		"leech_states": {
			"vocabulary_id", "failures_toward_leech", "active", "ever_leech", "marked_at",
			"failures_since_mark", "correct_streak", "auto_suspended_at", "cleared_at", "reset_after_result_id",
		},
		"meanings": {"vocabulary_id", "position", "text", "normalized_text"},
		"media": {
			"id", "kind", "mime_type", "sha256", "created_at",
			"source_name", "source_url", "license_name", "license_url",
		},
		"media_content":             {"media_id", "content"},
		"mining_capture_media":      {"capture_id", "purpose", "position", "media_id"},
		"mining_capture_tombstones": {"capture_nonce", "deleted_at"},
		"mining_captures": {
			"id", "raw_text", "expression", "normalized_expression", "context_text", "source_kind",
			"source_title", "source_url", "source_position_ms", "capture_nonce", "request_hash", "revision",
			"status", "vocabulary_id", "created_at", "suggested_entry_sequence",
		},
		"mistake_visibility": {"vocabulary_id", "hidden_at", "leech_hidden_at"},
		"review_prompts": {
			"id", "session_item_id", "prompt_type", "position", "status", "attempt_count",
			"last_incorrect_answer", "last_incorrect_content_revision", "queue_position",
		},
		"review_results": {
			"id", "session_item_id", "outcome", "stage_before", "stage_after", "due_before", "due_after",
			"last_reviewed_before", "created_at", "voided_at", "srs_applied", "first_attempt_correct_count",
			"prompt_count", "mistake_visibility_existed_before", "mistake_hidden_before", "mistake_leech_hidden_before",
		},
		"review_session_items": {"id", "session_id", "vocabulary_id", "position", "srs_applied", "status"},
		"review_sessions": {
			"id", "kind", "status", "completed_at", "max_attempts", "last_undo_result_id", "lesson_session_id",
			"answer_mode", "card_order",
		},
		"srs_states": {"vocabulary_id", "stage", "due_at", "last_reviewed_at", "suspended_at"},
		"user_settings": {
			"id", "time_zone", "lesson_window_hours", "extra_study_limit", "retry_count", "theme", "audio_enabled",
			"leech_failure_threshold", "leech_suspend_threshold", "leech_recovery_streak", "six_month_review_enabled",
			"review_mode", "review_order", "review_card_order", "review_auto_advance",
		},
		"vocabulary": {
			"id", "expression", "normalized_expression", "pronunciation", "normalized_pronunciation", "status",
			"source_label", "notes", "lesson_completed_at", "known_elsewhere_at", "content_revision", "is_duplicate",
			"created_at", "updated_at",
		},
		"vocabulary_examples": {
			"id", "vocabulary_id", "mining_capture_id", "origin", "sentence", "translation", "target_surface",
			"source_title", "source_url", "source_position_ms", "provider", "model", "created_at", "updated_at",
		},
		"vocabulary_media":  {"vocabulary_id", "purpose", "media_id"},
		"wanikani_subjects": {"subject_id", "expression", "synced_at"},
		"wanikani_sync_state": {
			"id", "user_id", "username", "user_level", "cursor_at", "last_attempt_at", "last_success_at", "last_error",
		},
		"web_sessions": {"token", "data", "expiry_at"},
	}

	tables := make([]string, 0, len(expected))
	for table := range expected {
		tables = append(tables, table)
	}
	slices.Sort(tables)
	for _, table := range tables {
		columns := tableColumns(t, db, table)
		if !slices.Equal(columns, expected[table]) {
			t.Errorf("%s columns = %q, want %q", table, columns, expected[table])
		}
	}
}

func TestInitSchemaApplicationID(t *testing.T) {
	db, _ := openTestDatabase(t)
	var actual int
	if err := db.QueryRow("PRAGMA application_id").Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != applicationID {
		t.Fatalf("application ID = %d, want %d", actual, applicationID)
	}
	var version int
	if err := db.QueryRow(`
		SELECT COALESCE(MAX(version_id), 0)
		FROM goose_db_version
		WHERE is_applied = 1`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
}

func TestBackupRetentionDefaultsToOneDay(t *testing.T) {
	db, _ := openTestDatabase(t)
	var days int
	if err := db.QueryRow("SELECT retention_days FROM backup_settings WHERE id = 1").Scan(&days); err != nil {
		t.Fatal(err)
	}
	if days != 1 {
		t.Fatalf("backup retention = %d days, want 1", days)
	}
}

func TestMigrateAcceptsPreSquashVersionHistory(t *testing.T) {
	ctx := t.Context()
	db, err := Open(ctx, t.TempDir()+"/goi.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO vocabulary (expression, normalized_expression, pronunciation, normalized_pronunciation, source_label, notes, created_at, updated_at) VALUES ('保存', '保存', 'ほぞん', 'ほぞん', 'pre-squash', 'migration proof', 1, 1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM goose_db_version"); err != nil {
		t.Fatal(err)
	}
	for version := 0; version <= schemaVersion; version++ {
		if _, err := db.Exec("INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)", version); err != nil {
			t.Fatal(err)
		}
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var expression, notes string
	if err := db.QueryRow("SELECT expression, notes FROM vocabulary").Scan(&expression, &notes); err != nil {
		t.Fatal(err)
	}
	if expression != "保存" || notes != "migration proof" {
		t.Fatalf("migrated data = %q, %q", expression, notes)
	}
}

func TestInitMigrationRollsBackCleanly(t *testing.T) {
	ctx := t.Context()
	db, err := Open(ctx, t.TempDir()+"/goi.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		db,
		files,
		goose.WithLogger(goose.NopLogger()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	var applicationID, vocabularyTables int
	if err := db.QueryRow("PRAGMA application_id").Scan(&applicationID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = 'vocabulary'").Scan(&vocabularyTables); err != nil {
		t.Fatal(err)
	}
	if applicationID != 0 || vocabularyTables != 0 {
		t.Fatalf("rolled back schema has application ID %d and %d vocabulary tables", applicationID, vocabularyTables)
	}
}

func TestInitSchemaDoesNotReuseGeneratedIDs(t *testing.T) {
	db, _ := openTestDatabase(t)
	tables := []string{
		"extension_tokens",
		"import_notes",
		"import_runs",
		"lesson_sessions",
		"media",
		"mining_captures",
		"review_prompts",
		"review_results",
		"review_session_items",
		"review_sessions",
		"vocabulary",
		"vocabulary_examples",
	}
	for _, table := range tables {
		var statement string
		if err := db.QueryRow("SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?", table).Scan(&statement); err != nil {
			t.Fatalf("load %s schema: %v", table, err)
		}
		if !strings.Contains(statement, "AUTOINCREMENT") {
			t.Errorf("%s can reuse a deleted generated ID", table)
		}
	}
}

func TestMigrateRejectsNonGoiDatabase(t *testing.T) {
	tests := []struct {
		name      string
		prepare   func(*testing.T, *sql.DB)
		wantError string
	}{
		{
			name: "foreign application ID",
			prepare: func(t *testing.T, db *sql.DB) {
				if _, err := db.Exec("PRAGMA application_id = 42"); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "not a Goi database",
		},
		{
			name: "unidentified user table",
			prepare: func(t *testing.T, db *sql.DB) {
				if _, err := db.Exec("CREATE TABLE personal_notes (id INTEGER PRIMARY KEY, body TEXT)"); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "is not empty",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := t.TempDir() + "/foreign.sqlite"
			db, err := Open(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			test.prepare(t, db)

			err = Migrate(t.Context(), db)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Migrate error = %v, want %q", err, test.wantError)
			}
			var vocabularyTables int
			if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = 'vocabulary'").Scan(&vocabularyTables); err != nil {
				t.Fatal(err)
			}
			if vocabularyTables != 0 {
				t.Fatal("migration changed a non-Goi database")
			}
		})
	}
}

func TestMigrateRejectsRetiredBaseline(t *testing.T) {
	path := t.TempDir() + "/legacy.sqlite"
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE goose_db_version (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version_id INTEGER NOT NULL,
			is_applied INTEGER NOT NULL,
			tstamp TIMESTAMP DEFAULT (datetime('now'))
		);
		INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1), (1, 1)`); err != nil {
		t.Fatal(err)
	}

	err = Migrate(t.Context(), db)
	if err == nil || !strings.Contains(err.Error(), "retired pre-baseline schema") {
		t.Fatalf("Migrate error = %v, want retired baseline error", err)
	}
}

func TestMigrateRejectsNewerGoiSchema(t *testing.T) {
	path := t.TempDir() + "/newer.sqlite"
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)", schemaVersion+1); err != nil {
		t.Fatal(err)
	}

	err = Migrate(t.Context(), db)
	if err == nil || !strings.Contains(err.Error(), "newer than") {
		t.Fatalf("Migrate error = %v, want newer schema error", err)
	}
}

func TestInitSchemaEnforcesCoreInvariants(t *testing.T) {
	db, _ := openTestDatabase(t)
	if _, err := db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at)
		VALUES ('語', '語', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	assertStatementFails(t, db, `
		INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at)
		VALUES ('同じ語', '語', 1, 1)`)
	if _, err := db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, is_duplicate, created_at, updated_at)
		VALUES ('意図した重複', '語', 1, 1, 1)`); err != nil {
		t.Fatalf("explicit duplicate vocabulary should be allowed: %v", err)
	}
	assertStatementFails(t, db, `
		INSERT INTO vocabulary (expression, normalized_expression, is_duplicate, created_at, updated_at)
		VALUES ('無効', '無効', 2, 1, 1)`)
	assertStatementFails(t, db, `
		INSERT INTO vocabulary (expression, normalized_expression, status, created_at, updated_at)
		VALUES ('削除済み', '削除済み', 'deleted', 1, 1)`)
	if _, err := db.Exec("INSERT INTO user_settings (id, theme) VALUES (1, 'system')"); err != nil {
		t.Fatalf("system theme should be valid: %v", err)
	}
	assertStatementFails(t, db, `
		INSERT INTO vocabulary (
			expression, normalized_expression, pronunciation, normalized_pronunciation,
			created_at, updated_at
		)
		VALUES ('読む', '読む', 'よむ', '', 1, 1)`)
	assertStatementFails(t, db, `
		INSERT INTO meanings (vocabulary_id, position, text, normalized_text)
		VALUES (1, -1, 'word', 'word')`)

	if _, err := db.Exec("INSERT INTO lesson_sessions (status) VALUES ('active')"); err != nil {
		t.Fatal(err)
	}
	assertStatementFails(t, db, "INSERT INTO lesson_sessions (status) VALUES ('active')")
	assertStatementFails(t, db, `
		INSERT INTO review_sessions (kind, status, lesson_session_id)
		VALUES ('normal', 'completed', 1)`)

	reviewResult, err := db.Exec("INSERT INTO review_sessions (kind, status) VALUES ('normal', 'active')")
	if err != nil {
		t.Fatal(err)
	}
	assertStatementFails(t, db, "INSERT INTO review_sessions (kind, status) VALUES ('normal', 'paused')")
	reviewID, err := reviewResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	itemResult, err := db.Exec(`
		INSERT INTO review_session_items (
			session_id, vocabulary_id, position, status
		) VALUES (?, 1, 0, 'current')`, reviewID)
	if err != nil {
		t.Fatal(err)
	}
	assertStatementFails(t, db, fmt.Sprintf(`
		INSERT INTO review_session_items (
			session_id, vocabulary_id, position, status
		) VALUES (%d, 1, 1, 'current')`, reviewID))
	itemID, err := itemResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO review_prompts (session_item_id, prompt_type, position, status)
		VALUES (?, 'meaning', 0, 'current')`, itemID); err != nil {
		t.Fatal(err)
	}
	assertStatementFails(t, db, fmt.Sprintf(`
		INSERT INTO review_prompts (session_item_id, prompt_type, position, status)
		VALUES (%d, 'pronunciation', 1, 'current')`, itemID))
	assertStatementFails(t, db, fmt.Sprintf(`
		UPDATE review_prompts SET last_incorrect_answer = '%s'`, strings.Repeat("a", 201)))
	assertStatementFails(t, db, `
		UPDATE review_prompts SET last_incorrect_content_revision = -1`)
	if _, err := db.Exec(`
		INSERT INTO review_results (session_item_id, outcome, stage_before, stage_after, created_at)
		VALUES (?, 'success', 0, 1, 1)`, itemID); err != nil {
		t.Fatal(err)
	}
	assertStatementFails(t, db, fmt.Sprintf(`
		INSERT INTO review_results (
			session_item_id, outcome, stage_before, stage_after, created_at,
			mistake_visibility_existed_before
		)
		VALUES (%d, 'failure', 1, 0, 2, 0)`, itemID))

	secondItemResult, err := db.Exec(`
		INSERT INTO review_session_items (
			session_id, vocabulary_id, position, status
		) VALUES (?, 1, 1, 'completed')`, reviewID)
	if err != nil {
		t.Fatal(err)
	}
	secondItemID, err := secondItemResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	assertStatementFails(t, db, fmt.Sprintf(`
		INSERT INTO review_results (session_item_id, outcome, created_at)
		VALUES (%d, 'success', 2)`, secondItemID))
	assertStatementFails(t, db, fmt.Sprintf(`
		INSERT INTO review_results (
			session_item_id, outcome, stage_before, stage_after, created_at
		)
		VALUES (%d, 'failure', 1, 0, 2)`, secondItemID))
}

func assertSchemaNames(t *testing.T, db *sql.DB, objectType string, expected []string) {
	t.Helper()
	rows, err := db.Query(`
		SELECT name
		FROM sqlite_schema
		WHERE type = ? AND name NOT LIKE 'sqlite_%'
		ORDER BY name`, objectType)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	actual := make([]string, 0, len(expected))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		actual = append(actual, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(actual, expected) {
		t.Fatalf("%s names = %q, want %q", objectType, actual, expected)
	}
}

func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%q)", table))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var position, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&position, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func assertStatementFails(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.Exec(statement); err == nil {
		t.Fatalf("statement unexpectedly succeeded: %s", statement)
	}
}
