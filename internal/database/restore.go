package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ncruces/go-sqlite3"
)

var (
	// ErrInvalidRestoreSource means retrying the same restore source cannot succeed.
	ErrInvalidRestoreSource  = errors.New("restore source is invalid")
	errInvalidImportManifest = errors.New("pending import manifest is invalid")
)

func RestoreWithImports(ctx context.Context, backupPath, databasePath, stagingPath string) error {
	return restore(ctx, backupPath, databasePath, stagingPath, true, nil, false)
}

func RestoreWithImportsUnderLock(
	ctx context.Context,
	backupPath, databasePath, stagingPath string,
	lock *Lock,
	preservePrevious bool,
) error {
	if !lock.holdsExclusive(databasePath + ".lock") {
		return errors.New("restore requires a held exclusive database lock")
	}
	return restore(ctx, backupPath, databasePath, stagingPath, true, lock, preservePrevious)
}

type stagedImport struct {
	id         int64
	checksum   string
	stagedPath string
	outputPath string
}

func restore(
	ctx context.Context,
	backupPath, databasePath, stagingPath string,
	includeImports bool,
	heldLock *Lock,
	preservePrevious bool,
) error {
	if err := requireRegularFile(backupPath); err != nil {
		return fmt.Errorf("inspect restore source: %w", err)
	}
	if err := validateRestorePaths(backupPath, databasePath); err != nil {
		return fmt.Errorf("validate restore destination: %w", err)
	}
	directory := filepath.Dir(databasePath)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	if heldLock == nil {
		lock, err := AcquireLock(databasePath, true)
		if err != nil {
			return err
		}
		defer lock.Close()
	}

	workspace, err := os.MkdirTemp(directory, ".restore-")
	if err != nil {
		return fmt.Errorf("create restore workspace: %w", err)
	}
	removeWorkspace := true
	defer func() {
		if removeWorkspace {
			_ = os.RemoveAll(workspace)
		}
	}()
	stagingDirectory := filepath.Join(workspace, "staged")
	if err := os.Mkdir(stagingDirectory, 0o750); err != nil {
		return fmt.Errorf("create restore staging directory: %w", err)
	}
	stagedDatabasePath := filepath.Join(stagingDirectory, filepath.Base(databasePath))
	databaseChecksum, err := copyFile(ctx, backupPath, stagedDatabasePath)
	if err != nil {
		return fmt.Errorf("stage restore database: %w", err)
	}
	if err := verifyChecksum(databaseChecksum, backupPath+".sha256"); err != nil {
		return classifyRestoreSourceError(err)
	}
	if err := prepareStagedRestore(ctx, stagedDatabasePath); err != nil {
		return classifyRestoreSourceError(err)
	}

	var imports []stagedImport
	var importWorkspace string
	if includeImports {
		imports, importWorkspace, err = stagePendingImports(ctx, backupPath, stagedDatabasePath, stagingPath)
		if err != nil {
			return classifyRestoreSourceError(err)
		}
		if importWorkspace != "" {
			defer os.RemoveAll(importWorkspace)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	publishedImports, err := publishPendingImports(ctx, imports)
	if err != nil {
		return err
	}
	if err := publishRestoredDatabase(stagedDatabasePath, databasePath, workspace, preservePrevious); err != nil {
		removeWorkspace = false
		removeErrors := removePublishedImports(publishedImports)
		cause := fmt.Errorf("%w; recovery files remain in %s", err, workspace)
		return errors.Join(append([]error{cause}, removeErrors...)...)
	}
	if importWorkspace != "" {
		if err := removePublishedRestoreWorkspace(importWorkspace); err != nil {
			return fmt.Errorf("remove import restore workspace: %w", err)
		}
	}
	if err := removePublishedRestoreWorkspace(workspace); err != nil {
		return fmt.Errorf("remove database restore workspace: %w", err)
	}
	removeWorkspace = false
	return nil
}

func classifyRestoreSourceError(err error) error {
	if errors.Is(err, errInvalidChecksum) ||
		errors.Is(err, errIntegrityCheckFailed) ||
		errors.Is(err, errInvalidSchema) ||
		errors.Is(err, errInvalidImportManifest) ||
		errors.Is(err, sqlite3.CORRUPT) ||
		errors.Is(err, sqlite3.NOTADB) {
		return fmt.Errorf("%w: %w", ErrInvalidRestoreSource, err)
	}
	return err
}

func prepareStagedRestore(ctx context.Context, stagedPath string) (returnErr error) {
	db, err := Open(ctx, stagedPath)
	if err != nil {
		return fmt.Errorf("open staged restore database: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			if err := db.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close staged restore database: %w", err))
			}
		}
	}()
	if err := IntegrityCheck(ctx, db); err != nil {
		return fmt.Errorf("check staged restore database: %w", err)
	}
	if err := Migrate(ctx, db); err != nil {
		return fmt.Errorf("migrate staged restore database: %w", err)
	}
	if err := IntegrityCheck(ctx, db); err != nil {
		return fmt.Errorf("check migrated restore database: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("checkpoint staged restore database: %w", err)
	}
	closeErr := db.Close()
	closed = true
	if closeErr != nil {
		return fmt.Errorf("close staged restore database: %w", closeErr)
	}
	if err := syncFile(stagedPath); err != nil {
		return fmt.Errorf("sync staged restore database: %w", err)
	}
	return nil
}

