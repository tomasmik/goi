package imports

import (
	"archive/zip"
	"bytes"
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

	"golang.org/x/net/html"

	"github.com/tomasmik/goi/internal/contextio"
	"github.com/tomasmik/goi/internal/textnorm"
	"github.com/tomasmik/goi/internal/vocabulary"
)

const MaxArchiveBytes int64 = 1 << 30

const maxImportFilenameRunes = 300

type Store struct {
	db         *sql.DB
	stagingDir string
	vocabulary *vocabulary.Store
}

type Note struct {
	ID      int64
	ModelID int64
	Fields  []string
}

type Preview struct {
	Fields     []string
	Notes      []Note
	MediaCount int
	ModelCount int
}

type Mapping struct {
	ExpressionField    int  `json:"expression_field"`
	PronunciationField int  `json:"pronunciation_field"`
	MeaningField       int  `json:"meaning_field"`
	NotesField         int  `json:"notes_field"`
	ExampleField       int  `json:"example_field"`
	TranslationField   int  `json:"translation_field"`
	AudioField         int  `json:"audio_field"`
	PictureField       int  `json:"picture_field"`
	KnownElsewhere     bool `json:"known_elsewhere"`
	AllowDuplicate     bool `json:"allow_duplicate"`
	ExtendedFields     bool `json:"extended_fields"`
}

type ApplyResult struct {
	Created       int
	Skipped       int
	Failed        int
	Errors        []string
	OmittedErrors int
	Retryable     bool
}

type ErrorReportRow struct {
	Action  string
	Message string
}

const applyResultErrorLimit = 25

func (result *ApplyResult) addError(message string) {
	if len(result.Errors) < applyResultErrorLimit {
		result.Errors = append(result.Errors, message)
		return
	}
	result.OmittedErrors++
}

type run struct {
	Filename    string
	Path        string
	Status      string
	CompletedAt sql.NullInt64
}

type stagedArchive struct {
	id   int64
	path string
}

