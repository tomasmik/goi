package reviews

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tomasmik/goi/internal/examples"
	"github.com/tomasmik/goi/internal/srs"
	"github.com/tomasmik/goi/internal/textnorm"
)

const (
	maxRejectedMeaningRunes = 200
	maxMeaningsRunes        = 2000
)

type Store struct {
	db           *sql.DB
	exampleStore *examples.Store
	startMu      sync.Mutex
}

type StudyCounts struct {
	RecentMistakes int
	Current        int
	Leeches        int
}

type LeechItem struct {
	ID                int64
	Expression        string
	Pronunciation     string
	FailuresSinceMark int
	CorrectStreak     int
	Suspended         bool
}

type AnswerOutcome struct {
	Value        MatchResult
	FinalFailure bool
}

type activePrompt struct {
	itemID          int64
	vocabularyID    int64
	contentRevision int64
	promptType      string
	answerMode      string
	attempts        int
	srsApplied      bool
	maxAttempts     int
	rejectedAnswer  string
}

type rejectedMeaningPrompt struct {
	itemID                  int64
	vocabularyID            int64
	currentContentRevision  int64
	rejectedContentRevision int64
	promptType              string
	promptStatus            string
	itemStatus              string
	recordedAnswer          string
	attempts                int
	srsApplied              bool
}

type stateError string

func (err stateError) Error() string {
	return string(err)
}

func (err stateError) UserMessage() string {
	return string(err)
}

func stateErrorf(format string, args ...any) error {
	return stateError(fmt.Sprintf(format, args...))
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, exampleStore: examples.NewStore(db)}
}

func (s *Store) CheckAnswer(ctx context.Context, sessionID, promptID int64, answer string) (MatchResult, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Incorrect, fmt.Errorf("begin answer check: %w", err)
	}
	defer tx.Rollback()

	_, result, err := s.evaluateAnswerTx(ctx, tx, sessionID, promptID, answer)
	if err != nil {
		return Incorrect, err
	}
	return result, nil
}

func (s *Store) Answer(ctx context.Context, sessionID, promptID int64, answer string) (AnswerOutcome, error) {
	return s.recordAnswer(ctx, sessionID, promptID, answer, false)
}

func (s *Store) ConfirmAnswer(ctx context.Context, sessionID, promptID int64, answer string) (AnswerOutcome, error) {
	return s.recordAnswer(ctx, sessionID, promptID, answer, true)
}

func (s *Store) recordAnswer(ctx context.Context, sessionID, promptID int64, answer string, requireCorrect bool) (AnswerOutcome, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AnswerOutcome{}, fmt.Errorf("begin answer: %w", err)
	}
	defer tx.Rollback()

	prompt, result, err := s.evaluateAnswerTx(ctx, tx, sessionID, promptID, answer)
	if err != nil {
		return AnswerOutcome{}, err
	}
	if requireCorrect && result == Incorrect {
		return AnswerOutcome{}, stateError("answer is no longer correct")
	}
	if prompt.answerMode != reviewModeTyped {
		return AnswerOutcome{}, stateError("this review uses self grading")
	}
	return s.recordPromptResultTx(ctx, tx, sessionID, promptID, prompt, result, answer, false)
}

func (s *Store) Grade(ctx context.Context, sessionID, promptID int64, correct bool) (AnswerOutcome, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AnswerOutcome{}, fmt.Errorf("begin self grade: %w", err)
	}
	defer tx.Rollback()

	prompt, err := s.loadActivePromptTx(ctx, tx, sessionID, promptID)
	if err != nil {
		return AnswerOutcome{}, err
	}
	if prompt.answerMode != reviewModeSelfGrade {
		return AnswerOutcome{}, stateError("this review uses typed answers")
	}
	result := Incorrect
	if correct {
		result = Correct
	}
	return s.recordPromptResultTx(ctx, tx, sessionID, promptID, prompt, result, "", !correct)
}

