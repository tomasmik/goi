package backups

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tomasmik/goi/internal/contextio"
	"github.com/tomasmik/goi/internal/database"
)

const (
	pendingRestoreName      = "pending-restore" + BundleSuffix
	restoreQueueLockName    = "restore-queue.lock"
	restoreReceiptName      = "restore-receipt.json"
	restorePrepared         = "prepared"
	restoreApplied          = "applied"
	maxReceiptWorkspaces    = 64
	maxReceiptWorkspaceName = 128
	maxRestoreReceiptSize   = 4 << 10
)

var restoreQueueMu sync.Mutex

var (
	ErrPendingRestoreRetry = errors.New("pending restore must be retried before the server starts")
	ErrRestoreQueueBusy    = errors.New("restore queue is busy in another process")
)

type LocalFile struct {
	Name      string
	Size      int64
	CreatedAt time.Time
}

type RestoreStatus struct {
	State      string    `json:"state"`
	OccurredAt time.Time `json:"occurred_at"`
	Message    string    `json:"message"`
}

type databaseArtifactFingerprint struct {
	Exists bool   `json:"exists"`
	SHA256 string `json:"sha256"`
}

type restoreReceipt struct {
	State                      string                      `json:"state"`
	BundleSHA256               string                      `json:"bundle_sha256"`
	OriginalDatabase           databaseArtifactFingerprint `json:"original_database"`
	OriginalPrevious           databaseArtifactFingerprint `json:"original_previous"`
	WorkspaceBaseline          bool                        `json:"workspace_baseline"`
	ExistingDatabaseWorkspaces []string                    `json:"existing_database_workspaces,omitempty"`
	ExistingImportWorkspaces   []string                    `json:"existing_import_workspaces,omitempty"`
	PreparedAt                 time.Time                   `json:"prepared_at"`
	AppliedAt                  time.Time                   `json:"applied_at,omitempty"`
}

