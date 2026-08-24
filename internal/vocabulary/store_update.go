package vocabulary

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tomasmik/goi/internal/media"
	"github.com/tomasmik/goi/internal/textnorm"
)

func (s *Store) Update(ctx context.Context, id, expectedRevision int64, input CreateInput) error {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin vocabulary update: %w", err)
	}
	defer tx.Rollback()
	var currentRevision int64
	var currentNormalizedExpression string
	var allowSparse bool
	if err := tx.QueryRowContext(ctx, `
		SELECT content_revision, known_elsewhere_at IS NOT NULL, normalized_expression
		FROM vocabulary
		WHERE id = ?`, id).Scan(
		&currentRevision, &allowSparse, &currentNormalizedExpression,
	); err != nil {
		return err
	}
	if expectedRevision <= 0 || currentRevision != expectedRevision {
		return revisionConflictError{}
	}
	validated, err := validateInputForUpdate(input, allowSparse)
	if err != nil {
		return err
	}
	normalizedExpression := textnorm.Normalize(validated.expression)
	if normalizedExpression != currentNormalizedExpression {
		var existingID int64
		err = tx.QueryRowContext(ctx, `
			SELECT id FROM vocabulary
			WHERE normalized_expression = ? AND id <> ?
			ORDER BY is_duplicate, id LIMIT 1`, normalizedExpression, id).Scan(&existingID)
		if err == nil {
			return duplicateError{id: existingID}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check duplicate expression: %w", err)
		}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE vocabulary
		SET expression = ?, normalized_expression = ?,
		    pronunciation = ?, normalized_pronunciation = ?,
		    source_label = ?, notes = ?,
		    updated_at = ?, content_revision = content_revision + 1
		WHERE id = ? AND content_revision = ?`,
		validated.expression, normalizedExpression,
		validated.pronunciation, normalizedPronunciation(validated.pronunciation), validated.sourceLabel,
		validated.notes, now.Unix(), id, expectedRevision)
	if err != nil {
		return fmt.Errorf("update vocabulary: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return fmt.Errorf("check vocabulary update: %w", err)
		}
		return revisionConflictError{}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM meanings WHERE vocabulary_id = ?", id); err != nil {
		return fmt.Errorf("replace meanings: %w", err)
	}
	if err := insertVocabularyText(ctx, tx, id, validated); err != nil {
		return err
	}
	for _, change := range []struct {
		purpose string
		upload  *media.Upload
		remove  bool
	}{
		{purpose: "pronunciation", upload: validated.audio, remove: validated.removeAudio},
		{purpose: "picture", upload: validated.picture, remove: validated.removePicture},
	} {
		if change.upload == nil && !change.remove {
			continue
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM vocabulary_media WHERE vocabulary_id = ? AND purpose = ?", id, change.purpose); err != nil {
			return fmt.Errorf("replace %s association: %w", change.purpose, err)
		}
		if change.upload == nil {
			continue
		}
		mediaID, err := media.SaveInTx(ctx, tx, *change.upload, now)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO vocabulary_media (vocabulary_id, purpose, media_id)
			VALUES (?, ?, ?)`, id, change.purpose, mediaID); err != nil {
			return fmt.Errorf("associate updated media: %w", err)
		}
	}
	if err := media.CollectUnusedInTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit vocabulary update: %w", err)
	}
	return nil
}

func (s *Store) SetPronunciationAudio(ctx context.Context, id, expectedRevision int64, upload media.Upload) error {
	if id <= 0 || expectedRevision <= 0 || upload.Kind != media.KindAudio {
		return validationError("pronunciation audio is invalid")
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin pronunciation audio update: %w", err)
	}
	defer tx.Rollback()

	var currentRevision int64
	if err := tx.QueryRowContext(ctx, "SELECT content_revision FROM vocabulary WHERE id = ?", id).Scan(&currentRevision); err != nil {
		return err
	}
	if currentRevision != expectedRevision {
		return revisionConflictError{}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE vocabulary
		SET updated_at = ?, content_revision = content_revision + 1
		WHERE id = ? AND content_revision = ?`, now.Unix(), id, expectedRevision)
	if err != nil {
		return fmt.Errorf("update vocabulary pronunciation revision: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return fmt.Errorf("check pronunciation audio update: %w", err)
		}
		return revisionConflictError{}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM vocabulary_media WHERE vocabulary_id = ? AND purpose = 'pronunciation'", id); err != nil {
		return fmt.Errorf("replace pronunciation audio: %w", err)
	}
	mediaID, err := media.SaveInTx(ctx, tx, upload, now)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO vocabulary_media (vocabulary_id, purpose, media_id)
		VALUES (?, 'pronunciation', ?)`, id, mediaID); err != nil {
		return fmt.Errorf("associate pronunciation audio: %w", err)
	}
	if err := media.CollectUnusedInTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit pronunciation audio update: %w", err)
	}
	return nil
}