func (s *Store) GiveUp(ctx context.Context, sessionID, promptID int64) (AnswerOutcome, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AnswerOutcome{}, fmt.Errorf("begin answer reveal: %w", err)
	}
	defer tx.Rollback()

	prompt, err := s.loadActivePromptTx(ctx, tx, sessionID, promptID)
	if err != nil {
		return AnswerOutcome{}, err
	}
	if prompt.answerMode != reviewModeTyped {
		return AnswerOutcome{}, stateError("this review uses self grading")
	}
	if prompt.attempts == 0 {
		return AnswerOutcome{}, stateError("submit an answer before revealing the result")
	}
	return s.recordPromptResultTx(
		ctx, tx, sessionID, promptID, prompt, Incorrect, prompt.rejectedAnswer, true,
	)
}

func (s *Store) recordPromptResultTx(
	ctx context.Context,
	tx *sql.Tx,
	sessionID, promptID int64,
	prompt activePrompt,
	result MatchResult,
	answer string,
	forceFinalFailure bool,
) (AnswerOutcome, error) {
	attemptNumber := prompt.attempts + 1
	if forceFinalFailure {
		attemptNumber = prompt.maxAttempts
	}
	now := time.Now().UTC()
	rejectedAnswer := ""
	rejectedContentRevision := int64(0)
	if result == Incorrect {
		rejectedAnswer = rejectedMeaningCandidate(prompt.promptType, answer)
		if rejectedAnswer != "" {
			rejectedContentRevision = prompt.contentRevision
		}
	}
	if result != Incorrect {
		if _, err := tx.ExecContext(ctx, `
			UPDATE review_prompts
			SET status = 'passed', attempt_count = ?,
			    last_incorrect_answer = '', last_incorrect_content_revision = 0
			WHERE id = ?`, attemptNumber, promptID); err != nil {
			return AnswerOutcome{}, fmt.Errorf("complete review prompt: %w", err)
		}
		var promptsRemaining int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM review_prompts
			WHERE session_item_id = ? AND status = 'pending'`, prompt.itemID).Scan(&promptsRemaining); err != nil {
			return AnswerOutcome{}, fmt.Errorf("count remaining review prompts: %w", err)
		}
		if promptsRemaining == 0 {
			if err := s.finishItemTx(ctx, tx, sessionID, prompt.itemID, prompt.vocabularyID, true, prompt.srsApplied, now); err != nil {
				return AnswerOutcome{}, err
			}
		} else {
			if _, err := tx.ExecContext(ctx, "UPDATE review_session_items SET status = 'pending' WHERE id = ?", prompt.itemID); err != nil {
				return AnswerOutcome{}, fmt.Errorf("queue unfinished review item: %w", err)
			}
			if err := s.promoteNextTx(ctx, tx, sessionID, now); err != nil {
				return AnswerOutcome{}, err
			}
		}
	} else if attemptNumber >= prompt.maxAttempts {
		if _, err := tx.ExecContext(ctx, `
			UPDATE review_prompts
			SET status = 'failed', attempt_count = ?, last_incorrect_answer = ?,
			    last_incorrect_content_revision = ?
			WHERE id = ?`, attemptNumber, rejectedAnswer, rejectedContentRevision, promptID); err != nil {
			return AnswerOutcome{}, fmt.Errorf("fail review prompt: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE review_session_items SET status = 'failed' WHERE id = ?", prompt.itemID); err != nil {
			return AnswerOutcome{}, fmt.Errorf("fail review item: %w", err)
		}
		if err := s.finishItemTx(ctx, tx, sessionID, prompt.itemID, prompt.vocabularyID, false, prompt.srsApplied, now); err != nil {
			return AnswerOutcome{}, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE review_prompts
			SET attempt_count = ?, last_incorrect_answer = ?,
			    last_incorrect_content_revision = ?
			WHERE id = ?`, attemptNumber, rejectedAnswer, rejectedContentRevision, promptID); err != nil {
			return AnswerOutcome{}, fmt.Errorf("record retry count: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return AnswerOutcome{}, fmt.Errorf("commit answer: %w", err)
	}
	return AnswerOutcome{Value: result, FinalFailure: result == Incorrect && attemptNumber >= prompt.maxAttempts}, nil
}

func (s *Store) AddSynonym(ctx context.Context, sessionID, promptID int64, submittedAnswer string) (string, error) {
	return s.AddEditedSynonym(ctx, sessionID, promptID, submittedAnswer, submittedAnswer)
}

func (s *Store) AddEditedSynonym(ctx context.Context, sessionID, promptID int64, rejectedAnswer, submittedAnswer string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin synonym update: %w", err)
	}
	defer tx.Rollback()

	prompt, err := loadRejectedMeaningPromptTx(ctx, tx, sessionID, promptID)
	if err != nil {
		return "", err
	}
	synonym, err := prompt.validate(rejectedAnswer, submittedAnswer)
	if err != nil {
		return "", err
	}
	accepted, err := s.acceptedAnswers(ctx, tx, prompt.vocabularyID, prompt.promptType)
	if err != nil {
		return "", err
	}
	if MatchAnswer(synonym, accepted) != Incorrect {
		return "", stateError("this answer is already accepted")
	}
	now := time.Now().UTC()
	if err := addSynonymMeaningTx(ctx, tx, prompt.vocabularyID, prompt.rejectedContentRevision, synonym, now); err != nil {
		return "", err
	}
	active := activePrompt{
		itemID:       prompt.itemID,
		vocabularyID: prompt.vocabularyID,
		promptType:   prompt.promptType,
		attempts:     prompt.attempts,
		srsApplied:   prompt.srsApplied,
	}
	if err := s.acceptIncorrectPromptTx(ctx, tx, sessionID, promptID, active, prompt.promptStatus, prompt.itemStatus, now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit synonym update: %w", err)
	}
	return synonym, nil
}

