package lessons

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tomasmik/goi/internal/examples"
	"github.com/tomasmik/goi/internal/srs"
)

type Store struct {
	db           *sql.DB
	exampleStore *examples.Store
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

const (
	BatchSize                = 5
	maximumAvailablePageSize = 100
)

type AvailableItem struct {
	ID            int64
	Expression    string
	Pronunciation string
	Meaning       string
	Selected      bool
}

type Session struct {
	ID              int64
	Status          string
	Phase           string
	CurrentBatch    int
	StudyPosition   int
	ViewedCount     int
	CanStudyBack    bool
	CanStudyNext    bool
	BatchCount      int
	ReviewSessionID int64
	Total           int
	Completed       int
	AudioEnabled    bool
	Items           []StudyItem
	StudyItem       StudyItem
	StudyReady      bool
}

type StudyItem struct {
	ID            int64
	Position      int
	Viewed        bool
	Expression    string
	Pronunciation string
	AudioID       int64
	PictureID     int64
	Meanings      []string
	Notes         string
	Example       examples.Example
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, exampleStore: examples.NewStore(db)}
}

func (s *Store) ActiveSession(ctx context.Context) (int64, bool, error) {
	var sessionID int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id
		FROM lesson_sessions
		WHERE status = 'active'
		ORDER BY id DESC
		LIMIT 1`).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("find active lesson: %w", err)
	}
	return sessionID, true, nil
}

func (s *Store) AvailableCount(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM vocabulary v"+availableFilter).Scan(&count); err != nil {
		return 0, fmt.Errorf("count lesson vocabulary: %w", err)
	}
	return count, nil
}

func (s *Store) AvailablePage(ctx context.Context, limit, offset int) ([]AvailableItem, error) {
	if limit <= 0 || limit > maximumAvailablePageSize {
		return nil, fmt.Errorf("lesson vocabulary page size must be between 1 and %d", maximumAvailablePageSize)
	}
	if offset < 0 {
		return nil, errors.New("lesson vocabulary page offset must not be negative")
	}
	items, err := s.available(ctx, limit, offset)
	if err != nil {
		return nil, err
	}
	return items, nil
}

const availableFilter = `
		WHERE v.status = 'unlearned'
		  AND v.known_elsewhere_at IS NULL
		  AND v.pronunciation <> ''
		  AND EXISTS (SELECT 1 FROM meanings m WHERE m.vocabulary_id = v.id)
		  AND NOT EXISTS (
			SELECT 1
			FROM lesson_session_items lsi
			JOIN lesson_sessions ls ON ls.id = lsi.session_id
			WHERE lsi.vocabulary_id = v.id AND ls.status = 'active'
		  )`

func (s *Store) available(ctx context.Context, limit, offset int) ([]AvailableItem, error) {
	query := `
		SELECT v.id, v.expression, v.pronunciation,
		       COALESCE((SELECT m.text FROM meanings m WHERE m.vocabulary_id = v.id ORDER BY m.position LIMIT 1), '')
		FROM vocabulary v` + availableFilter + `
		ORDER BY v.created_at, v.id
		LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list lesson vocabulary: %w", err)
	}
	defer rows.Close()
	items := make([]AvailableItem, 0)
	for rows.Next() {
		var item AvailableItem
		if err := rows.Scan(&item.ID, &item.Expression, &item.Pronunciation, &item.Meaning); err != nil {
			return nil, fmt.Errorf("scan lesson vocabulary: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate lesson vocabulary: %w", err)
	}
	return items, nil
}