func PrepareLocalDirectory(path string) error {
	if err := os.MkdirAll(path, 0o750); err != nil {
		return fmt.Errorf("create local backup directory: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect local backup directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("local backup path is not a directory")
	}
	probe, err := os.CreateTemp(path, ".goi-write-test-*")
	if err != nil {
		return fmt.Errorf("local backup directory is not writable: %w", err)
	}
	probePath := probe.Name()
	if err := errors.Join(probe.Close(), os.Remove(probePath)); err != nil {
		return fmt.Errorf("clean local backup write test: %w", err)
	}
	return nil
}

func ListLocal(backupDir string) ([]LocalFile, error) {
	entries, err := os.ReadDir(backupDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read local backups: %w", err)
	}
	files := make([]LocalFile, 0, len(entries))
	for _, entry := range entries {
		if !validLocalName(entry.Name()) || entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect local backup %q: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		files = append(files, LocalFile{Name: entry.Name(), Size: info.Size(), CreatedAt: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].CreatedAt.Equal(files[j].CreatedAt) {
			return files[i].Name > files[j].Name
		}
		return files[i].CreatedAt.After(files[j].CreatedAt)
	})
	return files, nil
}

func LocalPath(backupDir, name string) (string, error) {
	if !validLocalName(name) {
		return "", errors.New("invalid backup filename")
	}
	path := filepath.Join(backupDir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("backup is not a regular file")
	}
	return path, nil
}

func QueueRestore(ctx context.Context, dataDir string, source io.Reader) error {
	restoreQueueMu.Lock()
	defer restoreQueueMu.Unlock()

	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	queueLock, err := acquireRestoreQueueLock(dataDir)
	if err != nil {
		return err
	}
	defer queueLock.Close()
	if _, exists, err := readRestoreReceipt(dataDir); err != nil {
		return err
	} else if exists {
		return errors.New("the previous restore is still being finalized; restart Goi before queuing another")
	}
	pendingPath := filepath.Join(dataDir, pendingRestoreName)
	if _, err := os.Lstat(pendingPath); err == nil {
		return errors.New("a restore is already waiting for restart")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect pending restore: %w", err)
	}

	temporary, err := os.CreateTemp(dataDir, ".pending-restore-*.zip")
	if err != nil {
		return fmt.Errorf("create restore upload: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	limited := &io.LimitedReader{R: source, N: MaxBundleBytes + 1}
	written, copyErr := contextio.Copy(ctx, temporary, limited)
	closeErr := temporary.Close()
	if copyErr != nil || closeErr != nil {
		return fmt.Errorf("save restore upload: %w", errors.Join(copyErr, closeErr))
	}
	if written > MaxBundleBytes {
		return fmt.Errorf("backup bundle exceeds %d bytes", MaxBundleBytes)
	}
	if err := os.Chmod(temporaryPath, 0o640); err != nil {
		return fmt.Errorf("secure restore upload: %w", err)
	}
	if err := syncFile(temporaryPath); err != nil {
		return fmt.Errorf("sync restore upload: %w", err)
	}
	if err := ValidateBundle(ctx, temporaryPath); err != nil {
		return fmt.Errorf("validate restore upload: %w", err)
	}
	if err := linkRestoreFile(temporaryPath, pendingPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("a restore is already waiting for restart")
		}
		return fmt.Errorf("queue restore: %w", err)
	}
	if err := os.Remove(temporaryPath); err != nil {
		removeErr := os.Remove(pendingPath)
		rollbackSyncErr := syncDirectory(dataDir)
		return fmt.Errorf("remove restore upload workspace: %w", errors.Join(err, removeErr, rollbackSyncErr))
	}
	if err := syncDirectory(dataDir); err != nil {
		removeErr := os.Remove(pendingPath)
		rollbackSyncErr := syncDirectory(dataDir)
		return fmt.Errorf("sync queued restore: %w", errors.Join(err, removeErr, rollbackSyncErr))
	}
	return nil
}

func CancelPendingRestore(dataDir string) error {
	restoreQueueMu.Lock()
	defer restoreQueueMu.Unlock()

	queueLock, err := acquireRestoreQueueLock(dataDir)
	if err != nil {
		return err
	}
	defer queueLock.Close()
	if _, exists, err := readRestoreReceipt(dataDir); err != nil {
		return err
	} else if exists {
		return errors.New("the restore is still being finalized; restart Goi to finish it")
	}
	path := filepath.Join(dataDir, pendingRestoreName)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove pending restore: %w", err)
	}
	if err := syncDirectory(dataDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sync data directory: %w", err)
	}
	return writeRestoreStatus(dataDir, RestoreStatus{})
}

func ApplyPendingRestore(ctx context.Context, dataDir, databasePath string, now time.Time) (bool, error) {
	return applyPendingRestore(ctx, dataDir, databasePath, now, replaceRestoreReceipt)
}

func applyPendingRestore(
	ctx context.Context,
	dataDir, databasePath string,
	now time.Time,
	markApplied func(string, restoreReceipt) error,
) (bool, error) {
	restoreQueueMu.Lock()
	defer restoreQueueMu.Unlock()

	queueLock, err := acquireRestoreQueueLock(dataDir)
	if err != nil {
		return false, retryPendingRestore(err)
	}
	defer queueLock.Close()
	receipt, receiptExists, err := readRestoreReceipt(dataDir)
	if err != nil {
		return false, retryPendingRestore(fmt.Errorf("read restore receipt: %w", err))
	}
	pendingPath := filepath.Join(dataDir, pendingRestoreName)
	pendingExists := true
	if _, statErr := os.Lstat(pendingPath); errors.Is(statErr, os.ErrNotExist) {
		pendingExists = false
		if receiptExists {
			if receipt.State != restoreApplied {
				return false, retryPendingRestore(errors.New("prepared restore receipt has no pending bundle"))
			}
		} else {
			if err := syncDirectory(dataDir); err != nil {
				return false, retryPendingRestore(fmt.Errorf("sync restore queue state: %w", err))
			}
			return false, nil
		}
	} else if statErr != nil {
		return false, retryPendingRestore(fmt.Errorf("inspect pending restore: %w", statErr))
	}
	bundleChecksum := ""
	if pendingExists {
		bundleChecksum, err = restoreFileChecksum(ctx, pendingPath)
		if err != nil {
			return false, retryPendingRestore(fmt.Errorf("checksum pending restore: %w", err))
		}
		if receiptExists && !strings.EqualFold(receipt.BundleSHA256, bundleChecksum) {
			return false, retryPendingRestore(errors.New("restore receipt does not match the pending bundle"))
		}
	}

	databaseLock, err := database.AcquireLock(databasePath, true)
	if err != nil {
		return false, retryPendingRestore(err)
	}
	defer databaseLock.Close()
	importsPath := filepath.Join(dataDir, "imports")
	if receiptExists && receipt.State == restoreApplied {
		if err := cleanupReceiptRestoreWorkspaces(databasePath, importsPath, receipt); err != nil {
			return true, fmt.Errorf("clean applied restore workspaces: %w", err)
		}
		return true, finishAppliedRestore(dataDir, pendingPath, receipt)
	}

	preservePrevious := false
	if receiptExists {
		preservePrevious, err = restoreReplayMode(ctx, databasePath, receipt)
		if err != nil {
			return false, retryPendingRestore(err)
		}
	} else {
		receipt, err = prepareRestoreReceipt(ctx, databasePath, importsPath, bundleChecksum, now)
		if err != nil {
			return false, retryPendingRestore(fmt.Errorf("prepare restore receipt: %w", err))
		}
		if err := createRestoreReceipt(dataDir, receipt); err != nil {
			return false, retryPendingRestore(fmt.Errorf("record prepared restore: %w", err))
		}
	}

	err = restoreBundleUnderLock(
		ctx,
		pendingPath,
		databasePath,
		importsPath,
		databaseLock,
		preservePrevious,
	)
	if err != nil {
		if !errors.Is(err, ErrInvalidBundle) {
			return false, retryPendingRestore(fmt.Errorf("apply pending restore: %w", err))
		}
		if removeErr := removeRestoreReceipt(dataDir); removeErr != nil {
			return false, retryPendingRestore(fmt.Errorf("clear prepared restore receipt: %w", removeErr))
		}
		return false, quarantineInvalidRestore(dataDir, pendingPath, now, err)
	}
	receipt.State = restoreApplied
	receipt.AppliedAt = now.UTC()
	if err := markApplied(dataDir, receipt); err != nil {
		return true, fmt.Errorf("record applied restore: %w", err)
	}
	if err := cleanupReceiptRestoreWorkspaces(databasePath, importsPath, receipt); err != nil {
		return true, fmt.Errorf("clean applied restore workspaces: %w", err)
	}
	return true, finishAppliedRestore(dataDir, pendingPath, receipt)
}

func prepareRestoreReceipt(
	ctx context.Context,
	databasePath, importsPath, bundleChecksum string,
	now time.Time,
) (restoreReceipt, error) {
	originalDatabase, err := fingerprintDatabaseArtifacts(ctx, databasePath)
	if err != nil {
		return restoreReceipt{}, fmt.Errorf("fingerprint current database: %w", err)
	}
	originalPrevious, err := fingerprintDatabaseArtifacts(ctx, databasePath+".before-restore")
	if err != nil {
		return restoreReceipt{}, fmt.Errorf("fingerprint previous database: %w", err)
	}
	existingDatabaseWorkspaces, err := listRestoreWorkspaces(filepath.Dir(databasePath), maxReceiptWorkspaces)
	if err != nil {
		return restoreReceipt{}, fmt.Errorf("list database restore workspaces: %w", err)
	}
	existingImportWorkspaces, err := listRestoreWorkspaces(importsPath, maxReceiptWorkspaces)
	if err != nil {
		return restoreReceipt{}, fmt.Errorf("list import restore workspaces: %w", err)
	}
	return restoreReceipt{
		State:                      restorePrepared,
		BundleSHA256:               bundleChecksum,
		OriginalDatabase:           originalDatabase,
		OriginalPrevious:           originalPrevious,
		WorkspaceBaseline:          true,
		ExistingDatabaseWorkspaces: existingDatabaseWorkspaces,
		ExistingImportWorkspaces:   existingImportWorkspaces,
		PreparedAt:                 now.UTC(),
	}, nil
}

func restoreReplayMode(ctx context.Context, databasePath string, receipt restoreReceipt) (bool, error) {
	current, err := fingerprintDatabaseArtifacts(ctx, databasePath)
	if err != nil {
		return false, fmt.Errorf("fingerprint current database: %w", err)
	}
	previous, err := fingerprintDatabaseArtifacts(ctx, databasePath+".before-restore")
	if err != nil {
		return false, fmt.Errorf("fingerprint previous database: %w", err)
	}
	if fingerprintsEqual(current, receipt.OriginalDatabase) {
		return false, nil
	}
	if receipt.OriginalDatabase.Exists && fingerprintsEqual(previous, receipt.OriginalDatabase) {
		return true, nil
	}
	if !receipt.OriginalDatabase.Exists && current.Exists && fingerprintsEqual(previous, receipt.OriginalPrevious) {
		return true, nil
	}
	return false, errors.New("database files do not match the prepared restore state")
}

func listRestoreWorkspaces(directory string, limit int) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	workspaces := make([]string, 0)
	for _, entry := range entries {
		if !validRestoreWorkspaceName(entry.Name()) {
			continue
		}
		info, err := os.Lstat(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			continue
		}
		workspaces = append(workspaces, entry.Name())
		if limit > 0 && len(workspaces) > limit {
			return nil, errors.New("too many restore workspaces")
		}
	}
	sort.Strings(workspaces)
	return workspaces, nil
}