func loadRejectedMeaningPromptTx(ctx context.Context, tx *sql.Tx, sessionID, promptID int64) (rejectedMeaningPrompt, error) {
	var prompt rejectedMeaningPrompt
	var srsApplied int
	err := tx.QueryRowContext(ctx, `
		SELECT rp.session_item_id, rsi.vocabulary_id, v.content_revision, rp.prompt_type, rp.status, rsi.status,
		       rp.attempt_count, rp.last_incorrect_answer, rp.last_incorrect_content_revision,
		       rsi.srs_applied
		FROM review_prompts rp
		JOIN review_session_items rsi ON rsi.id = rp.session_item_id
		JOIN review_sessions rs ON rs.id = rsi.session_id
		JOIN vocabulary v ON v.id = rsi.vocabulary_id
		WHERE rs.id = ? AND rp.id = ? AND rs.status = 'active'`,
		sessionID, promptID,
	).Scan(
		&prompt.itemID, &prompt.vocabularyID, &prompt.currentContentRevision, &prompt.promptType,
		&prompt.promptStatus, &prompt.itemStatus, &prompt.attempts, &prompt.recordedAnswer,
		&prompt.rejectedContentRevision, &srsApplied,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return rejectedMeaningPrompt{}, stateError("review prompt is no longer available")
	}
	if err != nil {
		return rejectedMeaningPrompt{}, fmt.Errorf("load review prompt for synonym: %w", err)
	}
	prompt.srsApplied = srsApplied == 1
	return prompt, nil
}

func (prompt rejectedMeaningPrompt) validate(rejectedAnswer, submittedAnswer string) (string, error) {
	if prompt.promptType != "meaning" {
		return "", stateError("only rejected meaning answers can be added as synonyms")
	}
	if prompt.attempts == 0 || prompt.recordedAnswer == "" || prompt.rejectedContentRevision <= 0 {
		return "", stateError("this prompt has no rejected answer to add")
	}
	if !((prompt.promptStatus == "current" && prompt.itemStatus == "current") ||
		(prompt.promptStatus == "failed" && prompt.itemStatus == "failed")) {
		return "", stateError("review prompt is no longer available")
	}
	if prompt.recordedAnswer != rejectedAnswer {
		return "", stateError("the rejected answer has changed; return to the review and try again")
	}
	if submittedAnswer == "" || cleanRejectedMeaning(submittedAnswer) != submittedAnswer {
		return "", stateError("the submitted rejected answer is invalid")
	}
	if prompt.currentContentRevision != prompt.rejectedContentRevision {
		return "", stateError("this word changed after the answer was rejected")
	}
	return submittedAnswer, nil
}

