package database

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

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

func assertStatementFails(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.Exec(statement); err == nil {
		t.Fatalf("statement unexpectedly succeeded: %s", statement)
	}
}
