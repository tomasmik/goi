package reviews

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"
	"unicode"
)

const normalSessionLimit = 20

const (
	reviewModeTyped     = "typed"
	reviewModeSelfGrade = "self_grade"
	cardOrderTogether   = "together"
	cardOrderSpaced     = "spaced"
)

type reviewPreferences struct {
	maxAttempts int
	answerMode  string
	reviewOrder string
	cardOrder   string
}

type promptCard struct {
	itemID     int64
	promptType string
	position   int
}

func (s *Store) StartNormal(ctx context.Context, limit int) (int64, error) {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if sessionID, found, err := s.activeStandaloneSession(ctx, "normal"); err != nil {
		return 0, fmt.Errorf("check active review session: %w", err)
	} else if found {
		return sessionID, nil
	}
	if limit <= 0 || limit > normalSessionLimit {
		limit = normalSessionLimit
	}
	preferences, err := s.reviewPreferences(ctx)
	if err != nil {
		return 0, err
	}
	orderBy := "ss.stage ASC, ss.due_at, ss.vocabulary_id"
	switch preferences.reviewOrder {
	case "stage_descending":
		orderBy = "ss.stage DESC, ss.due_at, ss.vocabulary_id"
	case "random":
		orderBy = "RANDOM()"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT ss.vocabulary_id
		FROM srs_states ss INDEXED BY srs_states_due
		JOIN vocabulary v ON v.id = ss.vocabulary_id
		WHERE v.status = 'active' AND ss.suspended_at IS NULL AND ss.due_at IS NOT NULL AND ss.due_at <= ?
		ORDER BY `+orderBy+`
		LIMIT ?`, time.Now().UTC().Unix(), limit)
	if err != nil {
		return 0, fmt.Errorf("find due reviews: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, fmt.Errorf("scan due review: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate due reviews: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close due reviews: %w", err)
	}
	if len(ids) == 0 {
		return 0, stateError("no reviews are due")
	}

	return s.startSessionWithPreferences(ctx, "normal", ids, preferences, 0)
}

func (s *Store) StartExtraSource(ctx context.Context, source string, selected []int64) (int64, error) {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	extraStudyLimit := 10
	if err := s.db.QueryRowContext(ctx, "SELECT extra_study_limit FROM user_settings WHERE id = 1").Scan(&extraStudyLimit); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("load extra-study list size: %w", err)
	}
	ids, err := s.studyIDs(ctx, source, selected, extraStudyLimit)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, stateError("no words are available for this study list")
	}
	preferences, err := s.reviewPreferences(ctx)
	if err != nil {
		return 0, err
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE review_sessions
		SET status = 'abandoned'
		WHERE kind = 'extra' AND lesson_session_id IS NULL AND status IN ('active', 'paused')`); err != nil {
		return 0, fmt.Errorf("replace active practice session: %w", err)
	}
	return s.startSessionWithPreferences(ctx, "extra", ids, preferences, 0)
}

func (s *Store) activeStandaloneSession(ctx context.Context, kind string) (int64, bool, error) {
	var sessionID int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id
		FROM review_sessions
		WHERE kind = ? AND lesson_session_id IS NULL AND status IN ('active', 'paused')
		ORDER BY id DESC
		LIMIT 1`, kind).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return sessionID, true, nil
}

func (s *Store) reviewPreferences(ctx context.Context) (reviewPreferences, error) {
	preferences := reviewPreferences{
		maxAttempts: 3,
		answerMode:  reviewModeTyped,
		reviewOrder: "stage_ascending",
		cardOrder:   cardOrderTogether,
	}
	err := s.db.QueryRowContext(ctx, `
		SELECT retry_count, review_mode, review_order, review_card_order
		FROM user_settings WHERE id = 1`).Scan(
		&preferences.maxAttempts,
		&preferences.answerMode,
		&preferences.reviewOrder,
		&preferences.cardOrder,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return preferences, nil
	}
	if err != nil {
		return reviewPreferences{}, fmt.Errorf("load review settings: %w", err)
	}
	return preferences, nil
}

func (s *Store) DueCount(ctx context.Context) (int, error) {
	return s.DueCountAt(ctx, time.Now().UTC())
}

func (s *Store) DueCountAt(ctx context.Context, now time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM srs_states ss INDEXED BY srs_states_due
		JOIN vocabulary v ON v.id = ss.vocabulary_id
		WHERE v.status = 'active' AND ss.suspended_at IS NULL AND ss.due_at IS NOT NULL AND ss.due_at <= ?`, now.UTC().Unix()).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count due reviews: %w", err)
	}
	return count, nil
}