func cleanupReceiptRestoreWorkspaces(databasePath, importsPath string, receipt restoreReceipt) error {
	if !receipt.WorkspaceBaseline {
		return nil
	}
	return errors.Join(
		cleanupNewRestoreWorkspaces(filepath.Dir(databasePath), receipt.ExistingDatabaseWorkspaces),
		cleanupNewRestoreWorkspaces(importsPath, receipt.ExistingImportWorkspaces),
	)
}

func cleanupNewRestoreWorkspaces(directory string, existing []string) error {
	workspaces, err := listRestoreWorkspaces(directory, 0)
	if err != nil {
		return err
	}
	preserved := make(map[string]struct{}, len(existing))
	for _, name := range existing {
		preserved[name] = struct{}{}
	}
	var cleanupErrors []error
	changed := false
	for _, name := range workspaces {
		if _, ok := preserved[name]; ok {
			continue
		}
		changed = true
		if err := os.RemoveAll(filepath.Join(directory, name)); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove restore workspace %q: %w", name, err))
		}
	}
	if changed {
		if err := syncDirectory(directory); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("sync restore workspace directory: %w", err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func validRestoreWorkspaceName(name string) bool {
	return len(name) > len(".restore-") && len(name) <= maxReceiptWorkspaceName &&
		filepath.Base(name) == name && strings.HasPrefix(name, ".restore-")
}

func quarantineInvalidRestore(dataDir, pendingPath string, now time.Time, restoreErr error) error {
	return quarantineInvalidRestoreWithSync(dataDir, pendingPath, now, restoreErr, syncDirectory)
}

func quarantineInvalidRestoreWithSync(
	dataDir, pendingPath string,
	now time.Time,
	restoreErr error,
	syncDir func(string) error,
) error {
	failedName := "failed-restore-" + now.UTC().Format("20060102T150405.000000000Z") + BundleSuffix
	failedPath := filepath.Join(dataDir, failedName)
	if err := os.Rename(pendingPath, failedPath); err != nil {
		return retryPendingRestore(errors.Join(
			fmt.Errorf("apply pending restore: %w", restoreErr),
			fmt.Errorf("quarantine invalid restore: %w", err),
		))
	}
	if err := syncDir(dataDir); err != nil {
		return retryPendingRestore(errors.Join(
			fmt.Errorf("apply pending restore: %w", restoreErr),
			fmt.Errorf("sync quarantined restore: %w", err),
		))
	}
	statusErr := writeRestoreStatus(dataDir, RestoreStatus{
		State:      "failed",
		OccurredAt: now.UTC(),
		Message:    restoreErr.Error(),
	})
	return errors.Join(fmt.Errorf("apply pending restore: %w", restoreErr), statusErr)
}

func finishAppliedRestore(dataDir, pendingPath string, receipt restoreReceipt) error {
	if receipt.State != restoreApplied || receipt.AppliedAt.IsZero() {
		return errors.New("restore receipt is not applied")
	}
	if err := os.Remove(pendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove applied restore bundle: %w", err)
	}
	if err := syncDirectory(dataDir); err != nil {
		return fmt.Errorf("sync applied restore: %w", err)
	}
	if err := writeRestoreStatus(dataDir, RestoreStatus{
		State:      "success",
		OccurredAt: receipt.AppliedAt,
		Message:    "Restore completed.",
	}); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dataDir, restoreReceiptName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove restore receipt: %w", err)
	}
	return syncDirectory(dataDir)
}

func ReadRestoreStatus(dataDir string) (RestoreStatus, error) {
	pendingPath := filepath.Join(dataDir, pendingRestoreName)
	if info, err := os.Lstat(pendingPath); err == nil {
		if !info.Mode().IsRegular() {
			return RestoreStatus{}, errors.New("pending restore is not a regular file")
		}
		return RestoreStatus{State: "pending", Message: "Restart Goi to apply the restore."}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return RestoreStatus{}, fmt.Errorf("inspect pending restore: %w", err)
	}
	contents, err := os.ReadFile(filepath.Join(dataDir, "restore-status.json"))
	if errors.Is(err, os.ErrNotExist) {
		return RestoreStatus{}, nil
	}
	if err != nil {
		return RestoreStatus{}, fmt.Errorf("read restore status: %w", err)
	}
	if len(contents) > 16<<10 {
		return RestoreStatus{}, errors.New("restore status is too large")
	}
	var status RestoreStatus
	if err := json.Unmarshal(contents, &status); err != nil {
		return RestoreStatus{}, fmt.Errorf("parse restore status: %w", err)
	}
	if status.State == "pending" {
		return RestoreStatus{}, nil
	}
	return status, nil
}

func PruneLocalBefore(backupDir string, cutoff time.Time) error {
	files, err := ListLocal(backupDir)
	if err != nil {
		return err
	}
	var pruneErrors []error
	removed := false
	for _, file := range files {
		if !file.CreatedAt.Before(cutoff) {
			continue
		}
		path, err := LocalPath(backupDir, file.Name)
		if err != nil {
			pruneErrors = append(pruneErrors, err)
			continue
		}
		if err := os.Remove(path); err != nil {
			pruneErrors = append(pruneErrors, fmt.Errorf("remove local backup %q: %w", file.Name, err))
			continue
		}
		removed = true
	}
	if removed {
		if err := syncDirectory(backupDir); err != nil {
			pruneErrors = append(pruneErrors, err)
		}
	}
	return errors.Join(pruneErrors...)
}

func writeRestoreStatus(dataDir string, status RestoreStatus) error {
	path := filepath.Join(dataDir, "restore-status.json")
	if status.State == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return syncDirectory(dataDir)
	}
	contents, err := json.Marshal(status)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dataDir, ".restore-status-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o640); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(dataDir)
}

