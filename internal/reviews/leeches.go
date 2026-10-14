package reviews

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/tomasmik/goi/internal/srs"
)

type leechSettings struct {
	markAfter    int
	suspendAfter int
	clearAfter   int
}

type leechState struct {
	exists                 bool
	failuresTowardLeech    int
	active                 bool
	everLeech              bool
	markedAt               sql.NullInt64
	failuresSinceMark      int
	correctStreak          int
	autoSuspendedAt        sql.NullInt64
	clearedAt              sql.NullInt64
	resetAfterReviewResult int64
}

type leechResult struct {
	success     bool
	srsApplied  bool
	stageBefore int
	at          int64
}

const leechMinimumStage = int(srs.StageFour)

func updateLeechAfterResultTx(ctx context.Context, tx *sql.Tx, vocabularyID int64, result leechResult) error {
	previous, err := loadLeechStateTx(ctx, tx, vocabularyID)
	if err != nil {
		return err
	}
	if !previous.active && (result.success || !result.srsApplied || result.stageBefore < leechMinimumStage) {
		return nil
	}
	settings, err := loadLeechSettingsTx(ctx, tx)
	if err != nil {
		return err
	}
	state := previous
	state.exists = true
	applyLeechResult(&state, settings, result)
	if err := syncLeechSuspensionTx(ctx, tx, vocabularyID, previous, &state); err != nil {
		return err
	}
	return saveLeechStateTx(ctx, tx, vocabularyID, state)
}

func applyLeechResult(state *leechState, settings leechSettings, result leechResult) {
	if !state.active {
		if result.success || !result.srsApplied || result.stageBefore < leechMinimumStage {
			return
		}
		state.failuresTowardLeech++
		if state.failuresTowardLeech < settings.markAfter {
			return
		}
		state.failuresTowardLeech = 0
		state.active = true
		state.everLeech = true
		state.markedAt = sql.NullInt64{Int64: result.at, Valid: true}
		state.failuresSinceMark = 0
		state.correctStreak = 0
		state.autoSuspendedAt = sql.NullInt64{}
		state.clearedAt = sql.NullInt64{}
		return
	}
	if state.autoSuspendedAt.Valid {
		if !result.success {
			state.correctStreak = 0
			return
		}
		state.correctStreak++
		if state.correctStreak >= settings.clearAfter {
			state.autoSuspendedAt = sql.NullInt64{}
			state.failuresSinceMark = 0
			state.correctStreak = 0
		}
		return
	}
	if !result.srsApplied {
		return
	}
	if !result.success {
		state.failuresSinceMark++
		state.correctStreak = 0
		if state.failuresSinceMark >= settings.suspendAfter {
			state.autoSuspendedAt = sql.NullInt64{Int64: result.at, Valid: true}
		}
		return
	}
	state.correctStreak++
	if state.correctStreak < settings.clearAfter {
		return
	}
	state.active = false
	state.markedAt = sql.NullInt64{}
	state.failuresSinceMark = 0
	state.correctStreak = 0
	state.clearedAt = sql.NullInt64{Int64: result.at, Valid: true}
}

func syncLeechSuspensionTx(ctx context.Context, tx *sql.Tx, vocabularyID int64, previous leechState, state *leechState) error {
	switch {
	case previous.autoSuspendedAt.Valid && !state.autoSuspendedAt.Valid:
		return resumeAutoSuspensionTx(ctx, tx, vocabularyID, previous.autoSuspendedAt.Int64)
	case !previous.autoSuspendedAt.Valid && state.autoSuspendedAt.Valid:
		return autoSuspendLeechTx(ctx, tx, vocabularyID, state)
	case previous.autoSuspendedAt.Valid && state.autoSuspendedAt.Valid:
		state.autoSuspendedAt = previous.autoSuspendedAt
	}
	return nil
}

func loadLeechSettingsTx(ctx context.Context, tx *sql.Tx) (leechSettings, error) {
	settings := leechSettings{markAfter: 5, suspendAfter: 3, clearAfter: 3}
	err := tx.QueryRowContext(ctx, `
		SELECT leech_failure_threshold, leech_suspend_threshold, leech_recovery_streak
		FROM user_settings WHERE id = 1`).Scan(&settings.markAfter, &settings.suspendAfter, &settings.clearAfter)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return leechSettings{}, fmt.Errorf("load leech settings: %w", err)
	}
	return settings, nil
}

func loadLeechStateTx(ctx context.Context, tx *sql.Tx, vocabularyID int64) (leechState, error) {
	var state leechState
	var active, everLeech int
	err := tx.QueryRowContext(ctx, `
		SELECT failures_toward_leech, active, ever_leech, marked_at,
		       failures_since_mark, correct_streak, auto_suspended_at, cleared_at,
		       reset_after_result_id
		FROM leech_states WHERE vocabulary_id = ?`, vocabularyID).Scan(
		&state.failuresTowardLeech, &active, &everLeech, &state.markedAt,
		&state.failuresSinceMark, &state.correctStreak, &state.autoSuspendedAt, &state.clearedAt,
		&state.resetAfterReviewResult,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return leechState{}, fmt.Errorf("load leech state: %w", err)
	}
	state.exists = true
	state.active = active == 1
	state.everLeech = everLeech == 1
	return state, nil
}