func (s *Store) StartLesson(ctx context.Context, lessonSessionID int64, ids []int64) (int64, error) {
	if lessonSessionID <= 0 || len(ids) == 0 {
		return 0, stateError("lesson review requires a session and at least one word")
	}
	s.startMu.Lock()
	defer s.startMu.Unlock()
	preferences, err := s.reviewPreferences(ctx)
	if err != nil {
		return 0, err
	}
	return s.startSessionWithPreferences(ctx, "extra", ids, preferences, lessonSessionID)
}

func (s *Store) startSession(ctx context.Context, kind string, ids []int64, maxAttempts int, lessonSessionID int64) (int64, error) {
	preferences, err := s.reviewPreferences(ctx)
	if err != nil {
		return 0, err
	}
	preferences.maxAttempts = maxAttempts
	return s.startSessionWithPreferences(ctx, kind, ids, preferences, lessonSessionID)
}

func (s *Store) startSessionWithPreferences(ctx context.Context, kind string, ids []int64, preferences reviewPreferences, lessonSessionID int64) (int64, error) {
	if kind != "normal" && kind != "extra" {
		return 0, fmt.Errorf("unsupported review session kind %q", kind)
	}
	if len(ids) == 0 {
		return 0, errors.New("a review session requires at least one word")
	}
	if preferences.maxAttempts < 1 || preferences.maxAttempts > 5 {
		return 0, errors.New("review attempts must be between 1 and 5")
	}
	if preferences.answerMode != reviewModeTyped && preferences.answerMode != reviewModeSelfGrade {
		return 0, errors.New("review mode is invalid")
	}
	if preferences.cardOrder != cardOrderTogether && preferences.cardOrder != cardOrderSpaced {
		return 0, errors.New("review card order is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin %s study session: %w", kind, err)
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	if kind == "normal" {
		if lessonSessionID > 0 {
			return 0, errors.New("normal reviews cannot belong to a lesson")
		}
		if err := validateNormalReviewTx(ctx, tx, ids, now.Unix()); err != nil {
			return 0, err
		}
	} else if lessonSessionID > 0 {
		if err := validateLessonBatchTx(ctx, tx, lessonSessionID, ids); err != nil {
			return 0, err
		}
	} else if err := validateExtraStudyTx(ctx, tx, ids); err != nil {
		return 0, err
	}

	seed := now.UnixNano()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO review_sessions (kind, status, max_attempts, lesson_session_id, answer_mode, card_order)
		VALUES (?, 'active', ?, NULLIF(?, 0), ?, ?)`,
		kind, preferences.maxAttempts, lessonSessionID, preferences.answerMode, preferences.cardOrder)
	if err != nil {
		return 0, fmt.Errorf("insert %s study session: %w", kind, err)
	}
	sessionID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get study session ID: %w", err)
	}
	itemIDs := make([]int64, 0, len(ids))
	meaningOnlyItems := make(map[int64]bool)
	for position, vocabularyID := range ids {
		var expression string
		if err := tx.QueryRowContext(ctx, "SELECT expression FROM vocabulary WHERE id = ?", vocabularyID).Scan(&expression); err != nil {
			return 0, fmt.Errorf("load study word: %w", err)
		}
		itemResult, err := tx.ExecContext(ctx, `
			INSERT INTO review_session_items (session_id, vocabulary_id, position, srs_applied, status)
			VALUES (?, ?, ?, ?, 'pending')`, sessionID, vocabularyID, position, boolInt(kind == "normal"))
		if err != nil {
			return 0, fmt.Errorf("insert study item: %w", err)
		}
		itemID, err := itemResult.LastInsertId()
		if err != nil {
			return 0, fmt.Errorf("get study item ID: %w", err)
		}
		itemIDs = append(itemIDs, itemID)
		meaningOnlyItems[itemID] = isKanaOnly(expression)
	}
	random := rand.New(rand.NewSource(seed))
	cards := buildPromptCards(itemIDs, meaningOnlyItems, preferences.cardOrder, random)
	for queuePosition, card := range cards {
		status := "pending"
		if queuePosition == 0 {
			status = "current"
			if _, err := tx.ExecContext(ctx, "UPDATE review_session_items SET status = 'current' WHERE id = ?", card.itemID); err != nil {
				return 0, fmt.Errorf("activate first study item: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO review_prompts (session_item_id, prompt_type, position, status, queue_position)
			VALUES (?, ?, ?, ?, ?)`, card.itemID, card.promptType, card.position, status, queuePosition); err != nil {
			return 0, fmt.Errorf("insert study prompt: %w", err)
		}
	}
	if lessonSessionID > 0 {
		result, err := tx.ExecContext(ctx, `
			UPDATE lesson_sessions SET phase = 'review'
			WHERE id = ? AND status = 'active' AND phase = 'study'`, lessonSessionID)
		if err != nil {
			return 0, fmt.Errorf("link lesson review: %w", err)
		}
		if affected, err := result.RowsAffected(); err != nil {
			return 0, fmt.Errorf("check linked lesson review: %w", err)
		} else if affected == 0 {
			return 0, stateError("lesson batch is no longer in study phase")
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit study session: %w", err)
	}
	return sessionID, nil
}

