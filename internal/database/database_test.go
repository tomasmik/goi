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
)

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
	if _, err := BackupWithImports(ctx, sourcePath, outputPath); err == nil || !strings.Contains(err.Error(), "validate source database") {
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

func TestBackupProducesVerifiedSnapshot(t *testing.T) {
	db, sourcePath := openTestDatabase(t)
	if _, err := db.Exec(`INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at) VALUES ('食べる', '食べる', 1, 1)`); err != nil {
		t.Fatalf("insert test vocabulary: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close source database: %v", err)
	}

	outputPath := filepath.Join(t.TempDir(), "backup.sqlite")
	checksum, err := BackupWithImports(context.Background(), sourcePath, outputPath)
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
	if _, err := BackupWithImports(ctx, sourcePath, backupPath); err != nil {
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
	if err := RestoreWithImports(ctx, backupPath, sourcePath, t.TempDir()); err != nil {
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

	err = RestoreWithImports(ctx, backupPath, currentPath, t.TempDir())
	if err == nil || !errors.Is(err, ErrInvalidRestoreSource) || !strings.Contains(err.Error(), "is not empty") {
		t.Fatalf("RestoreWithImports() error = %v, want unidentified schema error", err)
	}
	assertVocabularyExpression(t, currentPath, "現在")
	if _, err := os.Stat(currentPath + ".before-restore"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected restore created a recovery database: %v", err)
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
	if _, err := BackupWithImports(ctx, sourcePath, backupPath); err != nil {
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
	if err := RestoreWithImports(ctx, backupPath, restoredPath, t.TempDir()); err != nil {
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
	if _, err := BackupWithImports(ctx, sourcePath, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(backupPath + ".sha256"); err != nil {
		t.Fatal(err)
	}

	restoredPath := filepath.Join(t.TempDir(), "restored.sqlite")
	if err := RestoreWithImports(ctx, backupPath, restoredPath, t.TempDir()); err == nil || !strings.Contains(err.Error(), "checksum is missing") {
		t.Fatalf("RestoreWithImports() error = %v, want missing checksum", err)
	}
	if _, err := os.Stat(restoredPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore destination exists after checksum failure: %v", err)
	}
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
