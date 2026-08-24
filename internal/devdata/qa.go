package devdata

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strconv"
	"time"

	"github.com/tomasmik/goi/internal/examples"
	"github.com/tomasmik/goi/internal/media"
	"github.com/tomasmik/goi/internal/mining"
	"github.com/tomasmik/goi/internal/srs"
	"github.com/tomasmik/goi/internal/textnorm"
	"github.com/tomasmik/goi/internal/vocabulary"
)

type qaCaptureInput struct {
	rawText                string
	expression             string
	contextText            string
	sourceKind             mining.SourceKind
	sourceTitle            string
	sourceURL              string
	sourcePositionMS       *int64
	suggestedEntrySequence *int64
	status                 mining.Status
	vocabularyID           *int64
}

func populateQA(ctx context.Context, db *sql.DB, tx *sql.Tx, baseIDs map[string]int64, now time.Time) error {
	consciousnessID, err := insertQAVocabulary(ctx, db, tx, now)
	if err != nil {
		return err
	}
	if err := activateWord(ctx, tx, consciousnessID, srs.StageOne, now.Add(-24*time.Hour), now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE vocabulary
		SET lesson_completed_at = ?
		WHERE id = ?`, now.Add(-48*time.Hour).Unix(), consciousnessID); err != nil {
		return fmt.Errorf("set QA synonym word history: %w", err)
	}

	largeID, err := qaVocabularyID(baseIDs, "大きい")
	if err != nil {
		return err
	}
	smallID, err := qaVocabularyID(baseIDs, "小さい")
	if err != nil {
		return err
	}
	exampleStore := examples.NewStore(db)
	if _, err := exampleStore.CreateInTx(ctx, tx, largeID, examples.Input{
		Origin:        examples.OriginManual,
		Sentence:      "この犬は大きい。",
		Translation:   "This dog is big.",
		TargetSurface: "大きい",
	}); err != nil {
		return fmt.Errorf("create manual QA example: %w", err)
	}
	if _, err := exampleStore.CreateInTx(ctx, tx, smallID, examples.Input{
		Origin:        examples.OriginGenerated,
		Sentence:      "小さい猫が窓のそばにいる。",
		Translation:   "A small cat is by the window.",
		TargetSurface: "小さい",
		Provider:      "QA fixture",
		Model:         "deterministic",
	}); err != nil {
		return fmt.Errorf("create generated QA example: %w", err)
	}

	if err := insertQACaptures(ctx, db, tx, baseIDs, now); err != nil {
		return err
	}
	return nil
}

func insertQAVocabulary(ctx context.Context, db *sql.DB, tx *sql.Tx, now time.Time) (int64, error) {
	store := vocabulary.NewStore(db)
	words := []struct {
		expression     string
		pronunciation  string
		meaning        string
		knownElsewhere bool
	}{
		{expression: "意識", pronunciation: "いしき", meaning: "consciousness"},
		{expression: "日本語", pronunciation: "にほんご", meaning: "Japanese language", knownElsewhere: true},
		{expression: "勉強する", pronunciation: "べんきょうする", meaning: "to study", knownElsewhere: true},
		{expression: "気をつける", pronunciation: "きをつける", meaning: "to be careful", knownElsewhere: true},
	}

	var consciousnessID int64
	for _, word := range words {
		id, err := store.CreateInTx(ctx, tx, vocabulary.CreateInput{
			Expression:    word.expression,
			Pronunciation: word.pronunciation,
			Meanings:      []string{word.meaning},
			SourceLabel:   "devdata",
		})
		if err != nil {
			return 0, fmt.Errorf("create QA vocabulary %q: %w", word.expression, err)
		}
		if word.expression == "意識" {
			consciousnessID = id
		}
		if !word.knownElsewhere {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE vocabulary
			SET known_elsewhere_at = ?, updated_at = ?,
			    content_revision = content_revision + 1
			WHERE id = ?`, now.Unix(), now.Unix(), id); err != nil {
			return 0, fmt.Errorf("mark QA vocabulary %q as known elsewhere: %w", word.expression, err)
		}
	}
	return consciousnessID, nil
}

