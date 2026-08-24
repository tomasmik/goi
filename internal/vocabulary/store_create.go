package vocabulary

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tomasmik/goi/internal/examples"
	"github.com/tomasmik/goi/internal/kana"
	"github.com/tomasmik/goi/internal/media"
	"github.com/tomasmik/goi/internal/textnorm"
)

func (s *Store) Create(ctx context.Context, input CreateInput) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin vocabulary transaction: %w", err)
	}
	defer tx.Rollback()
	id, err := s.CreateInTx(ctx, tx, input)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit vocabulary: %w", err)
	}
	return id, nil
}

func (s *Store) CreateInTx(ctx context.Context, tx *sql.Tx, input CreateInput) (int64, error) {
	validated, err := validateInputMode(input, input.AllowSparse)
	if err != nil {
		return 0, err
	}

	now := time.Now().UTC()
	normalizedExpression := textnorm.Normalize(validated.expression)
	if !input.AllowDuplicate {
		var existingID int64
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM vocabulary
			WHERE normalized_expression = ?
			ORDER BY is_duplicate, id LIMIT 1`, normalizedExpression).Scan(&existingID)
		if err == nil {
			return 0, duplicateError{id: existingID}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("check existing vocabulary: %w", err)
		}
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO vocabulary (
			expression, normalized_expression, pronunciation, normalized_pronunciation,
			source_label, notes, is_duplicate, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (normalized_expression) WHERE is_duplicate = 0 DO NOTHING`,
		validated.expression, normalizedExpression, validated.pronunciation, normalizedPronunciation(validated.pronunciation),
		validated.sourceLabel, validated.notes, input.AllowDuplicate, now.Unix(), now.Unix())
	if err != nil {
		return 0, fmt.Errorf("insert vocabulary: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("check inserted vocabulary: %w", err)
	}
	if affected == 0 {
		var existingID int64
		if err := tx.QueryRowContext(ctx, `
			SELECT id FROM vocabulary
			WHERE normalized_expression = ?
			ORDER BY is_duplicate, id LIMIT 1`, normalizedExpression).Scan(&existingID); err != nil {
			return 0, fmt.Errorf("load conflicting vocabulary: %w", err)
		}
		return 0, duplicateError{id: existingID}
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get vocabulary ID: %w", err)
	}

	if err := insertVocabularyText(ctx, tx, id, validated); err != nil {
		return 0, err
	}
	if strings.TrimSpace(input.ExampleSentence) == "" {
		if strings.TrimSpace(input.ExampleTranslation) != "" || strings.TrimSpace(input.ExampleTarget) != "" {
			return 0, validationError("example sentence is required when example details are provided")
		}
	} else if _, err := s.examples.CreateInTx(ctx, tx, id, examples.Input{
		Origin:        examples.OriginManual,
		Sentence:      input.ExampleSentence,
		Translation:   input.ExampleTranslation,
		TargetSurface: exampleTarget(input.ExampleTarget, validated.expression),
	}); err != nil {
		return 0, fmt.Errorf("create vocabulary example: %w", err)
	}

	for _, upload := range []*media.Upload{validated.audio, validated.picture} {
		if upload == nil {
			continue
		}
		mediaID, err := media.SaveInTx(ctx, tx, *upload, now)
		if err != nil {
			return 0, err
		}
		purpose := "pronunciation"
		if upload.Kind == media.KindImage {
			purpose = "picture"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO vocabulary_media (vocabulary_id, purpose, media_id)
			VALUES (?, ?, ?)`, id, purpose, mediaID); err != nil {
			return 0, fmt.Errorf("associate media: %w", err)
		}
	}

	return id, nil
}

func (s *Store) MarkKnownElsewhereInTx(ctx context.Context, tx *sql.Tx, id int64) error {
	now := time.Now().UTC().Unix()
	result, err := tx.ExecContext(ctx, `
		UPDATE vocabulary
		SET known_elsewhere_at = ?, updated_at = ?, content_revision = content_revision + 1
		WHERE id = ? AND status = 'unlearned' AND known_elsewhere_at IS NULL`, now, now, id)
	if err != nil {
		return fmt.Errorf("mark vocabulary %d as known elsewhere: %w", id, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check known vocabulary %d: %w", id, err)
	}
	if updated != 1 {
		return fmt.Errorf("vocabulary %d cannot be marked as known elsewhere", id)
	}
	return nil
}

func (s *Store) CompleteSparseInTx(ctx context.Context, tx *sql.Tx, id int64, pronunciation string, meanings []string) error {
	var currentPronunciation string
	var meaningCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT pronunciation, (SELECT COUNT(*) FROM meanings WHERE vocabulary_id = vocabulary.id)
		FROM vocabulary
		WHERE id = ?`, id).Scan(&currentPronunciation, &meaningCount); err != nil {
		return fmt.Errorf("load sparse vocabulary: %w", err)
	}
	if currentPronunciation != "" && meaningCount > 0 {
		return nil
	}

	var canonicalPronunciation string
	if currentPronunciation == "" {
		cleaned, err := cleanInputText(pronunciation, maxPronunciationRunes, "pronunciation", false)
		if err != nil {
			return err
		}
		if cleaned == "" {
			return validationError("pronunciation is required")
		}
		canonicalPronunciation, err = kana.Convert(cleaned)
		if err != nil {
			return validationError(fmt.Sprintf("pronunciation: %v", err))
		}
	}

	var cleanedMeanings []string
	if meaningCount == 0 {
		meaningsText, err := cleanInputText(strings.Join(meanings, "\n"), maxMeaningsRunes, "meanings", true)
		if err != nil {
			return err
		}
		cleanedMeanings = cleanLines(strings.Split(meaningsText, "\n"))
		if len(cleanedMeanings) == 0 {
			return validationError("at least one meaning is required")
		}
	}

	now := time.Now().UTC().Unix()
	if currentPronunciation == "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE vocabulary
			SET pronunciation = ?, normalized_pronunciation = ?, updated_at = ?, content_revision = content_revision + 1
			WHERE id = ?`, canonicalPronunciation, normalizedPronunciation(canonicalPronunciation), now, id); err != nil {
			return fmt.Errorf("add vocabulary pronunciation: %w", err)
		}
	}
	if meaningCount == 0 {
		if err := insertVocabularyText(ctx, tx, id, validatedInput{meanings: cleanedMeanings}); err != nil {
			return err
		}
		if currentPronunciation != "" {
			if _, err := tx.ExecContext(ctx, `
				UPDATE vocabulary
				SET updated_at = ?, content_revision = content_revision + 1
				WHERE id = ?`, now, id); err != nil {
				return fmt.Errorf("record vocabulary meanings update: %w", err)
			}
		}
	}
	return nil
}

func exampleTarget(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}
