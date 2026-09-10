package imports

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tomasmik/goi/internal/contextio"
)

const MaxArchiveBytes int64 = 1 << 30

const maxImportFilenameRunes = 300

type stagedArchive struct {
	id   int64
	path string
}

func (s *Store) Upload(ctx context.Context, file multipart.File, filename string) (int64, Preview, error) {
	filename, err := cleanImportFilename(filename)
	if err != nil {
		return 0, Preview{}, err
	}
	temporary, err := os.CreateTemp(s.stagingDir, "upload-*.apkg")
	if err != nil {
		return 0, Preview{}, fmt.Errorf("create staged Anki file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	written, err := contextio.Copy(ctx, temporary, io.LimitReader(file, MaxArchiveBytes+1))
	if err != nil {
		closeErr := temporary.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("close staged Anki file: %w", closeErr)
		}
		return 0, Preview{}, errors.Join(
			fmt.Errorf("stage Anki file: %w", err),
			closeErr,
		)
	}
	if written > MaxArchiveBytes {
		closeErr := temporary.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("close oversized Anki file: %w", closeErr)
		}
		return 0, Preview{}, errors.Join(
			invalidPackage(fmt.Sprintf("Anki package exceeds the %d byte limit", MaxArchiveBytes), nil),
			closeErr,
		)
	}
	if err := temporary.Sync(); err != nil {
		closeErr := temporary.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("close unsynced Anki file: %w", closeErr)
		}
		return 0, Preview{}, errors.Join(
			fmt.Errorf("sync staged Anki file: %w", err),
			closeErr,
		)
	}
	if err := temporary.Close(); err != nil {
		return 0, Preview{}, fmt.Errorf("close staged Anki file: %w", err)
	}
	preview, err := Inspect(ctx, temporaryPath)
	if err != nil {
		return 0, Preview{}, err
	}
	now := time.Now().UTC().Unix()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO import_runs (filename, archive_path, status, created_at)
		VALUES (?, '', 'previewed', ?)`, filename, now)
	if err != nil {
		return 0, Preview{}, fmt.Errorf("insert import run: %w", err)
	}
	runID, err := result.LastInsertId()
	if err != nil {
		return 0, Preview{}, fmt.Errorf("get import run ID: %w", err)
	}
	finalPath := s.runPath(runID)
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return 0, Preview{}, errors.Join(
			fmt.Errorf("publish staged Anki file: %w", err),
			s.cleanupFailedUpload(ctx, runID, ""),
		)
	}
	if err := syncDirectory(s.stagingDir); err != nil {
		return 0, Preview{}, errors.Join(
			fmt.Errorf("sync Anki staging directory: %w", err),
			s.cleanupFailedUpload(ctx, runID, finalPath),
		)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE import_runs SET archive_path = ? WHERE id = ?", finalPath, runID); err != nil {
		return 0, Preview{}, errors.Join(
			fmt.Errorf("save staged Anki path: %w", err),
			s.cleanupFailedUpload(ctx, runID, finalPath),
		)
	}
	return runID, preview, nil
}

func cleanImportFilename(filename string) (string, error) {
	if !utf8.ValidString(filename) {
		return "", invalidPackage("The Anki package filename must be valid UTF-8.", nil)
	}
	filename = strings.TrimSpace(filepath.Base(filename))
	if filename == "" || filename == "." || filename == string(filepath.Separator) {
		return "", invalidPackage("The Anki package must have a filename.", nil)
	}
	if utf8.RuneCountInString(filename) > maxImportFilenameRunes {
		return "", invalidPackage(fmt.Sprintf("The Anki package filename must be at most %d characters.", maxImportFilenameRunes), nil)
	}
	for _, character := range filename {
		if unicode.IsControl(character) {
			return "", invalidPackage("The Anki package filename contains a control character.", nil)
		}
	}
	return filename, nil
}

func (s *Store) cleanupFailedUpload(ctx context.Context, runID int64, archivePath string) error {
	ctx = context.WithoutCancel(ctx)
	var cleanupErrors []error
	if archivePath != "" {
		if err := os.Remove(archivePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove failed Anki archive %d: %w", runID, err))
		}
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM import_runs WHERE id = ?", runID); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("remove failed Anki import run %d: %w", runID, err))
	}
	return errors.Join(cleanupErrors...)
}

func (s *Store) cleanupAppliedArchive(ctx context.Context, runID int64, archivePath string) error {
	if err := os.Remove(archivePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove applied Anki archive: %w", err)
	}
	if _, err := s.db.ExecContext(context.WithoutCancel(ctx), `
		UPDATE import_runs SET archive_path = ''
		WHERE id = ? AND status = 'applied' AND archive_path = ?`, runID, archivePath); err != nil {
		return fmt.Errorf("clear applied Anki archive path: %w", err)
	}
	return nil
}

func (s *Store) cleanupStaging(ctx context.Context) error {
	cutoff := time.Now().Add(-24 * time.Hour)
	stale, err := s.staleArchives(ctx, cutoff)
	if err != nil {
		return err
	}
	for _, item := range stale {
		if archivePath, ok := s.resolveRunPath(item.id, item.path); ok {
			if err := os.Remove(archivePath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove stale Anki archive %d: %w", item.id, err)
			}
		}
		if _, err := s.db.ExecContext(ctx, "DELETE FROM import_runs WHERE id = ?", item.id); err != nil {
			return fmt.Errorf("remove stale Anki import %d: %w", item.id, err)
		}
	}
	if err := s.clearMissingAppliedArchives(ctx); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.stagingDir)
	if err != nil {
		return fmt.Errorf("read Anki staging directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasPrefix(entry.Name(), "upload-") && !strings.HasPrefix(entry.Name(), "run-")) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect staged Anki file: %w", err)
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		archivePath := filepath.Join(s.stagingDir, entry.Name())
		status := ""
		if strings.HasPrefix(entry.Name(), "run-") {
			err := s.db.QueryRowContext(ctx, `
				SELECT status FROM import_runs WHERE archive_path = ? LIMIT 1`, archivePath).Scan(&status)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("check staged Anki file: %w", err)
			}
			if err == nil && status != "applied" {
				continue
			}
		}
		if err := os.Remove(archivePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale staged Anki file: %w", err)
		}
		if status == "applied" {
			if _, err := s.db.ExecContext(ctx, `
				UPDATE import_runs SET archive_path = ''
				WHERE status = 'applied' AND archive_path = ?`, archivePath); err != nil {
				return fmt.Errorf("clear stale applied Anki path: %w", err)
			}
		}
	}
	return nil
}

func (s *Store) staleArchives(ctx context.Context, cutoff time.Time) ([]stagedArchive, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, archive_path
		FROM import_runs
		WHERE status IN ('previewed', 'applied', 'failed')
		  AND COALESCE(completed_at, created_at) < ?`, cutoff.Unix())
	if err != nil {
		return nil, fmt.Errorf("find stale Anki imports: %w", err)
	}
	defer rows.Close()

	var stale []stagedArchive
	for rows.Next() {
		var item stagedArchive
		if err := rows.Scan(&item.id, &item.path); err != nil {
			return nil, fmt.Errorf("scan stale Anki import: %w", err)
		}
		stale = append(stale, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stale Anki imports: %w", err)
	}
	return stale, nil
}

