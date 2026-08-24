package vocabulary

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tomasmik/goi/internal/srs"
)

func (s *Store) Get(ctx context.Context, id int64) (Item, error) {
	var item Item
	var createdAt, updatedAt int64
	var hasSRS bool
	err := s.db.QueryRowContext(ctx, `
		SELECT v.id, v.content_revision, v.expression, v.pronunciation,
		       v.status, v.known_elsewhere_at IS NOT NULL,
		       COALESCE(v.notes, ''), COALESCE(v.source_label, ''),
		       v.created_at, v.updated_at,
		       EXISTS (SELECT 1 FROM srs_states ss WHERE ss.vocabulary_id = v.id),
		       COALESCE(ls.active, 0),
		       COALESCE(ls.active = 1 AND v.status = 'suspended', 0),
		       COALESCE(ls.ever_leech = 1 AND ls.active = 0, 0)
		FROM vocabulary v
		LEFT JOIN leech_states ls ON ls.vocabulary_id = v.id
		WHERE v.id = ?`, id).Scan(
		&item.ID, &item.ContentRevision, &item.Expression, &item.Pronunciation, &item.Status, &item.KnownElsewhere,
		&item.Notes, &item.SourceLabel, &createdAt, &updatedAt, &hasSRS,
		&item.LeechActive, &item.LeechSuspended, &item.FormerLeech,
	)
	if err != nil {
		return Item{}, err
	}
	item.StatusLabel, item.StatusClass = displayStatus(item.Status, item.KnownElsewhere)
	item.CreatedAt = time.Unix(createdAt, 0)
	item.UpdatedAt = time.Unix(updatedAt, 0)
	item.CanMarkKnown = !item.KnownElsewhere && (item.Status == "unlearned" || item.Status == "active" || item.Status == "suspended")
	item.CanToggleSuspension = item.Status == "active" || item.Status == "suspended"
	item.CanResetProgress = hasSRS && item.Status != "unlearned"
	var stage int
	var dueAt sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
		SELECT stage, due_at FROM srs_states WHERE vocabulary_id = ?`, id).Scan(&stage, &dueAt); err == nil {
		item.StageLabel = memoryStageLabel(srs.Stage(stage))
		if dueAt.Valid {
			item.NextReview = time.Unix(dueAt.Int64, 0)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Item{}, fmt.Errorf("load vocabulary review state: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(rr.first_attempt_correct_count), 0),
		       COALESCE(SUM(rr.prompt_count), 0)
		FROM review_results rr
		JOIN review_session_items rsi ON rsi.id = rr.session_item_id
		WHERE rsi.vocabulary_id = ? AND rr.srs_applied = 1 AND rr.voided_at IS NULL`, id).Scan(
		&item.ReviewCount, &item.FirstTryCorrect, &item.PromptCount,
	); err != nil {
		return Item{}, fmt.Errorf("load vocabulary review summary: %w", err)
	}
	item.ReviewHistory, err = s.loadReviewHistory(ctx, id)
	if err != nil {
		return Item{}, err
	}
	item.Meanings, err = s.loadMeanings(ctx, id)
	if err != nil {
		return Item{}, err
	}
	item.Media, err = s.loadMedia(ctx, id)
	if err != nil {
		return Item{}, err
	}
	item.Examples, err = s.examples.List(ctx, id)
	if err != nil {
		return Item{}, fmt.Errorf("load vocabulary examples: %w", err)
	}
	return item, nil
}

func (s *Store) loadReviewHistory(ctx context.Context, vocabularyID int64) ([]ReviewHistoryItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT rr.created_at, rr.outcome, rr.stage_before, rr.stage_after
		FROM review_results rr
		JOIN review_session_items rsi ON rsi.id = rr.session_item_id
		WHERE rsi.vocabulary_id = ? AND rr.srs_applied = 1 AND rr.voided_at IS NULL
		ORDER BY rr.created_at DESC, rr.id DESC LIMIT 8`, vocabularyID)
	if err != nil {
		return nil, fmt.Errorf("load vocabulary review history: %w", err)
	}
	defer rows.Close()

	history := make([]ReviewHistoryItem, 0, 8)
	for rows.Next() {
		var reviewedAt int64
		var stageFrom, stageTo int
		var item ReviewHistoryItem
		if err := rows.Scan(&reviewedAt, &item.Outcome, &stageFrom, &stageTo); err != nil {
			return nil, fmt.Errorf("scan vocabulary review history: %w", err)
		}
		item.ReviewedAt = time.Unix(reviewedAt, 0)
		item.Outcome = reviewOutcomeLabel(item.Outcome)
		item.StageFrom = memoryStageLabel(srs.Stage(stageFrom))
		item.StageTo = memoryStageLabel(srs.Stage(stageTo))
		history = append(history, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vocabulary review history: %w", err)
	}
	return history, nil
}

func (s *Store) loadMeanings(ctx context.Context, vocabularyID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT text FROM meanings WHERE vocabulary_id = ? ORDER BY position`, vocabularyID)
	if err != nil {
		return nil, fmt.Errorf("load meanings: %w", err)
	}
	defer rows.Close()

	var meanings []string
	for rows.Next() {
		var meaning string
		if err := rows.Scan(&meaning); err != nil {
			return nil, fmt.Errorf("scan meaning: %w", err)
		}
		meanings = append(meanings, meaning)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate meanings: %w", err)
	}
	return meanings, nil
}

func (s *Store) loadMedia(ctx context.Context, vocabularyID int64) ([]MediaItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.kind, m.source_name, m.source_url, m.license_name, m.license_url
		FROM vocabulary_media vm JOIN media m ON m.id = vm.media_id
		WHERE vm.vocabulary_id = ? ORDER BY vm.purpose`, vocabularyID)
	if err != nil {
		return nil, fmt.Errorf("load vocabulary media: %w", err)
	}
	defer rows.Close()

	var mediaItems []MediaItem
	for rows.Next() {
		var item MediaItem
		if err := rows.Scan(
			&item.ID, &item.Kind, &item.SourceName, &item.SourceURL, &item.LicenseName, &item.LicenseURL,
		); err != nil {
			return nil, fmt.Errorf("scan vocabulary media: %w", err)
		}
		mediaItems = append(mediaItems, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vocabulary media: %w", err)
	}
	return mediaItems, nil
}

func memoryStageLabel(stage srs.Stage) string {
	switch {
	case stage <= srs.StageThree:
		return "New"
	case stage <= srs.StageFive:
		return "Learning"
	case stage == srs.StageSix:
		return "Familiar"
	case stage <= srs.StageEight:
		return "Mature"
	default:
		return "Burned"
	}
}

func reviewOutcomeLabel(outcome string) string {
	if outcome == "success" {
		return "Correct"
	}
	return "Incorrect"
}
