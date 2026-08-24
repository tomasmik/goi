package mining

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tomasmik/goi/internal/examples"
	"github.com/tomasmik/goi/internal/media"
	"github.com/tomasmik/goi/internal/vocabulary"
)

type Store struct {
	db         *sql.DB
	vocabulary *vocabulary.Store
	examples   *examples.Store
}

type rowScanner interface {
	Scan(dest ...any) error
}

type transitionState struct {
	revision             int64
	status               Status
	rawText              string
	expression           string
	normalizedExpression string
	contextText          string
	sourceKind           SourceKind
	sourceTitle          string
	sourceURL            string
	sourcePositionMS     *int64
}

const captureColumns = `
	c.id, c.raw_text, c.expression, c.normalized_expression, c.context_text,
	c.source_kind, c.source_title, c.source_url, c.source_position_ms,
	c.suggested_entry_sequence,
	c.capture_nonce, c.revision, c.status,
	(SELECT v.id FROM vocabulary v
	 WHERE v.id = c.vocabulary_id),
	(SELECT v.id FROM vocabulary v
	 WHERE v.normalized_expression = c.normalized_expression
	 ORDER BY v.is_duplicate, v.id LIMIT 1),
	COALESCE((SELECT GROUP_CONCAT(audio.media_id, ',') FROM (
	 SELECT mcm.media_id FROM mining_capture_media mcm
	 WHERE mcm.capture_id = c.id AND mcm.purpose = 'sentence_audio'
	 ORDER BY mcm.position, mcm.media_id
	) audio), ''),
	COALESCE((SELECT mcm.media_id FROM mining_capture_media mcm
	 WHERE mcm.capture_id = c.id AND mcm.purpose = 'video_frame'
	 ORDER BY mcm.position LIMIT 1), 0),
	COALESCE((SELECT mcm.media_id FROM mining_capture_media mcm
	 WHERE mcm.capture_id = c.id AND mcm.purpose = 'pronunciation'
	 ORDER BY mcm.position LIMIT 1), 0),
	c.created_at`

const maximumCapturePageSize = 100

func NewStore(db *sql.DB) *Store {
	return &Store{
		db:         db,
		vocabulary: vocabulary.NewStore(db),
		examples:   examples.NewStore(db),
	}
}

func (s *Store) Create(ctx context.Context, input CreateInput) (Capture, bool, error) {
	validated, err := validateCreate(input)
	if err != nil {
		return Capture{}, false, err
	}
	hash := requestHash(validated)

	var existingID int64
	var existingHash string
	err = s.db.QueryRowContext(ctx, `
		SELECT id, request_hash FROM mining_captures WHERE capture_nonce = ?`, validated.captureNonce).Scan(&existingID, &existingHash)
	if err == nil {
		if existingHash != hash {
			return Capture{}, false, ErrNonceConflict
		}
		capture, getErr := s.Get(ctx, existingID)
		if errors.Is(getErr, sql.ErrNoRows) {
			if deletedErr := s.deletedNonceError(ctx, validated.captureNonce); deletedErr != nil {
				return Capture{}, false, deletedErr
			}
		}
		return capture, true, getErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Capture{}, false, fmt.Errorf("check capture nonce: %w", err)
	}
	if err := s.deletedNonceError(ctx, validated.captureNonce); err != nil {
		return Capture{}, false, err
	}

	now := time.Now().UTC().Unix()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO mining_captures (
			raw_text, expression, normalized_expression, context_text,
			source_kind, source_title, source_url, source_position_ms,
			suggested_entry_sequence, capture_nonce, request_hash, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		validated.rawText, validated.expression, validated.normalizedExpression, validated.contextText,
		validated.sourceKind, validated.sourceTitle, validated.sourceURL, validated.sourcePositionMS,
		validated.suggestedEntrySequence, validated.captureNonce, hash, now)
	if err != nil {
		capture, replayed, replayErr := s.recoverNonceRace(ctx, validated.captureNonce, hash)
		if replayErr == nil || errors.Is(replayErr, ErrNonceConflict) || errors.Is(replayErr, ErrCaptureDeleted) {
			return capture, replayed, replayErr
		}
		return Capture{}, false, fmt.Errorf("insert mining capture: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Capture{}, false, fmt.Errorf("get mining capture ID: %w", err)
	}
	capture, err := s.Get(ctx, id)
	return capture, false, err
}

func (s *Store) ListCount(ctx context.Context, status Status) (int, error) {
	return s.ListCountFiltered(ctx, status, "", "")
}

func (s *Store) ListCountFiltered(ctx context.Context, status Status, search, source string) (int, error) {
	status, err := captureListStatus(status)
	if err != nil {
		return 0, err
	}
	var count int
	filter, args := miningListFilter(status, search, source)
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM mining_captures c"+filter, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count mining captures: %w", err)
	}
	return count, nil
}