func validateNormalReviewTx(ctx context.Context, tx *sql.Tx, ids []int64, now int64) error {
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("normal review contains duplicate vocabulary %d", id)
		}
		seen[id] = struct{}{}

		var status string
		var stage, dueAt, suspendedAt sql.NullInt64
		err := tx.QueryRowContext(ctx, `
			SELECT v.status, ss.stage, ss.due_at, ss.suspended_at
			FROM vocabulary v
			LEFT JOIN srs_states ss ON ss.vocabulary_id = v.id
			WHERE v.id = ?`, id).Scan(&status, &stage, &dueAt, &suspendedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return stateErrorf("vocabulary %d no longer exists", id)
		}
		if err != nil {
			return fmt.Errorf("validate vocabulary %d for review: %w", id, err)
		}
		if status != "active" {
			return stateErrorf("vocabulary %d is no longer active for review", id)
		}
		if !stage.Valid {
			return stateErrorf("vocabulary %d has no SRS state", id)
		}
		if suspendedAt.Valid {
			return stateErrorf("vocabulary %d is suspended from review", id)
		}
		if !dueAt.Valid || dueAt.Int64 > now {
			return stateErrorf("vocabulary %d is no longer due for review", id)
		}
	}
	return nil
}

func validateExtraStudyTx(ctx context.Context, tx *sql.Tx, ids []int64) error {
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("extra study contains duplicate vocabulary %d", id)
		}
		seen[id] = struct{}{}

		var status string
		var activeLeech bool
		err := tx.QueryRowContext(ctx, `
			SELECT v.status, COALESCE(ls.active, 0)
			FROM vocabulary v
			LEFT JOIN leech_states ls ON ls.vocabulary_id = v.id
			WHERE v.id = ?`, id).Scan(&status, &activeLeech)
		if errors.Is(err, sql.ErrNoRows) {
			return stateErrorf("vocabulary %d no longer exists", id)
		}
		if err != nil {
			return fmt.Errorf("validate vocabulary %d for extra study: %w", id, err)
		}
		if status != "active" && !(status == "suspended" && activeLeech) {
			return stateErrorf("vocabulary %d is no longer active for extra study", id)
		}
	}
	return nil
}

