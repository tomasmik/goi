package reviews

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tomasmik/goi/internal/srs"
)

// ReconcileCompletedLessons finishes batches left between the two commits used
// by older versions. Startup runs it before accepting requests, including after restore.
func (s *Store) ReconcileCompletedLessons(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin lesson reconciliation: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT rs.id
		FROM review_sessions rs
		JOIN lesson_sessions ls ON ls.id = rs.lesson_session_id
		WHERE rs.status = 'completed' AND ls.status = 'active' AND ls.phase = 'review'
		  AND rs.id = (SELECT MAX(latest.id) FROM review_sessions latest WHERE latest.lesson_session_id = ls.id)
		ORDER BY rs.id`)
	if err != nil {
		return fmt.Errorf("find unfinished lesson completions: %w", err)
	}
	var reviewIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan unfinished lesson completion: %w", err)
		}
		reviewIDs = append(reviewIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read unfinished lesson completions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close unfinished lesson completions: %w", err)
	}
	for _, reviewID := range reviewIDs {
		if err := completeLessonReviewTx(ctx, tx, reviewID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit lesson reconciliation: %w", err)
	}
	return nil
}

func completeLessonReviewTx(ctx context.Context, tx *sql.Tx, reviewID int64) error {
	var sessionID int64
	var lessonStatus, phase, reviewStatus string
	var currentBatch int
	var reviewCompletedAt sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT ls.id, ls.status, ls.phase, ls.current_batch, rs.status, rs.completed_at
		FROM review_sessions rs
		JOIN lesson_sessions ls ON ls.id = rs.lesson_session_id
		WHERE rs.id = ?
		  AND rs.id = (SELECT MAX(latest.id) FROM review_sessions latest WHERE latest.lesson_session_id = ls.id)`,
		reviewID).Scan(&sessionID, &lessonStatus, &phase, &currentBatch, &reviewStatus, &reviewCompletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load linked lesson completion: %w", err)
	}
	if lessonStatus != "active" || phase != "review" || reviewStatus != "completed" {
		return nil
	}
	if !reviewCompletedAt.Valid {
		return errors.New("completed lesson review has no completion time")
	}
	completedAt := reviewCompletedAt.Int64
	if _, err := tx.ExecContext(ctx, `
		UPDATE lesson_session_items SET review_completed_at = ?
		WHERE session_id = ? AND batch_number = ? AND review_completed_at IS NULL`, completedAt, sessionID, currentBatch); err != nil {
		return fmt.Errorf("complete lesson batch: %w", err)
	}
	if err := activateBatchTx(ctx, tx, sessionID, currentBatch, completedAt); err != nil {
		return err
	}
	var remainingItems int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM lesson_session_items
		WHERE session_id = ? AND batch_number > ?`, sessionID, currentBatch).Scan(&remainingItems); err != nil {
		return fmt.Errorf("count remaining lesson batches: %w", err)
	}
	if remainingItems == 0 {
		if _, err := tx.ExecContext(ctx, `
			UPDATE lesson_sessions SET status = 'completed'
			WHERE id = ?`, sessionID); err != nil {
			return fmt.Errorf("complete lesson session: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE lesson_sessions SET phase = 'study', current_batch = current_batch + 1, study_position = 0
			WHERE id = ?`, sessionID); err != nil {
			return fmt.Errorf("advance lesson batch: %w", err)
		}
	}
	return nil
}

func activateBatchTx(ctx context.Context, tx *sql.Tx, sessionID int64, batch int, now int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT v.id, v.status, v.known_elsewhere_at IS NOT NULL, ss.vocabulary_id
		FROM lesson_session_items lsi
		JOIN vocabulary v ON v.id = lsi.vocabulary_id
		LEFT JOIN srs_states ss ON ss.vocabulary_id = v.id
		WHERE lsi.session_id = ? AND lsi.batch_number = ?`, sessionID, batch)
	if err != nil {
		return fmt.Errorf("load lesson batch vocabulary: %w", err)
	}
	type activation struct {
		id             int64
		status         string
		knownElsewhere bool
		srsID          sql.NullInt64
	}
	var items []activation
	for rows.Next() {
		var item activation
		if err := rows.Scan(&item.id, &item.status, &item.knownElsewhere, &item.srsID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan lesson batch vocabulary: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate lesson batch vocabulary: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close lesson batch vocabulary: %w", err)
	}
	for _, item := range items {
		if item.status != "unlearned" {
			if (item.status == "active" || item.status == "suspended") && !item.srsID.Valid {
				return fmt.Errorf("vocabulary %d is %s but has no SRS state", item.id, item.status)
			}
			continue
		}
		if item.knownElsewhere {
			return fmt.Errorf("vocabulary %d is known elsewhere and cannot be activated", item.id)
		}
		if item.srsID.Valid {
			return fmt.Errorf("unlearned vocabulary %d already has an SRS state", item.id)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE vocabulary
			SET status = 'active', lesson_completed_at = ?, updated_at = ?
			WHERE id = ? AND status = 'unlearned' AND known_elsewhere_at IS NULL`, now, now, item.id)
		if err != nil {
			return fmt.Errorf("activate vocabulary %d: %w", item.id, err)
		}
		if affected, err := result.RowsAffected(); err != nil {
			return fmt.Errorf("check activated vocabulary %d: %w", item.id, err)
		} else if affected != 1 {
			return fmt.Errorf("vocabulary %d is no longer available for activation", item.id)
		}
		if _, err := tx.ExecContext(ctx, `
				INSERT INTO srs_states (vocabulary_id, stage, due_at)
				VALUES (?, ?, ?)`,
			item.id, srs.StageNew, srs.DueAt(srs.StageNew, time.Unix(now, 0)).Unix()); err != nil {
			return fmt.Errorf("schedule vocabulary %d: %w", item.id, err)
		}
	}
	return nil
}
