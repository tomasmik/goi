package reviews

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tomasmik/goi/internal/examples"
)

type State struct {
	ID              int64
	Status          string
	Kind            string
	Total           int
	Completed       int
	Remaining       int
	WordTotal       int
	WordCompleted   int
	WordsRemaining  int
	RetriesQueued   int
	ActiveRetry     bool
	FirstTryCorrect int
	PromptCount     int
	FirstTryMisses  int
	RetriesComplete int
	VocabularyID    int64
	Expression      string
	PromptID        int64
	PromptType      string
	Attempts        int
	MaxAttempts     int
	RejectedAnswer  string
	Feedback        bool
	UndoAvailable   bool
	AudioEnabled    bool
	AnswerMode      string
	AutoAdvance     bool
	AudioID         int64
	PictureID       int64
	Meanings        []string
	Pronunciation   string
	Notes           string
	Example         examples.Example
	LessonSessionID int64
	LeechActive     bool
	LeechSuspended  bool
	FormerLeech     bool
	NextReviewAt    time.Time
}

func (s *Store) Pause(ctx context.Context, sessionID int64) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE review_sessions SET status = 'paused'
		WHERE id = ? AND status = 'active'`, sessionID)
	if err != nil {
		return fmt.Errorf("pause review session: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check paused review session: %w", err)
	} else if affected == 0 {
		return stateError("review session is not active")
	}
	return nil
}

func (s *Store) Resume(ctx context.Context, sessionID int64) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE review_sessions SET status = 'active'
		WHERE id = ? AND status = 'paused'`, sessionID)
	if err != nil {
		return fmt.Errorf("resume review session: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check resumed review session: %w", err)
	} else if affected == 0 {
		return stateError("review session is not paused")
	}
	return nil
}

func (s *Store) State(ctx context.Context, sessionID int64) (State, error) {
	var state State
	if err := s.loadSessionSummary(ctx, sessionID, &state); err != nil {
		return State{}, err
	}
	if err := s.loadSessionPrompt(ctx, sessionID, &state); err != nil {
		return State{}, err
	}
	if state.VocabularyID != 0 {
		if err := s.loadAnswers(ctx, &state); err != nil {
			return State{}, err
		}
		if err := s.loadLeechPresentation(ctx, &state); err != nil {
			return State{}, err
		}
	}
	undoAvailable, err := s.undoAvailable(ctx, sessionID)
	if err != nil {
		return State{}, fmt.Errorf("check review undo: %w", err)
	}
	state.UndoAvailable = undoAvailable
	return state, nil
}

func (s *Store) loadSessionSummary(ctx context.Context, sessionID int64, state *State) error {
	if err := s.db.QueryRowContext(ctx, `
		SELECT id, status, kind, COALESCE(lesson_session_id, 0), answer_mode
		FROM review_sessions
		WHERE id = ?`, sessionID).Scan(
		&state.ID, &state.Status, &state.Kind, &state.LessonSessionID, &state.AnswerMode,
	); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN rsi.status = 'completed' THEN 1 ELSE 0 END), 0)
		FROM review_session_items rsi
		WHERE rsi.session_id = ?
		  AND NOT EXISTS (
		      SELECT 1
		      FROM review_results rr
		      WHERE rr.session_item_id = rsi.id AND rr.voided_at IS NOT NULL
		  )`, sessionID).Scan(&state.Total, &state.Completed); err != nil {
		return fmt.Errorf("count review items: %w", err)
	}
	state.Remaining = state.Total - state.Completed
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(completed), 0)
		FROM (
			SELECT rsi.vocabulary_id,
			       MIN(CASE WHEN rsi.status = 'completed' THEN 1 ELSE 0 END) AS completed
			FROM review_session_items rsi
			WHERE rsi.session_id = ?
			  AND NOT EXISTS (
			      SELECT 1 FROM review_results rr
			      WHERE rr.session_item_id = rsi.id AND rr.voided_at IS NOT NULL
			  )
			GROUP BY rsi.vocabulary_id
		)`, sessionID).Scan(&state.WordTotal, &state.WordCompleted); err != nil {
		return fmt.Errorf("count review words: %w", err)
	}
	state.WordsRemaining = state.WordTotal - state.WordCompleted
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM review_session_items queued
		WHERE queued.session_id = ? AND queued.status = 'pending'
		  AND EXISTS (
		      SELECT 1 FROM review_session_items earlier
		      WHERE earlier.session_id = queued.session_id
		        AND earlier.vocabulary_id = queued.vocabulary_id
		        AND earlier.id < queued.id
		  )`, sessionID).Scan(&state.RetriesQueued); err != nil {
		return fmt.Errorf("count queued review retries: %w", err)
	}
	if state.Status == "completed" {
		if err := s.db.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(rr.first_attempt_correct_count), 0),
			       COALESCE(SUM(rr.prompt_count), 0)
			FROM review_results rr
			JOIN review_session_items rsi ON rsi.id = rr.session_item_id
			WHERE rsi.session_id = ? AND rr.voided_at IS NULL`, sessionID).Scan(
			&state.FirstTryCorrect, &state.PromptCount,
		); err != nil {
			return fmt.Errorf("load review accuracy: %w", err)
		}
		state.FirstTryMisses = state.PromptCount - state.FirstTryCorrect
		state.RetriesComplete = state.Total - state.WordTotal
		if state.Kind == "normal" || state.LessonSessionID > 0 {
			var dueAt sql.NullInt64
			if err := s.db.QueryRowContext(ctx, `
				SELECT MIN(ss.due_at)
				FROM srs_states ss
				WHERE ss.vocabulary_id IN (
					SELECT DISTINCT vocabulary_id FROM review_session_items WHERE session_id = ?
				)`, sessionID).Scan(&dueAt); err != nil {
				return fmt.Errorf("load next review time: %w", err)
			}
			if dueAt.Valid {
				state.NextReviewAt = time.Unix(dueAt.Int64, 0).UTC()
			}
		}
	}
	return nil
}

