package reviews

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const queuePositionOffset = 1_000_000

type undoResult struct {
	id                 int64
	itemID             int64
	vocabularyID       int64
	stageBefore        sql.NullInt64
	stageAfter         sql.NullInt64
	dueBefore          sql.NullInt64
	dueAfter           sql.NullInt64
	lastReviewedBefore sql.NullInt64
	srsApplied         bool
	createdAt          int64
	itemSRSApplied     int
	itemStatus         string
	vocabularyStatus   string
	sessionStatus      string
	sessionKind        string
	lessonSessionID    sql.NullInt64
	visibilityBefore   mistakeVisibilitySnapshot
}

func (s *Store) undoAvailable(ctx context.Context, sessionID int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, fmt.Errorf("begin review undo check: %w", err)
	}
	defer tx.Rollback()

	latest, err := loadUndoResultTx(ctx, tx, sessionID)
	if err == nil {
		err = validateUndoResultTx(ctx, tx, sessionID, latest)
	}
	if undoIsUnavailable(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) Undo(ctx context.Context, sessionID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin review undo: %w", err)
	}
	defer tx.Rollback()

	latest, err := loadUndoResultTx(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	if err := validateUndoResultTx(ctx, tx, sessionID, latest); err != nil {
		return err
	}
	if latest.srsApplied {
		if err := restoreUndoSRSStateTx(ctx, tx, latest); err != nil {
			return err
		}
	}
	if err := restoreMistakeVisibilityTx(ctx, tx, latest.vocabularyID, latest.visibilityBefore); err != nil {
		return err
	}
	now := time.Now().UTC().Unix()
	if _, err := tx.ExecContext(ctx, "UPDATE review_results SET voided_at = ? WHERE id = ?", now, latest.id); err != nil {
		return fmt.Errorf("void review result: %w", err)
	}
	if err := s.rebuildLeechStateTx(ctx, tx, latest.vocabularyID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE review_session_items SET status = 'completed' WHERE id = ? AND status = 'failed'", latest.itemID); err != nil {
		return fmt.Errorf("close undone failed item: %w", err)
	}
	if err := s.insertUndoItemTx(ctx, tx, sessionID, latest.vocabularyID, latest.itemID, latest.itemSRSApplied); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE review_sessions SET last_undo_result_id = ? WHERE id = ?", latest.id, sessionID); err != nil {
		return fmt.Errorf("record undo boundary: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit review undo: %w", err)
	}
	return nil
}

func validateUndoResultTx(ctx context.Context, tx *sql.Tx, sessionID int64, result undoResult) error {
	if result.itemStatus != "failed" && result.itemStatus != "completed" {
		return stateError("latest review result is not undoable")
	}
	if result.sessionStatus != "active" && result.sessionStatus != "completed" {
		return stateError("review session is not available for undo")
	}
	if err := validateUndoContextTx(ctx, tx, sessionID, result.sessionKind, result.vocabularyStatus, result.lessonSessionID); err != nil {
		return err
	}
	if err := validateUndoQueueTx(ctx, tx, sessionID, result.vocabularyID); err != nil {
		return err
	}
	if err := validateMistakeVisibilityUndoTx(ctx, tx, result.vocabularyID, result.visibilityBefore); err != nil {
		return err
	}
	if result.srsApplied {
		return validateUndoSRSStateTx(ctx, tx, result)
	}
	return nil
}

func undoIsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var stateErr stateError
	return errors.Is(err, sql.ErrNoRows) || errors.As(err, &stateErr)
}