func NewStore(ctx context.Context, db *sql.DB, stagingDir string, vocabularyStore *vocabulary.Store) (*Store, error) {
	absoluteStagingDir, err := filepath.Abs(stagingDir)
	if err != nil {
		return nil, fmt.Errorf("resolve Anki staging directory: %w", err)
	}
	if err := os.MkdirAll(absoluteStagingDir, 0o750); err != nil {
		return nil, fmt.Errorf("create Anki staging directory: %w", err)
	}
	store := &Store{db: db, stagingDir: absoluteStagingDir, vocabulary: vocabularyStore}
	if err := store.cleanupStaging(ctx); err != nil {
		return nil, err
	}
	return store, nil
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
		if info.ModTime().Before(cutoff) {
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

func (s *Store) Preview(ctx context.Context, runID int64) (Preview, error) {
	item, err := s.loadRun(ctx, runID)
	if err != nil {
		return Preview{}, err
	}
	if !runCanBeApplied(item.Status) {
		return Preview{}, unavailableRun("import run is no longer available to preview")
	}
	return Inspect(ctx, item.Path)
}

func (s *Store) Apply(ctx context.Context, runID int64, mapping Mapping) (ApplyResult, error) {
	run, err := s.loadRun(ctx, runID)
	if err != nil {
		return ApplyResult{}, err
	}
	if !runCanBeApplied(run.Status) {
		return ApplyResult{}, unavailableRun("import run is no longer available to apply")
	}
	preview, err := Inspect(ctx, run.Path)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := mapping.validate(len(preview.Fields)); err != nil {
		return ApplyResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("begin import transaction: %w", err)
	}
	defer tx.Rollback()
	if err := claimRunForApply(ctx, tx, runID, run); err != nil {
		return ApplyResult{}, err
	}
	resolver, err := newMediaResolver(ctx, run.Path)
	if err != nil {
		return ApplyResult{}, err
	}
	defer resolver.Close()
	if _, err := tx.ExecContext(ctx, "DELETE FROM import_notes WHERE run_id = ?", runID); err != nil {
		return ApplyResult{}, fmt.Errorf("clear previous import results: %w", err)
	}
	result := ApplyResult{}
	for _, note := range preview.Notes {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		action, message, err := s.applyNote(ctx, tx, run, note, mapping, resolver)
		if err != nil {
			return result, err
		}
		result.addNote(action, message)
		if err := recordImportNote(ctx, tx, runID, note.ID, action, message); err != nil {
			return result, err
		}
	}
	status := "applied"
	if result.Failed > 0 {
		status = "failed"
		result.Retryable = true
	}
	completedAt := nextCompletionTime(run.CompletedAt, time.Now().UTC().Unix())
	update, err := tx.ExecContext(ctx, `UPDATE import_runs SET status = ?, completed_at = ? WHERE id = ?`, status, completedAt, runID)
	if err != nil {
		return result, fmt.Errorf("complete import run: %w", err)
	}
	if affected, err := update.RowsAffected(); err != nil {
		return result, fmt.Errorf("check completed import run: %w", err)
	} else if affected != 1 {
		return result, unavailableRun("import run is no longer available to apply")
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit import: %w", err)
	}
	if status == "applied" {
		if err := s.cleanupAppliedArchive(ctx, runID, run.Path); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *Store) applyNote(
	ctx context.Context,
	tx *sql.Tx,
	importRun run,
	note Note,
	mapping Mapping,
	resolver *mediaResolver,
) (string, string, error) {
	expression := cleanImportedText(field(note, mapping.ExpressionField))
	pronunciation := cleanImportedText(field(note, mapping.PronunciationField))
	meanings := cleanImportedMeanings(expression, field(note, mapping.MeaningField))
	if expression == "" || (!mapping.KnownElsewhere && (pronunciation == "" || len(meanings) == 0)) {
		return "failed", fmt.Sprintf("note %d is missing an expression, pronunciation, or meaning", note.ID), nil
	}
	audio, picture, err := resolver.Resolve(ctx, note, mapping)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", "", ctxErr
		}
		return "failed", fmt.Sprintf("note %d: %v", note.ID, err), nil
	}
	input := vocabulary.CreateInput{
		Expression:     expression,
		Pronunciation:  pronunciation,
		Meanings:       meanings,
		SourceLabel:    importRun.Filename,
		AllowDuplicate: mapping.AllowDuplicate,
		AllowSparse:    mapping.KnownElsewhere,
		Audio:          audio,
		Picture:        picture,
	}
	if mapping.ExtendedFields {
		input.Notes = cleanImportedText(field(note, mapping.NotesField))
		input.ExampleSentence = cleanImportedText(field(note, mapping.ExampleField))
		input.ExampleTranslation = cleanImportedText(field(note, mapping.TranslationField))
	}
	vocabularyID, err := s.vocabulary.CreateInTx(ctx, tx, input)
	if errors.Is(err, vocabulary.ErrDuplicate) {
		return "skipped", fmt.Sprintf("note %d: %v", note.ID, err), nil
	}
	if errors.Is(err, vocabulary.ErrInvalidInput) {
		return "failed", fmt.Sprintf("note %d: %v", note.ID, err), nil
	}
	if err != nil {
		return "", "", fmt.Errorf("import note %d: %w", note.ID, err)
	}
	if mapping.KnownElsewhere {
		if err := s.vocabulary.MarkKnownElsewhereInTx(ctx, tx, vocabularyID); err != nil {
			return "", "", fmt.Errorf("mark imported note %d as known elsewhere: %w", note.ID, err)
		}
	}
	return "created", "", nil
}

func (result *ApplyResult) addNote(action, message string) {
	switch action {
	case "created":
		result.Created++
	case "skipped":
		result.Skipped++
		result.addError(message)
	case "failed":
		result.Failed++
		result.addError(message)
	}
}

func (s *Store) Result(ctx context.Context, runID int64) (ApplyResult, error) {
	var status string
	var completedAt sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
		SELECT status, completed_at
		FROM import_runs
		WHERE id = ?`, runID).Scan(&status, &completedAt); err != nil {
		return ApplyResult{}, fmt.Errorf("load import result: %w", err)
	}
	if !completedAt.Valid || (status != "applied" && status != "failed") {
		return ApplyResult{}, unavailableRun("import result is not available yet")
	}

	result := ApplyResult{Retryable: status == "failed"}
	rows, err := s.db.QueryContext(ctx, `
		SELECT action, COUNT(*)
		FROM import_notes
		WHERE run_id = ?
		GROUP BY action`, runID)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("count import outcomes: %w", err)
	}
	for rows.Next() {
		var action string
		var count int
		if err := rows.Scan(&action, &count); err != nil {
			rows.Close()
			return ApplyResult{}, fmt.Errorf("scan import outcome: %w", err)
		}
		switch action {
		case "created":
			result.Created = count
		case "skipped":
			result.Skipped = count
		case "failed":
			result.Failed = count
		default:
			rows.Close()
			return ApplyResult{}, fmt.Errorf("load import result: unknown note action %q", action)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ApplyResult{}, fmt.Errorf("iterate import outcomes: %w", err)
	}
	if err := rows.Close(); err != nil {
		return ApplyResult{}, fmt.Errorf("close import outcomes: %w", err)
	}

	var errorCount int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM import_notes
		WHERE run_id = ? AND NULLIF(error_message, '') IS NOT NULL`, runID).Scan(&errorCount); err != nil {
		return ApplyResult{}, fmt.Errorf("count import messages: %w", err)
	}
	messageRows, err := s.db.QueryContext(ctx, `
		SELECT error_message
		FROM import_notes
		WHERE run_id = ? AND NULLIF(error_message, '') IS NOT NULL
		ORDER BY id
		LIMIT ?`, runID, applyResultErrorLimit)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("load import messages: %w", err)
	}
	defer messageRows.Close()
	for messageRows.Next() {
		var message string
		if err := messageRows.Scan(&message); err != nil {
			return ApplyResult{}, fmt.Errorf("scan import message: %w", err)
		}
		result.Errors = append(result.Errors, message)
	}
	if err := messageRows.Err(); err != nil {
		return ApplyResult{}, fmt.Errorf("iterate import messages: %w", err)
	}
	result.OmittedErrors = errorCount - len(result.Errors)
	return result, nil
}