func (s *Store) ListPage(ctx context.Context, status Status, limit, offset int) ([]Capture, error) {
	return s.ListPageFiltered(ctx, status, "", "", limit, offset)
}

func (s *Store) ListPageFiltered(ctx context.Context, status Status, search, source string, limit, offset int) ([]Capture, error) {
	if limit <= 0 || limit > maximumCapturePageSize {
		return nil, fmt.Errorf("mining page size must be between 1 and %d", maximumCapturePageSize)
	}
	if offset < 0 {
		return nil, errors.New("mining page offset must not be negative")
	}
	return s.list(ctx, status, search, source, limit, offset)
}

func (s *Store) list(ctx context.Context, status Status, search, source string, limit, offset int) ([]Capture, error) {
	status, err := captureListStatus(status)
	if err != nil {
		return nil, err
	}
	filter, args := miningListFilter(status, search, source)
	query := `SELECT ` + captureColumns + `
		FROM mining_captures c` + filter + `
		ORDER BY c.created_at DESC, c.id DESC
		LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list mining captures: %w", err)
	}
	defer rows.Close()

	captures := make([]Capture, 0)
	for rows.Next() {
		capture, err := scanCapture(rows)
		if err != nil {
			return nil, fmt.Errorf("scan mining capture list: %w", err)
		}
		captures = append(captures, capture)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mining captures: %w", err)
	}
	return captures, nil
}

func miningListFilter(status Status, search, source string) (string, []any) {
	filter := " WHERE c.status = ?"
	args := []any{status}
	if search = strings.TrimSpace(search); search != "" {
		filter += " AND (c.expression LIKE '%' || ? || '%' OR c.context_text LIKE '%' || ? || '%' OR c.source_title LIKE '%' || ? || '%')"
		args = append(args, search, search, search)
	}
	if source = strings.TrimSpace(source); source != "" {
		if !validSourceKind(SourceKind(source)) {
			return filter + " AND 0", args
		}
		filter += " AND c.source_kind = ?"
		args = append(args, source)
	}
	return filter, args
}

func (s *Store) BulkTransition(ctx context.Context, ids []int64, from, to Status) error {
	unique, err := validatedCaptureIDs(ids)
	if err != nil {
		return err
	}
	if !validStatus(from) || !validStatus(to) {
		return validationError("bulk capture action is invalid")
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unique)), ",")
	args := []any{to, from}
	for _, id := range unique {
		args = append(args, id)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE mining_captures
		SET status = ?, revision = revision + 1, vocabulary_id = NULL
		WHERE status = ? AND id IN (`+placeholders+")", args...)
	if err != nil {
		return fmt.Errorf("update selected captures: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check selected captures: %w", err)
	}
	if affected != int64(len(unique)) {
		return validationError("some selected captures changed; reload and try again")
	}
	return nil
}

func validatedCaptureIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, validationError("select at least one capture")
	}
	if len(ids) > maximumCapturePageSize {
		return nil, validationError("select at most 100 captures")
	}
	seen := make(map[int64]struct{}, len(ids))
	unique := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, validationError("capture selection is invalid")
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique, nil
}

