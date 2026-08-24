package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tomasmik/goi/internal/contextio"
)

const (
	maxChecksumFileBytes          int64 = 4 << 10
	maxPendingImportManifestBytes int64 = 16 << 20
)

var (
	errFileTooLarge         = errors.New("file is too large")
	errIntegrityCheckFailed = errors.New("database integrity check failed")
	errInvalidChecksum      = errors.New("backup checksum is invalid")
)

func requireRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	return nil
}

func validateBackupPaths(sourcePath, outputPath string, includeImports bool) error {
	databaseArtifacts := []string{sourcePath, sourcePath + "-wal", sourcePath + "-shm", sourcePath + ".lock"}
	outputArtifacts := []string{outputPath, outputPath + ".sha256", outputPath + ".backup.lock"}
	if includeImports {
		outputArtifacts = append(outputArtifacts, outputPath+".imports")
	}
	for _, databaseArtifact := range databaseArtifacts {
		for _, outputArtifact := range outputArtifacts {
			if err := requireDifferentPaths(databaseArtifact, outputArtifact); err != nil {
				return fmt.Errorf("backup output overlaps database artifact: %w", err)
			}
		}
	}
	if !includeImports {
		return nil
	}
	importsPath := outputPath + ".imports"
	containsSource, err := pathWithinDirectory(importsPath, sourcePath)
	if err != nil {
		return err
	}
	if containsSource {
		return errors.New("pending import backup directory contains the source database")
	}
	return nil
}

func validateRestorePaths(backupPath, databasePath string) error {
	previousPath := databasePath + ".before-restore"
	artifactPaths := []string{
		databasePath,
		databasePath + "-wal",
		databasePath + "-shm",
		databasePath + ".lock",
		previousPath,
		previousPath + "-wal",
		previousPath + "-shm",
	}
	backupArtifacts := []string{backupPath, backupPath + ".sha256", backupPath + ".backup.lock"}
	existingArtifacts := make(map[string]bool, len(artifactPaths))
	for _, artifactPath := range artifactPaths {
		for _, backupArtifact := range backupArtifacts {
			if err := requireDifferentPaths(backupArtifact, artifactPath); err != nil {
				return fmt.Errorf("restore destination overlaps backup artifact: %w", err)
			}
		}
		insideImports, err := pathWithinDirectory(backupPath+".imports", artifactPath)
		if err != nil {
			return err
		}
		if insideImports {
			return errors.New("restore destination overlaps pending import backup")
		}
		info, err := os.Lstat(artifactPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("restore artifact %q is not a regular file", artifactPath)
		}
		existingArtifacts[artifactPath] = true
	}
	if !existingArtifacts[databasePath] && (existingArtifacts[databasePath+"-wal"] || existingArtifacts[databasePath+"-shm"]) {
		return errors.New("restore destination has SQLite sidecars without a database")
	}
	return nil
}

func requireDifferentPaths(firstPath, secondPath string) error {
	firstResolved, err := resolvedPath(firstPath)
	if err != nil {
		return fmt.Errorf("resolve first path: %w", err)
	}
	secondResolved, err := resolvedPath(secondPath)
	if err != nil {
		return fmt.Errorf("resolve second path: %w", err)
	}
	if firstResolved == secondResolved {
		return errors.New("paths refer to the same file")
	}

	firstInfo, err := os.Stat(firstPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect first path: %w", err)
	}
	secondInfo, err := os.Stat(secondPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect second path: %w", err)
	}
	if os.SameFile(firstInfo, secondInfo) {
		return errors.New("paths refer to the same file")
	}
	return nil
}

func pathWithinDirectory(directory, path string) (bool, error) {
	directoryResolved, err := resolvedPath(directory)
	if err != nil {
		return false, err
	}
	pathResolved, err := resolvedPath(path)
	if err != nil {
		return false, err
	}
	relativePath, err := filepath.Rel(directoryResolved, pathResolved)
	if err != nil {
		return false, err
	}
	return relativePath == "." || (relativePath != ".." && !strings.HasPrefix(relativePath, ".."+string(filepath.Separator))), nil
}

