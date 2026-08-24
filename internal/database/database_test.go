package database

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tomasmik/goi/internal/contextio"
)

func testBackup(ctx context.Context, sourcePath, outputPath string) (string, error) {
	return createBackup(ctx, sourcePath, outputPath, false)
}

func testRestore(ctx context.Context, backupPath, databasePath string) error {
	return restore(ctx, backupPath, databasePath, "", false, nil, false)
}

func TestOpenAndMigrate(t *testing.T) {
	db, _ := openTestDatabase(t)
	var tableCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('vocabulary', 'media_content', 'review_sessions')`).Scan(&tableCount); err != nil {
		t.Fatalf("check migrated tables: %v", err)
	}
	if tableCount != 3 {
		t.Fatalf("migrated table count = %d, want 3", tableCount)
	}
	if err := IntegrityCheck(context.Background(), db); err != nil {
		t.Fatalf("integrity check: %v", err)
	}
}

func TestBackupRejectsNonGoiDatabase(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "notes.sqlite")
	db, err := Open(ctx, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE personal_notes (id INTEGER PRIMARY KEY, body TEXT)"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(directory, "backup.sqlite")
	if _, err := testBackup(ctx, sourcePath, outputPath); err == nil || !strings.Contains(err.Error(), "validate source database") {
		t.Fatalf("Backup error = %v, want source validation error", err)
	}
	if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid backup output exists: %v", err)
	}
}

func TestIntegrityCheckRejectsForeignKeyViolations(t *testing.T) {
	db, _ := openTestDatabase(t)
	if _, err := db.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO meanings (vocabulary_id, position, text, normalized_text)
		VALUES (999, 0, 'orphan', 'orphan')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}

	err := IntegrityCheck(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "foreign key check failed") {
		t.Fatalf("IntegrityCheck error = %v, want foreign key failure", err)
	}
}

func TestOpenRestrictsDatabasePermissions(t *testing.T) {
	previousUmask := syscall.Umask(0)
	defer syscall.Umask(previousUmask)

	path := filepath.Join(t.TempDir(), "vocab.sqlite")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at) VALUES ('権限', '権限', 1, 1)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	assertTestFileMode(t, path, 0o640)
	assertTestFileMode(t, path+"-wal", 0o640)
	assertTestFileMode(t, path+"-shm", 0o640)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	assertTestFileMode(t, path+"-wal", 0o640)
	assertTestFileMode(t, path+"-shm", 0o640)

	for _, filePath := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(filePath, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, filePath := range []string{path, path + "-wal", path + "-shm"} {
		assertTestFileMode(t, filePath, 0o600)
	}
}

func TestReconnectCreatesPrivateSidecarsForEscapedPath(t *testing.T) {
	previousUmask := syscall.Umask(0)
	defer syscall.Umask(previousUmask)

	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(directory, "vocab #1?.sqlite")
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativePath, err := filepath.Rel(workingDirectory, databasePath)
	if err != nil {
		t.Fatal(err)
	}

	db, err := Open(context.Background(), relativePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.SetMaxIdleConns(0)
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(databasePath + suffix); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at) VALUES ('再接続', '再接続', 1, 1)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for _, filePath := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		assertTestFileMode(t, filePath, 0o640)
	}
	assertVocabularyExpression(t, databasePath, "再接続")
}

func TestTightenDatabasePermissionsIncludesExistingSidecars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vocab.sqlite")
	for _, filePath := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.WriteFile(filePath, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filePath, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	if err := tightenDatabasePermissions(path); err != nil {
		t.Fatal(err)
	}
	for _, filePath := range []string{path, path + "-wal", path + "-shm"} {
		assertTestFileMode(t, filePath, 0o640)
	}
}

func TestBackupProducesVerifiedSnapshot(t *testing.T) {
	db, sourcePath := openTestDatabase(t)
	if _, err := db.Exec(`INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at) VALUES ('食べる', '食べる', 1, 1)`); err != nil {
		t.Fatalf("insert test vocabulary: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close source database: %v", err)
	}

	outputPath := filepath.Join(t.TempDir(), "backup.sqlite")
	checksum, err := testBackup(context.Background(), sourcePath, outputPath)
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	if len(checksum) != 64 {
		t.Fatalf("checksum length = %d, want 64", len(checksum))
	}
	assertTestFileMode(t, outputPath, 0o640)
	assertTestFileMode(t, outputPath+".sha256", 0o640)
	backup, err := Open(context.Background(), outputPath)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backup.Close()
	var expression string
	if err := backup.QueryRow("SELECT expression FROM vocabulary LIMIT 1").Scan(&expression); err != nil {
		t.Fatalf("read backup vocabulary: %v", err)
	}
	if expression != "食べる" {
		t.Fatalf("backup expression = %q, want 食べる", expression)
	}
}

func TestBackupRejectsArtifactAliases(t *testing.T) {
	t.Run("checksum", func(t *testing.T) {
		directory := t.TempDir()
		outputPath := filepath.Join(directory, "backup.sqlite")
		sourcePath := outputPath + ".sha256"
		db, err := Open(context.Background(), sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := Migrate(context.Background(), db); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		beforeChecksum, err := fileChecksum(t.Context(), sourcePath)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := testBackup(context.Background(), sourcePath, outputPath); err == nil {
			t.Fatal("backup with the live database at its checksum output succeeded")
		}
		afterChecksum, err := fileChecksum(t.Context(), sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		if afterChecksum != beforeChecksum {
			t.Fatal("rejected backup changed its source database")
		}
	})

	t.Run("pending import directory", func(t *testing.T) {
		directory := t.TempDir()
		outputPath := filepath.Join(directory, "backup.sqlite")
		importsPath := outputPath + ".imports"
		if err := os.Mkdir(importsPath, 0o750); err != nil {
			t.Fatal(err)
		}
		sourcePath := filepath.Join(importsPath, "vocab.sqlite")
		db, err := Open(context.Background(), sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := Migrate(context.Background(), db); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}

		if _, err := BackupWithImports(context.Background(), sourcePath, outputPath); err == nil {
			t.Fatal("backup whose pending import output contains the live database succeeded")
		}
		if err := requireRegularFile(sourcePath); err != nil {
			t.Fatalf("rejected backup removed its source database: %v", err)
		}
	})
}

func TestBackupRejectsSQLiteArtifactOutputs(t *testing.T) {
	db, sourcePath := openTestDatabase(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	beforeChecksum, err := fileChecksum(t.Context(), sourcePath)
	if err != nil {
		t.Fatal(err)
	}

	for _, suffix := range []string{"-wal", "-shm", ".lock"} {
		t.Run(suffix, func(t *testing.T) {
			if _, err := testBackup(context.Background(), sourcePath, sourcePath+suffix); err == nil {
				t.Fatalf("backup to live database artifact %q succeeded", suffix)
			}
			afterChecksum, err := fileChecksum(t.Context(), sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			if afterChecksum != beforeChecksum {
				t.Fatal("rejected backup changed its source database")
			}
		})
	}
}

func TestBackupFileIOHonorsCanceledContext(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source.sqlite")
	if err := os.WriteFile(sourcePath, []byte("database"), 0o640); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := fileChecksum(ctx, sourcePath); !errors.Is(err, context.Canceled) {
		t.Fatalf("fileChecksum() error = %v, want context cancellation", err)
	}
	destinationPath := filepath.Join(t.TempDir(), "destination.sqlite")
	if _, err := copyFile(ctx, sourcePath, destinationPath); !errors.Is(err, context.Canceled) {
		t.Fatalf("copyFile() error = %v, want context cancellation", err)
	}
	if _, err := os.Stat(destinationPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled copy destination exists: %v", err)
	}
}

func TestCopyWithContextStopsWhenWriteCancelsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	destination := &cancelingWriter{cancel: cancel}
	source := bytes.NewReader(make([]byte, 256<<10))

	written, err := contextio.Copy(ctx, destination, source)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("contextio.Copy() error = %v, want context cancellation", err)
	}
	if written == 0 || written >= 256<<10 {
		t.Fatalf("canceled copy wrote %d bytes", written)
	}
	if destination.writes != 1 {
		t.Fatalf("destination writes = %d, want 1", destination.writes)
	}
}

func TestBackupOutputLockRejectsConcurrentPublication(t *testing.T) {
	db, sourcePath := openTestDatabase(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(t.TempDir(), "backup.sqlite")
	lock, err := acquireBackupOutputLock(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testBackup(context.Background(), sourcePath, outputPath); err == nil {
		lock.Close()
		t.Fatal("backup succeeded while its output lock was held")
	} else if !strings.Contains(err.Error(), "backup output is in use") {
		lock.Close()
		t.Fatalf("contention error = %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := testBackup(context.Background(), sourcePath, outputPath); err != nil {
		t.Fatalf("backup failed after output lock was released: %v", err)
	}
}

func TestBackupRejectsDatabaseAliases(t *testing.T) {
	ctx := context.Background()
	db, sourcePath := openTestDatabase(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	t.Run("relative path", func(t *testing.T) {
		resolvedSource, err := filepath.EvalSymlinks(sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		workingDirectory, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		relativePath, err := filepath.Rel(workingDirectory, resolvedSource)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := testBackup(ctx, sourcePath, relativePath); err == nil {
			t.Fatal("backup to a relative alias of the source succeeded")
		}
	})

	t.Run("symlinked parent", func(t *testing.T) {
		aliasRoot := t.TempDir()
		aliasDirectory := filepath.Join(aliasRoot, "database")
		if err := os.Symlink(filepath.Dir(sourcePath), aliasDirectory); err != nil {
			t.Fatal(err)
		}
		if _, err := testBackup(ctx, sourcePath, filepath.Join(aliasDirectory, filepath.Base(sourcePath))); err == nil {
			t.Fatal("backup through a symlinked parent of the source succeeded")
		}
	})

	t.Run("hard link", func(t *testing.T) {
		outputPath := filepath.Join(t.TempDir(), "backup.sqlite")
		if err := os.Link(sourcePath, outputPath); err != nil {
			t.Fatal(err)
		}
		if _, err := testBackup(ctx, sourcePath, outputPath); err == nil {
			t.Fatal("backup to a hard link of the source succeeded")
		}
	})
}

func TestBackupRejectsNonRegularOutputArtifacts(t *testing.T) {
	db, sourcePath := openTestDatabase(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		artifact   func(string) string
		outputMade bool
	}{
		{name: "database", artifact: func(output string) string { return output }, outputMade: true},
		{name: "checksum", artifact: func(output string) string { return output + ".sha256" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), "backup.sqlite")
			artifactPath := test.artifact(outputPath)
			if err := os.Mkdir(artifactPath, 0o750); err != nil {
				t.Fatal(err)
			}
			sentinelPath := filepath.Join(artifactPath, "keep")
			if err := os.WriteFile(sentinelPath, []byte("keep"), 0o640); err != nil {
				t.Fatal(err)
			}

			if _, err := testBackup(context.Background(), sourcePath, outputPath); err == nil {
				t.Fatalf("backup with a directory at the %s path succeeded", test.name)
			}
			assertTestFileContents(t, sentinelPath, []byte("keep"))
			if !test.outputMade {
				if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("database output exists after rejected publication: %v", err)
				}
			}
		})
	}
}

func TestBackupOutputNameCannotCollideWithRecoveryFiles(t *testing.T) {
	ctx := context.Background()
	db, sourcePath := openTestDatabase(t)
	if _, err := db.Exec(`INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at) VALUES ('最初', '最初', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(t.TempDir(), "previous.sqlite")
	if _, err := testBackup(ctx, sourcePath, outputPath); err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE vocabulary SET expression = '次', normalized_expression = '次'`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := testBackup(ctx, sourcePath, outputPath); err != nil {
		t.Fatal(err)
	}
	assertVocabularyExpression(t, outputPath, "次")
}

func TestRestoreReplacesDatabaseAndPreservesCurrentFile(t *testing.T) {
	ctx := context.Background()
	db, sourcePath := openTestDatabase(t)
	if _, err := db.Exec(`INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at) VALUES ('食べる', '食べる', 1, 1)`); err != nil {
		t.Fatalf("insert source vocabulary: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close source database: %v", err)
	}

	backupPath := filepath.Join(t.TempDir(), "backup.sqlite")
	if _, err := testBackup(ctx, sourcePath, backupPath); err != nil {
		t.Fatalf("create restore backup: %v", err)
	}
	current, err := Open(ctx, sourcePath)
	if err != nil {
		t.Fatalf("reopen source database: %v", err)
	}
	if _, err := current.Exec("DELETE FROM vocabulary"); err != nil {
		current.Close()
		t.Fatalf("change current database: %v", err)
	}
	if err := current.Close(); err != nil {
		t.Fatalf("close changed database: %v", err)
	}
	if err := testRestore(ctx, backupPath, sourcePath); err != nil {
		t.Fatalf("restore database: %v", err)
	}
	restored, err := Open(ctx, sourcePath)
	if err != nil {
		t.Fatalf("open restored database: %v", err)
	}
	defer restored.Close()
	var expression string
	if err := restored.QueryRow("SELECT expression FROM vocabulary LIMIT 1").Scan(&expression); err != nil {
		t.Fatalf("read restored vocabulary: %v", err)
	}
	if expression != "食べる" {
		t.Fatalf("restored expression = %q, want 食べる", expression)
	}
	if _, err := os.Stat(sourcePath + ".before-restore"); err != nil {
		t.Fatalf("preserved pre-restore database: %v", err)
	}
}

func TestRestoreRejectsRetiredSchemaWithoutReplacingDatabase(t *testing.T) {
	ctx := context.Background()
	current, currentPath := openTestDatabase(t)
	if _, err := current.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at)
		VALUES ('現在', '現在', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}

	backupPath := filepath.Join(t.TempDir(), "retired.sqlite")
	retired, err := sql.Open("sqlite3", backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retired.Exec("PRAGMA journal_mode = DELETE"); err != nil {
		retired.Close()
		t.Fatal(err)
	}
	if _, err := retired.Exec(`
		CREATE TABLE goose_db_version (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version_id INTEGER NOT NULL,
			is_applied INTEGER NOT NULL,
			tstamp TIMESTAMP DEFAULT (datetime('now'))
		);
		INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1), (1, 1);
		CREATE TABLE legacy_vocabulary (id INTEGER PRIMARY KEY, expression TEXT NOT NULL);
		INSERT INTO legacy_vocabulary (expression) VALUES ('過去');
	`); err != nil {
		retired.Close()
		t.Fatal(err)
	}
	if err := retired.Close(); err != nil {
		t.Fatal(err)
	}
	checksum, err := fileChecksum(ctx, backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		backupPath+".sha256",
		[]byte(checksum+"  "+filepath.Base(backupPath)+"\n"),
		0o640,
	); err != nil {
		t.Fatal(err)
	}

	err = testRestore(ctx, backupPath, currentPath)
	if err == nil || !errors.Is(err, ErrInvalidRestoreSource) || !strings.Contains(err.Error(), "is not empty") {
		t.Fatalf("testRestore() error = %v, want unidentified schema error", err)
	}
	assertVocabularyExpression(t, currentPath, "現在")
	if _, err := os.Stat(currentPath + ".before-restore"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected restore created a recovery database: %v", err)
	}
}

func TestBackupAndRestorePreserveMiningCaptures(t *testing.T) {
	ctx := context.Background()
	db, sourcePath := openTestDatabase(t)
	vocabularyResult, err := db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at)
		VALUES ('食べる', '食べる', 1, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	vocabularyID, err := vocabularyResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO mining_captures (
			raw_text, expression, normalized_expression, context_text, source_kind,
			source_title, source_url, capture_nonce, request_hash, revision, status,
			created_at
		) VALUES ('猫', '猫', '猫', '猫を見た', 'web', 'Reading', 'https://example.com/book', ?, ?, 2, 'pending', 2)`,
		strings.Repeat("1", 32), strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO mining_captures (
			raw_text, expression, normalized_expression, context_text, source_kind,
			capture_nonce, request_hash, revision, status, vocabulary_id,
			created_at
		) VALUES ('食べる', '食べる', '食べる', '昼ご飯を食べる', 'manual', ?, ?, 2, 'accepted', ?, 4)`,
		strings.Repeat("2", 32), strings.Repeat("b", 64), vocabularyID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	backupPath := filepath.Join(t.TempDir(), "backup.sqlite")
	if _, err := testBackup(ctx, sourcePath, backupPath); err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(t.TempDir(), "restored.sqlite")
	if err := testRestore(ctx, backupPath, restoredPath); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()

	rows, err := restored.Query(`
		SELECT expression, context_text, status, vocabulary_id
		FROM mining_captures ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type restoredCapture struct {
		expression   string
		context      string
		status       string
		vocabularyID sql.NullInt64
	}
	var captures []restoredCapture
	for rows.Next() {
		var capture restoredCapture
		if err := rows.Scan(&capture.expression, &capture.context, &capture.status, &capture.vocabularyID); err != nil {
			t.Fatal(err)
		}
		captures = append(captures, capture)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(captures) != 2 {
		t.Fatalf("restored capture count = %d, want 2", len(captures))
	}
	if captures[0].expression != "猫" || captures[0].context != "猫を見た" || captures[0].status != "pending" || captures[0].vocabularyID.Valid {
		t.Fatalf("restored pending capture = %#v", captures[0])
	}
	if captures[1].expression != "食べる" || captures[1].context != "昼ご飯を食べる" || captures[1].status != "accepted" || captures[1].vocabularyID.Int64 != vocabularyID {
		t.Fatalf("restored accepted capture = %#v", captures[1])
	}
}