func saveLeechStateTx(ctx context.Context, tx *sql.Tx, vocabularyID int64, state leechState) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO leech_states (
			vocabulary_id, failures_toward_leech, active, ever_leech, marked_at,
			failures_since_mark, correct_streak, auto_suspended_at, cleared_at,
			reset_after_result_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(vocabulary_id) DO UPDATE SET
			failures_toward_leech = excluded.failures_toward_leech,
			active = excluded.active,
			ever_leech = excluded.ever_leech,
			marked_at = excluded.marked_at,
			failures_since_mark = excluded.failures_since_mark,
			correct_streak = excluded.correct_streak,
			auto_suspended_at = excluded.auto_suspended_at,
			cleared_at = excluded.cleared_at,
			reset_after_result_id = excluded.reset_after_result_id`,
		vocabularyID, state.failuresTowardLeech, boolInt(state.active), boolInt(state.everLeech), nullableInt64(state.markedAt),
		state.failuresSinceMark, state.correctStreak, nullableInt64(state.autoSuspendedAt), nullableInt64(state.clearedAt),
		state.resetAfterReviewResult,
	)
	if err != nil {
		return fmt.Errorf("save leech state: %w", err)
	}
	return nil
}

func autoSuspendLeechTx(ctx context.Context, tx *sql.Tx, vocabularyID int64, state *leechState) error {
	at := state.autoSuspendedAt.Int64
	state.autoSuspendedAt = sql.NullInt64{}
	result, err := tx.ExecContext(ctx, `
		UPDATE srs_states
		SET suspended_at = ?
		WHERE vocabulary_id = ? AND suspended_at IS NULL
		  AND EXISTS (SELECT 1 FROM vocabulary WHERE id = ? AND status = 'active')`, at, vocabularyID, vocabularyID)
	if err != nil {
		return fmt.Errorf("suspend leech reviews: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check suspended leech reviews: %w", err)
	}
	if affected == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE vocabulary SET status = 'suspended', updated_at = ? WHERE id = ?`, at, vocabularyID); err != nil {
		return fmt.Errorf("mark leech suspended: %w", err)
	}
	state.autoSuspendedAt = sql.NullInt64{Int64: at, Valid: true}
	return nil
}

func (s *Store) rebuildLeechStateTx(ctx context.Context, tx *sql.Tx, vocabularyID int64) error {
	previous, err := loadLeechStateTx(ctx, tx, vocabularyID)
	if err != nil || !previous.exists {
		return err
	}
	settings, err := loadLeechSettingsTx(ctx, tx)
	if err != nil {
		return err
	}
	state := leechState{
		exists:                 true,
		everLeech:              previous.resetAfterReviewResult > 0 && previous.everLeech,
		resetAfterReviewResult: previous.resetAfterReviewResult,
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT rr.outcome, rr.srs_applied, COALESCE(rr.stage_before, 0), rr.created_at
		FROM review_results rr
		JOIN review_session_items rsi ON rsi.id = rr.session_item_id
		WHERE rsi.vocabulary_id = ? AND rr.voided_at IS NULL AND rr.id > ?
		ORDER BY rr.id`, vocabularyID, state.resetAfterReviewResult)
	if err != nil {
		return fmt.Errorf("load leech review history: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var outcome string
		var result leechResult
		if err := rows.Scan(&outcome, &result.srsApplied, &result.stageBefore, &result.at); err != nil {
			return fmt.Errorf("scan leech review history: %w", err)
		}
		result.success = outcome == "success"
		applyLeechResult(&state, settings, result)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate leech review history: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close leech review history: %w", err)
	}
	if err := syncLeechSuspensionTx(ctx, tx, vocabularyID, previous, &state); err != nil {
		return err
	}
	return saveLeechStateTx(ctx, tx, vocabularyID, state)
}

func resumeAutoSuspensionTx(ctx context.Context, tx *sql.Tx, vocabularyID, suspendedAt int64) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE srs_states SET suspended_at = NULL
		WHERE vocabulary_id = ? AND suspended_at = ?`, vocabularyID, suspendedAt)
	if err != nil {
		return fmt.Errorf("resume automatic leech suspension: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check resumed automatic leech suspension: %w", err)
	} else if affected == 1 {
		if _, err := tx.ExecContext(ctx, `UPDATE vocabulary SET status = 'active' WHERE id = ? AND status = 'suspended'`, vocabularyID); err != nil {
			return fmt.Errorf("reactivate leech: %w", err)
		}
	}
	return nil
}
