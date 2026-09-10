package imports

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tomasmik/goi/internal/vocabulary"
)

type Store struct {
	db         *sql.DB
	stagingDir string
	vocabulary *vocabulary.Store
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
