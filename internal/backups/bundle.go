package backups

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tomasmik/goi/internal/contextio"
	"github.com/tomasmik/goi/internal/database"
)

const (
	BundleSuffix                      = ".goi-backup.zip"
	MaxBundleBytes              int64 = 4 << 30
	maxBundleFiles                    = 10_000
	interruptedBundleStaleAfter       = 24 * time.Hour
)

// ErrInvalidBundle means retrying the same backup bundle cannot succeed.
var ErrInvalidBundle = errors.New("backup bundle is invalid")

type bundleManifest struct {
	Format    int    `json:"format"`
	CreatedAt string `json:"created_at"`
}

func CreateBundle(ctx context.Context, databasePath, outputPath string, createdAt time.Time) error {
	if !strings.HasSuffix(filepath.Base(outputPath), BundleSuffix) {
		return fmt.Errorf("backup filename must end in %s", BundleSuffix)
	}
	directory := filepath.Dir(outputPath)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	workspace, err := os.MkdirTemp(directory, ".bundle-")
	if err != nil {
		return fmt.Errorf("create backup workspace: %w", err)
	}
	defer os.RemoveAll(workspace)

	databaseBackup := filepath.Join(workspace, "vocab.sqlite")
	if _, err := database.BackupWithImports(ctx, databasePath, databaseBackup); err != nil {
		return fmt.Errorf("create database backup: %w", err)
	}
	if err := sanitizeSnapshot(ctx, databaseBackup); err != nil {
		return err
	}

	temporary, err := os.CreateTemp(directory, ".goi-backup-*.zip")
	if err != nil {
		return fmt.Errorf("create backup bundle: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	archive := zip.NewWriter(temporary)
	manifest, err := json.Marshal(bundleManifest{Format: 1, CreatedAt: createdAt.UTC().Format(time.RFC3339)})
	if err == nil {
		err = ensureBundleSourceFits(databaseBackup, int64(len(manifest)), MaxBundleBytes)
	}
	if err == nil {
		err = writeZipBytes(archive, "manifest.json", manifest)
	}
	if err == nil {
		err = writeZipFile(ctx, archive, databaseBackup, "vocab.sqlite")
	}
	if err == nil {
		err = writeZipFile(ctx, archive, databaseBackup+".sha256", "vocab.sqlite.sha256")
	}
	if err == nil {
		err = writeImportFiles(ctx, archive, databaseBackup+".imports")
	}
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write backup bundle: %w", err)
	}
	if info, err := os.Stat(temporaryPath); err != nil {
		return fmt.Errorf("inspect backup bundle: %w", err)
	} else if info.Size() > MaxBundleBytes {
		return fmt.Errorf("backup bundle exceeds the %d byte limit", MaxBundleBytes)
	}
	if err := os.Chmod(temporaryPath, 0o640); err != nil {
		return fmt.Errorf("secure backup bundle: %w", err)
	}
	if err := os.Chtimes(temporaryPath, createdAt, createdAt); err != nil {
		return fmt.Errorf("set backup creation time: %w", err)
	}
	if err := syncFile(temporaryPath); err != nil {
		return fmt.Errorf("sync backup bundle: %w", err)
	}
	if err := ValidateBundle(ctx, temporaryPath); err != nil {
		return fmt.Errorf("validate completed backup: %w", err)
	}
	if err := os.Rename(temporaryPath, outputPath); err != nil {
		return fmt.Errorf("publish backup bundle: %w", err)
	}
	removeTemporary = false
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync backup directory: %w", err)
	}
	return nil
}

func sanitizeSnapshot(ctx context.Context, databasePath string) error {
	db, err := database.Open(ctx, databasePath)
	if err != nil {
		return fmt.Errorf("open backup snapshot: %w", err)
	}
	if err := sanitizeSnapshotDatabase(ctx, db); err != nil {
		return errors.Join(err, db.Close())
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return errors.Join(fmt.Errorf("checkpoint backup snapshot: %w", err), db.Close())
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close backup snapshot: %w", err)
	}
	return rewriteSnapshotChecksum(ctx, databasePath)
}