func TestRestoreDoesNotModifySourceBackup(t *testing.T) {
	ctx := context.Background()
	db, sourcePath := openTestDatabase(t)
	if _, err := db.Exec(`INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at) VALUES ('安全', '安全', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	backupPath := filepath.Join(t.TempDir(), "backup.sqlite")
	if _, err := testBackup(ctx, sourcePath, backupPath); err != nil {
		t.Fatal(err)
	}
	rawDB, err := sql.Open("sqlite3", backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.ExecContext(ctx, "PRAGMA journal_mode = DELETE"); err != nil {
		rawDB.Close()
		t.Fatal(err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatal(err)
	}
	checksum, err := fileChecksum(t.Context(), backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath+".sha256", []byte(checksum+"  backup.sqlite\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(backupPath, 0o440); err != nil {
		t.Fatal(err)
	}
	beforeChecksum, err := fileChecksum(t.Context(), backupPath)
	if err != nil {
		t.Fatal(err)
	}

	restoredPath := filepath.Join(t.TempDir(), "restored.sqlite")
	if err := testRestore(ctx, backupPath, restoredPath); err != nil {
		t.Fatal(err)
	}
	afterChecksum, err := fileChecksum(t.Context(), backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if afterChecksum != beforeChecksum {
		t.Fatal("restore modified its source backup")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(backupPath + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("restore created a source sidecar %q: %v", suffix, err)
		}
	}
	assertVocabularyExpression(t, restoredPath, "安全")
}

func TestRestoreRequiresChecksum(t *testing.T) {
	ctx := context.Background()
	db, sourcePath := openTestDatabase(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "backup.sqlite")
	if _, err := testBackup(ctx, sourcePath, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(backupPath + ".sha256"); err != nil {
		t.Fatal(err)
	}

	restoredPath := filepath.Join(t.TempDir(), "restored.sqlite")
	if err := testRestore(ctx, backupPath, restoredPath); err == nil || !strings.Contains(err.Error(), "checksum is missing") {
		t.Fatalf("testRestore() error = %v, want missing checksum", err)
	}
	if _, err := os.Stat(restoredPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore destination exists after checksum failure: %v", err)
	}
}

func TestRestoreUnderLockRejectsAnUnrelatedOrClosedLock(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "vocab.sqlite")
	lock, err := AcquireExclusiveFileLock(filepath.Join(directory, "other.lock"))
	if err != nil {
		t.Fatal(err)
	}
	err = RestoreWithImportsUnderLock(
		t.Context(),
		filepath.Join(directory, "backup.sqlite"),
		databasePath,
		filepath.Join(directory, "imports"),
		lock,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "held exclusive database lock") {
		lock.Close()
		t.Fatalf("RestoreWithImportsUnderLock() error = %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	lock, err = AcquireLock(databasePath, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	err = RestoreWithImportsUnderLock(
		t.Context(),
		filepath.Join(directory, "backup.sqlite"),
		databasePath,
		filepath.Join(directory, "imports"),
		lock,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "held exclusive database lock") {
		t.Fatalf("RestoreWithImportsUnderLock() accepted a closed lock: %v", err)
	}
}

func TestVerifyChecksumRejectsOversizedMetadata(t *testing.T) {
	checksumPath := filepath.Join(t.TempDir(), "backup.sqlite.sha256")
	if err := os.WriteFile(checksumPath, bytes.Repeat([]byte("a"), int(maxChecksumFileBytes)+1), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(strings.Repeat("a", 64), checksumPath); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("verifyChecksum() error = %v, want metadata size limit", err)
	}
}

func TestRestoreRejectsDatabaseAlias(t *testing.T) {
	ctx := context.Background()
	db, sourcePath := openTestDatabase(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "backup.sqlite")
	if _, err := testBackup(ctx, sourcePath, backupPath); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(t.TempDir(), "vocab.sqlite")
	if err := os.Link(backupPath, databasePath); err != nil {
		t.Fatal(err)
	}
	if err := testRestore(ctx, backupPath, databasePath); err == nil {
		t.Fatal("restore to a hard link of the source succeeded")
	}
}

func TestRestoreRejectsBackupArtifactDestinations(t *testing.T) {
	ctx := context.Background()
	db, sourcePath := openTestDatabase(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "backup.sqlite")
	if _, err := testBackup(ctx, sourcePath, backupPath); err != nil {
		t.Fatal(err)
	}
	beforeDatabase := mustReadTestFile(t, backupPath)
	beforeChecksum := mustReadTestFile(t, backupPath+".sha256")

	tests := []struct {
		name        string
		destination string
	}{
		{name: "checksum", destination: backupPath + ".sha256"},
		{name: "output lock", destination: backupPath + ".backup.lock"},
		{name: "pending import bundle", destination: filepath.Join(backupPath+".imports", "manifest.json")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := testRestore(ctx, backupPath, test.destination); err == nil {
				t.Fatal("restore to a backup artifact succeeded")
			}
			assertTestFileContents(t, backupPath, beforeDatabase)
			assertTestFileContents(t, backupPath+".sha256", beforeChecksum)
		})
	}
	if _, err := os.Stat(backupPath + ".imports"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected restore created a pending import directory: %v", err)
	}
}

func TestRestoreRejectsOrphanDestinationSidecars(t *testing.T) {
	ctx := context.Background()
	db, sourcePath := openTestDatabase(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "backup.sqlite")
	if _, err := testBackup(ctx, sourcePath, backupPath); err != nil {
		t.Fatal(err)
	}

	for _, suffix := range []string{"-wal", "-shm"} {
		t.Run(suffix, func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "vocab.sqlite")
			sidecarPath := databasePath + suffix
			if err := os.WriteFile(sidecarPath, []byte("orphan"), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := testRestore(ctx, backupPath, databasePath); err == nil {
				t.Fatal("restore with an orphan destination sidecar succeeded")
			}
			if _, err := os.Stat(databasePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("restore destination exists after rejected restore: %v", err)
			}
			assertTestFileContents(t, sidecarPath, []byte("orphan"))
		})
	}
}

func TestPublishRestoreRejectsSidecarCreatedAfterValidation(t *testing.T) {
	directory := t.TempDir()
	workspace := filepath.Join(directory, "workspace")
	if err := os.Mkdir(workspace, 0o750); err != nil {
		t.Fatal(err)
	}
	stagedPath := filepath.Join(workspace, "vocab.sqlite")
	if err := os.WriteFile(stagedPath, []byte("restored"), 0o640); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(directory, "vocab.sqlite")
	if err := os.WriteFile(databasePath+"-wal", []byte("orphan"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := publishRestoredDatabase(stagedPath, databasePath, workspace, false); err == nil {
		t.Fatal("published a restored database beside a newly created orphan sidecar")
	}
	assertTestFileContents(t, stagedPath, []byte("restored"))
	assertTestFileContents(t, databasePath+"-wal", []byte("orphan"))
}

func TestRestoreRejectsSourceAtRecoveryPath(t *testing.T) {
	for _, suffix := range []string{".before-restore", "-wal", ".before-restore-shm"} {
		t.Run(suffix, func(t *testing.T) {
			ctx := context.Background()
			db, sourcePath := openTestDatabase(t)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			databasePath := filepath.Join(directory, "vocab.sqlite")
			backupPath := databasePath + suffix
			if _, err := testBackup(ctx, sourcePath, backupPath); err != nil {
				t.Fatal(err)
			}
			beforeChecksum, err := fileChecksum(t.Context(), backupPath)
			if err != nil {
				t.Fatal(err)
			}

			if err := testRestore(ctx, backupPath, databasePath); err == nil {
				t.Fatal("restore from a recovery artifact path succeeded")
			}
			afterChecksum, err := fileChecksum(t.Context(), backupPath)
			if err != nil {
				t.Fatal(err)
			}
			if afterChecksum != beforeChecksum {
				t.Fatal("rejected restore changed its source backup")
			}
		})
	}
}

func TestRestoreDatabaseNameCannotCollideWithRecoveryFiles(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "older-0")
	previousPath := databasePath + ".before-restore"
	if err := os.WriteFile(databasePath, []byte("current"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(previousPath, []byte("previous"), 0o640); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(directory, ".restore")
	stagingDirectory := filepath.Join(workspace, "staged")
	if err := os.MkdirAll(stagingDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	stagedPath := filepath.Join(stagingDirectory, "older-0")
	if err := os.WriteFile(stagedPath, []byte("restored"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := publishRestoredDatabase(stagedPath, databasePath, workspace, false); err != nil {
		t.Fatal(err)
	}
	assertTestFileContents(t, databasePath, []byte("restored"))
	assertTestFileContents(t, previousPath, []byte("current"))
}

func TestBackupAndRestorePreservePendingImports(t *testing.T) {
	ctx := context.Background()
	db, sourcePath := openTestDatabase(t)
	stagingPath := filepath.Join(filepath.Dir(sourcePath), "imports")
	if err := os.MkdirAll(stagingPath, 0o750); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(stagingPath, "run-1.apkg")
	if err := os.WriteFile(archivePath, []byte("pending"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO import_runs (id, filename, archive_path, status, created_at) VALUES (1, 'deck.apkg', ?, 'failed', 1)`, archivePath); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	backupPath := filepath.Join(t.TempDir(), "backup.sqlite")
	if _, err := BackupWithImports(ctx, sourcePath, backupPath); err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(t.TempDir(), "restored", "vocab.sqlite")
	restoredStaging := filepath.Join(filepath.Dir(restoredPath), "imports")
	if err := RestoreWithImports(ctx, backupPath, restoredPath, restoredStaging); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var restoredArchive string
	if err := restored.QueryRow("SELECT archive_path FROM import_runs WHERE id = 1").Scan(&restoredArchive); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(restoredArchive) != restoredStaging || !strings.HasPrefix(filepath.Base(restoredArchive), "run-1-restored-") {
		t.Fatalf("restored archive path = %q", restoredArchive)
	}
	contents, err := os.ReadFile(restoredArchive)
	if err != nil {
		t.Fatalf("read restored pending archive: %v", err)
	}
	if string(contents) != "pending" {
		t.Fatalf("restored pending archive = %q", contents)
	}
}