func loadUndoResultTx(ctx context.Context, tx *sql.Tx, sessionID int64) (undoResult, error) {
	var result undoResult
	var visibilityExisted, visibilityHidden, visibilityLeechHidden sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT rr.id, rr.session_item_id, rsi.vocabulary_id, rr.stage_before,
		       rr.stage_after, rr.due_before, rr.due_after, rr.last_reviewed_before,
		       rr.srs_applied, rr.created_at, rsi.srs_applied, rsi.status,
		       v.status, rs.status, rs.kind, rs.lesson_session_id,
		       rr.mistake_visibility_existed_before, rr.mistake_hidden_before,
		       rr.mistake_leech_hidden_before
		FROM review_results rr
		JOIN review_session_items rsi ON rsi.id = rr.session_item_id
		JOIN review_sessions rs ON rs.id = rsi.session_id
		JOIN vocabulary v ON v.id = rsi.vocabulary_id
		WHERE rsi.session_id = ? AND rr.voided_at IS NULL AND rr.id > rs.last_undo_result_id
		ORDER BY rr.created_at DESC, rr.id DESC LIMIT 1`, sessionID).Scan(
		&result.id, &result.itemID, &result.vocabularyID, &result.stageBefore, &result.stageAfter,
		&result.dueBefore, &result.dueAfter, &result.lastReviewedBefore, &result.srsApplied,
		&result.createdAt, &result.itemSRSApplied, &result.itemStatus, &result.vocabularyStatus,
		&result.sessionStatus, &result.sessionKind, &result.lessonSessionID,
		&visibilityExisted, &visibilityHidden, &visibilityLeechHidden,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return undoResult{}, stateError("no review result can be undone")
	}
	if err != nil {
		return undoResult{}, fmt.Errorf("load latest review result: %w", err)
	}
	result.visibilityBefore, err = newMistakeVisibilitySnapshot(visibilityExisted, visibilityHidden, visibilityLeechHidden)
	if err != nil {
		return undoResult{}, err
	}
	return result, nil
}

func validateUndoQueueTx(ctx context.Context, tx *sql.Tx, sessionID, vocabularyID int64) error {
	var activeAttempts int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM review_prompts rp
		JOIN review_session_items rsi ON rsi.id = rp.session_item_id
		WHERE rsi.session_id = ? AND rsi.status IN ('pending', 'current')
		  AND rp.status = 'current' AND rp.attempt_count > 0`, sessionID).Scan(&activeAttempts); err != nil {
		return fmt.Errorf("check active review attempts: %w", err)
	}
	if activeAttempts > 0 {
		return stateError("finish the current prompt before undoing an earlier result")
	}

	var reinforcementPending int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM review_session_items
		WHERE session_id = ? AND vocabulary_id = ? AND srs_applied = 0 AND status IN ('pending', 'current')`, sessionID, vocabularyID).Scan(&reinforcementPending); err != nil {
		return fmt.Errorf("check reinforcement queue: %w", err)
	}
	if reinforcementPending > 0 {
		return stateError("finish the reinforcement review before undoing this result")
	}
	return nil
}

func validateUndoSRSStateTx(ctx context.Context, tx *sql.Tx, result undoResult) error {
	if !result.stageBefore.Valid || !result.stageAfter.Valid {
		return errors.New("review result is missing its SRS snapshot")
	}
	var newerResults int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM review_results rr
			JOIN review_session_items rsi ON rsi.id = rr.session_item_id
			WHERE rsi.vocabulary_id = ?
			  AND rr.srs_applied = 1
			  AND rr.voided_at IS NULL
			  AND rr.id > ?
		)`, result.vocabularyID, result.id).Scan(&newerResults); err != nil {
		return fmt.Errorf("check newer review results: %w", err)
	}
	if newerResults != 0 {
		return stateError("a newer review has already changed this word")
	}

	var currentStage int64
	var currentDue, currentLastReviewed sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT stage, due_at, last_reviewed_at
		FROM srs_states
		WHERE vocabulary_id = ?`, result.vocabularyID).Scan(&currentStage, &currentDue, &currentLastReviewed)
	if errors.Is(err, sql.ErrNoRows) {
		return stateError("the word's SRS state has changed since this review")
	}
	if err != nil {
		return fmt.Errorf("load current SRS state: %w", err)
	}
	if currentStage != result.stageAfter.Int64 ||
		!nullableInt64Equal(currentDue, result.dueAfter) ||
		!currentLastReviewed.Valid ||
		currentLastReviewed.Int64 != result.createdAt {
		return stateError("the word's SRS state has changed since this review")
	}
	return nil
}

func restoreUndoSRSStateTx(ctx context.Context, tx *sql.Tx, result undoResult) error {
	update, err := tx.ExecContext(ctx, `
		UPDATE srs_states SET stage = ?, due_at = ?, last_reviewed_at = ? WHERE vocabulary_id = ?`,
		result.stageBefore.Int64, nullableInt64(result.dueBefore), nullableInt64(result.lastReviewedBefore), result.vocabularyID,
	)
	if err != nil {
		return fmt.Errorf("restore SRS state: %w", err)
	}
	if affected, err := update.RowsAffected(); err != nil {
		return fmt.Errorf("check restored SRS state: %w", err)
	} else if affected != 1 {
		return stateError("the word's SRS state could not be restored")
	}
	return nil
}

type mistakeVisibilitySnapshot struct {
	recorded bool
	before   mistakeVisibilityState
}

func newMistakeVisibilitySnapshot(existed, hiddenAt, leechHiddenAt sql.NullInt64) (mistakeVisibilitySnapshot, error) {
	if !existed.Valid {
		return mistakeVisibilitySnapshot{}, nil
	}
	if existed.Int64 != 0 && existed.Int64 != 1 {
		return mistakeVisibilitySnapshot{}, errors.New("review result has an invalid mistake visibility snapshot")
	}
	if existed.Int64 == 0 && (hiddenAt.Valid || leechHiddenAt.Valid) {
		return mistakeVisibilitySnapshot{}, errors.New("review result has an invalid mistake visibility snapshot")
	}
	return mistakeVisibilitySnapshot{
		recorded: true,
		before: mistakeVisibilityState{
			exists:        existed.Int64 == 1,
			hiddenAt:      hiddenAt,
			leechHiddenAt: leechHiddenAt,
		},
	}, nil
}

func validateMistakeVisibilityUndoTx(ctx context.Context, tx *sql.Tx, vocabularyID int64, snapshot mistakeVisibilitySnapshot) error {
	if !snapshot.recorded {
		return nil
	}
	current, err := loadMistakeVisibilityTx(ctx, tx, vocabularyID)
	if err != nil {
		return err
	}
	if current.exists != snapshot.before.exists ||
		current.hiddenAt.Valid || current.leechHiddenAt.Valid {
		return stateError("mistake visibility has changed since this review")
	}
	return nil
}

func restoreMistakeVisibilityTx(ctx context.Context, tx *sql.Tx, vocabularyID int64, snapshot mistakeVisibilitySnapshot) error {
	if !snapshot.recorded || !snapshot.before.exists {
		return nil
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE mistake_visibility
		SET hidden_at = ?, leech_hidden_at = ?
		WHERE vocabulary_id = ?`,
		nullableInt64(snapshot.before.hiddenAt), nullableInt64(snapshot.before.leechHiddenAt), vocabularyID,
	)
	if err != nil {
		return fmt.Errorf("restore mistake visibility: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check restored mistake visibility: %w", err)
	}
	if affected != 1 {
		return stateError("mistake visibility could not be restored")
	}
	return nil
}