func acquireRestoreQueueLock(dataDir string) (*database.Lock, error) {
	lock, err := database.AcquireExclusiveFileLock(filepath.Join(dataDir, restoreQueueLockName))
	if errors.Is(err, database.ErrFileLockInUse) {
		return nil, fmt.Errorf("%w: %w", ErrRestoreQueueBusy, err)
	}
	if err != nil {
		return nil, fmt.Errorf("lock restore queue: %w", err)
	}
	return lock, nil
}

func linkRestoreFile(sourcePath, destinationPath string) error {
	return os.Link(sourcePath, destinationPath)
}

func retryPendingRestore(err error) error {
	return fmt.Errorf("%w: %w", ErrPendingRestoreRetry, err)
}

func restoreFileChecksum(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := contextio.Copy(ctx, hash, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return "", errors.Join(copyErr, closeErr)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func fingerprintDatabaseArtifacts(ctx context.Context, basePath string) (databaseArtifactFingerprint, error) {
	hash := sha256.New()
	exists := false
	for index, suffix := range []string{"", "-wal", "-shm"} {
		if err := ctx.Err(); err != nil {
			return databaseArtifactFingerprint{}, err
		}
		path := basePath + suffix
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			hash.Write([]byte{byte(index), 0})
			continue
		}
		if err != nil {
			return databaseArtifactFingerprint{}, err
		}
		if !info.Mode().IsRegular() {
			return databaseArtifactFingerprint{}, fmt.Errorf("%q is not a regular file", path)
		}
		exists = true
		hash.Write([]byte{byte(index), 1})
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(info.Size()))
		hash.Write(size[:])
		file, err := os.Open(path)
		if err != nil {
			return databaseArtifactFingerprint{}, err
		}
		_, copyErr := contextio.Copy(ctx, hash, file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			return databaseArtifactFingerprint{}, errors.Join(copyErr, closeErr)
		}
	}
	return databaseArtifactFingerprint{
		Exists: exists,
		SHA256: hex.EncodeToString(hash.Sum(nil)),
	}, nil
}