func TestFailedBackupKeepsPreviousArtifactSet(t *testing.T) {
	ctx := context.Background()
	db, sourcePath := openTestDatabase(t)
	archivePath := filepath.Join(t.TempDir(), "run-1.apkg")
	if err := os.WriteFile(archivePath, []byte("first archive"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO import_runs (id, filename, archive_path, status, created_at) VALUES (1, 'deck.apkg', ?, 'previewed', 1)`, archivePath); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(t.TempDir(), "backup.sqlite")
	if _, err := BackupWithImports(ctx, sourcePath, outputPath); err != nil {
		t.Fatal(err)
	}
	previousDatabase := mustReadTestFile(t, outputPath)
	previousChecksum := mustReadTestFile(t, outputPath+".sha256")
	previousManifest := mustReadTestFile(t, filepath.Join(outputPath+".imports", "manifest.json"))
	previousArchive := mustReadTestFile(t, filepath.Join(outputPath+".imports", "run-1.apkg"))

	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}
	if _, err := BackupWithImports(ctx, sourcePath, outputPath); err == nil {
		t.Fatal("backup with missing pending archive succeeded")
	}
	assertTestFileContents(t, outputPath, previousDatabase)
	assertTestFileContents(t, outputPath+".sha256", previousChecksum)
	assertTestFileContents(t, filepath.Join(outputPath+".imports", "manifest.json"), previousManifest)
	assertTestFileContents(t, filepath.Join(outputPath+".imports", "run-1.apkg"), previousArchive)
}

func TestPublishBackupKeepsPreviousSetWhenChecksumIsMissing(t *testing.T) {
	directory := t.TempDir()
	outputPath := filepath.Join(directory, "backup.sqlite")
	if err := os.WriteFile(outputPath, []byte("old database"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath+".sha256", []byte("old checksum"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outputPath+".imports", 0o750); err != nil {
		t.Fatal(err)
	}
	oldArchivePath := filepath.Join(outputPath+".imports", "run-1.apkg")
	if err := os.WriteFile(oldArchivePath, []byte("old archive"), 0o640); err != nil {
		t.Fatal(err)
	}

	workspace := filepath.Join(directory, "workspace")
	if err := os.Mkdir(workspace, 0o750); err != nil {
		t.Fatal(err)
	}
	stagedPath := filepath.Join(workspace, "backup.sqlite")
	if err := os.WriteFile(stagedPath, []byte("new database"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := publishBackup(stagedPath, outputPath, true); err == nil {
		t.Fatal("published backup without a staged checksum")
	}
	assertTestFileContents(t, outputPath, []byte("old database"))
	assertTestFileContents(t, outputPath+".sha256", []byte("old checksum"))
	assertTestFileContents(t, oldArchivePath, []byte("old archive"))
}

func TestRestoreStagesPendingImportsBeforeReplacingDatabase(t *testing.T) {
	ctx := context.Background()
	backupDB, backupSourcePath := openTestDatabase(t)
	archivePath := filepath.Join(t.TempDir(), "run-1.apkg")
	if err := os.WriteFile(archivePath, []byte("pending"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := backupDB.Exec(`INSERT INTO import_runs (id, filename, archive_path, status, created_at) VALUES (1, 'deck.apkg', ?, 'previewed', 1)`, archivePath); err != nil {
		t.Fatal(err)
	}
	if err := backupDB.Close(); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "backup.sqlite")
	if _, err := BackupWithImports(ctx, backupSourcePath, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(backupPath+".imports", "run-1.apkg")); err != nil {
		t.Fatal(err)
	}

	currentDB, currentPath := openTestDatabase(t)
	if _, err := currentDB.Exec(`INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at) VALUES ('現在', '現在', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := currentDB.Close(); err != nil {
		t.Fatal(err)
	}
	stagingPath := filepath.Join(filepath.Dir(currentPath), "imports")
	if err := RestoreWithImports(ctx, backupPath, currentPath, stagingPath); !errors.Is(err, ErrInvalidRestoreSource) {
		t.Fatalf("restore with a missing pending archive error = %v, want invalid source", err)
	}

	currentDB, err := Open(ctx, currentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer currentDB.Close()
	var expression string
	if err := currentDB.QueryRow("SELECT expression FROM vocabulary").Scan(&expression); err != nil {
		t.Fatal(err)
	}
	if expression != "現在" {
		t.Fatalf("current database was replaced after failed staging: %q", expression)
	}
	if _, err := os.Stat(currentPath + ".before-restore"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-restore database created after failed staging: %v", err)
	}
}

func TestRestoreRejectsChangedPendingImport(t *testing.T) {
	ctx := context.Background()
	backupDB, backupSourcePath := openTestDatabase(t)
	archivePath := filepath.Join(t.TempDir(), "run-1.apkg")
	if err := os.WriteFile(archivePath, []byte("pending"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := backupDB.Exec(`INSERT INTO import_runs (id, filename, archive_path, status, created_at) VALUES (1, 'deck.apkg', ?, 'previewed', 1)`, archivePath); err != nil {
		t.Fatal(err)
	}
	if err := backupDB.Close(); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "backup.sqlite")
	if _, err := BackupWithImports(ctx, backupSourcePath, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupPath+".imports", "run-1.apkg"), []byte("changed"), 0o640); err != nil {
		t.Fatal(err)
	}

	currentDB, currentPath := openTestDatabase(t)
	if _, err := currentDB.Exec(`INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at) VALUES ('現在', '現在', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := currentDB.Close(); err != nil {
		t.Fatal(err)
	}
	err := RestoreWithImports(ctx, backupPath, currentPath, filepath.Join(filepath.Dir(currentPath), "imports"))
	if err == nil || !errors.Is(err, ErrInvalidRestoreSource) || !strings.Contains(err.Error(), "checksum does not match") {
		t.Fatalf("RestoreWithImports() error = %v, want checksum mismatch", err)
	}
	assertVocabularyExpression(t, currentPath, "現在")
}

func TestPendingImportManifestRequiresChecksum(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(directory, "manifest.json"),
		[]byte(`[{"id":1,"file":"run-1.apkg"}]`),
		0o640,
	); err != nil {
		t.Fatal(err)
	}

	_, err := readPendingImportManifest(directory)
	if err == nil || !strings.Contains(err.Error(), "missing pending import checksum") {
		t.Fatalf("readPendingImportManifest error = %v, want missing checksum", err)
	}
}

func TestPendingImportManifestRejectsOversizedMetadata(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(directory, "manifest.json"),
		bytes.Repeat([]byte(" "), int(maxPendingImportManifestBytes)+1),
		0o640,
	); err != nil {
		t.Fatal(err)
	}

	_, err := readPendingImportManifest(directory)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("readPendingImportManifest error = %v, want metadata size limit", err)
	}
}

func TestCorruptImportManifestDoesNotReplaceDatabase(t *testing.T) {
	ctx := context.Background()
	backupDB, backupSourcePath := openTestDatabase(t)
	archivePath := filepath.Join(t.TempDir(), "run-1.apkg")
	if err := os.WriteFile(archivePath, []byte("pending"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := backupDB.Exec(`INSERT INTO import_runs (id, filename, archive_path, status, created_at) VALUES (1, 'deck.apkg', ?, 'previewed', 1)`, archivePath); err != nil {
		t.Fatal(err)
	}
	if err := backupDB.Close(); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "backup.sqlite")
	if _, err := BackupWithImports(ctx, backupSourcePath, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupPath+".imports", "manifest.json"), []byte("{"), 0o640); err != nil {
		t.Fatal(err)
	}

	currentDB, currentPath := openTestDatabase(t)
	if _, err := currentDB.Exec(`INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at) VALUES ('現在', '現在', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := currentDB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RestoreWithImports(ctx, backupPath, currentPath, filepath.Join(filepath.Dir(currentPath), "imports")); !errors.Is(err, ErrInvalidRestoreSource) {
		t.Fatalf("restore with a corrupt pending import manifest error = %v, want invalid source", err)
	}
	assertVocabularyExpression(t, currentPath, "現在")
	if _, err := os.Stat(currentPath + ".before-restore"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-restore database created after manifest validation failed: %v", err)
	}
}

func TestPruneBackupsLeavesUnrelatedDatabasesAlone(t *testing.T) {
	directory := t.TempDir()
	livePath := filepath.Join(directory, "vocab.sqlite")
	unrelatedPath := filepath.Join(directory, "notes.sqlite")
	oldBackup := filepath.Join(directory, "backup-old.sqlite")
	newBackup := filepath.Join(directory, "backup-new.sqlite")
	for _, path := range []string{livePath, oldBackup, newBackup} {
		createPruneTestDatabase(t, path, true)
	}
	createPruneTestDatabase(t, unrelatedPath, false)
	for _, path := range []string{unrelatedPath, oldBackup, newBackup} {
		if err := os.WriteFile(path+".sha256", []byte("checksum"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(oldBackup, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	if err := PruneBackups(directory, 1, livePath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("live database was removed: %v", err)
	}
	if _, err := os.Stat(unrelatedPath); err != nil {
		t.Fatalf("unrelated database was removed: %v", err)
	}
	if _, err := os.Stat(unrelatedPath + ".sha256"); err != nil {
		t.Fatalf("unrelated database checksum was removed: %v", err)
	}
	if _, err := os.Stat(oldBackup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old backup still exists or could not be checked: %v", err)
	}
	if _, err := os.Stat(newBackup); err != nil {
		t.Fatalf("new backup was removed: %v", err)
	}
}

func TestPruneBackupsProtectsDatabaseSymlinkTarget(t *testing.T) {
	directory := t.TempDir()
	livePath := filepath.Join(directory, "vocab.sqlite")
	liveAlias := filepath.Join(directory, "current-database")
	newBackup := filepath.Join(directory, "backup-new.sqlite")
	for _, path := range []string{livePath, newBackup} {
		createPruneTestDatabase(t, path, true)
		if err := os.WriteFile(path+".sha256", []byte("checksum"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(livePath, liveAlias); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(livePath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	if err := PruneBackups(directory, 1, liveAlias); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("protected database symlink target was removed: %v", err)
	}
	if _, err := os.Stat(newBackup); err != nil {
		t.Fatalf("new backup was removed: %v", err)
	}
}

func TestPruneBackupsSkipsAnOutputBeingPublished(t *testing.T) {
	directory := t.TempDir()
	livePath := filepath.Join(directory, "vocab.sqlite")
	oldBackup := filepath.Join(directory, "backup-old.sqlite")
	newBackup := filepath.Join(directory, "backup-new.sqlite")
	for _, path := range []string{livePath, oldBackup, newBackup} {
		createPruneTestDatabase(t, path, true)
		if path != livePath {
			if err := os.WriteFile(path+".sha256", []byte("checksum"), 0o640); err != nil {
				t.Fatal(err)
			}
		}
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(oldBackup, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	publicationLock, err := acquireBackupOutputLock(oldBackup)
	if err != nil {
		t.Fatal(err)
	}
	if err := PruneBackups(directory, 1, livePath); err != nil {
		publicationLock.Close()
		t.Fatal(err)
	}
	if _, err := os.Stat(oldBackup); err != nil {
		publicationLock.Close()
		t.Fatalf("locked backup was removed: %v", err)
	}
	if err := publicationLock.Close(); err != nil {
		t.Fatal(err)
	}

	if err := PruneBackups(directory, 1, livePath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldBackup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unlocked old backup still exists or could not be checked: %v", err)
	}
}

func createPruneTestDatabase(t *testing.T, path string, goi bool) {
	t.Helper()
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if goi {
		err = Migrate(t.Context(), db)
	} else {
		_, err = db.ExecContext(t.Context(), "PRAGMA application_id = 42")
	}
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

type cancelingWriter struct {
	cancel context.CancelFunc
	writes int
}

func (writer *cancelingWriter) Write(buffer []byte) (int, error) {
	writer.writes++
	if writer.writes > 1 {
		return 0, errors.New("write after context cancellation")
	}
	writer.cancel()
	return len(buffer), nil
}

func mustReadTestFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return contents
}

func assertTestFileContents(t *testing.T, path string, expected []byte) {
	t.Helper()
	actual := mustReadTestFile(t, path)
	if !bytes.Equal(actual, expected) {
		t.Fatalf("%s changed after failed operation", path)
	}
}

func assertTestFileMode(t *testing.T, path string, expected os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if actual := info.Mode().Perm(); actual != expected {
		t.Fatalf("%s mode = %04o, want %04o", path, actual, expected)
	}
}

func assertVocabularyExpression(t *testing.T, databasePath, expected string) {
	t.Helper()
	db, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var expression string
	if err := db.QueryRow("SELECT expression FROM vocabulary").Scan(&expression); err != nil {
		t.Fatal(err)
	}
	if expression != expected {
		t.Fatalf("vocabulary expression = %q, want %q", expression, expected)
	}
}

func openTestDatabase(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vocab.sqlite")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		db.Close()
		t.Fatalf("migrate test database: %v", err)
	}
	return db, path
}