func validateUndoContextTx(ctx context.Context, tx *sql.Tx, sessionID int64, sessionKind, vocabularyStatus string, lessonSessionID sql.NullInt64) error {
	if lessonSessionID.Valid {
		if vocabularyStatus != "unlearned" && vocabularyStatus != "active" && vocabularyStatus != "suspended" {
			return stateError("the vocabulary is no longer available for lesson review")
		}
		var linked bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1
				FROM lesson_sessions ls
				WHERE ls.id = ?
				  AND ls.status = 'active'
				  AND ls.phase = 'review'
				  AND ? = (
				      SELECT current_review.id
				      FROM review_sessions current_review
				      WHERE current_review.lesson_session_id = ls.id
				      ORDER BY current_review.id DESC
				      LIMIT 1
				  )
			)`, lessonSessionID.Int64, sessionID).Scan(&linked); err != nil {
			return fmt.Errorf("check lesson review link: %w", err)
		}
		if !linked {
			return stateError("lesson review is no longer available for undo")
		}
		return nil
	}

	if vocabularyStatus != "active" && vocabularyStatus != "suspended" {
		return stateError("the vocabulary is no longer available for review")
	}
	var anotherActive bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM review_sessions
			WHERE id <> ?
			  AND kind = ?
			  AND lesson_session_id IS NULL
			  AND status IN ('active', 'paused')
		)`, sessionID, sessionKind).Scan(&anotherActive); err != nil {
		return fmt.Errorf("check active review sessions: %w", err)
	}
	if anotherActive {
		return stateError("another review session of this kind is already active")
	}
	return nil
}

func nullableInt64Equal(left, right sql.NullInt64) bool {
	if left.Valid != right.Valid {
		return false
	}
	return !left.Valid || left.Int64 == right.Int64
}

func (s *Store) insertUndoItemTx(ctx context.Context, tx *sql.Tx, sessionID, vocabularyID, sourceItemID int64, srsApplied int) error {
	var target int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MIN(position), (SELECT COALESCE(MAX(position) + 1, 0) FROM review_session_items WHERE session_id = ?))
		FROM review_session_items WHERE session_id = ? AND status IN ('pending', 'current')`, sessionID, sessionID).Scan(&target); err != nil {
		return fmt.Errorf("find undo queue position: %w", err)
	}
	if err := makeQueueRoomTx(ctx, tx, sessionID, target); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE review_session_items SET status = 'pending' WHERE session_id = ? AND status = 'current'`, sessionID); err != nil {
		return fmt.Errorf("reset current review item: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE review_prompts
		SET status = 'pending', attempt_count = 0,
		    last_incorrect_answer = '', last_incorrect_content_revision = 0
		WHERE status = 'current'
		  AND session_item_id IN (SELECT id FROM review_session_items WHERE session_id = ? AND status = 'pending')`, sessionID); err != nil {
		return fmt.Errorf("reset queued review prompts: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO review_session_items (session_id, vocabulary_id, position, srs_applied, status)
		VALUES (?, ?, ?, ?, 'current')`, sessionID, vocabularyID, target, srsApplied)
	if err != nil {
		return fmt.Errorf("insert undo review item: %w", err)
	}
	itemID, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get undo review item ID: %w", err)
	}
	if err := cloneReviewPromptsTx(ctx, tx, sourceItemID, itemID, true); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE review_sessions
		SET status = 'active', completed_at = NULL
		WHERE id = ? AND status IN ('active', 'completed')`, sessionID)
	if err != nil {
		return fmt.Errorf("reopen review session: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check reopened review session: %w", err)
	} else if affected != 1 {
		return stateError("review session is no longer available for undo")
	}
	return nil
}

func makeQueueRoomTx(ctx context.Context, tx *sql.Tx, sessionID int64, target int) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE review_session_items
		SET position = position + ?
		WHERE session_id = ? AND position >= ?`, queuePositionOffset, sessionID, target); err != nil {
		return fmt.Errorf("make room in review queue: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE review_session_items
		SET position = position - ?
		WHERE session_id = ? AND position >= ?`,
		queuePositionOffset-1, sessionID, target+queuePositionOffset); err != nil {
		return fmt.Errorf("shift review queue: %w", err)
	}
	return nil
}
