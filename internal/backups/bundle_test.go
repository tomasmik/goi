package backups

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tomasmik/goi/internal/database"
)

func TestBundleRoundTripPreservesDatabaseAndPendingImports(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	if _, err := db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at)
		VALUES ('食べる', '食べる', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	importsDir := filepath.Join(dataDir, "imports")
	if err := os.MkdirAll(importsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(importsDir, "run-1.apkg")
	if err := os.WriteFile(archivePath, []byte("pending import"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO import_runs (id, filename, archive_path, status, created_at)
		VALUES (1, 'deck.apkg', ?, 'previewed', 1)`, archivePath); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{restoreQueueLockName, restoreReceiptName, installationIDFile, "google-drive.json"} {
		if err := os.WriteFile(filepath.Join(dataDir, name), []byte("operational state"), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	bundlePath := filepath.Join(t.TempDir(), "goi-test"+BundleSuffix)
	createdAt := time.Date(2026, 8, 5, 9, 30, 0, 0, time.UTC)
	if err := CreateBundle(ctx, databasePath, bundlePath, createdAt); err != nil {
		t.Fatal(err)
	}
	bundleInfo, err := os.Stat(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bundleInfo.ModTime().Equal(createdAt) {
		t.Fatalf("bundle modification time = %v, want %v", bundleInfo.ModTime(), createdAt)
	}
	if err := ValidateBundle(ctx, bundlePath); err != nil {
		t.Fatalf("ValidateBundle() error = %v", err)
	}
	archive, err := zip.OpenReader(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, entry := range archive.File {
		for _, excluded := range []string{restoreQueueLockName, restoreReceiptName, installationIDFile, "google-drive.json"} {
			if entry.Name == excluded {
				t.Fatalf("backup bundle contains operational file %q", excluded)
			}
		}
	}

	restoredDir := t.TempDir()
	restoredPath := filepath.Join(restoredDir, "vocab.sqlite")
	if err := RestoreBundle(ctx, bundlePath, restoredPath, filepath.Join(restoredDir, "imports")); err != nil {
		t.Fatal(err)
	}
	restored, err := database.Open(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var expression, restoredArchive string
	if err := restored.QueryRow("SELECT expression FROM vocabulary").Scan(&expression); err != nil {
		t.Fatal(err)
	}
	if err := restored.QueryRow("SELECT archive_path FROM import_runs WHERE id = 1").Scan(&restoredArchive); err != nil {
		t.Fatal(err)
	}
	if expression != "食べる" {
		t.Fatalf("restored expression = %q", expression)
	}
	contents, err := os.ReadFile(restoredArchive)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "pending import" {
		t.Fatalf("restored pending import = %q", contents)
	}
}

func TestBundleRemovesTransientStateWithoutChangingSource(t *testing.T) {
	ctx := t.Context()
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	defer db.Close()
	store := NewStore(db)
	previousSuccess := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	if err := store.Begin(ctx, "manual", previousSuccess.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.Succeed(ctx, previousSuccess, "goi-previous"+BundleSuffix, "previous-remote"); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimScheduledDate(ctx, "2026-08-04"); err != nil || !claimed {
		t.Fatalf("ClaimScheduledDate() = %v, %v", claimed, err)
	}
	startedAt := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	if err := store.Begin(ctx, "manual", startedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO web_sessions (token, data, expiry_at) VALUES ('session', X'01', 9999999999);
		INSERT INTO extension_tokens (name, token_hash, token_prefix, created_at)
		VALUES ('Browser', zeroblob(32), 'goi_ext_v1_12345678', 1);
		INSERT INTO mining_captures (
			raw_text, expression, normalized_expression, source_kind,
			capture_nonce, request_hash, created_at
		) VALUES (
			'言葉', '言葉', '言葉', 'manual',
			'11111111111111111111111111111111',
			'1111111111111111111111111111111111111111111111111111111111111111', 1
		)`); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(dataDir, "goi-state"+BundleSuffix)
	if err := CreateBundle(ctx, databasePath, bundlePath, startedAt); err != nil {
		t.Fatal(err)
	}
	sourceState, err := store.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sourceState.Status != "running" || !sourceState.LastAttemptAt.Equal(startedAt) {
		t.Fatalf("source backup state = %+v", sourceState)
	}

	restoredPath := filepath.Join(t.TempDir(), "vocab.sqlite")
	if err := RestoreBundle(ctx, bundlePath, restoredPath, filepath.Join(filepath.Dir(restoredPath), "imports")); err != nil {
		t.Fatal(err)
	}
	restoredDB, err := database.Open(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredDB.Close()
	restoredStore := NewStore(restoredDB)
	restoredState, err := restoredStore.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if restoredState.Status != "idle" || restoredState.Trigger != "" || !restoredState.LastAttemptAt.IsZero() ||
		!restoredState.LastSuccessAt.IsZero() || restoredState.LastScheduledDate != "" ||
		restoredState.LocalName != "" || restoredState.RemoteID != "" || restoredState.ErrorMessage != "" {
		t.Fatalf("restored operational state = %+v", restoredState)
	}
	for _, table := range []string{"web_sessions", "extension_tokens"} {
		var count int
		if err := restoredDB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("restored %s count = %d, want 0", table, count)
		}
	}
	for _, table := range []string{"web_sessions", "extension_tokens"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("source %s count = %d, want 1", table, count)
		}
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".validate-restore-") {
			t.Fatalf("validation workspace was not removed: %s", entry.Name())
		}
	}
	if err := restoredStore.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	recoveredState, err := restoredStore.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredState.Status != "idle" || recoveredState.ErrorMessage != "" {
		t.Fatalf("startup recovery changed clean restored state = %+v", recoveredState)
	}
}