func insertQACaptures(ctx context.Context, db *sql.DB, tx *sql.Tx, baseIDs map[string]int64, now time.Time) error {
	_, err := insertQACapture(ctx, tx, 1, now.Add(-5*time.Minute), qaCaptureInput{
		rawText:     "見つけた",
		expression:  "見つける",
		contextText: "図書館で面白い本を見つけた。",
		sourceKind:  mining.SourceManual,
		status:      mining.StatusPending,
	})
	if err != nil {
		return err
	}
	suggestedEntrySequence := int64(1579510)
	_, err = insertQACapture(ctx, tx, 2, now.Add(-4*time.Minute), qaCaptureInput{
		rawText:                "生",
		expression:             "生",
		contextText:            "生の魚を食べる。",
		sourceKind:             mining.SourceWeb,
		sourceTitle:            "QA Japanese reading",
		sourceURL:              "https://example.com/japanese/food",
		suggestedEntrySequence: &suggestedEntrySequence,
		status:                 mining.StatusPending,
	})
	if err != nil {
		return err
	}
	videoPosition := int64(12_500)
	_, err = insertQACapture(ctx, tx, 3, now.Add(-3*time.Minute), qaCaptureInput{
		rawText:          "食べる",
		expression:       "食べる",
		contextText:      "朝ご飯にパンを食べる。",
		sourceKind:       mining.SourceVideo,
		sourceTitle:      "QA subtitle fixture",
		sourceURL:        "https://www.youtube.com/watch?v=qa-fixture1",
		sourcePositionMS: &videoPosition,
		status:           mining.StatusPending,
	})
	if err != nil {
		return err
	}
	acceptedPosition := int64(42_000)
	acceptedVocabularyID, err := qaVocabularyID(baseIDs, "読む")
	if err != nil {
		return err
	}
	acceptedID, err := insertQACapture(ctx, tx, 4, now.Add(-2*time.Minute), qaCaptureInput{
		rawText:          "読む",
		expression:       "読む",
		contextText:      "静かな部屋で本を読む。",
		sourceKind:       mining.SourceVideo,
		sourceTitle:      "QA local video media fixture",
		sourcePositionMS: &acceptedPosition,
		status:           mining.StatusAccepted,
		vocabularyID:     &acceptedVocabularyID,
	})
	if err != nil {
		return err
	}
	if err := attachQAMedia(ctx, tx, acceptedID, now); err != nil {
		return err
	}
	if _, err := examples.NewStore(db).CreateInTx(ctx, tx, acceptedVocabularyID, examples.Input{
		MiningCaptureID:  &acceptedID,
		Origin:           examples.OriginMined,
		Sentence:         "静かな部屋で本を読む。",
		Translation:      "I read a book in a quiet room.",
		TargetSurface:    "読む",
		SourceTitle:      "QA local video media fixture",
		SourcePositionMS: &acceptedPosition,
	}); err != nil {
		return fmt.Errorf("create mined QA example: %w", err)
	}

	_, err = insertQACapture(ctx, tx, 5, now.Add(-time.Minute), qaCaptureInput{
		rawText:     "忘れる",
		expression:  "忘れる",
		contextText: "大切な約束を忘れない。",
		sourceKind:  mining.SourceEbook,
		sourceTitle: "QA graded reader",
		sourceURL:   "https://example.com/reader/chapter-1",
		status:      mining.StatusDiscarded,
	})
	return err
}

func insertQACapture(ctx context.Context, tx *sql.Tx, sequence int, createdAt time.Time, input qaCaptureInput) (int64, error) {
	switch input.status {
	case mining.StatusPending, mining.StatusDiscarded:
		if input.vocabularyID != nil {
			return 0, fmt.Errorf("QA %s capture %q cannot reference vocabulary", input.status, input.expression)
		}
	case mining.StatusAccepted:
		if input.vocabularyID == nil {
			return 0, fmt.Errorf("QA accepted capture %q requires vocabulary", input.expression)
		}
	default:
		return 0, fmt.Errorf("QA capture %q has invalid status %q", input.expression, input.status)
	}
	nonce := fmt.Sprintf("%032x", sequence)
	result, err := tx.ExecContext(ctx, `
		INSERT INTO mining_captures (
			raw_text, expression, normalized_expression, context_text,
			source_kind, source_title, source_url, source_position_ms,
			suggested_entry_sequence, capture_nonce, request_hash, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.rawText, input.expression, textnorm.Normalize(input.expression), input.contextText,
		string(input.sourceKind), input.sourceTitle, input.sourceURL, input.sourcePositionMS,
		input.suggestedEntrySequence, nonce, qaCaptureRequestHash(input), createdAt.Unix())
	if err != nil {
		return 0, fmt.Errorf("insert QA mining capture %q: %w", input.expression, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get QA mining capture ID: %w", err)
	}
	if input.status != mining.StatusPending {
		result, err := tx.ExecContext(ctx, `
			UPDATE mining_captures
			SET status = ?, vocabulary_id = ?, revision = 2
			WHERE id = ? AND status = 'pending' AND revision = 1`,
			string(input.status), input.vocabularyID, id)
		if err != nil {
			return 0, fmt.Errorf("transition QA mining capture %q: %w", input.expression, err)
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			if err != nil {
				return 0, fmt.Errorf("check QA mining transition: %w", err)
			}
			return 0, fmt.Errorf("transition QA mining capture %q: capture changed", input.expression)
		}
	}
	return id, nil
}

func qaCaptureRequestHash(input qaCaptureInput) string {
	fields := []string{
		input.rawText,
		input.expression,
		input.contextText,
		string(input.sourceKind),
		input.sourceTitle,
		input.sourceURL,
	}
	if input.sourcePositionMS == nil {
		fields = append(fields, "-")
	} else {
		fields = append(fields, strconv.FormatInt(*input.sourcePositionMS, 10))
	}
	if input.suggestedEntrySequence == nil {
		fields = append(fields, "-")
	} else {
		fields = append(fields, strconv.FormatInt(*input.suggestedEntrySequence, 10))
	}
	hash := sha256.New()
	for _, field := range fields {
		hash.Write([]byte(strconv.Itoa(len(field))))
		hash.Write([]byte{':'})
		hash.Write([]byte(field))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func qaVocabularyID(ids map[string]int64, expression string) (int64, error) {
	id, ok := ids[expression]
	if !ok {
		return 0, fmt.Errorf("QA base vocabulary %q not found", expression)
	}
	return id, nil
}

func attachQAMedia(ctx context.Context, tx *sql.Tx, captureID int64, now time.Time) error {
	frame, err := qaFrameUpload()
	if err != nil {
		return err
	}
	audio, err := qaAudioUpload()
	if err != nil {
		return err
	}
	attachments := []struct {
		purpose string
		upload  media.Upload
	}{
		{purpose: "video_frame", upload: frame},
		{purpose: "sentence_audio", upload: audio},
	}
	for _, attachment := range attachments {
		mediaID, err := media.SaveInTx(ctx, tx, attachment.upload, now)
		if err != nil {
			return fmt.Errorf("save QA %s: %w", attachment.purpose, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO mining_capture_media (capture_id, purpose, media_id)
			VALUES (?, ?, ?)`, captureID, attachment.purpose, mediaID); err != nil {
			return fmt.Errorf("attach QA %s: %w", attachment.purpose, err)
		}
	}
	return nil
}

