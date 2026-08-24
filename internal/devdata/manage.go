package devdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tomasmik/goi/internal/srs"
)

type Entry struct {
	ID             int64
	Expression     string
	Status         string
	KnownElsewhere bool
	Stage          int
	HasStage       bool
	StageName      string
	DueAt          time.Time
	HasDue         bool
}

func List(ctx context.Context, db *sql.DB) ([]Entry, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT v.id, v.expression, v.status, v.known_elsewhere_at IS NOT NULL,
		       ss.stage, ss.due_at
		FROM vocabulary v
		LEFT JOIN srs_states ss ON ss.vocabulary_id = v.id
		ORDER BY v.id`)
	if err != nil {
		return nil, fmt.Errorf("list test vocabulary: %w", err)
	}
	defer rows.Close()

	entries := make([]Entry, 0)
	for rows.Next() {
		var entry Entry
		var stage, dueAt sql.NullInt64
		if err := rows.Scan(&entry.ID, &entry.Expression, &entry.Status, &entry.KnownElsewhere, &stage, &dueAt); err != nil {
			return nil, fmt.Errorf("scan test vocabulary: %w", err)
		}
		if stage.Valid {
			entry.Stage = int(stage.Int64)
			entry.HasStage = true
			entry.StageName = stageName(srs.Stage(stage.Int64))
		}
		if dueAt.Valid {
			entry.DueAt = time.Unix(dueAt.Int64, 0).UTC()
			entry.HasDue = true
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate test vocabulary: %w", err)
	}
	return entries, nil
}

func MakeDue(ctx context.Context, db *sql.DB, count int, now time.Time) (int, error) {
	if count < 1 || count > 100 {
		return 0, errors.New("count must be between 1 and 100")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin due-date update: %w", err)
	}
	defer tx.Rollback()
	if err := ensureNoActiveStudy(ctx, tx); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE srs_states
		SET due_at = ?
		WHERE vocabulary_id IN (
			SELECT ss.vocabulary_id
			FROM srs_states ss
			JOIN vocabulary v ON v.id = ss.vocabulary_id
			WHERE v.status = 'active'
			  AND ss.suspended_at IS NULL
			  AND ss.stage < ?
			ORDER BY ss.vocabulary_id
			LIMIT ?
		)`, now.UTC().Add(-time.Minute).Unix(), srs.StageEvergreen, count)
	if err != nil {
		return 0, fmt.Errorf("make reviews due: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count due-date updates: %w", err)
	}
	if updated == 0 {
		return 0, errors.New("no active words can be made due")
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit due-date update: %w", err)
	}
	return int(updated), nil
}

func SetStage(ctx context.Context, db *sql.DB, id int64, stage srs.Stage, now time.Time) error {
	if id < 1 {
		return errors.New("word ID must be positive")
	}
	if stage < srs.StageNew || stage > srs.StageEvergreen {
		return fmt.Errorf("stage must be between 0 and %d", srs.StageEvergreen)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin stage update: %w", err)
	}
	defer tx.Rollback()
	if err := ensureNoActiveStudy(ctx, tx); err != nil {
		return err
	}
	if err := requireAvailableWord(ctx, tx, id); err != nil {
		return err
	}

	now = now.UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE vocabulary
		SET status = 'active',
		    lesson_completed_at = COALESCE(lesson_completed_at, ?),
		    known_elsewhere_at = NULL,
		    content_revision = content_revision +
		        CASE WHEN known_elsewhere_at IS NULL THEN 0 ELSE 1 END,
		    updated_at = ?
		WHERE id = ?`,
		now.Unix(), now.Unix(), id); err != nil {
		return fmt.Errorf("activate test word: %w", err)
	}
	var dueAt any
	if stage != srs.StageEvergreen {
		dueAt = now.Add(-time.Minute).Unix()
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO srs_states (vocabulary_id, stage, due_at)
		VALUES (?, ?, ?)
		ON CONFLICT(vocabulary_id) DO UPDATE SET
			stage = excluded.stage,
			due_at = excluded.due_at,
			suspended_at = NULL`,
		id, stage, dueAt); err != nil {
		return fmt.Errorf("set test word stage: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit stage update: %w", err)
	}
	return nil
}

func Unlearn(ctx context.Context, db *sql.DB, id int64, now time.Time) error {
	if id < 1 {
		return errors.New("word ID must be positive")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin unlearn update: %w", err)
	}
	defer tx.Rollback()
	if err := ensureNoActiveStudy(ctx, tx); err != nil {
		return err
	}
	if err := requireAvailableWord(ctx, tx, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE vocabulary
		SET status = 'unlearned', lesson_completed_at = NULL,
		    known_elsewhere_at = NULL,
		    content_revision = content_revision +
		        CASE WHEN known_elsewhere_at IS NULL THEN 0 ELSE 1 END,
		    updated_at = ?
		WHERE id = ?`, now.UTC().Unix(), id); err != nil {
		return fmt.Errorf("return test word to lessons: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM srs_states WHERE vocabulary_id = ?", id); err != nil {
		return fmt.Errorf("remove test word schedule: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit unlearn update: %w", err)
	}
	return nil
}

func ensureNoActiveStudy(ctx context.Context, tx *sql.Tx) error {
	var active int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM review_sessions WHERE status IN ('active', 'paused')
			UNION ALL
			SELECT 1 FROM lesson_sessions WHERE status = 'active'
		)`).Scan(&active); err != nil {
		return fmt.Errorf("check active study sessions: %w", err)
	}
	if active != 0 {
		return errors.New("finish the active lesson or review before changing test data")
	}
	return nil
}

func requireAvailableWord(ctx context.Context, tx *sql.Tx, id int64) error {
	var exists int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM vocabulary WHERE id = ?", id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("word %d does not exist", id)
	}
	if err != nil {
		return fmt.Errorf("load test word %d: %w", id, err)
	}
	return nil
}

func stageName(stage srs.Stage) string {
	switch {
	case stage >= srs.StageNew && stage <= srs.StageThree:
		return "New"
	case stage >= srs.StageFour && stage <= srs.StageFive:
		return "Learning"
	case stage == srs.StageSix:
		return "Familiar"
	case stage >= srs.StageSeven && stage <= srs.StageEight:
		return "Mature"
	case stage == srs.StageEvergreen:
		return "Burned"
	default:
		return ""
	}
}