func (s *Store) NextPendingID(ctx context.Context, afterID int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM mining_captures
		WHERE status = 'pending' AND id <> ?
		ORDER BY CASE WHEN id > ? THEN 0 ELSE 1 END, id
		LIMIT 1`, afterID, afterID).Scan(&id)
	return id, err
}

func captureListStatus(status Status) (Status, error) {
	if status == "" {
		status = StatusPending
	}
	if !validStatus(status) {
		return "", validationError("capture status is invalid")
	}
	return status, nil
}

func (s *Store) Get(ctx context.Context, id int64) (Capture, error) {
	capture, err := scanCapture(s.db.QueryRowContext(ctx, `SELECT `+captureColumns+`
		FROM mining_captures c WHERE c.id = ?`, id))
	if err != nil {
		return Capture{}, err
	}
	return capture, nil
}

func (s *Store) Update(ctx context.Context, id, expectedRevision int64, input UpdateInput) (Capture, error) {
	validated, err := validateUpdate(input)
	if err != nil {
		return Capture{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Capture{}, fmt.Errorf("begin mining update: %w", err)
	}
	defer tx.Rollback()

	state, err := loadTransitionState(ctx, tx, id)
	if err != nil {
		return Capture{}, err
	}
	if err := requirePendingRevision(state, expectedRevision); err != nil {
		return Capture{}, err
	}
	if editableMatches(state, validated) {
		if err := tx.Commit(); err != nil {
			return Capture{}, fmt.Errorf("commit unchanged mining update: %w", err)
		}
		return s.Get(ctx, id)
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE mining_captures
		SET expression = ?, normalized_expression = ?, context_text = ?, source_kind = ?,
			source_title = ?, source_url = ?, source_position_ms = ?,
			revision = revision + 1
		WHERE id = ? AND status = 'pending' AND revision = ?`,
		validated.expression, validated.normalizedExpression, validated.contextText, validated.sourceKind,
		validated.sourceTitle, validated.sourceURL, validated.sourcePositionMS, id, expectedRevision)
	if err != nil {
		return Capture{}, fmt.Errorf("update mining capture: %w", err)
	}
	if err := requireOneRow(result, "update mining capture"); err != nil {
		return Capture{}, err
	}
	if !captureMediaMatches(state, validated) {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM mining_capture_media
			WHERE capture_id = ? AND purpose IN ('sentence_audio', 'video_frame')`, id); err != nil {
			return Capture{}, fmt.Errorf("clear stale mining media: %w", err)
		}
	}
	if state.expression != validated.expression {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM mining_capture_media
			WHERE capture_id = ? AND purpose = 'pronunciation'`, id); err != nil {
			return Capture{}, fmt.Errorf("clear stale pronunciation audio: %w", err)
		}
	}
	if err := media.CollectUnusedInTx(ctx, tx); err != nil {
		return Capture{}, err
	}
	if err := tx.Commit(); err != nil {
		return Capture{}, fmt.Errorf("commit mining update: %w", err)
	}
	return s.Get(ctx, id)
}

func (s *Store) Accept(ctx context.Context, id, expectedRevision int64, input vocabulary.CreateInput) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin mining acceptance: %w", err)
	}
	defer tx.Rollback()
	state, err := loadTransitionState(ctx, tx, id)
	if err != nil {
		return 0, err
	}
	if err := requirePendingRevision(state, expectedRevision); err != nil {
		return 0, err
	}
	vocabularyID, err := s.acceptInTx(ctx, tx, id, expectedRevision, state, input)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit mining acceptance: %w", err)
	}
	return vocabularyID, nil
}

func (s *Store) acceptInTx(ctx context.Context, tx *sql.Tx, id, expectedRevision int64, state transitionState, input vocabulary.CreateInput) (int64, error) {
	example := minedExampleDetails{
		Sentence:      input.ExampleSentence,
		Translation:   input.ExampleTranslation,
		TargetSurface: input.ExampleTarget,
	}
	input.ExampleSentence = ""
	input.ExampleTranslation = ""
	input.ExampleTarget = ""
	input.Expression = state.expression
	input.SourceLabel = miningSourceLabel(state.sourceTitle, state.sourceURL)

	vocabularyID, err := s.vocabulary.CreateInTx(ctx, tx, input)
	if err != nil {
		return 0, err
	}
	if err := s.attachInTx(ctx, tx, id, expectedRevision, vocabularyID, state, example); err != nil {
		return 0, err
	}
	return vocabularyID, nil
}

type minedExampleDetails struct {
	Sentence      string
	Translation   string
	TargetSurface string
}

func (s *Store) attachInTx(ctx context.Context, tx *sql.Tx, id, expectedRevision, vocabularyID int64, state transitionState, example minedExampleDetails) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE mining_captures
		SET status = 'accepted', vocabulary_id = ?, revision = revision + 1
		WHERE id = ? AND status = 'pending' AND revision = ?`,
		vocabularyID, id, expectedRevision)
	if err != nil {
		return fmt.Errorf("resolve accepted mining capture: %w", err)
	}
	if err := requireOneRow(result, "resolve accepted mining capture"); err != nil {
		return err
	}
	if err := s.vocabulary.QueueMinedForLessonInTx(ctx, tx, vocabularyID, time.Now()); err != nil {
		return fmt.Errorf("queue known mined vocabulary for lessons: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO vocabulary_media (vocabulary_id, purpose, media_id)
		SELECT ?, 'pronunciation', media_id
		FROM mining_capture_media
		WHERE capture_id = ? AND purpose = 'pronunciation' AND position = 0
		ON CONFLICT (vocabulary_id, purpose) DO NOTHING`, vocabularyID, id); err != nil {
		return fmt.Errorf("attach mined pronunciation audio: %w", err)
	}
	sentence := strings.TrimSpace(example.Sentence)
	if sentence == "" {
		sentence = state.contextText
	}
	if strings.TrimSpace(sentence) == "" {
		return nil
	}
	targetSurface := strings.TrimSpace(example.TargetSurface)
	if targetSurface == "" {
		targetSurface = state.rawText
	}
	if strings.TrimSpace(targetSurface) == "" {
		targetSurface = state.expression
	}
	_, err = s.examples.CreateInTx(ctx, tx, vocabularyID, examples.Input{
		MiningCaptureID:  &id,
		Origin:           examples.OriginMined,
		Sentence:         sentence,
		Translation:      example.Translation,
		TargetSurface:    targetSurface,
		SourceTitle:      state.sourceTitle,
		SourceURL:        state.sourceURL,
		SourcePositionMS: state.sourcePositionMS,
	})
	if err != nil {
		return fmt.Errorf("create example from mining capture: %w", err)
	}
	return nil
}