func (s *Store) clearMissingAppliedArchives(ctx context.Context) error {
	archives, err := s.appliedArchives(ctx)
	if err != nil {
		return err
	}

	for _, archive := range archives {
		resolvedPath, valid := s.resolveRunPath(archive.id, archive.path)
		if valid {
			if _, err := os.Stat(resolvedPath); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("inspect applied Anki archive %d: %w", archive.id, err)
			}
		}
		if _, err := s.db.ExecContext(ctx, `
			UPDATE import_runs SET archive_path = ''
			WHERE id = ? AND status = 'applied' AND archive_path = ?`, archive.id, archive.path); err != nil {
			return fmt.Errorf("clear missing applied Anki archive %d: %w", archive.id, err)
		}
	}
	return nil
}

func (s *Store) appliedArchives(ctx context.Context) ([]stagedArchive, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, archive_path
		FROM import_runs
		WHERE status = 'applied' AND archive_path <> ''`)
	if err != nil {
		return nil, fmt.Errorf("find applied Anki archives: %w", err)
	}
	defer rows.Close()

	var archives []stagedArchive
	for rows.Next() {
		var archive stagedArchive
		if err := rows.Scan(&archive.id, &archive.path); err != nil {
			return nil, fmt.Errorf("scan applied Anki archive: %w", err)
		}
		archives = append(archives, archive)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied Anki archives: %w", err)
	}
	return archives, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		return errors.Join(err, directory.Close())
	}
	return directory.Close()
}

func (s *Store) runPath(runID int64) string {
	return filepath.Join(s.stagingDir, fmt.Sprintf("run-%d.apkg", runID))
}

func (s *Store) resolveRunPath(runID int64, archivePath string) (string, bool) {
	if archivePath == "" {
		return "", false
	}
	absolutePath, err := filepath.Abs(archivePath)
	if err != nil {
		return "", false
	}
	absolutePath = filepath.Clean(absolutePath)
	if filepath.Dir(absolutePath) != s.stagingDir {
		return "", false
	}

	name := filepath.Base(absolutePath)
	canonicalName := fmt.Sprintf("run-%d.apkg", runID)
	if name == canonicalName {
		return absolutePath, true
	}
	restoredPrefix := fmt.Sprintf("run-%d-restored-", runID)
	restoredToken, ok := strings.CutPrefix(name, restoredPrefix)
	if !ok || !strings.HasSuffix(restoredToken, ".apkg") {
		return "", false
	}
	restoredToken = strings.TrimSuffix(restoredToken, ".apkg")
	if restoredToken == "" {
		return "", false
	}
	return absolutePath, true
}