func stagePendingImports(ctx context.Context, backupPath, stagedDatabasePath, stagingPath string) ([]stagedImport, string, error) {
	manifest, err := readPendingImportManifest(backupPath + ".imports")
	if err != nil {
		return nil, "", err
	}

	db, err := Open(ctx, stagedDatabasePath)
	if err != nil {
		return nil, "", fmt.Errorf("open staged restore database: %w", err)
	}
	pendingIDs, err := pendingImportIDs(ctx, db)
	if err != nil {
		db.Close()
		return nil, "", err
	}
	if err := validatePendingImportManifest(pendingIDs, manifest); err != nil {
		db.Close()
		return nil, "", err
	}
	if len(manifest) == 0 {
		if err := db.Close(); err != nil {
			return nil, "", fmt.Errorf("close staged restore database: %w", err)
		}
		return nil, "", nil
	}

	if err := os.MkdirAll(stagingPath, 0o750); err != nil {
		db.Close()
		return nil, "", fmt.Errorf("create import staging directory: %w", err)
	}
	archiveWorkspace, err := os.MkdirTemp(stagingPath, ".restore-")
	if err != nil {
		db.Close()
		return nil, "", fmt.Errorf("create import restore workspace: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(archiveWorkspace)
		}
	}()

	imports := make([]stagedImport, 0, len(manifest))
	for _, item := range manifest {
		outputName := fmt.Sprintf("run-%d-restored-%s.apkg", item.ID, strings.ToLower(item.SHA256))
		stagedPath := filepath.Join(archiveWorkspace, item.File)
		checksum, err := copyFile(ctx, filepath.Join(backupPath+".imports", item.File), stagedPath)
		if err != nil {
			db.Close()
			if errors.Is(err, os.ErrNotExist) {
				return nil, "", fmt.Errorf("%w: pending import %d is missing", errInvalidImportManifest, item.ID)
			}
			return nil, "", fmt.Errorf("stage pending import %d: %w", item.ID, err)
		}
		if !strings.EqualFold(item.SHA256, checksum) {
			db.Close()
			return nil, "", fmt.Errorf("%w: pending import %d checksum does not match", errInvalidChecksum, item.ID)
		}
		imports = append(imports, stagedImport{
			id:         item.ID,
			checksum:   checksum,
			stagedPath: stagedPath,
			outputPath: filepath.Join(stagingPath, outputName),
		})
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		db.Close()
		return nil, "", fmt.Errorf("begin restored import update: %w", err)
	}
	for _, item := range imports {
		result, err := tx.ExecContext(ctx, "UPDATE import_runs SET archive_path = ? WHERE id = ?", item.outputPath, item.id)
		if err != nil {
			tx.Rollback()
			db.Close()
			return nil, "", fmt.Errorf("update restored import %d: %w", item.id, err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			tx.Rollback()
			db.Close()
			return nil, "", fmt.Errorf("check restored import %d: %w", item.id, err)
		}
		if updated != 1 {
			tx.Rollback()
			db.Close()
			return nil, "", fmt.Errorf("update restored import %d: row not found", item.id)
		}
	}
	if err := tx.Commit(); err != nil {
		db.Close()
		return nil, "", fmt.Errorf("commit restored import paths: %w", err)
	}
	if err := IntegrityCheck(ctx, db); err != nil {
		db.Close()
		return nil, "", fmt.Errorf("check staged restore database: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		db.Close()
		return nil, "", fmt.Errorf("checkpoint staged restore database: %w", err)
	}
	if err := db.Close(); err != nil {
		return nil, "", fmt.Errorf("close staged restore database: %w", err)
	}
	if err := syncFile(stagedDatabasePath); err != nil {
		return nil, "", fmt.Errorf("sync staged restore database: %w", err)
	}
	cleanup = false
	return imports, archiveWorkspace, nil
}

func readPendingImportManifest(importsPath string) ([]pendingImport, error) {
	info, err := os.Stat(importsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect pending import backup: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: pending import backup is not a directory", errInvalidImportManifest)
	}
	manifestBytes, err := readFileAtMost(filepath.Join(importsPath, "manifest.json"), maxPendingImportManifestBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, errFileTooLarge) {
			return nil, fmt.Errorf("%w: %w", errInvalidImportManifest, err)
		}
		return nil, fmt.Errorf("read pending import manifest: %w", err)
	}
	var imports []pendingImport
	if err := json.Unmarshal(manifestBytes, &imports); err != nil {
		return nil, fmt.Errorf("%w: parse pending import manifest: %w", errInvalidImportManifest, err)
	}
	seenIDs := make(map[int64]struct{}, len(imports))
	seenFiles := make(map[string]struct{}, len(imports))
	for _, item := range imports {
		if item.ID < 1 {
			return nil, fmt.Errorf("%w: invalid pending import id %d", errInvalidImportManifest, item.ID)
		}
		if item.File == "" || filepath.Base(item.File) != item.File {
			return nil, fmt.Errorf("%w: unsafe pending import filename %q", errInvalidImportManifest, item.File)
		}
		if item.SHA256 == "" {
			return nil, fmt.Errorf("%w: missing pending import checksum for id %d", errInvalidImportManifest, item.ID)
		}
		decoded, err := hex.DecodeString(item.SHA256)
		if err != nil || len(decoded) != sha256.Size {
			return nil, fmt.Errorf("%w: invalid pending import checksum for id %d", errInvalidImportManifest, item.ID)
		}
		if _, ok := seenIDs[item.ID]; ok {
			return nil, fmt.Errorf("%w: duplicate pending import id %d", errInvalidImportManifest, item.ID)
		}
		if _, ok := seenFiles[item.File]; ok {
			return nil, fmt.Errorf("%w: duplicate pending import filename %q", errInvalidImportManifest, item.File)
		}
		seenIDs[item.ID] = struct{}{}
		seenFiles[item.File] = struct{}{}
	}
	return imports, nil
}

func pendingImportIDs(ctx context.Context, db *sql.DB) ([]int64, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id
		FROM import_runs
		WHERE status = 'previewed'
		   OR (status = 'failed' AND archive_path <> '')
		ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("load restored pending imports: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan restored pending import: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate restored pending imports: %w", err)
	}
	return ids, nil
}