func resolvedPath(path string) (string, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	missingParts := make([]string, 0)
	currentPath := absolutePath
	for {
		resolvedPath, err := filepath.EvalSymlinks(currentPath)
		if err == nil {
			for index := len(missingParts) - 1; index >= 0; index-- {
				resolvedPath = filepath.Join(resolvedPath, missingParts[index])
			}
			return filepath.Clean(resolvedPath), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(currentPath)
		if parent == currentPath {
			return filepath.Clean(absolutePath), nil
		}
		missingParts = append(missingParts, filepath.Base(currentPath))
		currentPath = parent
	}
}

func PruneBackups(directory string, keep int, protectedPath string) error {
	if keep < 1 {
		return errors.New("backup retention must be at least one")
	}
	protectedPath, err := resolvedPath(protectedPath)
	if err != nil {
		return fmt.Errorf("resolve protected database path: %w", err)
	}
	protectedInfo, err := os.Stat(protectedPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect protected database path: %w", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read backup directory: %w", err)
	}
	type backupFile struct {
		path    string
		modTime time.Time
		info    os.FileInfo
	}
	backups := make([]backupFile, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sqlite") {
			continue
		}
		candidatePath := filepath.Join(directory, entry.Name())
		candidateResolved, err := resolvedPath(candidatePath)
		if err != nil {
			return fmt.Errorf("resolve backup path %q: %w", entry.Name(), err)
		}
		candidateInfo, err := os.Stat(candidatePath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect backup %q: %w", entry.Name(), err)
		}
		if !candidateInfo.Mode().IsRegular() {
			continue
		}
		if candidateResolved == protectedPath || (protectedInfo != nil && os.SameFile(protectedInfo, candidateInfo)) {
			continue
		}
		if _, err := os.Stat(candidatePath + ".sha256"); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("inspect backup checksum %q: %w", entry.Name(), err)
		}
		isGoiDatabase, err := hasGoiApplicationID(candidatePath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect backup application ID %q: %w", entry.Name(), err)
		}
		if !isGoiDatabase {
			continue
		}
		backups = append(backups, backupFile{path: candidatePath, modTime: candidateInfo.ModTime(), info: candidateInfo})
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].modTime.After(backups[j].modTime) })
	if len(backups) <= keep {
		return nil
	}
	var pruneErrors []error
	for _, backup := range backups[keep:] {
		lock, err := acquireBackupOutputLock(backup.path)
		if errors.Is(err, errLockBusy) {
			continue
		}
		if err != nil {
			pruneErrors = append(pruneErrors, fmt.Errorf("lock old backup %q: %w", backup.path, err))
			continue
		}
		currentInfo, err := os.Stat(backup.path)
		if errors.Is(err, os.ErrNotExist) || (err == nil && !os.SameFile(backup.info, currentInfo)) {
			if closeErr := lock.Close(); closeErr != nil {
				pruneErrors = append(pruneErrors, fmt.Errorf("close old backup lock %q: %w", backup.path, closeErr))
			}
			continue
		}
		if err != nil {
			pruneErrors = append(pruneErrors, fmt.Errorf("recheck old backup %q: %w", backup.path, err))
			if closeErr := lock.Close(); closeErr != nil {
				pruneErrors = append(pruneErrors, fmt.Errorf("close old backup lock %q: %w", backup.path, closeErr))
			}
			continue
		}
		if err := os.Remove(backup.path); err != nil {
			pruneErrors = append(pruneErrors, fmt.Errorf("remove old backup %q: %w", backup.path, err))
		} else {
			if err := os.Remove(backup.path + ".sha256"); err != nil && !errors.Is(err, os.ErrNotExist) {
				pruneErrors = append(pruneErrors, fmt.Errorf("remove old backup checksum %q: %w", backup.path, err))
			}
			if err := os.RemoveAll(backup.path + ".imports"); err != nil {
				pruneErrors = append(pruneErrors, fmt.Errorf("remove old pending import backup %q: %w", backup.path, err))
			}
		}
		if err := lock.Close(); err != nil {
			pruneErrors = append(pruneErrors, fmt.Errorf("close old backup lock %q: %w", backup.path, err))
		}
	}
	if err := syncDirectory(directory); err != nil {
		pruneErrors = append(pruneErrors, fmt.Errorf("sync backup directory: %w", err))
	}
	return errors.Join(pruneErrors...)
}

