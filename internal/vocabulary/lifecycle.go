package vocabulary

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tomasmik/goi/internal/media"
)

type lifecycleError string

func (err lifecycleError) Error() string {
	return string(err)
}

func (err lifecycleError) UserMessage() string {
	return string(err)
}

func lifecycleErrorf(format string, args ...any) error {
	return lifecycleError(fmt.Sprintf(format, args...))
}

func (s *Store) ApplyAction(ctx context.Context, id int64, action Action) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin vocabulary action: %w", err)
	}
	defer tx.Rollback()

	state, err := loadLifecycleState(ctx, tx, id)
	if err != nil {
		return err
	}

	now := time.Now().UTC().Unix()
	switch action {
	case ActionSuspend:
		if err := toggleSuspension(ctx, tx, id, state, now); err != nil {
			return err
		}
	case ActionArchive:
		if err := ensureNoActiveStudy(ctx, tx, id); err != nil {
			return err
		}
		if err := toggleArchive(ctx, tx, id, state, now); err != nil {
			return err
		}
	case ActionReset:
		if err := ensureNoActiveStudy(ctx, tx, id); err != nil {
			return err
		}
		if err := resetProgress(ctx, tx, id, state, now); err != nil {
			return err
		}
	case ActionDelete:
		if err := abandonActiveStudy(ctx, tx, id, now); err != nil {
			return err
		}
		if err := deleteVocabulary(ctx, tx, id, now); err != nil {
			return err
		}
		if err := media.CollectUnusedInTx(ctx, tx); err != nil {
			return err
		}
	case ActionHideLeech:
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO mistake_visibility (vocabulary_id, leech_hidden_at) VALUES (?, ?)
			ON CONFLICT(vocabulary_id) DO UPDATE SET leech_hidden_at = excluded.leech_hidden_at`, id, now); err != nil {
			return fmt.Errorf("hide leech: %w", err)
		}
	case ActionRestoreLeech:
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO mistake_visibility (vocabulary_id, leech_hidden_at) VALUES (?, NULL)
			ON CONFLICT(vocabulary_id) DO UPDATE SET leech_hidden_at = NULL`, id); err != nil {
			return fmt.Errorf("restore leech: %w", err)
		}
	case ActionLearn:
		if state.status != "unlearned" || state.srsID.Valid {
			return lifecycleError("only words known elsewhere can be moved into lessons")
		}
		if !state.complete {
			return lifecycleError("add a reading and meaning before moving this word to lessons")
		}
		if err := ensureNoActiveStudy(ctx, tx, id); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE vocabulary
			SET known_elsewhere_at = NULL, updated_at = ?, content_revision = content_revision + 1
			WHERE id = ? AND status = 'unlearned' AND known_elsewhere_at IS NOT NULL`, now, id)
		if err != nil {
			return fmt.Errorf("move known vocabulary into lessons: %w", err)
		}
		if affected, err := result.RowsAffected(); err != nil {
			return fmt.Errorf("check known vocabulary update: %w", err)
		} else if affected != 1 {
			return lifecycleError("this word is no longer marked as known elsewhere")
		}
	case ActionMarkKnown:
		if err := abandonActiveStudy(ctx, tx, id, now); err != nil {
			return err
		}
		if err := markKnownElsewhere(ctx, tx, id, state, now); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported vocabulary action %q", action)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit vocabulary action: %w", err)
	}
	return nil
}

func deleteVocabulary(ctx context.Context, tx *sql.Tx, id, now int64) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO mining_capture_tombstones (capture_nonce, deleted_at)
		SELECT capture_nonce, ?
		FROM mining_captures
		WHERE vocabulary_id = ?`, now, id); err != nil {
		return fmt.Errorf("remember deleted vocabulary captures: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM mining_captures WHERE vocabulary_id = ?", id); err != nil {
		return fmt.Errorf("delete vocabulary captures: %w", err)
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM vocabulary WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete vocabulary: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check deleted vocabulary: %w", err)
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func loadLifecycleState(ctx context.Context, tx *sql.Tx, id int64) (lifecycleState, error) {
	var state lifecycleState
	if err := tx.QueryRowContext(ctx, `
		SELECT v.status, ss.vocabulary_id, ss.suspended_at,
		       v.known_elsewhere_at IS NOT NULL,
		       v.pronunciation <> '' AND EXISTS (
		           SELECT 1 FROM meanings m WHERE m.vocabulary_id = v.id
		       )
		FROM vocabulary v
		LEFT JOIN srs_states ss ON ss.vocabulary_id = v.id
		WHERE v.id = ?`, id).Scan(&state.status, &state.srsID, &state.suspendedAt, &state.knownElsewhere, &state.complete); err != nil {
		return lifecycleState{}, fmt.Errorf("load vocabulary %d: %w", id, err)
	}
	return state, nil
}

func markKnownElsewhere(ctx context.Context, tx *sql.Tx, id int64, state lifecycleState, now int64) error {
	if state.knownElsewhere {
		return lifecycleError("this word is already marked as known elsewhere")
	}
	switch state.status {
	case "unlearned":
		if state.srsID.Valid {
			return errors.New("unlearned vocabulary unexpectedly has an SRS state")
		}
	case "active", "suspended":
		if !state.srsID.Valid {
			return fmt.Errorf("%s vocabulary has no SRS state", state.status)
		}
	case "archived":
		return lifecycleError("restore archived vocabulary before marking it as known elsewhere")
	default:
		return fmt.Errorf("cannot mark vocabulary with status %q as known elsewhere", state.status)
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE vocabulary
		SET status = 'unlearned', known_elsewhere_at = ?, lesson_completed_at = NULL,
		    updated_at = ?, content_revision = content_revision + 1
		WHERE id = ? AND known_elsewhere_at IS NULL`, now, now, id)
	if err != nil {
		return fmt.Errorf("mark vocabulary as known elsewhere: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check known vocabulary update: %w", err)
	} else if affected != 1 {
		return lifecycleError("this word is already marked as known elsewhere")
	}
	if !state.srsID.Valid {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM srs_states WHERE vocabulary_id = ?", id); err != nil {
		return fmt.Errorf("clear known vocabulary review state: %w", err)
	}
	return resetLeechStateTx(ctx, tx, id, now)
}

func toggleSuspension(ctx context.Context, tx *sql.Tx, id int64, state lifecycleState, now int64) error {
	switch state.status {
	case "active":
		if !state.srsID.Valid {
			return errors.New("active vocabulary has no SRS state")
		}
		if state.suspendedAt.Valid {
			return errors.New("active vocabulary has a suspended SRS state")
		}
		if _, err := tx.ExecContext(ctx, "UPDATE vocabulary SET status = 'suspended', updated_at = ? WHERE id = ?", now, id); err != nil {
			return fmt.Errorf("suspend vocabulary: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE srs_states SET suspended_at = ? WHERE vocabulary_id = ?", now, id); err != nil {
			return fmt.Errorf("suspend SRS state: %w", err)
		}
	case "suspended":
		if !state.srsID.Valid {
			return errors.New("suspended vocabulary has no SRS state")
		}
		if !state.suspendedAt.Valid {
			return errors.New("suspended vocabulary has an active SRS state")
		}
		if _, err := tx.ExecContext(ctx, "UPDATE vocabulary SET status = 'active', updated_at = ? WHERE id = ?", now, id); err != nil {
			return fmt.Errorf("resume vocabulary: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE srs_states SET suspended_at = NULL WHERE vocabulary_id = ?", id); err != nil {
			return fmt.Errorf("resume SRS state: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE leech_states SET auto_suspended_at = NULL WHERE vocabulary_id = ?`, id); err != nil {
			return fmt.Errorf("clear automatic leech suspension: %w", err)
		}
	case "unlearned":
		return lifecycleError("unlearned vocabulary has no reviews to suspend")
	case "archived":
		return lifecycleError("restore archived vocabulary before changing its suspension")
	default:
		return fmt.Errorf("cannot change suspension for vocabulary with status %q", state.status)
	}
	return nil
}

func toggleArchive(ctx context.Context, tx *sql.Tx, id int64, state lifecycleState, now int64) error {
	if state.status == "archived" {
		status := "unlearned"
		if state.srsID.Valid {
			status = "active"
			if state.suspendedAt.Valid {
				status = "suspended"
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE vocabulary SET status = ?, updated_at = ? WHERE id = ?", status, now, id); err != nil {
			return fmt.Errorf("restore archived vocabulary: %w", err)
		}
		return nil
	}

	switch state.status {
	case "unlearned":
		if state.srsID.Valid {
			return errors.New("unlearned vocabulary unexpectedly has an SRS state")
		}
	case "active":
		if !state.srsID.Valid {
			return errors.New("active vocabulary has no SRS state")
		}
		if state.suspendedAt.Valid {
			return errors.New("active vocabulary has a suspended SRS state")
		}
	case "suspended":
		if !state.srsID.Valid {
			return errors.New("suspended vocabulary has no SRS state")
		}
		if !state.suspendedAt.Valid {
			return errors.New("suspended vocabulary has an active SRS state")
		}
	default:
		return fmt.Errorf("cannot archive vocabulary with status %q", state.status)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE vocabulary SET status = 'archived', updated_at = ? WHERE id = ?", now, id); err != nil {
		return fmt.Errorf("archive vocabulary: %w", err)
	}
	return nil
}

func resetProgress(ctx context.Context, tx *sql.Tx, id int64, state lifecycleState, now int64) error {
	if !state.srsID.Valid {
		if state.status == "active" || state.status == "suspended" {
			return fmt.Errorf("%s vocabulary has no SRS state", state.status)
		}
		return lifecycleError("vocabulary has no learning progress to reset")
	}
	if state.status == "unlearned" {
		return errors.New("unlearned vocabulary unexpectedly has an SRS state")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE vocabulary
		SET status = 'unlearned', lesson_completed_at = NULL, updated_at = ?
		WHERE id = ?`, now, id); err != nil {
		return fmt.Errorf("reset vocabulary status: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM srs_states WHERE vocabulary_id = ?", id); err != nil {
		return fmt.Errorf("reset SRS state: %w", err)
	}
	if err := resetLeechStateTx(ctx, tx, id, now); err != nil {
		return err
	}
	return nil
}

func (s *Store) QueueMinedForLessonInTx(ctx context.Context, tx *sql.Tx, id int64, now time.Time) error {
	var status, pronunciation string
	var knownElsewhere, hasSRS bool
	var meaningCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT status, pronunciation, known_elsewhere_at IS NOT NULL,
		       EXISTS (SELECT 1 FROM srs_states WHERE vocabulary_id = vocabulary.id),
		       (SELECT COUNT(*) FROM meanings WHERE vocabulary_id = vocabulary.id)
		FROM vocabulary
		WHERE id = ?`, id).Scan(&status, &pronunciation, &knownElsewhere, &hasSRS, &meaningCount); err != nil {
		return fmt.Errorf("load known vocabulary for mining: %w", err)
	}
	known := status == "active" || status == "suspended" || knownElsewhere || hasSRS
	if !known {
		return nil
	}
	if strings.TrimSpace(pronunciation) == "" || meaningCount == 0 {
		return lifecycleError("add a reading and meaning before moving this card to lessons")
	}

	now = now.UTC()
	unixNow := now.Unix()
	if err := abandonActiveStudy(ctx, tx, id, unixNow); err != nil {
		return err
	}
	if err := resetLeechStateTx(ctx, tx, id, unixNow); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE vocabulary
		SET status = 'unlearned', known_elsewhere_at = NULL,
		    lesson_completed_at = NULL, updated_at = ?
		WHERE id = ?`, unixNow, id); err != nil {
		return fmt.Errorf("queue mined vocabulary for lessons: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM srs_states WHERE vocabulary_id = ?", id); err != nil {
		return fmt.Errorf("clear mined vocabulary progress: %w", err)
	}
	return nil
}

func resetLeechStateTx(ctx context.Context, tx *sql.Tx, vocabularyID, now int64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE leech_states
		SET failures_toward_leech = 0,
		    active = 0,
		    marked_at = NULL,
		    failures_since_mark = 0,
		    correct_streak = 0,
		    auto_suspended_at = NULL,
		    cleared_at = CASE WHEN ever_leech = 1 THEN ? ELSE cleared_at END,
		    reset_after_result_id = COALESCE((
		        SELECT MAX(rr.id)
		        FROM review_results rr
		        JOIN review_session_items rsi ON rsi.id = rr.session_item_id
		        WHERE rsi.vocabulary_id = ?
		    ), 0)
		WHERE vocabulary_id = ?`, now, vocabularyID, vocabularyID)
	if err != nil {
		return fmt.Errorf("reset leech state: %w", err)
	}
	return nil
}

func abandonActiveStudy(ctx context.Context, tx *sql.Tx, vocabularyID, now int64) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE review_sessions
		SET status = 'abandoned', completed_at = ?
		WHERE status IN ('active', 'paused')
		  AND (
			  EXISTS (
				  SELECT 1
				  FROM review_session_items rsi
				  WHERE rsi.session_id = review_sessions.id
				    AND rsi.vocabulary_id = ?
			  )
			  OR EXISTS (
				  SELECT 1
				  FROM lesson_sessions ls
				  JOIN lesson_session_items lsi ON lsi.session_id = ls.id
				  WHERE ls.id = review_sessions.lesson_session_id
				    AND ls.status = 'active'
				    AND lsi.vocabulary_id = ?
			  )
		  )`, now, vocabularyID, vocabularyID); err != nil {
		return fmt.Errorf("abandon active reviews: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE lesson_sessions
		SET status = 'abandoned'
		WHERE status = 'active'
		  AND EXISTS (
			  SELECT 1
			  FROM lesson_session_items lsi
			  WHERE lsi.session_id = lesson_sessions.id
			    AND lsi.vocabulary_id = ?
		  )`, vocabularyID); err != nil {
		return fmt.Errorf("abandon active lessons: %w", err)
	}
	return nil
}

func ensureNoActiveStudy(ctx context.Context, tx *sql.Tx, vocabularyID int64) error {
	var sessionID int64
	err := tx.QueryRowContext(ctx, `
		SELECT rs.id
		FROM review_session_items rsi
		JOIN review_sessions rs ON rs.id = rsi.session_id
		WHERE rsi.vocabulary_id = ?
		  AND rs.status IN ('active', 'paused')
		ORDER BY rs.id
		LIMIT 1`, vocabularyID).Scan(&sessionID)
	if err == nil {
		return lifecycleErrorf("vocabulary is still in review session %d", sessionID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check active reviews: %w", err)
	}

	err = tx.QueryRowContext(ctx, `
		SELECT ls.id
		FROM lesson_session_items lsi
		JOIN lesson_sessions ls ON ls.id = lsi.session_id
		WHERE lsi.vocabulary_id = ? AND ls.status = 'active'
		ORDER BY ls.id
		LIMIT 1`, vocabularyID).Scan(&sessionID)
	if err == nil {
		return lifecycleErrorf("vocabulary is still in lesson session %d", sessionID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check active lessons: %w", err)
	}
	return nil
}