func (s *Store) Attach(ctx context.Context, id, expectedRevision, vocabularyID int64) error {
	return s.attachWithExample(ctx, id, expectedRevision, vocabularyID, minedExampleDetails{})
}

func (s *Store) attachWithExample(ctx context.Context, id, expectedRevision, vocabularyID int64, example minedExampleDetails) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin mining attachment: %w", err)
	}
	defer tx.Rollback()
	state, err := loadTransitionState(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := requirePendingRevision(state, expectedRevision); err != nil {
		return err
	}
	var normalizedExpression string
	if err := tx.QueryRowContext(ctx, `
		SELECT normalized_expression FROM vocabulary WHERE id = ?`, vocabularyID).Scan(&normalizedExpression); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return validationError("matching vocabulary item was not found")
		}
		return fmt.Errorf("load vocabulary for mining attachment: %w", err)
	}
	if normalizedExpression != state.normalizedExpression {
		return validationError("vocabulary expression does not match the capture")
	}
	if err := s.attachInTx(ctx, tx, id, expectedRevision, vocabularyID, state, example); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mining attachment: %w", err)
	}
	return nil
}

func (s *Store) Discard(ctx context.Context, id, expectedRevision int64) error {
	return s.transition(ctx, id, expectedRevision, StatusPending, StatusDiscarded)
}

func (s *Store) Restore(ctx context.Context, id, expectedRevision int64) error {
	return s.transition(ctx, id, expectedRevision, StatusDiscarded, StatusPending)
}