func validatePendingImportManifest(pendingIDs []int64, manifest []pendingImport) error {
	manifestIDs := make([]int64, 0, len(manifest))
	for _, item := range manifest {
		manifestIDs = append(manifestIDs, item.ID)
	}
	sort.Slice(manifestIDs, func(i, j int) bool { return manifestIDs[i] < manifestIDs[j] })
	if len(pendingIDs) != len(manifestIDs) {
		return fmt.Errorf("%w: pending import manifest does not match the restore database", errInvalidImportManifest)
	}
	for i := range pendingIDs {
		if pendingIDs[i] != manifestIDs[i] {
			return fmt.Errorf("%w: pending import manifest does not match the restore database", errInvalidImportManifest)
		}
	}
	return nil
}

func publishPendingImports(ctx context.Context, imports []stagedImport) ([]stagedImport, error) {
	published := make([]stagedImport, 0, len(imports))
	for _, item := range imports {
		created, err := publishPendingImport(ctx, item)
		if err != nil {
			removeErrors := removePublishedImports(published)
			return nil, errors.Join(append([]error{fmt.Errorf("publish pending import %d: %w", item.id, err)}, removeErrors...)...)
		}
		if created {
			published = append(published, item)
		}
	}
	outputPaths := make([]string, 0, len(published))
	for _, item := range published {
		outputPaths = append(outputPaths, item.outputPath)
	}
	if err := syncParentDirectories(outputPaths...); err != nil {
		removeErrors := removePublishedImports(published)
		return nil, errors.Join(append([]error{fmt.Errorf("sync pending import directory: %w", err)}, removeErrors...)...)
	}
	return published, nil
}

func publishPendingImport(ctx context.Context, item stagedImport) (bool, error) {
	if err := os.Link(item.stagedPath, item.outputPath); err == nil {
		if err := os.Remove(item.stagedPath); err != nil {
			removeErr := os.Remove(item.outputPath)
			return false, errors.Join(err, removeErr)
		}
		return true, nil
	} else if !errors.Is(err, os.ErrExist) {
		return false, err
	}
	info, err := os.Lstat(item.outputPath)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("existing restored import is not a regular file")
	}
	checksum, err := fileChecksum(ctx, item.outputPath)
	if err != nil {
		return false, err
	}
	if !strings.EqualFold(checksum, item.checksum) {
		return false, errors.New("existing restored import does not match its content address")
	}
	if err := os.Remove(item.stagedPath); err != nil {
		return false, err
	}
	return false, nil
}