func sanitizeSnapshotDatabase(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin backup snapshot cleanup: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM web_sessions"); err != nil {
		return fmt.Errorf("remove sessions from backup snapshot: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM extension_tokens"); err != nil {
		return fmt.Errorf("remove extension tokens from backup snapshot: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE backup_state
		SET status = 'idle', trigger = '', last_attempt_at = NULL,
		    last_success_at = NULL, last_scheduled_date = '', local_name = '',
		    remote_id = '', error_message = ''
		WHERE id = 1`)
	if err != nil {
		return fmt.Errorf("reset backup snapshot state: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check backup snapshot state: %w", err)
	}
	if updated != 1 {
		return errors.New("backup snapshot state does not exist")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit backup snapshot cleanup: %w", err)
	}
	return nil
}

func rewriteSnapshotChecksum(ctx context.Context, databasePath string) error {
	file, err := os.Open(databasePath)
	if err != nil {
		return fmt.Errorf("open backup snapshot for checksum: %w", err)
	}
	hash := sha256.New()
	_, copyErr := contextio.Copy(ctx, hash, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return fmt.Errorf("checksum backup snapshot: %w", errors.Join(copyErr, closeErr))
	}
	checksum := hex.EncodeToString(hash.Sum(nil))
	contents := []byte(checksum + "  " + filepath.Base(databasePath) + "\n")
	if err := os.WriteFile(databasePath+".sha256", contents, 0o640); err != nil {
		return fmt.Errorf("write backup snapshot checksum: %w", err)
	}
	if err := syncFile(databasePath + ".sha256"); err != nil {
		return fmt.Errorf("sync backup snapshot checksum: %w", err)
	}
	return nil
}

func cleanupInterruptedBundles(backupDir string, now time.Time) error {
	entries, err := os.ReadDir(backupDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read backup directory: %w", err)
	}
	var cleanupErrors []error
	removed := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".bundle-") &&
			!strings.HasPrefix(name, ".validate-restore-") &&
			!(strings.HasPrefix(name, ".goi-backup-") && strings.HasSuffix(name, ".zip")) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("inspect interrupted backup file %q: %w", name, err))
			continue
		}
		if info.ModTime().Add(interruptedBundleStaleAfter).After(now) {
			continue
		}
		path := filepath.Join(backupDir, name)
		if err := os.RemoveAll(path); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove interrupted backup file %q: %w", name, err))
			continue
		}
		removed = true
	}
	if removed {
		if err := syncDirectory(backupDir); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("sync backup directory: %w", err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func ensureBundleSourceFits(databaseBackup string, manifestBytes, limit int64) error {
	if manifestBytes < 0 || limit < 1 || manifestBytes > limit {
		return fmt.Errorf("backup contents exceed the %d byte limit", limit)
	}
	total := manifestBytes
	paths := []string{databaseBackup, databaseBackup + ".sha256"}
	entries, err := os.ReadDir(databaseBackup + ".imports")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect pending import backups: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Base(entry.Name()) != entry.Name() {
			return fmt.Errorf("pending import backup contains unexpected entry %q", entry.Name())
		}
		paths = append(paths, filepath.Join(databaseBackup+".imports", entry.Name()))
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("inspect backup input %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%q is not a regular file", path)
		}
		if info.Size() > limit-total {
			return fmt.Errorf("backup contents exceed the %d byte limit", limit)
		}
		total += info.Size()
	}
	return nil
}

func ValidateBundle(ctx context.Context, bundlePath string) error {
	workspace, err := os.MkdirTemp(filepath.Dir(bundlePath), ".validate-restore-")
	if err != nil {
		return fmt.Errorf("create restore validation workspace: %w", err)
	}
	defer os.RemoveAll(workspace)
	databaseBackup, err := extractBundle(ctx, bundlePath, workspace)
	if err != nil {
		return err
	}
	err = database.RestoreWithImports(
		ctx,
		databaseBackup,
		filepath.Join(workspace, "validated", "vocab.sqlite"),
		filepath.Join(workspace, "validated", "imports"),
	)
	return classifyBundleDatabaseError(err)
}

func RestoreBundle(ctx context.Context, bundlePath, databasePath, importsPath string) error {
	return restoreBundle(ctx, bundlePath, databasePath, importsPath, nil, false)
}

func restoreBundleUnderLock(
	ctx context.Context,
	bundlePath, databasePath, importsPath string,
	lock *database.Lock,
	preservePrevious bool,
) error {
	return restoreBundle(ctx, bundlePath, databasePath, importsPath, lock, preservePrevious)
}

func restoreBundle(
	ctx context.Context,
	bundlePath, databasePath, importsPath string,
	lock *database.Lock,
	preservePrevious bool,
) error {
	workspace, err := os.MkdirTemp(filepath.Dir(databasePath), ".bundle-restore-")
	if err != nil {
		return fmt.Errorf("create bundle restore workspace: %w", err)
	}
	defer os.RemoveAll(workspace)
	databaseBackup, err := extractBundle(ctx, bundlePath, workspace)
	if err != nil {
		return err
	}
	if lock == nil {
		err = database.RestoreWithImports(ctx, databaseBackup, databasePath, importsPath)
	} else {
		err = database.RestoreWithImportsUnderLock(ctx, databaseBackup, databasePath, importsPath, lock, preservePrevious)
	}
	return classifyBundleDatabaseError(err)
}

func classifyBundleDatabaseError(err error) error {
	if errors.Is(err, database.ErrInvalidRestoreSource) {
		return invalidBundleError(err)
	}
	return err
}

func extractBundle(ctx context.Context, bundlePath, directory string) (string, error) {
	info, err := os.Lstat(bundlePath)
	if err != nil {
		return "", fmt.Errorf("inspect backup bundle: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", invalidBundleError(errors.New("backup bundle is not a regular file"))
	}
	if info.Size() < 1 || info.Size() > MaxBundleBytes {
		return "", invalidBundleError(fmt.Errorf("backup bundle must be between 1 byte and %d bytes", MaxBundleBytes))
	}
	archive, err := zip.OpenReader(bundlePath)
	if err != nil {
		if errors.Is(err, zip.ErrFormat) {
			return "", invalidBundleError(err)
		}
		return "", fmt.Errorf("open backup bundle: %w", err)
	}
	defer archive.Close()
	if len(archive.File) < 3 || len(archive.File) > maxBundleFiles {
		return "", invalidBundleError(errors.New("backup bundle has an invalid number of files"))
	}

	databaseBackup := filepath.Join(directory, "vocab.sqlite")
	seen := make(map[string]struct{}, len(archive.File))
	var total uint64
	for _, entry := range archive.File {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if _, exists := seen[entry.Name]; exists {
			return "", invalidBundleError(fmt.Errorf("backup bundle contains duplicate file %q", entry.Name))
		}
		seen[entry.Name] = struct{}{}
		if !validBundleEntry(entry.Name) || entry.FileInfo().IsDir() || !entry.Mode().IsRegular() {
			return "", invalidBundleError(fmt.Errorf("backup bundle contains unexpected file %q", entry.Name))
		}
		if entry.UncompressedSize64 > uint64(MaxBundleBytes) || total > uint64(MaxBundleBytes)-entry.UncompressedSize64 {
			return "", invalidBundleError(errors.New("backup bundle expands beyond the size limit"))
		}
		total += entry.UncompressedSize64
		outputPath := filepath.Join(directory, filepath.FromSlash(entry.Name))
		if strings.HasPrefix(entry.Name, "imports/") {
			outputPath = filepath.Join(databaseBackup+".imports", strings.TrimPrefix(entry.Name, "imports/"))
		}
		if err := extractZipFile(ctx, entry, outputPath); err != nil {
			if corruptArchiveError(err) {
				return "", invalidBundleError(err)
			}
			return "", err
		}
	}
	for _, required := range []string{"manifest.json", "vocab.sqlite", "vocab.sqlite.sha256"} {
		if _, exists := seen[required]; !exists {
			return "", invalidBundleError(fmt.Errorf("backup bundle is missing %s", required))
		}
	}
	if err := validateBundleManifest(filepath.Join(directory, "manifest.json")); err != nil {
		return "", err
	}
	return databaseBackup, nil
}

func invalidBundleError(err error) error {
	return fmt.Errorf("%w: %w", ErrInvalidBundle, err)
}

func corruptArchiveError(err error) bool {
	return errors.Is(err, zip.ErrAlgorithm) ||
		errors.Is(err, zip.ErrChecksum) ||
		errors.Is(err, zip.ErrFormat) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

func validBundleEntry(name string) bool {
	if name == "manifest.json" || name == "vocab.sqlite" || name == "vocab.sqlite.sha256" {
		return true
	}
	if !strings.HasPrefix(name, "imports/") {
		return false
	}
	base := strings.TrimPrefix(name, "imports/")
	return base != "" && filepath.Base(base) == base && !strings.Contains(base, "\\")
}

func validateBundleManifest(path string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read backup manifest: %w", err)
	}
	if len(contents) > 4<<10 {
		return invalidBundleError(errors.New("backup manifest is too large"))
	}
	var manifest bundleManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		return invalidBundleError(fmt.Errorf("parse backup manifest: %w", err))
	}
	if manifest.Format != 1 {
		return invalidBundleError(fmt.Errorf("backup format %d is not supported", manifest.Format))
	}
	if _, err := time.Parse(time.RFC3339, manifest.CreatedAt); err != nil {
		return invalidBundleError(errors.New("backup manifest has an invalid creation time"))
	}
	return nil
}

func writeImportFiles(ctx context.Context, archive *zip.Writer, directory string) error {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) > maxBundleFiles-3 {
		return errors.New("too many pending import files")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || filepath.Base(entry.Name()) != entry.Name() {
			return fmt.Errorf("pending import backup contains unexpected entry %q", entry.Name())
		}
		if err := writeZipFile(ctx, archive, filepath.Join(directory, entry.Name()), "imports/"+entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func writeZipBytes(archive *zip.Writer, name string, contents []byte) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(0o640)
	writer, err := archive.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = writer.Write(contents)
	return err
}

func writeZipFile(ctx context.Context, archive *zip.Writer, sourcePath, name string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%q is not a regular file", sourcePath)
	}
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(0o640)
	writer, err := archive.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = contextio.Copy(ctx, writer, source)
	return err
}

func extractZipFile(ctx context.Context, entry *zip.File, outputPath string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o750); err != nil {
		return err
	}
	input, err := entry.Open()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	_, copyErr := contextio.Copy(ctx, output, input)
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(outputPath)
		return errors.Join(copyErr, closeErr)
	}
	return nil
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