func (s *Store) ErrorReport(ctx context.Context, runID int64) ([]ErrorReportRow, error) {
	if _, err := s.Result(ctx, runID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT action, error_message
		FROM import_notes
		WHERE run_id = ? AND NULLIF(error_message, '') IS NOT NULL
		ORDER BY id`, runID)
	if err != nil {
		return nil, fmt.Errorf("load import error report: %w", err)
	}
	defer rows.Close()
	report := make([]ErrorReportRow, 0)
	for rows.Next() {
		var row ErrorReportRow
		if err := rows.Scan(&row.Action, &row.Message); err != nil {
			return nil, fmt.Errorf("scan import error report: %w", err)
		}
		report = append(report, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate import error report: %w", err)
	}
	return report, nil
}

func claimRunForApply(ctx context.Context, tx *sql.Tx, runID int64, expected run) error {
	if expected.Path == "" {
		return unavailableRun("import run has no staged archive")
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE import_runs
		SET status = status
		WHERE id = ?
		  AND status = ?
		  AND archive_path = ?
		  AND archive_path <> ''
		  AND completed_at IS ?`, runID, expected.Status, expected.Path, nullableCompletionTime(expected.CompletedAt))
	if err != nil {
		return fmt.Errorf("claim import run: %w", err)
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check import run claim: %w", err)
	}
	if claimed != 1 {
		return unavailableRun("import run is no longer available to apply")
	}
	return nil
}