func qaFrameUpload() (media.Upload, error) {
	frame := image.NewRGBA(image.Rect(0, 0, 320, 180))
	draw.Draw(frame, frame.Bounds(), image.NewUniform(color.RGBA{R: 38, G: 52, B: 79, A: 255}), image.Point{}, draw.Src)
	draw.Draw(frame, image.Rect(0, 120, 320, 180), image.NewUniform(color.RGBA{R: 235, G: 96, B: 80, A: 255}), image.Point{}, draw.Src)
	draw.Draw(frame, image.Rect(120, 50, 200, 130), image.NewUniform(color.RGBA{R: 35, G: 138, B: 141, A: 255}), image.Point{}, draw.Src)

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, frame); err != nil {
		return media.Upload{}, fmt.Errorf("encode QA video frame: %w", err)
	}
	upload, err := media.Prepare(media.KindImage, "qa-frame.png", encoded.Bytes())
	if err != nil {
		return media.Upload{}, fmt.Errorf("prepare QA video frame: %w", err)
	}
	return upload, nil
}

func qaAudioUpload() (media.Upload, error) {
	const (
		sampleRate = 16_000
		duration   = 400 * time.Millisecond
		frequency  = 440.0
	)
	sampleCount := int(float64(sampleRate) * duration.Seconds())
	dataSize := sampleCount * 2

	var encoded bytes.Buffer
	encoded.WriteString("RIFF")
	if err := binary.Write(&encoded, binary.LittleEndian, uint32(36+dataSize)); err != nil {
		return media.Upload{}, fmt.Errorf("encode QA audio size: %w", err)
	}
	encoded.WriteString("WAVEfmt ")
	format := struct {
		Size          uint32
		AudioFormat   uint16
		Channels      uint16
		SampleRate    uint32
		ByteRate      uint32
		BlockAlign    uint16
		BitsPerSample uint16
	}{
		Size:          16,
		AudioFormat:   1,
		Channels:      1,
		SampleRate:    sampleRate,
		ByteRate:      sampleRate * 2,
		BlockAlign:    2,
		BitsPerSample: 16,
	}
	if err := binary.Write(&encoded, binary.LittleEndian, format); err != nil {
		return media.Upload{}, fmt.Errorf("encode QA audio header: %w", err)
	}
	encoded.WriteString("data")
	if err := binary.Write(&encoded, binary.LittleEndian, uint32(dataSize)); err != nil {
		return media.Upload{}, fmt.Errorf("encode QA audio data size: %w", err)
	}
	samples := make([]int16, sampleCount)
	for index := range samples {
		samples[index] = int16(5_000 * math.Sin(2*math.Pi*frequency*float64(index)/sampleRate))
	}
	if err := binary.Write(&encoded, binary.LittleEndian, samples); err != nil {
		return media.Upload{}, fmt.Errorf("encode QA audio samples: %w", err)
	}

	upload, err := media.Prepare(media.KindAudio, "qa-tone.wav", encoded.Bytes())
	if err != nil {
		return media.Upload{}, fmt.Errorf("prepare QA audio: %w", err)
	}
	return upload, nil
}