func fingerprintsEqual(left, right databaseArtifactFingerprint) bool {
	return left.Exists == right.Exists && strings.EqualFold(left.SHA256, right.SHA256)
}

func readRestoreReceipt(dataDir string) (restoreReceipt, bool, error) {
	path := filepath.Join(dataDir, restoreReceiptName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return restoreReceipt{}, false, nil
	}
	if err != nil {
		return restoreReceipt{}, false, err
	}
	if !info.Mode().IsRegular() {
		return restoreReceipt{}, false, errors.New("restore receipt is not a regular file")
	}
	if info.Size() > maxRestoreReceiptSize {
		return restoreReceipt{}, false, errors.New("restore receipt is too large")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return restoreReceipt{}, false, err
	}
	var receipt restoreReceipt
	if err := json.Unmarshal(contents, &receipt); err != nil {
		return restoreReceipt{}, false, fmt.Errorf("parse restore receipt: %w", err)
	}
	if (receipt.State != restorePrepared && receipt.State != restoreApplied) ||
		!validSHA256(receipt.BundleSHA256) ||
		!validSHA256(receipt.OriginalDatabase.SHA256) ||
		!validSHA256(receipt.OriginalPrevious.SHA256) ||
		!validRestoreWorkspaceNames(receipt.ExistingDatabaseWorkspaces) ||
		!validRestoreWorkspaceNames(receipt.ExistingImportWorkspaces) ||
		receipt.PreparedAt.IsZero() ||
		(receipt.State == restorePrepared && !receipt.AppliedAt.IsZero()) ||
		(receipt.State == restoreApplied && receipt.AppliedAt.IsZero()) {
		return restoreReceipt{}, false, errors.New("restore receipt is invalid")
	}
	return receipt, true, nil
}