func (s *Store) Delete(ctx context.Context, id, expectedRevision int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin mining capture deletion: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO mining_capture_tombstones (capture_nonce, deleted_at)
		SELECT capture_nonce, ?
		FROM mining_captures
		WHERE id = ? AND revision = ?`, time.Now().UTC().Unix(), id, expectedRevision)
	if err != nil {
		return fmt.Errorf("remember deleted mining capture: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check deleted mining capture receipt: %w", err)
	}
	if affected != 1 {
		var revision int64
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM mining_captures WHERE id = ?`, id).Scan(&revision); err != nil {
			return err
		}
		return ErrRevisionConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mining_captures WHERE id = ? AND revision = ?`, id, expectedRevision); err != nil {
		return fmt.Errorf("delete mining capture: %w", err)
	}
	if err := media.CollectUnusedInTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mining capture deletion: %w", err)
	}
	return nil
}

func (s *Store) transition(ctx context.Context, id, expectedRevision int64, from, to Status) error {
	query := `
		UPDATE mining_captures
		SET status = ?, revision = revision + 1, vocabulary_id = NULL
		WHERE id = ? AND status = ? AND revision = ?`
	result, err := s.db.ExecContext(ctx, query, to, id, from, expectedRevision)
	if err != nil {
		return fmt.Errorf("transition mining capture to %s: %w", to, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check mining transition to %s: %w", to, err)
	}
	if affected == 1 {
		return nil
	}
	var revision int64
	var status Status
	if err := s.db.QueryRowContext(ctx, `SELECT revision, status FROM mining_captures WHERE id = ?`, id).Scan(&revision, &status); err != nil {
		return err
	}
	if revision != expectedRevision {
		return ErrRevisionConflict
	}
	return ErrInvalidTransition
}

func (s *Store) recoverNonceRace(ctx context.Context, nonce, hash string) (Capture, bool, error) {
	var id int64
	var existingHash string
	if err := s.db.QueryRowContext(ctx, `
		SELECT id, request_hash FROM mining_captures WHERE capture_nonce = ?`, nonce).Scan(&id, &existingHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if deletedErr := s.deletedNonceError(ctx, nonce); deletedErr != nil {
				return Capture{}, false, deletedErr
			}
		}
		return Capture{}, false, err
	}
	if existingHash != hash {
		return Capture{}, false, ErrNonceConflict
	}
	capture, err := s.Get(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		if deletedErr := s.deletedNonceError(ctx, nonce); deletedErr != nil {
			return Capture{}, false, deletedErr
		}
	}
	return capture, true, err
}

func (s *Store) deletedNonceError(ctx context.Context, nonce string) error {
	var deleted bool
	err := s.db.QueryRowContext(ctx, `
		SELECT TRUE FROM mining_capture_tombstones WHERE capture_nonce = ?`, nonce).Scan(&deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check deleted capture nonce: %w", err)
	}
	return ErrCaptureDeleted
}

func scanCapture(scanner rowScanner) (Capture, error) {
	var capture Capture
	var sourcePosition, suggestedEntrySequence, vocabularyID, existingVocabularyID sql.NullInt64
	var sentenceAudioIDs string
	var createdAt int64
	err := scanner.Scan(
		&capture.ID, &capture.RawText, &capture.Expression, &capture.NormalizedExpression, &capture.ContextText,
		&capture.SourceKind, &capture.SourceTitle, &capture.SourceURL, &sourcePosition,
		&suggestedEntrySequence,
		&capture.CaptureNonce, &capture.Revision, &capture.Status, &vocabularyID, &existingVocabularyID,
		&sentenceAudioIDs, &capture.VideoFrameID, &capture.PronunciationAudioID,
		&createdAt,
	)
	if err != nil {
		return Capture{}, err
	}
	capture.SourcePositionMS = nullableInt64(sourcePosition)
	capture.SuggestedEntrySequence = nullableInt64(suggestedEntrySequence)
	capture.VocabularyID = nullableInt64(vocabularyID)
	capture.ExistingVocabularyID = nullableInt64(existingVocabularyID)
	capture.SentenceAudioIDs = parseMediaIDs(sentenceAudioIDs)
	if len(capture.SentenceAudioIDs) > 0 {
		capture.SentenceAudioID = capture.SentenceAudioIDs[0]
	}
	capture.CreatedAt = time.Unix(createdAt, 0).UTC()
	return capture, nil
}

func parseMediaIDs(value string) []int64 {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseInt(part, 10, 64)
		if err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

func loadTransitionState(ctx context.Context, tx *sql.Tx, id int64) (transitionState, error) {
	var state transitionState
	var sourcePosition sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT revision, status, raw_text, expression, normalized_expression, context_text,
			source_kind, source_title, source_url, source_position_ms
		FROM mining_captures WHERE id = ?`, id).Scan(
		&state.revision, &state.status, &state.rawText, &state.expression, &state.normalizedExpression, &state.contextText,
		&state.sourceKind, &state.sourceTitle, &state.sourceURL, &sourcePosition,
	)
	if err != nil {
		return transitionState{}, err
	}
	state.sourcePositionMS = nullableInt64(sourcePosition)
	return state, nil
}

func requirePendingRevision(state transitionState, expectedRevision int64) error {
	if state.revision != expectedRevision {
		return ErrRevisionConflict
	}
	if state.status != StatusPending {
		return ErrInvalidTransition
	}
	return nil
}

func editableMatches(state transitionState, input validatedCapture) bool {
	return state.expression == input.expression &&
		state.normalizedExpression == input.normalizedExpression &&
		state.contextText == input.contextText &&
		state.sourceKind == input.sourceKind &&
		state.sourceTitle == input.sourceTitle &&
		state.sourceURL == input.sourceURL &&
		equalInt64(state.sourcePositionMS, input.sourcePositionMS)
}

func captureMediaMatches(state transitionState, input validatedCapture) bool {
	return state.contextText == input.contextText &&
		state.sourceURL == input.sourceURL &&
		equalInt64(state.sourcePositionMS, input.sourcePositionMS)
}

func requireOneRow(result sql.Result, operation string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check %s: %w", operation, err)
	}
	if affected != 1 {
		return ErrRevisionConflict
	}
	return nil
}

func validStatus(status Status) bool {
	return status == StatusPending || status == StatusAccepted || status == StatusDiscarded
}

func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func equalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func miningSourceLabel(title, sourceURL string) string {
	label := "Mining inbox"
	if title != "" {
		label = title
	} else if sourceURL != "" {
		label = sourceURL
	}
	runes := []rune(label)
	if len(runes) > maxTitleRunes {
		return string(runes[:maxTitleRunes])
	}
	return label
}