func hasGoiApplicationID(path string) (matches bool, returnErr error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, file.Close())
	}()

	// SQLite stores application_id as a big-endian uint32 at byte 68.
	const applicationIDOffset = 68
	var header [applicationIDOffset + 4]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
		return false, err
	}
	if string(header[:16]) != "SQLite format 3\x00" {
		return false, nil
	}
	return binary.BigEndian.Uint32(header[applicationIDOffset:]) == uint32(applicationID), nil
}

func IntegrityCheck(ctx context.Context, db *sql.DB) error {
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("run integrity check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("%w: integrity check returned %q", errIntegrityCheckFailed, result)
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("run foreign key check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowID any
		var foreignKeyID int
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			return fmt.Errorf("scan foreign key check: %w", err)
		}
		return fmt.Errorf("%w: foreign key check failed for table %q row %v referencing %q (constraint %d)", errIntegrityCheckFailed, table, rowID, parent, foreignKeyID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate foreign key check: %w", err)
	}
	return nil
}

func fileChecksum(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open backup for checksum: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := contextio.Copy(ctx, hash, file); err != nil {
		return "", fmt.Errorf("hash backup: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func verifyChecksum(actual, checksumPath string) error {
	contents, err := readFileAtMost(checksumPath, maxChecksumFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: backup checksum is missing", errInvalidChecksum)
	}
	if err != nil {
		if errors.Is(err, errFileTooLarge) {
			return fmt.Errorf("%w: %w", errInvalidChecksum, err)
		}
		return fmt.Errorf("read backup checksum: %w", err)
	}
	parts := strings.Fields(string(contents))
	if len(parts) == 0 {
		return fmt.Errorf("%w: backup checksum is empty", errInvalidChecksum)
	}
	if !strings.EqualFold(actual, parts[0]) {
		return fmt.Errorf("%w: backup checksum does not match", errInvalidChecksum)
	}
	return nil
}

func readFileAtMost(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if int64(len(contents)) > limit {
		return nil, fmt.Errorf("%w: file exceeds the %d byte limit", errFileTooLarge, limit)
	}
	return contents, nil
}

func copyFile(ctx context.Context, sourcePath, destinationPath string) (checksum string, returnErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := source.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close source file: %w", err))
		}
	}()
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return "", err
	}
	complete := false
	closed := false
	defer func() {
		if !complete {
			var cleanupErrors []error
			if !closed {
				if err := destination.Close(); err != nil {
					cleanupErrors = append(cleanupErrors, fmt.Errorf("close incomplete destination file: %w", err))
				}
			}
			if err := os.Remove(destinationPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("remove incomplete destination file: %w", err))
			}
			returnErr = errors.Join(returnErr, errors.Join(cleanupErrors...))
		}
	}()
	hash := sha256.New()
	if _, err := contextio.Copy(ctx, io.MultiWriter(destination, hash), source); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := destination.Sync(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	closeErr := destination.Close()
	closed = true
	if closeErr != nil {
		return "", closeErr
	}
	if err := syncDirectory(filepath.Dir(destinationPath)); err != nil {
		return "", err
	}
	complete = true
	return hex.EncodeToString(hash.Sum(nil)), nil
}