func (s *Store) NextBatch(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.id
		FROM vocabulary v`+availableFilter+`
		ORDER BY v.created_at, v.id
		LIMIT ?`, BatchSize)
	if err != nil {
		return nil, fmt.Errorf("find next lesson words: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0, BatchSize)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan next lesson word: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate next lesson words: %w", err)
	}
	if len(ids) == 0 {
		return nil, stateError("no new words are available")
	}
	return ids, nil
}

func (s *Store) Start(ctx context.Context, vocabularyIDs []int64) (int64, error) {
	if len(vocabularyIDs) == 0 {
		return 0, stateError("select at least one word")
	}
	if len(vocabularyIDs) > maximumAvailablePageSize {
		return 0, stateErrorf("select at most %d words", maximumAvailablePageSize)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin lesson: %w", err)
	}
	defer tx.Rollback()

	var activeSessionID int64
	err = tx.QueryRowContext(ctx, `
		SELECT id
		FROM lesson_sessions
		WHERE status = 'active'
		ORDER BY id DESC
		LIMIT 1`).Scan(&activeSessionID)
	if err == nil {
		return 0, stateError("finish or return the current lesson before starting another")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("check active lesson: %w", err)
	}

	result, err := tx.ExecContext(ctx, "INSERT INTO lesson_sessions (status) VALUES ('active')")
	if err != nil {
		return 0, fmt.Errorf("insert lesson session: %w", err)
	}
	sessionID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get lesson session ID: %w", err)
	}
	seen := make(map[int64]struct{}, len(vocabularyIDs))
	position := 0
	for _, vocabularyID := range vocabularyIDs {
		if _, exists := seen[vocabularyID]; exists {
			continue
		}
		seen[vocabularyID] = struct{}{}
		var status string
		var knownElsewhere, complete bool
		var activeSessionID sql.NullInt64
		err := tx.QueryRowContext(ctx, `
			SELECT v.status, v.known_elsewhere_at IS NOT NULL,
			       v.pronunciation <> '' AND EXISTS (
			           SELECT 1 FROM meanings m WHERE m.vocabulary_id = v.id
			       ), (
				SELECT lsi.session_id
				FROM lesson_session_items lsi
				JOIN lesson_sessions ls ON ls.id = lsi.session_id
				WHERE lsi.vocabulary_id = v.id AND ls.status = 'active'
				ORDER BY lsi.session_id
				LIMIT 1
			)
			FROM vocabulary v
			WHERE v.id = ?`, vocabularyID).Scan(&status, &knownElsewhere, &complete, &activeSessionID)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, stateErrorf("vocabulary %d is not available for a lesson", vocabularyID)
		}
		if err != nil {
			return 0, fmt.Errorf("check lesson vocabulary %d: %w", vocabularyID, err)
		}
		if status != "unlearned" || knownElsewhere || !complete {
			return 0, stateErrorf("vocabulary %d is not available for a lesson", vocabularyID)
		}
		if activeSessionID.Valid {
			return 0, stateErrorf("vocabulary %d is already in lesson session %d", vocabularyID, activeSessionID.Int64)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO lesson_session_items (
				session_id, vocabulary_id, position, batch_number, study_viewed_at
			)
			VALUES (?, ?, ?, ?, NULL)`, sessionID, vocabularyID, position, position/BatchSize); err != nil {
			return 0, fmt.Errorf("insert lesson item: %w", err)
		}
		position++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit lesson start: %w", err)
	}
	return sessionID, nil
}

