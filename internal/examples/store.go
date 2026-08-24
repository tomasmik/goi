package examples

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Store struct {
	db *sql.DB
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type rowScanner interface {
	Scan(...any) error
}

const exampleColumns = `
	e.id, e.vocabulary_id, e.mining_capture_id, e.origin,
	e.sentence, e.translation, e.target_surface,
	e.source_title, e.source_url, e.source_position_ms,
	COALESCE((SELECT GROUP_CONCAT(audio.media_id, ',') FROM (
	 SELECT mcm.media_id FROM mining_capture_media mcm
	 WHERE mcm.capture_id = e.mining_capture_id AND mcm.purpose = 'sentence_audio'
	 ORDER BY mcm.position, mcm.media_id
	) audio), ''),
	COALESCE((SELECT mcm.media_id FROM mining_capture_media mcm
	 WHERE mcm.capture_id = e.mining_capture_id AND mcm.purpose = 'video_frame'
	 ORDER BY mcm.position LIMIT 1), 0),
	e.provider, e.model, e.created_at, e.updated_at`

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Create(ctx context.Context, vocabularyID int64, input Input) (Example, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Example{}, fmt.Errorf("begin vocabulary example creation: %w", err)
	}
	defer tx.Rollback()
	example, err := s.CreateInTx(ctx, tx, vocabularyID, input)
	if err != nil {
		return Example{}, err
	}
	if err := tx.Commit(); err != nil {
		return Example{}, fmt.Errorf("commit vocabulary example creation: %w", err)
	}
	return example, nil
}