func removePublishedRestoreWorkspace(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func removePublishedImports(imports []stagedImport) []error {
	var removeErrors []error
	outputPaths := make([]string, 0, len(imports))
	for _, item := range imports {
		outputPaths = append(outputPaths, item.outputPath)
		if err := os.Remove(item.outputPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			removeErrors = append(removeErrors, fmt.Errorf("remove restored import %d: %w", item.id, err))
		}
	}
	if err := syncParentDirectories(outputPaths...); err != nil {
		removeErrors = append(removeErrors, err)
	}
	return removeErrors
}

type fileMove struct {
	source string
	target string
}

func publishRestoredDatabase(stagedPath, databasePath, workspace string, preservePrevious bool) error {
	suffixes := []string{"", "-wal", "-shm"}
	exists, err := pathExists(databasePath)
	if err != nil {
		return fmt.Errorf("inspect current database: %w", err)
	}
	if !exists {
		for _, suffix := range suffixes[1:] {
			sidecarPath := databasePath + suffix
			sidecarExists, err := pathExists(sidecarPath)
			if err != nil {
				return fmt.Errorf("inspect destination database sidecar: %w", err)
			}
			if sidecarExists {
				return fmt.Errorf("destination database is missing but sidecar %q exists", sidecarPath)
			}
		}
		if err := os.Rename(stagedPath, databasePath); err != nil {
			return fmt.Errorf("publish restored database: %w", err)
		}
		if err := syncDirectory(filepath.Dir(databasePath)); err != nil {
			rollbackError := os.Rename(databasePath, stagedPath)
			syncError := syncParentDirectories(databasePath, stagedPath)
			if rollbackError != nil {
				rollbackError = fmt.Errorf("remove new database: %w", rollbackError)
			}
			return errors.Join(
				fmt.Errorf("sync restored database directory: %w", err),
				rollbackError,
				syncError,
			)
		}
		return nil
	}

	previousPath := databasePath + ".before-restore"
	displacedDirectory, err := os.MkdirTemp(workspace, "displaced-")
	if err != nil {
		return fmt.Errorf("create displaced database workspace: %w", err)
	}
	var savedPrevious []fileMove
	var savedCurrent []fileMove
	published := false
	rollback := func(cause error) error {
		var rollbackErrors []error
		if published {
			if err := os.Rename(databasePath, stagedPath); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("remove new database: %w", err))
			}
		}
		rollbackErrors = append(rollbackErrors, reverseMoves(savedCurrent)...)
		rollbackErrors = append(rollbackErrors, reverseMoves(savedPrevious)...)
		if err := syncParentDirectories(databasePath, stagedPath); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
		return errors.Join(append([]error{cause}, rollbackErrors...)...)
	}

	if !preservePrevious {
		for i, suffix := range suffixes {
			source := previousPath + suffix
			exists, err := pathExists(source)
			if err != nil {
				return rollback(fmt.Errorf("inspect previous restore backup: %w", err))
			}
			if !exists {
				continue
			}
			target := filepath.Join(displacedDirectory, fmt.Sprintf("database-%d", i))
			if err := os.Rename(source, target); err != nil {
				return rollback(fmt.Errorf("preserve previous restore backup: %w", err))
			}
			savedPrevious = append(savedPrevious, fileMove{source: source, target: target})
		}
	}
	for i, suffix := range suffixes {
		source := databasePath + suffix
		exists, err := pathExists(source)
		if err != nil {
			return rollback(fmt.Errorf("inspect current database sidecar: %w", err))
		}
		if !exists {
			continue
		}
		target := previousPath + suffix
		if preservePrevious {
			target = filepath.Join(displacedDirectory, fmt.Sprintf("current-%d", i))
		}
		if err := os.Rename(source, target); err != nil {
			return rollback(fmt.Errorf("preserve current database: %w", err))
		}
		savedCurrent = append(savedCurrent, fileMove{source: source, target: target})
	}
	if err := os.Rename(stagedPath, databasePath); err != nil {
		return rollback(fmt.Errorf("publish restored database: %w", err))
	}
	published = true
	if err := syncDirectory(filepath.Dir(databasePath)); err != nil {
		return rollback(fmt.Errorf("sync restored database directory: %w", err))
	}
	return nil
}

func reverseMoves(moves []fileMove) []error {
	var moveErrors []error
	for i := len(moves) - 1; i >= 0; i-- {
		move := moves[i]
		if err := os.Rename(move.target, move.source); err != nil {
			moveErrors = append(moveErrors, fmt.Errorf("restore %q: %w", move.source, err))
		}
	}
	return moveErrors
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}