func (s *Store) ReturnToQueue(ctx context.Context, sessionID int64) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE lesson_sessions
		SET status = 'abandoned'
		WHERE id = ? AND status = 'active' AND phase = 'study'`, sessionID)
	if err != nil {
		return fmt.Errorf("return lesson to queue: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check returned lesson: %w", err)
	}
	if affected == 0 {
		return stateError("this lesson can no longer be returned to the queue")
	}
	return nil
}

func (s *Store) Current(ctx context.Context, sessionID int64) (Session, error) {
	var session Session
	if err := s.db.QueryRowContext(ctx, `
		SELECT ls.id, ls.status, ls.phase, ls.current_batch, ls.study_position,
		       COALESCE((
		           SELECT rs.id
		           FROM review_sessions rs
		           WHERE rs.lesson_session_id = ls.id
		           ORDER BY rs.id DESC
		           LIMIT 1
		       ), 0)
		FROM lesson_sessions ls WHERE ls.id = ?`, sessionID).Scan(
		&session.ID, &session.Status, &session.Phase, &session.CurrentBatch,
		&session.StudyPosition, &session.ReviewSessionID,
	); err != nil {
		return Session{}, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(review_completed_at), COALESCE(MAX(batch_number) + 1, 0)
		FROM lesson_session_items
		WHERE session_id = ?`, sessionID).Scan(&session.Total, &session.Completed, &session.BatchCount); err != nil {
		return Session{}, fmt.Errorf("load lesson progress: %w", err)
	}
	var audioEnabled int
	if err := s.db.QueryRowContext(ctx, `SELECT audio_enabled FROM user_settings WHERE id = 1`).Scan(&audioEnabled); err == nil {
		session.AudioEnabled = audioEnabled == 1
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Session{}, fmt.Errorf("load lesson audio setting: %w", err)
	}
	if session.Status == "completed" {
		return session, nil
	}
	items, err := s.batchItems(ctx, sessionID, session.CurrentBatch)
	if err != nil {
		return Session{}, err
	}
	session.Items = items
	if session.StudyPosition >= 0 && session.StudyPosition < len(items) {
		session.StudyItem = items[session.StudyPosition]
	}
	for _, item := range items {
		if item.Viewed {
			session.ViewedCount++
		}
	}
	session.StudyReady = len(items) > 0 && session.ViewedCount == len(items)
	session.CanStudyBack = session.StudyPosition > 0
	session.CanStudyNext = session.StudyPosition+1 < len(items)
	return session, nil
}

