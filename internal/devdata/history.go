package devdata

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/tomasmik/goi/internal/srs"
)

type historyEvent struct {
	daysAgo     int
	fixture     int
	success     bool
	stageBefore srs.Stage
	stageAfter  srs.Stage
}

func insertReviewHistory(ctx context.Context, tx *sql.Tx, ids []int64, now time.Time) error {
	events := []historyEvent{
		{daysAgo: 6, fixture: 10, success: true, stageBefore: srs.StageTwo, stageAfter: srs.StageThree},
		{daysAgo: 5, fixture: 19, stageBefore: srs.StageOne, stageAfter: srs.StageNew},
		{daysAgo: 4, fixture: 19, success: true, stageBefore: srs.StageNew, stageAfter: srs.StageOne},
		{daysAgo: 3, fixture: 19, stageBefore: srs.StageOne, stageAfter: srs.StageNew},
		{daysAgo: 2, fixture: 19, success: true, stageBefore: srs.StageNew, stageAfter: srs.StageOne},
		{daysAgo: 1, fixture: 11, success: true, stageBefore: srs.StageFour, stageAfter: srs.StageFive},
		{fixture: 19, stageBefore: srs.StageOne, stageAfter: srs.StageNew},
	}
	for _, event := range events {
		eventAt := now.AddDate(0, 0, -event.daysAgo)
		if err := insertHistoryEvent(ctx, tx, ids[event.fixture], event, eventAt); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO leech_states (vocabulary_id, active, ever_leech, marked_at)
		VALUES (?, 1, 1, ?)`, ids[19], now.Unix()); err != nil {
		return fmt.Errorf("mark fixture leech: %w", err)
	}
	return nil
}

func insertHistoryEvent(
	ctx context.Context,
	tx *sql.Tx,
	vocabularyID int64,
	event historyEvent,
	eventAt time.Time,
) error {
	sessionResult, err := tx.ExecContext(ctx, `
		INSERT INTO review_sessions (kind, status, completed_at, max_attempts)
		VALUES ('normal', 'completed', ?, 3)`,
		eventAt.Unix())
	if err != nil {
		return fmt.Errorf("insert fixture review session: %w", err)
	}
	sessionID, err := sessionResult.LastInsertId()
	if err != nil {
		return fmt.Errorf("get fixture review session ID: %w", err)
	}

	itemResult, err := tx.ExecContext(ctx, `
		INSERT INTO review_session_items (
			session_id, vocabulary_id, position, srs_applied, status
		)
		VALUES (?, ?, 0, 1, 'completed')`,
		sessionID, vocabularyID)
	if err != nil {
		return fmt.Errorf("insert fixture review item: %w", err)
	}
	itemID, err := itemResult.LastInsertId()
	if err != nil {
		return fmt.Errorf("get fixture review item ID: %w", err)
	}

	if event.success {
		if err := insertSuccessfulPrompts(ctx, tx, itemID); err != nil {
			return err
		}
	} else if err := insertFailedPrompts(ctx, tx, itemID); err != nil {
		return err
	}
	if err := insertAppliedResult(ctx, tx, itemID, event, eventAt); err != nil {
		return err
	}

	if !event.success {
		if err := insertReinforcement(ctx, tx, sessionID, vocabularyID, eventAt); err != nil {
			return err
		}
	}
	return nil
}

func insertSuccessfulPrompts(ctx context.Context, tx *sql.Tx, itemID int64) error {
	for position, promptType := range []string{"meaning", "pronunciation"} {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO review_prompts (
				session_item_id, prompt_type, position, status, attempt_count
			)
			VALUES (?, ?, ?, 'passed', 1)`,
			itemID, promptType, position)
		if err != nil {
			return fmt.Errorf("insert successful fixture prompt: %w", err)
		}
	}
	return nil
}

func insertFailedPrompts(ctx context.Context, tx *sql.Tx, itemID int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO review_prompts (
			session_item_id, prompt_type, position, status, attempt_count
		)
		VALUES (?, 'meaning', 0, 'failed', 3)`, itemID)
	if err != nil {
		return fmt.Errorf("insert failed fixture prompt: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO review_prompts (session_item_id, prompt_type, position, status)
		VALUES (?, 'pronunciation', 1, 'pending')`, itemID); err != nil {
		return fmt.Errorf("insert unattempted fixture prompt: %w", err)
	}
	return nil
}

func insertAppliedResult(ctx context.Context, tx *sql.Tx, itemID int64, event historyEvent, eventAt time.Time) error {
	outcome := "failure"
	firstCorrect := 0
	promptCount := 1
	if event.success {
		outcome = "success"
		firstCorrect = 2
		promptCount = 2
	}
	var mistakeVisibilityExistedBefore any
	if !event.success {
		mistakeVisibilityExistedBefore = 0
	}
	nextDue := srs.DueAt(event.stageAfter, eventAt)
	var dueAfter any
	if !nextDue.IsZero() {
		dueAfter = nextDue.Unix()
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO review_results (
			session_item_id, outcome,
			stage_before, stage_after, due_before, due_after, created_at,
			srs_applied, first_attempt_correct_count, prompt_count,
			mistake_visibility_existed_before
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`,
		itemID, outcome,
		event.stageBefore, event.stageAfter, eventAt.Add(-time.Minute).Unix(), dueAfter,
		eventAt.Unix(), firstCorrect, promptCount, mistakeVisibilityExistedBefore); err != nil {
		return fmt.Errorf("insert fixture review result: %w", err)
	}
	return nil
}

func insertReinforcement(
	ctx context.Context,
	tx *sql.Tx,
	sessionID, vocabularyID int64,
	eventAt time.Time,
) error {
	itemResult, err := tx.ExecContext(ctx, `
		INSERT INTO review_session_items (
			session_id, vocabulary_id, position, srs_applied, status
		)
		VALUES (?, ?, 1, 0, 'completed')`,
		sessionID, vocabularyID)
	if err != nil {
		return fmt.Errorf("insert fixture reinforcement: %w", err)
	}
	itemID, err := itemResult.LastInsertId()
	if err != nil {
		return fmt.Errorf("get fixture reinforcement ID: %w", err)
	}
	if err := insertSuccessfulPrompts(ctx, tx, itemID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO review_results (
			session_item_id, outcome,
			created_at, srs_applied, first_attempt_correct_count, prompt_count
		)
		VALUES (?, 'success', ?, 0, 2, 2)`,
		itemID, eventAt.Unix()); err != nil {
		return fmt.Errorf("insert fixture reinforcement result: %w", err)
	}
	return nil
}