func addSynonymMeaningTx(ctx context.Context, tx *sql.Tx, vocabularyID, contentRevision int64, meaning string, now time.Time) error {
	var meaningCount, meaningRunes int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(length(text)), 0)
		FROM meanings
		WHERE vocabulary_id = ?`, vocabularyID).Scan(&meaningCount, &meaningRunes); err != nil {
		return fmt.Errorf("measure vocabulary meanings: %w", err)
	}
	if meaningRunes+meaningCount+utf8.RuneCountInString(meaning) > maxMeaningsRunes {
		return stateError("this word has too much meaning text to add another synonym")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO meanings (vocabulary_id, position, text, normalized_text)
		VALUES (
			?,
			(SELECT COALESCE(MAX(position), -1) + 1 FROM meanings WHERE vocabulary_id = ?),
			?,
			?
		)`, vocabularyID, vocabularyID, meaning, textnorm.Normalize(meaning)); err != nil {
		return fmt.Errorf("insert review synonym: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE vocabulary
		SET updated_at = ?, content_revision = content_revision + 1
		WHERE id = ? AND content_revision = ?`,
		now.Unix(), vocabularyID, contentRevision)
	if err != nil {
		return fmt.Errorf("update vocabulary after synonym: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check vocabulary after synonym: %w", err)
	}
	if affected != 1 {
		return stateError("this word changed after the answer was rejected")
	}
	return nil
}

func (s *Store) MarkCorrect(ctx context.Context, sessionID, promptID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin review correction: %w", err)
	}
	defer tx.Rollback()

	prompt, promptStatus, itemStatus, err := loadIncorrectPromptTx(ctx, tx, sessionID, promptID)
	if err != nil {
		return err
	}
	if err := s.acceptIncorrectPromptTx(ctx, tx, sessionID, promptID, prompt, promptStatus, itemStatus, time.Now().UTC()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit review correction: %w", err)
	}
	return nil
}

func loadIncorrectPromptTx(ctx context.Context, tx *sql.Tx, sessionID, promptID int64) (activePrompt, string, string, error) {
	var prompt activePrompt
	var promptStatus, itemStatus string
	var srsApplied int
	err := tx.QueryRowContext(ctx, `
		SELECT rp.session_item_id, rsi.vocabulary_id, v.content_revision, rp.prompt_type,
		       rp.attempt_count, rsi.srs_applied, rs.max_attempts, rs.answer_mode,
		       rp.status, rsi.status
		FROM review_prompts rp
		JOIN review_session_items rsi ON rsi.id = rp.session_item_id
		JOIN review_sessions rs ON rs.id = rsi.session_id
		JOIN vocabulary v ON v.id = rsi.vocabulary_id
		WHERE rs.id = ? AND rp.id = ? AND rs.status = 'active'
		  AND rp.attempt_count > 0`, sessionID, promptID).Scan(
		&prompt.itemID, &prompt.vocabularyID, &prompt.contentRevision, &prompt.promptType,
		&prompt.attempts, &srsApplied, &prompt.maxAttempts, &prompt.answerMode,
		&promptStatus, &itemStatus,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return activePrompt{}, "", "", stateError("this incorrect answer is no longer available")
	}
	if err != nil {
		return activePrompt{}, "", "", fmt.Errorf("load incorrect review prompt: %w", err)
	}
	prompt.srsApplied = srsApplied == 1
	if !((promptStatus == "current" && itemStatus == "current") ||
		(promptStatus == "failed" && itemStatus == "failed")) {
		return activePrompt{}, "", "", stateError("this incorrect answer is no longer available")
	}
	return prompt, promptStatus, itemStatus, nil
}

func (s *Store) acceptIncorrectPromptTx(
	ctx context.Context,
	tx *sql.Tx,
	sessionID, promptID int64,
	prompt activePrompt,
	promptStatus, itemStatus string,
	now time.Time,
) error {
	if promptStatus == "failed" && itemStatus == "failed" {
		if err := s.restoreFailedReviewTx(ctx, tx, sessionID, prompt.itemID); err != nil {
			return err
		}
	} else if promptStatus != "current" || itemStatus != "current" {
		return stateError("this incorrect answer is no longer available")
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE review_prompts
		SET status = 'passed', attempt_count = MAX(attempt_count, 2),
		    last_incorrect_answer = '', last_incorrect_content_revision = 0
		WHERE id = ?`, promptID); err != nil {
		return fmt.Errorf("mark corrected review prompt: %w", err)
	}
	var promptsRemaining int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM review_prompts
		WHERE session_item_id = ? AND status = 'pending'`, prompt.itemID).Scan(&promptsRemaining); err != nil {
		return fmt.Errorf("count prompts after correction: %w", err)
	}
	if promptsRemaining == 0 {
		return s.finishItemTx(ctx, tx, sessionID, prompt.itemID, prompt.vocabularyID, true, prompt.srsApplied, now)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE review_session_items SET status = 'pending' WHERE id = ?", prompt.itemID); err != nil {
		return fmt.Errorf("queue corrected review item: %w", err)
	}
	return s.promoteNextTx(ctx, tx, sessionID, now)
}

func (s *Store) restoreFailedReviewTx(ctx context.Context, tx *sql.Tx, sessionID, itemID int64) error {
	latest, err := loadUndoResultTx(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	if latest.itemID != itemID || latest.itemStatus != "failed" {
		return stateError("this failed answer is no longer the latest review result")
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
	if _, err := tx.ExecContext(ctx, "DELETE FROM review_results WHERE id = ?", latest.id); err != nil {
		return fmt.Errorf("remove corrected review result: %w", err)
	}
	if err := s.rebuildLeechStateTx(ctx, tx, latest.vocabularyID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE review_session_items SET status = 'pending' WHERE id = ?", itemID); err != nil {
		return fmt.Errorf("restore corrected review item: %w", err)
	}
	return nil
}

func rejectedMeaningCandidate(promptType, answer string) string {
	if promptType != "meaning" {
		return ""
	}
	return cleanRejectedMeaning(answer)
}

func cleanRejectedMeaning(value string) string {
	if !utf8.ValidString(value) {
		return ""
	}
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > maxRejectedMeaningRunes {
		return ""
	}
	hasWordCharacter := false
	for _, character := range value {
		if unicode.IsControl(character) {
			return ""
		}
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			hasWordCharacter = true
		}
	}
	if !hasWordCharacter {
		return ""
	}
	return strings.Join(strings.Fields(value), " ")
}

func (s *Store) evaluateAnswerTx(ctx context.Context, tx *sql.Tx, sessionID, promptID int64, answer string) (activePrompt, MatchResult, error) {
	prompt, err := s.loadActivePromptTx(ctx, tx, sessionID, promptID)
	if err != nil {
		return activePrompt{}, Incorrect, err
	}

	accepted, err := s.acceptedAnswers(ctx, tx, prompt.vocabularyID, prompt.promptType)
	if err != nil {
		return activePrompt{}, Incorrect, err
	}
	if prompt.promptType != "pronunciation" {
		return prompt, MatchAnswer(answer, accepted), nil
	}

	result, matchErr := MatchPronunciation(answer, accepted)
	if matchErr != nil {
		return prompt, Incorrect, nil
	}
	return prompt, result, nil
}

func (s *Store) loadActivePromptTx(ctx context.Context, tx *sql.Tx, sessionID, promptID int64) (activePrompt, error) {
	var prompt activePrompt
	var promptStatus, itemStatus string
	var srsApplied int
	err := tx.QueryRowContext(ctx, `
		SELECT rp.session_item_id, rsi.vocabulary_id, v.content_revision,
		       rp.prompt_type, rp.status, rp.attempt_count, rp.last_incorrect_answer, rsi.status,
		       rsi.srs_applied, rs.max_attempts, rs.answer_mode
		FROM review_prompts rp
		JOIN review_session_items rsi ON rsi.id = rp.session_item_id
		JOIN review_sessions rs ON rs.id = rsi.session_id
		JOIN vocabulary v ON v.id = rsi.vocabulary_id
		WHERE rs.id = ? AND rp.id = ? AND rs.status = 'active'`, sessionID, promptID).Scan(
		&prompt.itemID, &prompt.vocabularyID, &prompt.contentRevision, &prompt.promptType,
		&promptStatus, &prompt.attempts, &prompt.rejectedAnswer, &itemStatus, &srsApplied, &prompt.maxAttempts, &prompt.answerMode)
	if errors.Is(err, sql.ErrNoRows) {
		return activePrompt{}, stateError("prompt is no longer active")
	}
	if err != nil {
		return activePrompt{}, fmt.Errorf("load active prompt: %w", err)
	}
	if promptStatus != "current" || itemStatus != "current" {
		return activePrompt{}, stateError("prompt is no longer active")
	}
	prompt.srsApplied = srsApplied == 1
	return prompt, nil
}

func (s *Store) Continue(ctx context.Context, sessionID int64) error {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin review continuation: %w", err)
	}
	defer tx.Rollback()
	var failedID, vocabularyID int64
	var failedPosition int
	err = tx.QueryRowContext(ctx, `
		SELECT rsi.id, rsi.vocabulary_id, rsi.position
		FROM review_session_items rsi
		JOIN review_sessions rs ON rs.id = rsi.session_id
		WHERE rsi.session_id = ? AND rsi.status = 'failed' AND rs.status = 'active'
		ORDER BY rsi.position DESC
		LIMIT 1`, sessionID).Scan(&failedID, &vocabularyID, &failedPosition)
	if errors.Is(err, sql.ErrNoRows) {
		return stateError("review session has no failed item ready to continue")
	}
	if err != nil {
		return fmt.Errorf("load failed review item: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE review_session_items SET status = 'completed' WHERE id = ?", failedID); err != nil {
		return fmt.Errorf("complete failed review item: %w", err)
	}
	var maximumPosition int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), -1) FROM review_session_items WHERE session_id = ?`, sessionID).Scan(&maximumPosition); err != nil {
		return fmt.Errorf("find review queue end: %w", err)
	}
	target := failedPosition + 5
	if target > maximumPosition+1 {
		target = maximumPosition + 1
	}
	if target <= failedPosition {
		target = failedPosition + 1
	}
	if err := makeQueueRoomTx(ctx, tx, sessionID, target); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO review_session_items (session_id, vocabulary_id, position, srs_applied, status)
		VALUES (?, ?, ?, 0, 'pending')`, sessionID, vocabularyID, target)
	if err != nil {
		return fmt.Errorf("insert review reinforcement: %w", err)
	}
	reinforcementID, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get reinforcement ID: %w", err)
	}
	if err := cloneReviewPromptsTx(ctx, tx, failedID, reinforcementID, false); err != nil {
		return err
	}
	if err := s.promoteNextTx(ctx, tx, sessionID, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit review continuation: %w", err)
	}
	return nil
}

func cloneReviewPromptsTx(ctx context.Context, tx *sql.Tx, sourceItemID, targetItemID int64, firstCurrent bool) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT prompt_type, position
		FROM review_prompts
		WHERE session_item_id = ?
		ORDER BY position`, sourceItemID)
	if err != nil {
		return fmt.Errorf("load review prompts to clone: %w", err)
	}
	defer rows.Close()
	type clonedPrompt struct {
		promptType string
		position   int
	}
	prompts := make([]clonedPrompt, 0, 2)
	for rows.Next() {
		var prompt clonedPrompt
		if err := rows.Scan(&prompt.promptType, &prompt.position); err != nil {
			return fmt.Errorf("scan review prompt to clone: %w", err)
		}
		prompts = append(prompts, prompt)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate review prompts to clone: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close review prompts to clone: %w", err)
	}
	if len(prompts) == 0 {
		return errors.New("review item has no prompts to clone")
	}

	sessionID, err := sessionIDForItemTx(ctx, tx, targetItemID)
	if err != nil {
		return err
	}
	target, err := promptQueueInsertionPointTx(ctx, tx, sessionID, firstCurrent)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE review_prompts
		SET queue_position = queue_position + ?
		WHERE session_item_id IN (SELECT id FROM review_session_items WHERE session_id = ?)
		  AND queue_position >= ?`, len(prompts), sessionID, target); err != nil {
		return fmt.Errorf("make room in review card queue: %w", err)
	}
	for index, prompt := range prompts {
		status := "pending"
		if firstCurrent && index == 0 {
			status = "current"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO review_prompts (session_item_id, prompt_type, position, status, queue_position)
			VALUES (?, ?, ?, ?, ?)`, targetItemID, prompt.promptType, prompt.position, status, target+index); err != nil {
			return fmt.Errorf("clone review prompt: %w", err)
		}
	}
	return nil
}