func (s *Store) SelectStudyItem(ctx context.Context, sessionID int64, position int) error {
	if position < 0 {
		return stateError("invalid study position")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin study navigation: %w", err)
	}
	defer tx.Rollback()
	var batch, currentPosition int
	err = tx.QueryRowContext(ctx, `
		SELECT current_batch, study_position FROM lesson_sessions
		WHERE id = ? AND status = 'active' AND phase = 'study'`, sessionID).Scan(&batch, &currentPosition)
	if errors.Is(err, sql.ErrNoRows) {
		return stateError("lesson is not available for study")
	}
	if err != nil {
		return fmt.Errorf("load study session: %w", err)
	}
	now := time.Now().UTC().Unix()
	if _, err := tx.ExecContext(ctx, `
		UPDATE lesson_session_items
		SET study_viewed_at = COALESCE(study_viewed_at, ?)
		WHERE session_id = ? AND batch_number = ? AND position - (? * ?) = ?`,
		now, sessionID, batch, batch, BatchSize, currentPosition); err != nil {
		return fmt.Errorf("mark current study word: %w", err)
	}
	var selected int
	err = tx.QueryRowContext(ctx, `
		SELECT 1
		FROM lesson_session_items
		WHERE session_id = ? AND batch_number = ? AND position - (? * ?) = ?`,
		sessionID, batch, batch, BatchSize, position).Scan(&selected)
	if errors.Is(err, sql.ErrNoRows) {
		return stateError("study word not found")
	}
	if err != nil {
		return fmt.Errorf("find study word: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE lesson_sessions SET study_position = ? WHERE id = ?", position, sessionID); err != nil {
		return fmt.Errorf("select study word: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit study navigation: %w", err)
	}
	return nil
}

func (s *Store) MarkCurrentViewed(ctx context.Context, sessionID int64) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE lesson_session_items
		SET study_viewed_at = COALESCE(study_viewed_at, ?)
		WHERE session_id = ?
		  AND batch_number = (SELECT current_batch FROM lesson_sessions WHERE id = ?)
		  AND position = (
		      SELECT current_batch * ? + study_position FROM lesson_sessions WHERE id = ?
		  )`, time.Now().UTC().Unix(), sessionID, sessionID, BatchSize, sessionID)
	if err != nil {
		return fmt.Errorf("mark current lesson word: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check current lesson word: %w", err)
	} else if affected == 0 {
		return stateError("lesson is not available for study")
	}
	return nil
}

func (s *Store) CompleteReviewedBatch(ctx context.Context, sessionID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin lesson batch completion: %w", err)
	}
	defer tx.Rollback()

	var lessonStatus, phase string
	var currentBatch int
	var reviewSessionID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `
		SELECT ls.status, ls.phase, ls.current_batch, (
			SELECT rs.id
			FROM review_sessions rs
			WHERE rs.lesson_session_id = ls.id
			ORDER BY rs.id DESC
			LIMIT 1
		)
		FROM lesson_sessions ls
		WHERE ls.id = ?`, sessionID).Scan(&lessonStatus, &phase, &currentBatch, &reviewSessionID); err != nil {
		return err
	}
	if lessonStatus != "active" || phase != "review" || !reviewSessionID.Valid {
		return nil
	}
	var reviewStatus string
	var reviewCompletedAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `
		SELECT status, completed_at
		FROM review_sessions
		WHERE id = ? AND lesson_session_id = ?`, reviewSessionID.Int64, sessionID).Scan(&reviewStatus, &reviewCompletedAt); err != nil {
		return fmt.Errorf("load lesson review status: %w", err)
	}
	if reviewStatus != "completed" {
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
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit lesson batch completion: %w", err)
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
	items := make([]activation, 0, BatchSize)
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

func (s *Store) batchItems(ctx context.Context, sessionID int64, batch int) ([]StudyItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.id, lsi.position - (? * ?), lsi.study_viewed_at, v.expression,
		       v.pronunciation,
		       COALESCE(v.notes, ''),
		       COALESCE((
		           SELECT vm.media_id FROM vocabulary_media vm
		           WHERE vm.vocabulary_id = v.id AND vm.purpose = 'pronunciation'
		       ), 0),
		       COALESCE((
		           SELECT vm.media_id FROM vocabulary_media vm
		           WHERE vm.vocabulary_id = v.id AND vm.purpose = 'picture'
		       ), 0)
		FROM lesson_session_items lsi
		JOIN vocabulary v ON v.id = lsi.vocabulary_id
		WHERE lsi.session_id = ? AND lsi.batch_number = ?
		ORDER BY lsi.position`, batch, BatchSize, sessionID, batch)
	if err != nil {
		return nil, fmt.Errorf("load lesson batch: %w", err)
	}
	defer rows.Close()
	items := make([]StudyItem, 0, BatchSize)
	for rows.Next() {
		var item StudyItem
		var viewedAt sql.NullInt64
		if err := rows.Scan(
			&item.ID,
			&item.Position,
			&viewedAt,
			&item.Expression,
			&item.Pronunciation,
			&item.Notes,
			&item.AudioID,
			&item.PictureID,
		); err != nil {
			return nil, fmt.Errorf("scan lesson batch item: %w", err)
		}
		item.Viewed = viewedAt.Valid
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate lesson batch: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close lesson batch: %w", err)
	}
	for index := range items {
		meaningRows, err := s.db.QueryContext(ctx, `
			SELECT text FROM meanings WHERE vocabulary_id = ? ORDER BY position`, items[index].ID)
		if err != nil {
			return nil, fmt.Errorf("load lesson meanings: %w", err)
		}
		for meaningRows.Next() {
			var meaning string
			if err := meaningRows.Scan(&meaning); err != nil {
				meaningRows.Close()
				return nil, fmt.Errorf("scan lesson meaning: %w", err)
			}
			items[index].Meanings = append(items[index].Meanings, meaning)
		}
		if err := meaningRows.Err(); err != nil {
			meaningRows.Close()
			return nil, fmt.Errorf("iterate lesson meanings: %w", err)
		}
		if err := meaningRows.Close(); err != nil {
			return nil, fmt.Errorf("close lesson meanings: %w", err)
		}
		example, err := s.exampleStore.Preferred(ctx, items[index].ID)
		if err == nil {
			items[index].Example = example
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("load lesson example: %w", err)
		}
	}
	return items, nil
}