func nullableCompletionTime(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func nextCompletionTime(previous sql.NullInt64, now int64) int64 {
	if previous.Valid && now <= previous.Int64 {
		return previous.Int64 + 1
	}
	return now
}

func recordImportNote(ctx context.Context, tx *sql.Tx, runID, noteID int64, action, message string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO import_notes (run_id, action, error_message)
		VALUES (?, ?, NULLIF(?, ''))`,
		runID, action, message,
	); err != nil {
		return fmt.Errorf("record imported note %d: %w", noteID, err)
	}
	return nil
}

func (mapping Mapping) validate(fieldCount int) error {
	required := []struct {
		name  string
		index int
	}{
		{name: "expression", index: mapping.ExpressionField},
	}
	if !mapping.KnownElsewhere {
		required = append(required,
			struct {
				name  string
				index int
			}{name: "pronunciation", index: mapping.PronunciationField},
			struct {
				name  string
				index int
			}{name: "meaning", index: mapping.MeaningField},
		)
	}
	for _, field := range required {
		if field.index < 0 || field.index >= fieldCount {
			return invalidMapping(fmt.Sprintf("select a valid %s field", field.name))
		}
	}
	if !mapping.KnownElsewhere && (mapping.ExpressionField == mapping.PronunciationField ||
		mapping.ExpressionField == mapping.MeaningField ||
		mapping.PronunciationField == mapping.MeaningField) {
		return invalidMapping("expression, pronunciation, and meaning must use different fields")
	}
	optional := []struct {
		name  string
		index int
	}{
		{name: "audio", index: mapping.AudioField},
		{name: "picture", index: mapping.PictureField},
	}
	if mapping.ExtendedFields {
		optional = append(optional,
			struct {
				name  string
				index int
			}{name: "notes", index: mapping.NotesField},
			struct {
				name  string
				index int
			}{name: "example", index: mapping.ExampleField},
			struct {
				name  string
				index int
			}{name: "translation", index: mapping.TranslationField},
		)
	}
	for _, field := range optional {
		if field.index < -1 || field.index >= fieldCount {
			return invalidMapping(fmt.Sprintf("select a valid %s field", field.name))
		}
	}
	return nil
}

func readZipEntryContext(ctx context.Context, entry *zip.File, limit int64) ([]byte, error) {
	reader, err := entry.Open()
	if err != nil {
		return nil, err
	}
	var content bytes.Buffer
	written, readErr := contextio.Copy(ctx, &content, io.LimitReader(reader, limit+1))
	closeErr := reader.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if written > limit {
		return nil, fmt.Errorf("archive entry exceeds the %d byte limit", limit)
	}
	return content.Bytes(), nil
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

func (s *Store) loadRun(ctx context.Context, runID int64) (run, error) {
	var item run
	if err := s.db.QueryRowContext(ctx, "SELECT filename, archive_path, status, completed_at FROM import_runs WHERE id = ?", runID).Scan(&item.Filename, &item.Path, &item.Status, &item.CompletedAt); err != nil {
		return run{}, fmt.Errorf("load import run: %w", err)
	}
	if item.Path != "" {
		resolvedPath, ok := s.resolveRunPath(runID, item.Path)
		if !ok {
			return run{}, errors.New("import run has an invalid staged archive path")
		}
		item.Path = resolvedPath
	}
	return item, nil
}

func runCanBeApplied(status string) bool {
	return status == "previewed" || status == "failed"
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

func field(note Note, index int) string {
	if index < 0 || index >= len(note.Fields) {
		return ""
	}
	return note.Fields[index]
}

func cleanField(value string) string {
	return strings.TrimSpace(value)
}

func cleanImportedMeanings(expression, value string) []string {
	value = cleanImportedText(value)
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '\n' || r == ';' || r == '；'
	})
	meanings := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		for _, prefix := range []string{"• ", "· ", "- ", "– ", "— "} {
			part = strings.TrimSpace(strings.TrimPrefix(part, prefix))
		}
		part = stripExpressionPrefix(expression, part)
		if part == "" || textnorm.Normalize(part) == textnorm.Normalize(expression) {
			continue
		}
		duplicate := false
		for _, meaning := range meanings {
			if strings.EqualFold(meaning, part) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			meanings = append(meanings, part)
		}
	}
	return meanings
}

func cleanImportedText(value string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(value))
	var text strings.Builder
	skipDepth := 0
	lastTextWasBreak := false
	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case html.ErrorToken:
			return strings.TrimSpace(text.String())
		case html.TextToken:
			if skipDepth == 0 {
				value := tokenizer.Text()
				text.Write(value)
				if len(value) > 0 {
					lastTextWasBreak = value[len(value)-1] == '\n'
				}
			}
		case html.StartTagToken:
			name, _ := tokenizer.TagName()
			tag := string(name)
			if skipDepth > 0 {
				if !voidAnkiElement(tag) {
					skipDepth++
				}
				continue
			}
			if ignoredAnkiElement(tag) {
				skipDepth = 1
				continue
			}
			if tag == "br" || blockAnkiElement(tag) {
				appendTextBreak(&text, &lastTextWasBreak)
			}
		case html.SelfClosingTagToken:
			name, _ := tokenizer.TagName()
			if skipDepth == 0 && string(name) == "br" {
				appendTextBreak(&text, &lastTextWasBreak)
			}
		case html.EndTagToken:
			name, _ := tokenizer.TagName()
			if skipDepth > 0 {
				skipDepth--
				continue
			}
			if blockAnkiElement(string(name)) {
				appendTextBreak(&text, &lastTextWasBreak)
			}
		}
	}
}

func appendTextBreak(text *strings.Builder, lastTextWasBreak *bool) {
	if text.Len() > 0 && !*lastTextWasBreak {
		text.WriteByte('\n')
		*lastTextWasBreak = true
	}
}

func ignoredAnkiElement(tag string) bool {
	switch tag {
	case "rt", "rp", "script", "style":
		return true
	default:
		return false
	}
}

func voidAnkiElement(tag string) bool {
	switch tag {
	case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
		return true
	default:
		return false
	}
}

func blockAnkiElement(tag string) bool {
	switch tag {
	case "address", "article", "aside", "blockquote", "div", "footer", "header", "h1", "h2", "h3", "h4", "h5", "h6", "li", "main", "nav", "ol", "p", "pre", "section", "table", "tbody", "td", "th", "thead", "tr", "ul":
		return true
	default:
		return false
	}
}

func stripExpressionPrefix(expression, value string) string {
	if expression == "" {
		return value
	}
	for _, separator := range []string{" - ", " – ", " — ", ":", "："} {
		index := strings.Index(value, separator)
		if index < 0 || textnorm.Normalize(value[:index]) != textnorm.Normalize(expression) {
			continue
		}
		return strings.TrimSpace(value[index+len(separator):])
	}
	return value
}