func (s *Store) loadSessionPrompt(ctx context.Context, sessionID int64, state *State) error {
	err := s.db.QueryRowContext(ctx, `
		SELECT rsi.vocabulary_id, v.expression, rp.id, rp.prompt_type, rp.attempt_count,
		       rs.max_attempts, rp.last_incorrect_answer
		FROM review_session_items rsi
		JOIN vocabulary v ON v.id = rsi.vocabulary_id
		JOIN review_prompts rp ON rp.session_item_id = rsi.id
		JOIN review_sessions rs ON rs.id = rsi.session_id
		WHERE rsi.session_id = ? AND rp.status = 'current'
		ORDER BY rp.queue_position, rp.id LIMIT 1`, sessionID).Scan(
		&state.VocabularyID, &state.Expression, &state.PromptID, &state.PromptType, &state.Attempts,
		&state.MaxAttempts, &state.RejectedAnswer,
	)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx, `
			SELECT rsi.vocabulary_id, v.expression, rp.id, rp.prompt_type, rp.attempt_count,
			       rs.max_attempts, rp.last_incorrect_answer
			FROM review_session_items rsi
			JOIN vocabulary v ON v.id = rsi.vocabulary_id
			JOIN review_prompts rp ON rp.session_item_id = rsi.id
			JOIN review_sessions rs ON rs.id = rsi.session_id
			WHERE rsi.session_id = ? AND rsi.status = 'failed' AND rp.status = 'failed'
			ORDER BY rsi.position DESC LIMIT 1`, sessionID).Scan(
			&state.VocabularyID, &state.Expression, &state.PromptID, &state.PromptType, &state.Attempts,
			&state.MaxAttempts, &state.RejectedAnswer,
		)
		state.Feedback = err == nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load current review prompt: %w", err)
	}
	if err == nil {
		if err := s.db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM review_session_items current_item
				JOIN review_prompts current_prompt ON current_prompt.session_item_id = current_item.id
				JOIN review_session_items earlier ON earlier.session_id = current_item.session_id
					AND earlier.vocabulary_id = current_item.vocabulary_id
					AND earlier.id < current_item.id
				WHERE current_prompt.id = ?
			)`, state.PromptID).Scan(&state.ActiveRetry); err != nil {
			return fmt.Errorf("check active review retry: %w", err)
		}
	}
	return nil
}

func (s *Store) loadLeechPresentation(ctx context.Context, state *State) error {
	var active, everLeech, suspended bool
	err := s.db.QueryRowContext(ctx, `
		SELECT ls.active, ls.ever_leech, v.status = 'suspended'
		FROM leech_states ls
		JOIN vocabulary v ON v.id = ls.vocabulary_id
		WHERE ls.vocabulary_id = ?`, state.VocabularyID).Scan(&active, &everLeech, &suspended)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load review leech state: %w", err)
	}
	state.LeechActive = active
	state.LeechSuspended = active && suspended
	state.FormerLeech = everLeech && !active
	return nil
}

func (s *Store) loadAnswers(ctx context.Context, state *State) error {
	rows, err := s.db.QueryContext(ctx, `SELECT text FROM meanings WHERE vocabulary_id = ? ORDER BY position`, state.VocabularyID)
	if err != nil {
		return fmt.Errorf("load review meanings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return fmt.Errorf("scan review meaning: %w", err)
		}
		state.Meanings = append(state.Meanings, value)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate review meanings: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close review meanings: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT pronunciation FROM vocabulary WHERE id = ?`, state.VocabularyID).Scan(&state.Pronunciation); err != nil {
		return fmt.Errorf("load review pronunciation: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(notes, '') FROM vocabulary WHERE id = ?`, state.VocabularyID).Scan(&state.Notes); err != nil {
		return fmt.Errorf("load review notes: %w", err)
	}
	example, err := s.exampleStore.Preferred(ctx, state.VocabularyID)
	if err == nil {
		state.Example = example
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load review example: %w", err)
	}
	var audioEnabled, autoAdvance int
	if err := s.db.QueryRowContext(ctx, `
		SELECT audio_enabled, review_auto_advance FROM user_settings WHERE id = 1`).Scan(&audioEnabled, &autoAdvance); err == nil {
		state.AudioEnabled = audioEnabled == 1
		state.AutoAdvance = autoAdvance == 1
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load audio setting: %w", err)
	}
	var audioID sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
		SELECT m.id FROM vocabulary_media vm JOIN media m ON m.id = vm.media_id
		WHERE vm.vocabulary_id = ? AND vm.purpose = 'pronunciation' LIMIT 1`, state.VocabularyID).Scan(&audioID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load review audio: %w", err)
	}
	if audioID.Valid {
		state.AudioID = audioID.Int64
	}
	var pictureID sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
		SELECT m.id FROM vocabulary_media vm JOIN media m ON m.id = vm.media_id
		WHERE vm.vocabulary_id = ? AND vm.purpose = 'picture' LIMIT 1`, state.VocabularyID).Scan(&pictureID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load review picture: %w", err)
	}
	if pictureID.Valid {
		state.PictureID = pictureID.Int64
	}
	return nil
}
