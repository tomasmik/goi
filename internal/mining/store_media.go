package mining

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tomasmik/goi/internal/media"
)

func (s *Store) AttachMedia(ctx context.Context, id, expectedRevision int64, captureNonce string, input CaptureMediaInput) error {
	if id <= 0 {
		return validationError("capture is invalid")
	}
	if expectedRevision <= 0 {
		return validationError("capture revision is invalid")
	}
	nonce, err := validateNonce(captureNonce)
	if err != nil {
		return err
	}
	if input.SentenceAudio == nil && input.VideoFrame == nil {
		return validationError("at least one media file is required")
	}
	if input.SentenceAudio != nil && input.SentenceAudio.Kind != media.KindAudio {
		return validationError("sentence audio has the wrong media kind")
	}
	if input.VideoFrame != nil && input.VideoFrame.Kind != media.KindImage {
		return validationError("video frame has the wrong media kind")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin mining media attachment: %w", err)
	}
	defer tx.Rollback()
	var storedNonce, contextText string
	var revision int64
	var status Status
	if err := tx.QueryRowContext(ctx, `
		SELECT capture_nonce, revision, status, context_text
		FROM mining_captures
		WHERE id = ?`, id).Scan(&storedNonce, &revision, &status, &contextText); err != nil {
		return err
	}
	if storedNonce != nonce {
		return validationError("capture nonce does not match")
	}
	if strings.TrimSpace(contextText) == "" {
		return validationError("capture context is required for media attachment")
	}
	switch status {
	case StatusPending:
		if revision != expectedRevision {
			return ErrRevisionConflict
		}
	case StatusAccepted:
		if revision != expectedRevision && (revision <= 1 || revision-1 != expectedRevision) {
			return ErrRevisionConflict
		}
	default:
		return ErrInvalidTransition
	}

	now := time.Now().UTC()
	attachments := []struct {
		purpose string
		upload  *media.Upload
	}{
		{purpose: "sentence_audio", upload: input.SentenceAudio},
		{purpose: "video_frame", upload: input.VideoFrame},
	}
	for _, attachment := range attachments {
		if attachment.upload == nil {
			continue
		}
		mediaID, err := media.SaveInTx(ctx, tx, *attachment.upload, now)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO mining_capture_media (capture_id, purpose, position, media_id)
			VALUES (?, ?, 0, ?)
			ON CONFLICT (capture_id, purpose, position) DO UPDATE SET media_id = excluded.media_id`,
			id, attachment.purpose, mediaID); err != nil {
			return fmt.Errorf("associate mining %s: %w", attachment.purpose, err)
		}
	}
	if err := media.CollectUnusedInTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mining media attachment: %w", err)
	}
	return nil
}

func (s *Store) AddMedia(ctx context.Context, id, expectedRevision int64, audio []media.Upload, frame, pronunciation *media.Upload) error {
	if id <= 0 || expectedRevision <= 0 {
		return validationError("capture is invalid")
	}
	if len(audio) == 0 && frame == nil && pronunciation == nil {
		return validationError("choose at least one audio or image file")
	}
	for _, upload := range audio {
		if upload.Kind != media.KindAudio {
			return validationError("sentence audio has the wrong media kind")
		}
	}
	if frame != nil && frame.Kind != media.KindImage {
		return validationError("video frame has the wrong media kind")
	}
	if pronunciation != nil && pronunciation.Kind != media.KindAudio {
		return validationError("pronunciation audio has the wrong media kind")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin mining media upload: %w", err)
	}
	defer tx.Rollback()
	state, err := loadTransitionState(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := requirePendingRevision(state, expectedRevision); err != nil {
		return err
	}
	if (len(audio) > 0 || frame != nil) && strings.TrimSpace(state.contextText) == "" {
		return validationError("add a sentence before attaching sentence media")
	}

	var nextPosition int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(position) + 1, 0)
		FROM mining_capture_media
		WHERE capture_id = ? AND purpose = 'sentence_audio'`, id).Scan(&nextPosition); err != nil {
		return fmt.Errorf("find next sentence audio position: %w", err)
	}
	now := time.Now().UTC()
	for _, upload := range audio {
		mediaID, err := media.SaveInTx(ctx, tx, upload, now)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO mining_capture_media (capture_id, purpose, position, media_id)
			VALUES (?, 'sentence_audio', ?, ?)`, id, nextPosition, mediaID)
		if err != nil {
			return fmt.Errorf("associate sentence audio: %w", err)
		}
		if inserted, err := result.RowsAffected(); err != nil {
			return fmt.Errorf("check sentence audio association: %w", err)
		} else if inserted == 1 {
			nextPosition++
		}
	}
	if frame != nil {
		mediaID, err := media.SaveInTx(ctx, tx, *frame, now)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO mining_capture_media (capture_id, purpose, position, media_id)
			VALUES (?, 'video_frame', 0, ?)
			ON CONFLICT (capture_id, purpose, position) DO UPDATE SET media_id = excluded.media_id`, id, mediaID); err != nil {
			return fmt.Errorf("associate video frame: %w", err)
		}
	}
	if pronunciation != nil {
		mediaID, err := media.SaveInTx(ctx, tx, *pronunciation, now)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO mining_capture_media (capture_id, purpose, position, media_id)
			VALUES (?, 'pronunciation', 0, ?)
			ON CONFLICT (capture_id, purpose, position) DO UPDATE SET media_id = excluded.media_id`, id, mediaID); err != nil {
			return fmt.Errorf("associate pronunciation audio: %w", err)
		}
	}
	if err := media.CollectUnusedInTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mining media upload: %w", err)
	}
	return nil
}

func (s *Store) SetPronunciationAudio(ctx context.Context, id, expectedRevision int64, upload media.Upload) error {
	return s.AddMedia(ctx, id, expectedRevision, nil, nil, &upload)
}

func (s *Store) RemoveMedia(ctx context.Context, id, expectedRevision, mediaID int64) error {
	if id <= 0 || expectedRevision <= 0 || mediaID <= 0 {
		return validationError("capture media is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin mining media removal: %w", err)
	}
	defer tx.Rollback()
	state, err := loadTransitionState(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := requirePendingRevision(state, expectedRevision); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		DELETE FROM mining_capture_media
		WHERE capture_id = ? AND media_id = ?`, id, mediaID)
	if err != nil {
		return fmt.Errorf("remove mining media: %w", err)
	}
	if err := requireOneRow(result, "remove mining media"); err != nil {
		return err
	}
	if err := media.CollectUnusedInTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mining media removal: %w", err)
	}
	return nil
}