func TestValidateBundleRejectsUnexpectedAndDuplicateFiles(t *testing.T) {
	for _, test := range []struct {
		name    string
		entries []string
		want    string
	}{
		{name: "path traversal", entries: []string{"manifest.json", "vocab.sqlite", "vocab.sqlite.sha256", "../secret"}, want: "unexpected file"},
		{name: "duplicate", entries: []string{"manifest.json", "vocab.sqlite", "vocab.sqlite.sha256", "vocab.sqlite"}, want: "duplicate file"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid"+BundleSuffix)
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			archive := zip.NewWriter(file)
			for _, name := range test.entries {
				header := &zip.FileHeader{Name: name, Method: zip.Store}
				header.SetMode(0o640)
				writer, err := archive.CreateHeader(header)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = writer.Write([]byte("data"))
			}
			if err := errors.Join(archive.Close(), file.Close()); err != nil {
				t.Fatal(err)
			}
			err = ValidateBundle(context.Background(), path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateBundle() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestBundleSourceSizeMatchesRestoreLimit(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "vocab.sqlite")
	if err := os.WriteFile(databasePath, []byte("database"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(databasePath+".sha256", []byte("checksum"), 0o640); err != nil {
		t.Fatal(err)
	}
	importsPath := databasePath + ".imports"
	if err := os.Mkdir(importsPath, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(importsPath, "run.apkg"), []byte("import"), 0o640); err != nil {
		t.Fatal(err)
	}
	const total = int64(len("manifest") + len("database") + len("checksum") + len("import"))
	if err := ensureBundleSourceFits(databasePath, int64(len("manifest")), total); err != nil {
		t.Fatalf("exact-size bundle rejected: %v", err)
	}
	if err := ensureBundleSourceFits(databasePath, int64(len("manifest")), total-1); err == nil {
		t.Fatal("oversized bundle source was accepted")
	}
}

func TestQueueAndApplyRestoreWaitsForRestart(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	if _, err := db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at)
		VALUES ('以前', '以前', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "goi-restore"+BundleSuffix)
	if err := CreateBundle(ctx, databasePath, bundlePath, time.Now()); err != nil {
		t.Fatal(err)
	}

	db, err := database.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE vocabulary SET expression = '現在', normalized_expression = '現在'"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	bundle, err := os.Open(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := QueueRestore(ctx, dataDir, bundle); err != nil {
		bundle.Close()
		t.Fatal(err)
	}
	bundle.Close()
	assertBackupExpression(t, databasePath, "現在")

	restored, err := ApplyPendingRestore(ctx, dataDir, databasePath, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !restored {
		t.Fatal("ApplyPendingRestore() did not apply queued restore")
	}
	assertBackupExpression(t, databasePath, "以前")
	assertBackupExpression(t, databasePath+".before-restore", "現在")
	status, err := ReadRestoreStatus(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "success" {
		t.Fatalf("restore status = %+v", status)
	}
}

func TestApplyPendingRestoreRetriesAfterDatabaseLockContention(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	if _, err := db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at)
		VALUES ('以前', '以前', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "goi-retry"+BundleSuffix)
	if err := CreateBundle(ctx, databasePath, bundlePath, time.Now()); err != nil {
		t.Fatal(err)
	}

	db, err := database.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE vocabulary SET expression = '現在', normalized_expression = '現在'"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	bundle, err := os.Open(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := QueueRestore(ctx, dataDir, bundle); err != nil {
		bundle.Close()
		t.Fatal(err)
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}

	lock, err := database.AcquireLock(databasePath, false)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := ApplyPendingRestore(ctx, dataDir, databasePath, time.Now())
	if !errors.Is(err, database.ErrDatabaseInUse) {
		lock.Close()
		t.Fatalf("ApplyPendingRestore() error = %v, want database contention", err)
	}
	if restored {
		lock.Close()
		t.Fatal("ApplyPendingRestore() reported a restore while the database was locked")
	}
	status, statusErr := ReadRestoreStatus(dataDir)
	if statusErr != nil {
		lock.Close()
		t.Fatal(statusErr)
	}
	if status.State != "pending" {
		lock.Close()
		t.Fatalf("restore status = %+v, want pending", status)
	}
	failed, globErr := filepath.Glob(filepath.Join(dataDir, "failed-restore-*"+BundleSuffix))
	if globErr != nil {
		lock.Close()
		t.Fatal(globErr)
	}
	if len(failed) != 0 {
		lock.Close()
		t.Fatalf("lock contention quarantined the pending bundle: %v", failed)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	restored, err = ApplyPendingRestore(ctx, dataDir, databasePath, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !restored {
		t.Fatal("ApplyPendingRestore() did not retry the pending bundle")
	}
	assertBackupExpression(t, databasePath, "以前")
}

func TestApplyPendingRestorePreservesCanceledBundleForRetry(t *testing.T) {
	dataDir, databasePath := queueRestoreFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	restored, err := ApplyPendingRestore(ctx, dataDir, databasePath, time.Now())
	if restored || !errors.Is(err, ErrPendingRestoreRetry) || !errors.Is(err, context.Canceled) {
		t.Fatalf("ApplyPendingRestore() = %t, %v, want retryable cancellation", restored, err)
	}
	status, statusErr := ReadRestoreStatus(dataDir)
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	if status.State != "pending" {
		t.Fatalf("restore status = %+v, want pending", status)
	}
	assertBackupExpression(t, databasePath, "現在")

	restored, err = ApplyPendingRestore(context.Background(), dataDir, databasePath, time.Now())
	if err != nil || !restored {
		t.Fatalf("retry ApplyPendingRestore() = %t, %v", restored, err)
	}
	assertBackupExpression(t, databasePath, "以前")
}

func TestApplyPendingRestorePreservesBundleAfterTargetIOFailure(t *testing.T) {
	dataDir, _ := queueRestoreFixture(t)
	blockedParent := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blockedParent, []byte("not a directory"), 0o640); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(blockedParent, "vocab.sqlite")

	restored, err := ApplyPendingRestore(context.Background(), dataDir, databasePath, time.Now())
	if restored || !errors.Is(err, ErrPendingRestoreRetry) {
		t.Fatalf("ApplyPendingRestore() = %t, %v, want retryable I/O failure", restored, err)
	}
	status, statusErr := ReadRestoreStatus(dataDir)
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	if status.State != "pending" {
		t.Fatalf("restore status = %+v, want pending", status)
	}
	if err := os.Remove(blockedParent); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(blockedParent, 0o750); err != nil {
		t.Fatal(err)
	}

	restored, err = ApplyPendingRestore(context.Background(), dataDir, databasePath, time.Now())
	if err != nil || !restored {
		t.Fatalf("retry ApplyPendingRestore() = %t, %v", restored, err)
	}
	assertBackupExpression(t, databasePath, "以前")
}

func TestPreparedRestoreReceiptRecoversAfterAppliedReceiptWriteFailure(t *testing.T) {
	dataDir, databasePath := queueRestoreFixtureWithPendingImport(t)
	writeFailure := errors.New("injected receipt write failure")
	startedAt := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	restored, err := applyPendingRestore(
		context.Background(),
		dataDir,
		databasePath,
		startedAt,
		func(string, restoreReceipt) error { return writeFailure },
	)
	if !restored || !errors.Is(err, writeFailure) {
		t.Fatalf("applyPendingRestore() = %t, %v, want published restore and receipt failure", restored, err)
	}
	receipt, exists, err := readRestoreReceipt(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || receipt.State != restorePrepared {
		t.Fatalf("restore receipt = %+v, %t, want prepared receipt", receipt, exists)
	}
	previous, err := fingerprintDatabaseArtifacts(context.Background(), databasePath+".before-restore")
	if err != nil {
		t.Fatal(err)
	}
	if !fingerprintsEqual(previous, receipt.OriginalDatabase) {
		t.Fatal("published restore did not preserve the original database")
	}
	firstArchive := restoredImportArchivePath(t, databasePath)

	completedAt := startedAt.Add(time.Minute)
	restored, err = ApplyPendingRestore(context.Background(), dataDir, databasePath, completedAt)
	if err != nil || !restored {
		t.Fatalf("retry ApplyPendingRestore() = %t, %v", restored, err)
	}
	assertBackupExpression(t, databasePath, "以前")
	assertBackupExpression(t, databasePath+".before-restore", "現在")
	secondArchive := restoredImportArchivePath(t, databasePath)
	if secondArchive != firstArchive {
		t.Fatalf("restored import path changed across retry: %q, then %q", firstArchive, secondArchive)
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "imports"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || filepath.Join(dataDir, "imports", entries[0].Name()) != secondArchive {
		t.Fatalf("restored import files = %v, want only %q", entries, secondArchive)
	}
	for _, name := range []string{pendingRestoreName, restoreReceiptName} {
		if _, err := os.Stat(filepath.Join(dataDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("restore marker %q remains: %v", name, err)
		}
	}
	status, err := ReadRestoreStatus(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "success" || !status.OccurredAt.Equal(completedAt) {
		t.Fatalf("restore status = %+v", status)
	}
}

func TestPreparedRestoreReceiptRecoversWithoutAnOriginalDatabase(t *testing.T) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	sourcePath := filepath.Join(sourceDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, sourcePath)
	if _, err := db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at)
		VALUES ('以前', '以前', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "goi-empty-target"+BundleSuffix)
	if err := CreateBundle(ctx, sourcePath, bundlePath, time.Now()); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	bundle, err := os.Open(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := QueueRestore(ctx, dataDir, bundle); err != nil {
		bundle.Close()
		t.Fatal(err)
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	writeFailure := errors.New("injected receipt write failure")
	restored, err := applyPendingRestore(
		ctx,
		dataDir,
		databasePath,
		time.Now(),
		func(string, restoreReceipt) error { return writeFailure },
	)
	if !restored || !errors.Is(err, writeFailure) {
		t.Fatalf("applyPendingRestore() = %t, %v, want published restore and receipt failure", restored, err)
	}
	receipt, exists, err := readRestoreReceipt(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || receipt.OriginalDatabase.Exists {
		t.Fatalf("restore receipt = %+v, %t, want no original database", receipt, exists)
	}

	restored, err = ApplyPendingRestore(ctx, dataDir, databasePath, time.Now())
	if err != nil || !restored {
		t.Fatalf("retry ApplyPendingRestore() = %t, %v", restored, err)
	}
	assertBackupExpression(t, databasePath, "以前")
	if _, err := os.Stat(databasePath + ".before-restore"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore created a rollback database for an empty target: %v", err)
	}
}

func TestAppliedRestoreCleansOnlyNewWorkspacesUnderDatabaseLock(t *testing.T) {
	dataDir, databasePath := queueRestoreFixture(t)
	preservedWorkspace := filepath.Join(filepath.Dir(databasePath), ".restore-preserved")
	if err := os.Mkdir(preservedWorkspace, 0o750); err != nil {
		t.Fatal(err)
	}
	importsPath := filepath.Join(dataDir, "imports")
	preservedImportWorkspace := filepath.Join(importsPath, ".restore-preserved")
	if err := os.MkdirAll(preservedImportWorkspace, 0o750); err != nil {
		t.Fatal(err)
	}
	staleWorkspace := filepath.Join(filepath.Dir(databasePath), ".restore-stale")
	staleImportWorkspace := filepath.Join(importsPath, ".restore-stale")
	stopAfterReceipt := errors.New("stop after applied receipt")
	restored, err := applyPendingRestore(
		context.Background(),
		dataDir,
		databasePath,
		time.Now(),
		func(receiptDir string, receipt restoreReceipt) error {
			if err := os.Mkdir(staleWorkspace, 0o750); err != nil {
				return err
			}
			if err := os.Mkdir(staleImportWorkspace, 0o750); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(staleWorkspace, "displaced-database"), []byte("stale"), 0o640); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(staleImportWorkspace, "pending.apkg"), []byte("stale"), 0o640); err != nil {
				return err
			}
			if err := replaceRestoreReceipt(receiptDir, receipt); err != nil {
				return err
			}
			return stopAfterReceipt
		},
	)
	if !restored || !errors.Is(err, stopAfterReceipt) {
		t.Fatalf("applyPendingRestore() = %t, %v, want applied receipt interruption", restored, err)
	}

	lock, err := database.AcquireLock(databasePath, false)
	if err != nil {
		t.Fatal(err)
	}
	restored, err = ApplyPendingRestore(context.Background(), dataDir, databasePath, time.Now())
	if restored || !errors.Is(err, ErrPendingRestoreRetry) || !errors.Is(err, database.ErrDatabaseInUse) {
		lock.Close()
		t.Fatalf("ApplyPendingRestore() under lock = %t, %v", restored, err)
	}
	if _, err := os.Stat(staleWorkspace); err != nil {
		lock.Close()
		t.Fatalf("workspace was cleaned without the database lock: %v", err)
	}
	if _, err := os.Stat(staleImportWorkspace); err != nil {
		lock.Close()
		t.Fatalf("import workspace was cleaned without the database lock: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	restored, err = ApplyPendingRestore(context.Background(), dataDir, databasePath, time.Now())
	if err != nil || !restored {
		t.Fatalf("retry ApplyPendingRestore() = %t, %v", restored, err)
	}
	if _, err := os.Stat(staleWorkspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale restore workspace remains: %v", err)
	}
	if _, err := os.Stat(staleImportWorkspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale import restore workspace remains: %v", err)
	}
	if _, err := os.Stat(preservedWorkspace); err != nil {
		t.Fatalf("pre-existing restore workspace was removed: %v", err)
	}
	if _, err := os.Stat(preservedImportWorkspace); err != nil {
		t.Fatalf("pre-existing import restore workspace was removed: %v", err)
	}
}

func TestRestoreWorkspaceBaselineCountFailsBeforePublication(t *testing.T) {
	dataDir, databasePath := queueRestoreFixture(t)
	for index := 0; index <= maxReceiptWorkspaces; index++ {
		name := fmt.Sprintf(".restore-existing-%02d", index)
		if err := os.Mkdir(filepath.Join(filepath.Dir(databasePath), name), 0o750); err != nil {
			t.Fatal(err)
		}
	}

	restored, err := ApplyPendingRestore(context.Background(), dataDir, databasePath, time.Now())
	if restored || !errors.Is(err, ErrPendingRestoreRetry) || !strings.Contains(err.Error(), "too many restore workspaces") {
		t.Fatalf("ApplyPendingRestore() = %t, %v, want bounded workspace failure", restored, err)
	}
	assertBackupExpression(t, databasePath, "現在")
	if _, err := os.Stat(filepath.Join(dataDir, restoreReceiptName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized baseline published a restore receipt: %v", err)
	}
}

func TestRestoreWorkspaceBaselineMustFitReceipt(t *testing.T) {
	receipt := restoreReceipt{
		State:             restorePrepared,
		BundleSHA256:      strings.Repeat("0", 64),
		OriginalDatabase:  databaseArtifactFingerprint{SHA256: strings.Repeat("1", 64)},
		OriginalPrevious:  databaseArtifactFingerprint{SHA256: strings.Repeat("2", 64)},
		WorkspaceBaseline: true,
		PreparedAt:        time.Now().UTC(),
	}
	for index := 0; index < maxReceiptWorkspaces; index++ {
		receipt.ExistingDatabaseWorkspaces = append(
			receipt.ExistingDatabaseWorkspaces,
			fmt.Sprintf(".restore-%02d-%s", index, strings.Repeat("x", 96)),
		)
	}
	dataDir := t.TempDir()
	err := createRestoreReceipt(dataDir, receipt)
	if err == nil || !strings.Contains(err.Error(), "restore receipt is too large") {
		t.Fatalf("createRestoreReceipt() error = %v, want receipt size limit", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, restoreReceiptName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized restore receipt was published: %v", err)
	}
}

func TestRestoreQueueFileLockSerializesQueueAndCancel(t *testing.T) {
	dataDir := t.TempDir()
	lock, err := acquireRestoreQueueLock(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := QueueRestore(context.Background(), dataDir, bytes.NewReader(nil)); !errors.Is(err, ErrRestoreQueueBusy) {
		lock.Close()
		t.Fatalf("QueueRestore() error = %v, want restore queue contention", err)
	}
	if err := CancelPendingRestore(dataDir); !errors.Is(err, ErrRestoreQueueBusy) {
		lock.Close()
		t.Fatalf("CancelPendingRestore() error = %v, want restore queue contention", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRestorePublicationDoesNotReplaceExistingPendingBundle(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "source")
	pendingPath := filepath.Join(directory, pendingRestoreName)
	if err := os.WriteFile(sourcePath, []byte("second"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pendingPath, []byte("first"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := linkRestoreFile(sourcePath, pendingPath); !errors.Is(err, os.ErrExist) {
		t.Fatalf("linkRestoreFile() error = %v, want existing destination", err)
	}
	contents, err := os.ReadFile(pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "first" {
		t.Fatalf("pending restore = %q, want first bundle", contents)
	}
}

func TestApplyPendingRestoreQuarantinesInvalidBundle(t *testing.T) {
	dataDir := t.TempDir()
	pendingPath := filepath.Join(dataDir, pendingRestoreName)
	if err := os.WriteFile(pendingPath, []byte("not a backup"), 0o640); err != nil {
		t.Fatal(err)
	}

	restored, err := ApplyPendingRestore(
		context.Background(),
		dataDir,
		filepath.Join(dataDir, "vocab.sqlite"),
		time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
	)
	if err == nil {
		t.Fatal("ApplyPendingRestore() accepted an invalid bundle")
	}
	if !errors.Is(err, ErrInvalidBundle) {
		t.Fatalf("ApplyPendingRestore() error = %v, want invalid bundle", err)
	}
	if restored {
		t.Fatal("ApplyPendingRestore() reported an invalid bundle as restored")
	}
	if _, statErr := os.Stat(pendingPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid pending restore still exists: %v", statErr)
	}
	failed, globErr := filepath.Glob(filepath.Join(dataDir, "failed-restore-*"+BundleSuffix))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(failed) != 1 {
		t.Fatalf("quarantined restore bundles = %v, want one", failed)
	}
	status, statusErr := ReadRestoreStatus(dataDir)
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	if status.State != "failed" {
		t.Fatalf("restore status = %+v, want failed", status)
	}
}

func TestQuarantineSyncFailureRequiresStartupRetry(t *testing.T) {
	dataDir := t.TempDir()
	pendingPath := filepath.Join(dataDir, pendingRestoreName)
	if err := os.WriteFile(pendingPath, []byte("invalid"), 0o640); err != nil {
		t.Fatal(err)
	}
	restoreErr := fmt.Errorf("%w: invalid fixture", ErrInvalidBundle)
	syncErr := errors.New("injected directory sync failure")
	err := quarantineInvalidRestoreWithSync(
		dataDir,
		pendingPath,
		time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
		restoreErr,
		func(string) error { return syncErr },
	)
	if !errors.Is(err, ErrPendingRestoreRetry) || !errors.Is(err, ErrInvalidBundle) || !errors.Is(err, syncErr) {
		t.Fatalf("quarantineInvalidRestoreWithSync() error = %v", err)
	}
}

func TestQueueRestoreRejectsCorruptBundleWithoutCreatingPendingFile(t *testing.T) {
	dataDir := t.TempDir()
	err := QueueRestore(context.Background(), dataDir, bytes.NewBufferString("not a zip"))
	if err == nil {
		t.Fatal("QueueRestore() accepted corrupt upload")
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, pendingRestoreName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("pending restore exists after rejection: %v", statErr)
	}
}

func TestConcurrentRestoreQueuesKeepTheFirstBundle(t *testing.T) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	databasePath := filepath.Join(sourceDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "goi-concurrent"+BundleSuffix)
	if err := CreateBundle(ctx, databasePath, bundlePath, time.Now()); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- QueueRestore(ctx, dataDir, bytes.NewReader(contents))
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful concurrent restore queues = %d, want 1", succeeded)
	}
	if err := ValidateBundle(ctx, filepath.Join(dataDir, pendingRestoreName)); err != nil {
		t.Fatalf("published restore bundle is invalid: %v", err)
	}
}

func TestQueuedBundleDefinesPendingRestoreWithoutAStatusFile(t *testing.T) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	databasePath := filepath.Join(sourceDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "goi-status"+BundleSuffix)
	if err := CreateBundle(ctx, databasePath, bundlePath, time.Now()); err != nil {
		t.Fatal(err)
	}
	bundle, err := os.Open(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()

	dataDir := t.TempDir()
	if err := QueueRestore(ctx, dataDir, bundle); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "restore-status.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("queue created an auxiliary status file: %v", err)
	}
	status, err := ReadRestoreStatus(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "pending" {
		t.Fatalf("restore status = %+v, want pending", status)
	}
}

func TestCleanupInterruptedBundlesLeavesPublishedBackupsAlone(t *testing.T) {
	dataDir := t.TempDir()
	directory := filepath.Join(dataDir, "backups")
	staleWorkspace := filepath.Join(directory, ".bundle-stale")
	if err := os.MkdirAll(staleWorkspace, 0o750); err != nil {
		t.Fatal(err)
	}
	staleArchive := filepath.Join(directory, ".goi-backup-stale.zip")
	if err := os.WriteFile(staleArchive, []byte("partial"), 0o640); err != nil {
		t.Fatal(err)
	}
	staleValidation := filepath.Join(directory, ".validate-restore-stale")
	if err := os.Mkdir(staleValidation, 0o750); err != nil {
		t.Fatal(err)
	}
	recentWorkspace := filepath.Join(directory, ".bundle-active")
	if err := os.Mkdir(recentWorkspace, 0o750); err != nil {
		t.Fatal(err)
	}
	published := filepath.Join(directory, "goi-published"+BundleSuffix)
	if err := os.WriteFile(published, []byte("complete"), 0o640); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(directory, ".keep")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o640); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	staleTime := now.Add(-interruptedBundleStaleAfter - time.Minute)
	for _, path := range []string{staleWorkspace, staleArchive, staleValidation} {
		if err := os.Chtimes(path, staleTime, staleTime); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(recentWorkspace, now, now); err != nil {
		t.Fatal(err)
	}

	if err := cleanupInterruptedBundles(directory, now); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{staleWorkspace, staleArchive, staleValidation} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale path %q still exists: %v", path, err)
		}
	}
	for _, path := range []string{recentWorkspace, published, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("preserved path %q: %v", path, err)
		}
	}
}

func openBackupTestDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := database.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(t.Context(), db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_settings (id, time_zone) VALUES (1, 'UTC')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func queueRestoreFixture(t *testing.T) (string, string) {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	if _, err := db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at)
		VALUES ('以前', '以前', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "goi-retry"+BundleSuffix)
	if err := CreateBundle(ctx, databasePath, bundlePath, time.Now()); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE vocabulary SET expression = '現在', normalized_expression = '現在'"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	bundle, err := os.Open(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := QueueRestore(ctx, dataDir, bundle); err != nil {
		bundle.Close()
		t.Fatal(err)
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	return dataDir, databasePath
}

func queueRestoreFixtureWithPendingImport(t *testing.T) (string, string) {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	if _, err := db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at)
		VALUES ('現在', '現在', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	sourceDir := t.TempDir()
	sourceDatabasePath := filepath.Join(sourceDir, "vocab.sqlite")
	sourceDB := openBackupTestDatabase(t, sourceDatabasePath)
	if _, err := sourceDB.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at)
		VALUES ('以前', '以前', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	importsDir := filepath.Join(sourceDir, "imports")
	if err := os.Mkdir(importsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(importsDir, "run-1.apkg")
	if err := os.WriteFile(archivePath, []byte("pending import"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceDB.Exec(`
		INSERT INTO import_runs (id, filename, archive_path, status, created_at)
		VALUES (1, 'deck.apkg', ?, 'previewed', 1)`, archivePath); err != nil {
		t.Fatal(err)
	}
	if err := sourceDB.Close(); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "goi-retry-import"+BundleSuffix)
	if err := CreateBundle(ctx, sourceDatabasePath, bundlePath, time.Now()); err != nil {
		t.Fatal(err)
	}
	bundle, err := os.Open(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := QueueRestore(ctx, dataDir, bundle); err != nil {
		bundle.Close()
		t.Fatal(err)
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	return dataDir, databasePath
}

func restoredImportArchivePath(t *testing.T, databasePath string) string {
	t.Helper()
	db, err := database.Open(t.Context(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var archivePath string
	if err := db.QueryRow("SELECT archive_path FROM import_runs WHERE id = 1").Scan(&archivePath); err != nil {
		t.Fatal(err)
	}
	return archivePath
}

func assertBackupExpression(t *testing.T, path, want string) {
	t.Helper()
	db, err := database.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var expression string
	if err := db.QueryRow("SELECT expression FROM vocabulary").Scan(&expression); err != nil {
		t.Fatal(err)
	}
	if expression != want {
		t.Fatalf("expression in %s = %q, want %q", path, expression, want)
	}
}