func (s *Store) CreateInTx(ctx context.Context, tx *sql.Tx, vocabularyID int64, input Input) (Example, error) {
	if vocabularyID <= 0 {
		return Example{}, validationError("vocabulary is invalid")
	}
	validated, err := validateInput(input)
	if err != nil {
		return Example{}, err
	}
	if err := requireVocabulary(ctx, tx, vocabularyID); err != nil {
		return Example{}, err
	}
	if err := requireMiningCapture(ctx, tx, vocabularyID, validated); err != nil {
		return Example{}, err
	}
	now := time.Now().UTC().Unix()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO vocabulary_examples (
			vocabulary_id, mining_capture_id, origin, sentence, translation,
			target_surface, source_title, source_url, source_position_ms,
			provider, model, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		vocabularyID, validated.miningCaptureID, validated.origin, validated.sentence, validated.translation,
		validated.targetSurface, validated.sourceTitle, validated.sourceURL, validated.sourcePositionMS,
		validated.provider, validated.model, now, now)
	if err != nil {
		return Example{}, fmt.Errorf("insert vocabulary example: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Example{}, fmt.Errorf("get vocabulary example ID: %w", err)
	}
	return get(ctx, tx, vocabularyID, id)
}

func (s *Store) CreateGeneratedIfEmpty(ctx context.Context, vocabularyID, expectedRevision int64, input Input) (Example, error) {
	if vocabularyID <= 0 || expectedRevision <= 0 {
		return Example{}, validationError("vocabulary is invalid")
	}
	input.Origin = OriginGenerated
	validated, err := validateInput(input)
	if err != nil {
		return Example{}, err
	}
	now := time.Now().UTC().Unix()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO vocabulary_examples (
			vocabulary_id, mining_capture_id, origin, sentence, translation,
			target_surface, source_title, source_url, source_position_ms,
			provider, model, created_at, updated_at
		)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE EXISTS (
			SELECT 1 FROM vocabulary
			WHERE id = ? AND content_revision = ?
		)
		AND NOT EXISTS (
			SELECT 1 FROM vocabulary_examples WHERE vocabulary_id = ?
		)`,
		vocabularyID, validated.miningCaptureID, validated.origin, validated.sentence, validated.translation,
		validated.targetSurface, validated.sourceTitle, validated.sourceURL, validated.sourcePositionMS,
		validated.provider, validated.model, now, now, vocabularyID, expectedRevision, vocabularyID)
	if err != nil {
		return Example{}, fmt.Errorf("insert generated vocabulary example: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Example{}, fmt.Errorf("check generated vocabulary example creation: %w", err)
	}
	if affected == 0 {
		var currentRevision int64
		if err := s.db.QueryRowContext(ctx, `
			SELECT content_revision
			FROM vocabulary
			WHERE id = ?`, vocabularyID).Scan(&currentRevision); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Example{}, sql.ErrNoRows
			}
			return Example{}, fmt.Errorf("load vocabulary revision: %w", err)
		}
		if currentRevision != expectedRevision {
			return Example{}, ErrVocabularyChanged
		}
		return Example{}, ErrAlreadyExists
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Example{}, fmt.Errorf("get generated vocabulary example ID: %w", err)
	}
	return get(ctx, s.db, vocabularyID, id)
}

func (s *Store) List(ctx context.Context, vocabularyID int64) ([]Example, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+exampleColumns+`
		FROM vocabulary_examples e
		WHERE e.vocabulary_id = ?
		ORDER BY e.created_at DESC, e.id DESC`, vocabularyID)
	if err != nil {
		return nil, fmt.Errorf("list vocabulary examples: %w", err)
	}
	defer rows.Close()

	examples := make([]Example, 0)
	for rows.Next() {
		example, err := scanExample(rows)
		if err != nil {
			return nil, fmt.Errorf("scan vocabulary example list: %w", err)
		}
		examples = append(examples, example)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vocabulary examples: %w", err)
	}
	return examples, nil
}

func (s *Store) Preferred(ctx context.Context, vocabularyID int64) (Example, error) {
	example, err := scanExample(s.db.QueryRowContext(ctx, `SELECT `+exampleColumns+`
		FROM vocabulary_examples e
		WHERE e.vocabulary_id = ?
		ORDER BY CASE e.origin
			WHEN 'manual' THEN 0
			WHEN 'mined' THEN 1
			ELSE 2
		END, e.updated_at DESC, e.id DESC
		LIMIT 1`, vocabularyID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Example{}, sql.ErrNoRows
		}
		return Example{}, fmt.Errorf("load preferred vocabulary example: %w", err)
	}
	return example, nil
}

func (s *Store) Update(ctx context.Context, vocabularyID, exampleID int64, input Input) (Example, error) {
	if vocabularyID <= 0 || exampleID <= 0 {
		return Example{}, validationError("vocabulary example is invalid")
	}
	validated, err := validateInput(input)
	if err != nil {
		return Example{}, err
	}
	if err := requireMiningCapture(ctx, s.db, vocabularyID, validated); err != nil {
		return Example{}, err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE vocabulary_examples
		SET mining_capture_id = CASE
				WHEN mining_capture_id IS NOT NULL AND sentence <> ? THEN NULL
				ELSE ?
			END,
			origin = CASE
				WHEN mining_capture_id IS NOT NULL AND sentence <> ? THEN 'manual'
				ELSE ?
			END,
			sentence = ?, translation = ?,
			target_surface = ?, source_title = ?, source_url = ?, source_position_ms = ?,
			provider = ?, model = ?, updated_at = ?
		WHERE id = ? AND vocabulary_id = ?
		  AND EXISTS (
			SELECT 1 FROM vocabulary v
			WHERE v.id = vocabulary_examples.vocabulary_id
		  )`,
		validated.sentence, validated.miningCaptureID, validated.sentence, validated.origin,
		validated.sentence, validated.translation,
		validated.targetSurface, validated.sourceTitle, validated.sourceURL, validated.sourcePositionMS,
		validated.provider, validated.model, time.Now().UTC().Unix(), exampleID, vocabularyID)
	if err != nil {
		return Example{}, fmt.Errorf("update vocabulary example: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return Example{}, err
	}
	return get(ctx, s.db, vocabularyID, exampleID)
}

func (s *Store) Delete(ctx context.Context, vocabularyID, exampleID int64) error {
	if vocabularyID <= 0 || exampleID <= 0 {
		return validationError("vocabulary example is invalid")
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM vocabulary_examples
		WHERE id = ? AND vocabulary_id = ?
		  AND EXISTS (
			SELECT 1 FROM vocabulary v
			WHERE v.id = vocabulary_examples.vocabulary_id
		  )`, exampleID, vocabularyID)
	if err != nil {
		return fmt.Errorf("delete vocabulary example: %w", err)
	}
	return requireOneRow(result)
}

func requireVocabulary(ctx context.Context, queryer queryer, vocabularyID int64) error {
	var exists bool
	if err := queryer.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM vocabulary WHERE id = ?
		)`, vocabularyID).Scan(&exists); err != nil {
		return fmt.Errorf("check vocabulary for example: %w", err)
	}
	if !exists {
		return sql.ErrNoRows
	}
	return nil
}

func requireMiningCapture(ctx context.Context, queryer queryer, vocabularyID int64, input validatedInput) error {
	if input.miningCaptureID == nil {
		return nil
	}
	var matches bool
	if err := queryer.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM mining_captures
			WHERE id = ? AND status = 'accepted' AND vocabulary_id = ?
		)`, *input.miningCaptureID, vocabularyID).Scan(&matches); err != nil {
		return fmt.Errorf("check mining capture for example: %w", err)
	}
	if !matches {
		return validationError("mining capture does not belong to this vocabulary")
	}
	return nil
}

func requireOneRow(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check vocabulary example change: %w", err)
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func get(ctx context.Context, queryer queryer, vocabularyID, exampleID int64) (Example, error) {
	example, err := scanExample(queryer.QueryRowContext(ctx, `SELECT `+exampleColumns+`
		FROM vocabulary_examples e
		WHERE e.id = ? AND e.vocabulary_id = ?`, exampleID, vocabularyID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Example{}, sql.ErrNoRows
		}
		return Example{}, fmt.Errorf("load vocabulary example: %w", err)
	}
	return example, nil
}

func scanExample(scanner rowScanner) (Example, error) {
	var example Example
	var miningCaptureID, sourcePositionMS sql.NullInt64
	var sentenceAudioIDs string
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&example.ID, &example.VocabularyID, &miningCaptureID, &example.Origin,
		&example.Sentence, &example.Translation, &example.TargetSurface,
		&example.SourceTitle, &example.SourceURL, &sourcePositionMS,
		&sentenceAudioIDs, &example.VideoFrameID,
		&example.Provider, &example.Model, &createdAt, &updatedAt,
	); err != nil {
		return Example{}, err
	}
	example.MiningCaptureID = nullableInt64(miningCaptureID)
	example.SentenceAudioIDs = parseMediaIDs(sentenceAudioIDs)
	if len(example.SentenceAudioIDs) > 0 {
		example.SentenceAudioID = example.SentenceAudioIDs[0]
	}
	example.SourcePositionMS = nullableInt64(sourcePositionMS)
	example.CreatedAt = time.Unix(createdAt, 0).UTC()
	example.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	example.SourceLink = LinkForSource(example.SourceURL, example.SourcePositionMS)
	example.SourcePositionLabel = FormatSourcePosition(example.SourcePositionMS)
	example.BeforeTarget, example.MatchedTarget, example.AfterTarget, example.HasTarget = SplitTarget(example.Sentence, example.TargetSurface)
	return example, nil
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

func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}