func validRestoreWorkspaceNames(names []string) bool {
	if len(names) > maxReceiptWorkspaces {
		return false
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !validRestoreWorkspaceName(name) {
			return false
		}
		if _, exists := seen[name]; exists {
			return false
		}
		seen[name] = struct{}{}
	}
	return true
}

func validSHA256(value string) bool {
	checksum, err := hex.DecodeString(value)
	return err == nil && len(checksum) == sha256.Size
}

func createRestoreReceipt(dataDir string, receipt restoreReceipt) error {
	temporaryPath, err := writeRestoreReceiptTemporary(dataDir, receipt)
	if err != nil {
		return err
	}
	defer os.Remove(temporaryPath)
	receiptPath := filepath.Join(dataDir, restoreReceiptName)
	if err := linkRestoreFile(temporaryPath, receiptPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("restore receipt already exists")
		}
		return err
	}
	if err := os.Remove(temporaryPath); err != nil {
		removeErr := os.Remove(receiptPath)
		rollbackSyncErr := syncDirectory(dataDir)
		return errors.Join(err, removeErr, rollbackSyncErr)
	}
	if err := syncDirectory(dataDir); err != nil {
		removeErr := os.Remove(receiptPath)
		rollbackSyncErr := syncDirectory(dataDir)
		return errors.Join(err, removeErr, rollbackSyncErr)
	}
	return nil
}

func replaceRestoreReceipt(dataDir string, receipt restoreReceipt) error {
	temporaryPath, err := writeRestoreReceiptTemporary(dataDir, receipt)
	if err != nil {
		return err
	}
	defer os.Remove(temporaryPath)
	if err := os.Rename(temporaryPath, filepath.Join(dataDir, restoreReceiptName)); err != nil {
		return err
	}
	return syncDirectory(dataDir)
}

func writeRestoreReceiptTemporary(dataDir string, receipt restoreReceipt) (string, error) {
	contents, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	if len(contents) > maxRestoreReceiptSize {
		return "", errors.New("restore receipt is too large")
	}
	temporary, err := os.CreateTemp(dataDir, ".restore-receipt-*.json")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Chmod(0o640); err != nil {
		temporary.Close()
		os.Remove(temporaryPath)
		return "", err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		os.Remove(temporaryPath)
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		os.Remove(temporaryPath)
		return "", err
	}
	if err := temporary.Close(); err != nil {
		os.Remove(temporaryPath)
		return "", err
	}
	return temporaryPath, nil
}

func removeRestoreReceipt(dataDir string) error {
	if err := os.Remove(filepath.Join(dataDir, restoreReceiptName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(dataDir)
}

func validLocalName(name string) bool {
	return filepath.Base(name) == name && strings.HasPrefix(name, "goi-") && strings.HasSuffix(name, BundleSuffix)
}
