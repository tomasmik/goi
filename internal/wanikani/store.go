package wanikani

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type Store struct {
	db *sql.DB
}

type Status struct {
	UserID        string
	Username      string
	UserLevel     int
	CursorAt      time.Time
	LastAttemptAt time.Time
	LastSuccessAt time.Time
	LastError     string
	SubjectCount  int
}

type SubjectMapping struct {
	ID         int64
	Expression string
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Status(ctx context.Context) (Status, error) {
	var status Status
	var cursor string
	var lastAttempt, lastSuccess sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT user_id, username, user_level, cursor_at,
		       last_attempt_at, last_success_at, last_error,
		       (SELECT COUNT(*) FROM wanikani_subjects)
		FROM wanikani_sync_state
		WHERE id = 1`).Scan(
		&status.UserID, &status.Username, &status.UserLevel, &cursor,
		&lastAttempt, &lastSuccess, &status.LastError, &status.SubjectCount,
	)
	if err != nil {
		return Status{}, fmt.Errorf("load WaniKani status: %w", err)
	}
	if cursor != "" {
		status.CursorAt, err = time.Parse(time.RFC3339Nano, cursor)
		if err != nil {
			return Status{}, fmt.Errorf("parse WaniKani cursor: %w", err)
		}
	}
	if lastAttempt.Valid {
		status.LastAttemptAt = time.Unix(lastAttempt.Int64, 0).UTC()
	}
	if lastSuccess.Valid {
		status.LastSuccessAt = time.Unix(lastSuccess.Int64, 0).UTC()
	}
	return status, nil
}

func (s *Store) ConfigureAccount(ctx context.Context, user User) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin WaniKani account update: %w", err)
	}
	defer tx.Rollback()

	var existingID string
	if err := tx.QueryRowContext(ctx, "SELECT user_id FROM wanikani_sync_state WHERE id = 1").Scan(&existingID); err != nil {
		return false, fmt.Errorf("load WaniKani account: %w", err)
	}
	changed := existingID != "" && existingID != user.ID
	if changed {
		if _, err := tx.ExecContext(ctx, "DELETE FROM wanikani_subjects"); err != nil {
			return false, fmt.Errorf("clear WaniKani subjects: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE wanikani_sync_state
			SET user_id = ?, username = ?, user_level = ?, cursor_at = '',
			    last_attempt_at = NULL, last_success_at = NULL, last_error = ''
			WHERE id = 1`, user.ID, user.Username, user.Level); err != nil {
			return false, fmt.Errorf("replace WaniKani account: %w", err)
		}
	} else if _, err := tx.ExecContext(ctx, `
		UPDATE wanikani_sync_state
		SET user_id = ?, username = ?, user_level = ?
		WHERE id = 1`, user.ID, user.Username, user.Level); err != nil {
		return false, fmt.Errorf("update WaniKani account: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit WaniKani account update: %w", err)
	}
	return changed, nil
}

func (s *Store) UnseenSubjectIDs(ctx context.Context, ids []int64) ([]int64, error) {
	seen := make(map[int64]struct{})
	for start := 0; start < len(ids); start += 250 {
		end := min(start+250, len(ids))
		arguments := make([]any, 0, end-start)
		placeholders := make([]string, 0, end-start)
		for _, id := range ids[start:end] {
			arguments = append(arguments, id)
			placeholders = append(placeholders, "?")
		}
		rows, err := s.db.QueryContext(ctx,
			"SELECT subject_id FROM wanikani_subjects WHERE subject_id IN ("+strings.Join(placeholders, ",")+")",
			arguments...,
		)
		if err != nil {
			return nil, fmt.Errorf("load known WaniKani subjects: %w", err)
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan known WaniKani subject: %w", err)
			}
			seen[id] = struct{}{}
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close known WaniKani subjects: %w", err)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate known WaniKani subjects: %w", err)
		}
	}

	unseen := make([]int64, 0, len(ids)-len(seen))
	for _, id := range ids {
		if _, exists := seen[id]; !exists {
			unseen = append(unseen, id)
		}
	}
	return unseen, nil
}

func (s *Store) CompleteSync(ctx context.Context, user User, cursor, completedAt time.Time, subjects []SubjectMapping) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin WaniKani sync update: %w", err)
	}
	defer tx.Rollback()

	for _, subject := range subjects {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO wanikani_subjects (subject_id, expression, synced_at)
			VALUES (?, ?, ?)`, subject.ID, subject.Expression, completedAt.UTC().Unix()); err != nil {
			return fmt.Errorf("record WaniKani subject %d: %w", subject.ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE wanikani_sync_state
		SET user_id = ?, username = ?, user_level = ?, cursor_at = ?,
		    last_attempt_at = ?, last_success_at = ?, last_error = ''
		WHERE id = 1`,
		user.ID, user.Username, user.Level, cursor.UTC().Format(time.RFC3339Nano),
		completedAt.UTC().Unix(), completedAt.UTC().Unix(),
	); err != nil {
		return fmt.Errorf("record successful WaniKani sync: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit WaniKani sync update: %w", err)
	}
	return nil
}

func (s *Store) RecordFailure(ctx context.Context, attemptedAt time.Time, message string) error {
	message = strings.TrimSpace(message)
	if len(message) > 500 {
		message = message[:500]
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE wanikani_sync_state
		SET last_attempt_at = ?, last_error = ?
		WHERE id = 1`, attemptedAt.UTC().Unix(), message); err != nil {
		return fmt.Errorf("record WaniKani sync failure: %w", err)
	}
	return nil
}

func (s *Store) Clear(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin WaniKani disconnect: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM wanikani_subjects"); err != nil {
		return fmt.Errorf("clear WaniKani subjects: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE wanikani_sync_state
		SET user_id = '', username = '', user_level = 0, cursor_at = '',
		    last_attempt_at = NULL, last_success_at = NULL, last_error = ''
		WHERE id = 1`); err != nil {
		return fmt.Errorf("clear WaniKani state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit WaniKani disconnect: %w", err)
	}
	return nil
}