func validateLessonBatchTx(ctx context.Context, tx *sql.Tx, lessonSessionID int64, ids []int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT lsi.vocabulary_id, lsi.study_viewed_at
		FROM lesson_sessions ls
		JOIN lesson_session_items lsi
		  ON lsi.session_id = ls.id AND lsi.batch_number = ls.current_batch
		WHERE ls.id = ? AND ls.status = 'active' AND ls.phase = 'study'
		ORDER BY lsi.position`, lessonSessionID)
	if err != nil {
		return fmt.Errorf("load current lesson batch: %w", err)
	}
	defer rows.Close()

	expected := make(map[int64]struct{}, len(ids))
	allViewed := true
	for rows.Next() {
		var id int64
		var viewedAt sql.NullInt64
		if err := rows.Scan(&id, &viewedAt); err != nil {
			return fmt.Errorf("scan current lesson batch: %w", err)
		}
		expected[id] = struct{}{}
		allViewed = allViewed && viewedAt.Valid
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate current lesson batch: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close current lesson batch: %w", err)
	}
	if len(expected) == 0 || len(expected) != len(ids) {
		return stateError("lesson review words do not match the current batch")
	}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := expected[id]; !ok {
			return stateError("lesson review words do not match the current batch")
		}
		if _, duplicate := seen[id]; duplicate {
			return stateError("lesson review contains a duplicate word")
		}
		seen[id] = struct{}{}
	}
	if !allViewed {
		return stateError("view every word in the lesson batch before starting its review")
	}
	return nil
}

func buildPromptCards(itemIDs []int64, meaningOnlyItems map[int64]bool, cardOrder string, random *rand.Rand) []promptCard {
	cards := make([]promptCard, 0, len(itemIDs)*2)
	if cardOrder != cardOrderSpaced || len(itemIDs) == 1 {
		for _, itemID := range itemIDs {
			meaningPosition := 0
			if !meaningOnlyItems[itemID] {
				cards = append(cards, promptCard{itemID: itemID, promptType: "pronunciation", position: 0})
				meaningPosition = 1
			}
			cards = append(cards, promptCard{itemID: itemID, promptType: "meaning", position: meaningPosition})
		}
		return cards
	}

	for start := 0; start < len(itemIDs); {
		groupSize := randomGroupSize(len(itemIDs)-start, random)
		group := itemIDs[start : start+groupSize]
		for _, itemID := range group {
			if meaningOnlyItems[itemID] {
				cards = append(cards, promptCard{itemID: itemID, promptType: "meaning", position: 0})
			} else {
				cards = append(cards, promptCard{itemID: itemID, promptType: "pronunciation", position: 0})
			}
		}
		for _, itemID := range group {
			if !meaningOnlyItems[itemID] {
				cards = append(cards, promptCard{itemID: itemID, promptType: "meaning", position: 1})
			}
		}
		start += groupSize
	}
	return cards
}

func isKanaOnly(expression string) bool {
	hasKana := false
	for _, character := range strings.TrimSpace(expression) {
		switch {
		case unicode.In(character, unicode.Hiragana, unicode.Katakana):
			hasKana = true
		case unicode.IsSpace(character), strings.ContainsRune("ーｰ・･゠\u3099\u309a", character):
		default:
			return false
		}
	}
	return hasKana
}

func randomGroupSize(remaining int, random *rand.Rand) int {
	if remaining <= 1 {
		return remaining
	}
	maximum := min(5, remaining)
	choices := make([]int, 0, maximum-1)
	for size := 2; size <= maximum; size++ {
		if remaining-size != 1 {
			choices = append(choices, size)
		}
	}
	return choices[random.Intn(len(choices))]
}