func sessionIDForItemTx(ctx context.Context, tx *sql.Tx, itemID int64) (int64, error) {
	var sessionID int64
	if err := tx.QueryRowContext(ctx, "SELECT session_id FROM review_session_items WHERE id = ?", itemID).Scan(&sessionID); err != nil {
		return 0, fmt.Errorf("load review session for item: %w", err)
	}
	return sessionID, nil
}

func promptQueueInsertionPointTx(ctx context.Context, tx *sql.Tx, sessionID int64, first bool) (int, error) {
	offset := 5
	if first {
		offset = 0
	}
	var target int
	err := tx.QueryRowContext(ctx, `
		SELECT rp.queue_position
		FROM review_prompts rp
		JOIN review_session_items rsi ON rsi.id = rp.session_item_id
		WHERE rsi.session_id = ? AND rsi.status IN ('pending', 'current') AND rp.status IN ('pending', 'current')
		ORDER BY rp.queue_position, rp.id
		LIMIT 1 OFFSET ?`, sessionID, offset).Scan(&target)
	if err == nil {
		return target, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("find review card queue position: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(rp.queue_position) + 1, 0)
		FROM review_prompts rp
		JOIN review_session_items rsi ON rsi.id = rp.session_item_id
		WHERE rsi.session_id = ?`, sessionID).Scan(&target); err != nil {
		return 0, fmt.Errorf("find review card queue end: %w", err)
	}
	return target, nil
}

func (s *Store) finishItemTx(ctx context.Context, tx *sql.Tx, sessionID, itemID, vocabularyID int64, success, applySRS bool, now time.Time) error {
	var stageBefore, stageAfter, dueBefore, dueAfter, lastReviewedBefore any
	if applySRS {
		var stage, sixMonthReviewEnabled int
		var due, lastReviewed sql.NullInt64
		if err := tx.QueryRowContext(ctx, `
			SELECT ss.stage, ss.due_at, ss.last_reviewed_at,
			       COALESCE(us.six_month_review_enabled, 0)
			FROM srs_states ss
			LEFT JOIN user_settings us ON us.id = 1
			WHERE ss.vocabulary_id = ?`, vocabularyID).Scan(&stage, &due, &lastReviewed, &sixMonthReviewEnabled); err != nil {
			return fmt.Errorf("load SRS state: %w", err)
		}
		stageBefore = stage
		dueBefore = nullableInt64(due)
		lastReviewedBefore = nullableInt64(lastReviewed)
		nextStage, nextDue := srs.NextReview(srs.Stage(stage), success, sixMonthReviewEnabled == 1, now)
		stageAfter = nextStage
		if !nextDue.IsZero() {
			dueAfter = nextDue.Unix()
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE srs_states SET stage = ?, due_at = ?, last_reviewed_at = ? WHERE vocabulary_id = ?`, nextStage, dueAfter, now.Unix(), vocabularyID); err != nil {
			return fmt.Errorf("update SRS state: %w", err)
		}
	}
	status := "failure"
	if success {
		status = "success"
		if _, err := tx.ExecContext(ctx, "UPDATE review_session_items SET status = 'completed' WHERE id = ?", itemID); err != nil {
			return fmt.Errorf("complete successful review item: %w", err)
		}
	}
	var promptCount, firstAttemptCorrect int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'passed' AND attempt_count = 1 THEN 1 ELSE 0 END), 0)
		FROM review_prompts
		WHERE session_item_id = ? AND status IN ('passed', 'failed')`, itemID).Scan(&promptCount, &firstAttemptCorrect); err != nil {
		return fmt.Errorf("calculate first-attempt result: %w", err)
	}
	var mistakeVisibilityExistedBefore, mistakeHiddenBefore, mistakeLeechHiddenBefore any
	if applySRS && !success {
		visibility, err := loadMistakeVisibilityTx(ctx, tx, vocabularyID)
		if err != nil {
			return err
		}
		mistakeVisibilityExistedBefore = boolInt(visibility.exists)
		mistakeHiddenBefore = nullableInt64(visibility.hiddenAt)
		mistakeLeechHiddenBefore = nullableInt64(visibility.leechHiddenAt)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO review_results (
			session_item_id, outcome,
			stage_before, stage_after, due_before, due_after, last_reviewed_before,
			created_at, srs_applied, first_attempt_correct_count, prompt_count,
			mistake_visibility_existed_before, mistake_hidden_before,
			mistake_leech_hidden_before
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		itemID, status,
		stageBefore, stageAfter, dueBefore, dueAfter, lastReviewedBefore,
		now.Unix(), boolInt(applySRS), firstAttemptCorrect, promptCount,
		mistakeVisibilityExistedBefore, mistakeHiddenBefore, mistakeLeechHiddenBefore,
	); err != nil {
		return fmt.Errorf("insert review result: %w", err)
	}
	if err := updateLeechAfterResultTx(ctx, tx, vocabularyID, success, applySRS, now); err != nil {
		return err
	}
	if applySRS && !success {
		if _, err := tx.ExecContext(ctx, `
			UPDATE mistake_visibility
			SET hidden_at = NULL, leech_hidden_at = NULL
			WHERE vocabulary_id = ?`, vocabularyID); err != nil {
			return fmt.Errorf("restore mistake visibility: %w", err)
		}
	}
	if success {
		return s.promoteNextTx(ctx, tx, sessionID, now)
	}
	return nil
}

type mistakeVisibilityState struct {
	exists        bool
	hiddenAt      sql.NullInt64
	leechHiddenAt sql.NullInt64
}

func loadMistakeVisibilityTx(ctx context.Context, tx *sql.Tx, vocabularyID int64) (mistakeVisibilityState, error) {
	var visibility mistakeVisibilityState
	err := tx.QueryRowContext(ctx, `
		SELECT hidden_at, leech_hidden_at
		FROM mistake_visibility
		WHERE vocabulary_id = ?`, vocabularyID).Scan(
		&visibility.hiddenAt, &visibility.leechHiddenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return visibility, nil
	}
	if err != nil {
		return mistakeVisibilityState{}, fmt.Errorf("load mistake visibility: %w", err)
	}
	visibility.exists = true
	return visibility, nil
}

func (s *Store) promoteNextTx(ctx context.Context, tx *sql.Tx, sessionID int64, now time.Time) error {
	var itemID, promptID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT rsi.id, rp.id
		FROM review_prompts rp
		JOIN review_session_items rsi ON rsi.id = rp.session_item_id
		WHERE rsi.session_id = ? AND rsi.status = 'pending' AND rp.status = 'pending'
		ORDER BY rp.queue_position, rp.id
		LIMIT 1`, sessionID).Scan(&itemID, &promptID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if _, updateErr := tx.ExecContext(ctx, `UPDATE review_sessions SET status = 'completed', completed_at = ? WHERE id = ?`, now.Unix(), sessionID); updateErr != nil {
				return fmt.Errorf("complete review session: %w", updateErr)
			}
			return nil
		}
		return fmt.Errorf("find next review item: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE review_session_items SET status = 'current' WHERE id = ?", itemID); err != nil {
		return fmt.Errorf("promote review item: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE review_prompts SET status = 'current' WHERE id = ?", promptID); err != nil {
		return fmt.Errorf("promote first prompt: %w", err)
	}
	return nil
}

func (s *Store) acceptedAnswers(ctx context.Context, tx *sql.Tx, vocabularyID int64, promptType string) ([]string, error) {
	switch promptType {
	case "meaning":
		rows, err := tx.QueryContext(ctx, "SELECT normalized_text FROM meanings WHERE vocabulary_id = ?", vocabularyID)
		if err != nil {
			return nil, fmt.Errorf("load accepted answers: %w", err)
		}
		defer rows.Close()
		answers := make([]string, 0)
		for rows.Next() {
			var answer string
			if err := rows.Scan(&answer); err != nil {
				return nil, fmt.Errorf("scan accepted answer: %w", err)
			}
			answers = append(answers, answer)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate accepted answers: %w", err)
		}
		return answers, nil
	case "pronunciation":
		var answer string
		if err := tx.QueryRowContext(ctx, "SELECT normalized_pronunciation FROM vocabulary WHERE id = ?", vocabularyID).Scan(&answer); err != nil {
			return nil, fmt.Errorf("load accepted answer: %w", err)
		}
		return []string{answer}, nil
	default:
		return nil, fmt.Errorf("unsupported prompt type %q", promptType)
	}
}

func nullableInt64(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
