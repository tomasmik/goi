package examples

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tomasmik/goi/internal/database"
)

func TestStoreCRUDScopesExamplesToVocabulary(t *testing.T) {
	ctx, db := openExamplesTestDatabase(t)
	vocabularyID := insertVocabulary(t, db, "食べる")
	otherVocabularyID := insertVocabulary(t, db, "飲む")
	position := int64(62_500)

	store := NewStore(db)
	example, err := store.Create(ctx, vocabularyID, Input{
		Sentence:         "  昨日、寿司を食べました。\r\n",
		Translation:      " I ate sushi yesterday. ",
		TargetSurface:    "食べました",
		SourceTitle:      " A lesson ",
		SourceURL:        "https://user:secret@www.youtube.com/watch?v=abc",
		SourcePositionMS: &position,
	})
	if err != nil {
		t.Fatal(err)
	}
	if example.Origin != OriginManual || example.Sentence != "昨日、寿司を食べました。" {
		t.Fatalf("created example = %#v", example)
	}
	if example.SourceURL != "https://www.youtube.com/watch?v=abc" || example.SourcePositionLabel != "1:02" {
		t.Fatalf("created source = URL %q, label %q", example.SourceURL, example.SourcePositionLabel)
	}
	link, err := url.Parse(example.SourceLink)
	if err != nil {
		t.Fatal(err)
	}
	if link.Query().Get("v") != "abc" || link.Query().Get("t") != "62s" {
		t.Fatalf("source link = %q", example.SourceLink)
	}
	if !example.HasTarget || example.BeforeTarget != "昨日、寿司を" || example.MatchedTarget != "食べました" || example.AfterTarget != "。" {
		t.Fatalf("target split = (%q, %q, %q, %t)", example.BeforeTarget, example.MatchedTarget, example.AfterTarget, example.HasTarget)
	}

	items, err := store.List(ctx, vocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != example.ID {
		t.Fatalf("listed examples = %#v", items)
	}

	if _, err := store.Update(ctx, otherVocabularyID, example.ID, Input{Sentence: "違う例。"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wrong-owner update error = %v, want sql.ErrNoRows", err)
	}
	updated, err := store.Update(ctx, vocabularyID, example.ID, Input{
		Sentence:      "毎朝パンを食べる。",
		Translation:   "I eat bread every morning.",
		TargetSurface: "食べる",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Sentence != "毎朝パンを食べる。" || !updated.HasTarget {
		t.Fatalf("updated example = %#v", updated)
	}
	if err := store.Delete(ctx, otherVocabularyID, example.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wrong-owner delete error = %v, want sql.ErrNoRows", err)
	}
	if err := store.Delete(ctx, vocabularyID, example.ID); err != nil {
		t.Fatal(err)
	}
	items, err = store.List(ctx, vocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("examples after delete = %#v", items)
	}
}

func TestPreferredUsesCuratedExamplesBeforeGeneratedFallbacks(t *testing.T) {
	ctx, db := openExamplesTestDatabase(t)
	vocabularyID := insertVocabulary(t, db, "意識")
	store := NewStore(db)

	generated, err := store.Create(ctx, vocabularyID, Input{
		Origin:      OriginGenerated,
		Sentence:    "意識を集中させてください。",
		Provider:    "local",
		Model:       "small-ja",
		Translation: "Please concentrate.",
	})
	if err != nil {
		t.Fatal(err)
	}
	preferred, err := store.Preferred(ctx, vocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	if preferred.ID != generated.ID {
		t.Fatalf("preferred generated ID = %d, want %d", preferred.ID, generated.ID)
	}

	manual, err := store.Create(ctx, vocabularyID, Input{Sentence: "彼は意識を失った。", TargetSurface: "意識"})
	if err != nil {
		t.Fatal(err)
	}
	preferred, err = store.Preferred(ctx, vocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	if preferred.ID != manual.ID {
		t.Fatalf("preferred manual ID = %d, want %d", preferred.ID, manual.ID)
	}

	if _, err := db.Exec("DELETE FROM vocabulary WHERE id = ?", vocabularyID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Preferred(ctx, vocabularyID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted vocabulary preferred error = %v, want sql.ErrNoRows", err)
	}
	if _, err := store.Create(ctx, vocabularyID, Input{Sentence: "追加の例。"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted vocabulary creation error = %v, want sql.ErrNoRows", err)
	}
	if err := store.Delete(ctx, vocabularyID, generated.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted vocabulary example deletion error = %v, want sql.ErrNoRows", err)
	}
}

func TestCreateGeneratedIfEmptyDoesNotReplaceExistingContext(t *testing.T) {
	ctx, db := openExamplesTestDatabase(t)
	vocabularyID := insertVocabulary(t, db, "読む")
	store := NewStore(db)
	manual, err := store.Create(ctx, vocabularyID, Input{Sentence: "本を読む。", TargetSurface: "読む"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.CreateGeneratedIfEmpty(ctx, vocabularyID, 1, Input{
		Sentence:      "新聞を読みます。",
		Translation:   "I read a newspaper.",
		TargetSurface: "読みます",
		Provider:      "local",
		Model:         "small-ja",
	})
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("CreateGeneratedIfEmpty() error = %v, want ErrAlreadyExists", err)
	}
	items, err := store.List(ctx, vocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != manual.ID {
		t.Fatalf("examples = %#v, want only manual example %d", items, manual.ID)
	}

	otherVocabularyID := insertVocabulary(t, db, "書く")
	generated, err := store.CreateGeneratedIfEmpty(ctx, otherVocabularyID, 1, Input{
		Sentence:      "名前を書きます。",
		Translation:   "I write my name.",
		TargetSurface: "書きます",
		Provider:      "local",
		Model:         "small-ja",
	})
	if err != nil {
		t.Fatal(err)
	}
	if generated.Origin != OriginGenerated || generated.Model != "small-ja" {
		t.Fatalf("generated example = %#v", generated)
	}
}

func TestCreateGeneratedIfEmptyRejectsChangedVocabulary(t *testing.T) {
	ctx, db := openExamplesTestDatabase(t)
	vocabularyID := insertVocabulary(t, db, "読む")
	if _, err := db.Exec(`
		UPDATE vocabulary
		SET expression = '読み込む', normalized_expression = '読み込む',
		    content_revision = content_revision + 1
		WHERE id = ?`, vocabularyID); err != nil {
		t.Fatal(err)
	}

	_, err := NewStore(db).CreateGeneratedIfEmpty(ctx, vocabularyID, 1, Input{
		Sentence:      "本を読みます。",
		Translation:   "I read a book.",
		TargetSurface: "読みます",
		Provider:      "local",
		Model:         "small-ja",
	})
	if !errors.Is(err, ErrVocabularyChanged) {
		t.Fatalf("CreateGeneratedIfEmpty() error = %v, want ErrVocabularyChanged", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary_examples WHERE vocabulary_id = ?", vocabularyID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("generated example count = %d, want 0", count)
	}
}

func TestMinedExampleIsUniqueAndFollowsCaptureDeletion(t *testing.T) {
	ctx, db := openExamplesTestDatabase(t)
	vocabularyID := insertVocabulary(t, db, "猫")
	otherVocabularyID := insertVocabulary(t, db, "犬")
	captureID := insertAcceptedCapture(t, db, vocabularyID, "猫を見た。")
	store := NewStore(db)
	if _, err := store.Create(ctx, otherVocabularyID, Input{
		MiningCaptureID: &captureID,
		Origin:          OriginMined,
		Sentence:        "猫を見た。",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("wrong-owner mining capture error = %v, want ErrInvalidInput", err)
	}

	example, err := store.Create(ctx, vocabularyID, Input{
		MiningCaptureID: &captureID,
		Origin:          OriginMined,
		Sentence:        "猫を見た。",
		TargetSurface:   "猫",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, vocabularyID, Input{
		MiningCaptureID: &captureID,
		Origin:          OriginMined,
		Sentence:        "同じ捕獲。",
	}); err == nil {
		t.Fatal("duplicate mining capture example succeeded")
	}
	if _, err := db.Exec("DELETE FROM mining_captures WHERE id = ?", captureID); err != nil {
		t.Fatal(err)
	}
	items, err := store.List(ctx, vocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("examples after capture deletion = %#v; created ID %d", items, example.ID)
	}
}

func TestEditingMinedExampleDetachesMediaOnlyWhenSentenceChanges(t *testing.T) {
	ctx, db := openExamplesTestDatabase(t)
	vocabularyID := insertVocabulary(t, db, "猫")
	captureID := insertAcceptedCapture(t, db, vocabularyID, "猫を見た。")
	audioID := attachCaptureMedia(t, db, captureID, "sentence_audio", "audio", "audio/webm", "b")
	frameID := attachCaptureMedia(t, db, captureID, "video_frame", "image", "image/jpeg", "c")
	store := NewStore(db)
	example, err := store.Create(ctx, vocabularyID, Input{
		MiningCaptureID: &captureID,
		Origin:          OriginMined,
		Sentence:        "猫を見た。",
		TargetSurface:   "猫",
	})
	if err != nil {
		t.Fatal(err)
	}

	unchanged, err := store.Update(ctx, vocabularyID, example.ID, Input{
		MiningCaptureID: &captureID,
		Origin:          OriginMined,
		Sentence:        "  猫を見た。  ",
		Translation:     "I saw a cat.",
		TargetSurface:   "猫",
	})
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.MiningCaptureID == nil || *unchanged.MiningCaptureID != captureID || unchanged.Origin != OriginMined {
		t.Fatalf("unchanged example association = capture %v, origin %q", unchanged.MiningCaptureID, unchanged.Origin)
	}
	if unchanged.SentenceAudioID != audioID || unchanged.VideoFrameID != frameID {
		t.Fatalf("unchanged example media = audio %d, frame %d; want %d and %d", unchanged.SentenceAudioID, unchanged.VideoFrameID, audioID, frameID)
	}

	changed, err := store.Update(ctx, vocabularyID, example.ID, Input{
		MiningCaptureID: &captureID,
		Origin:          OriginMined,
		Sentence:        "猫を二匹見た。",
		Translation:     "I saw two cats.",
		TargetSurface:   "猫",
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed.MiningCaptureID != nil || changed.Origin != OriginManual {
		t.Fatalf("changed example association = capture %v, origin %q", changed.MiningCaptureID, changed.Origin)
	}
	if changed.SentenceAudioID != 0 || changed.VideoFrameID != 0 {
		t.Fatalf("changed example media = audio %d, frame %d; want none", changed.SentenceAudioID, changed.VideoFrameID)
	}
}

func TestCreateInTxRollsBackWithCallerTransaction(t *testing.T) {
	ctx, db := openExamplesTestDatabase(t)
	vocabularyID := insertVocabulary(t, db, "本")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(db).CreateInTx(ctx, tx, vocabularyID, Input{Sentence: "本を読む。"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	items, err := NewStore(db).List(ctx, vocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("rolled-back examples = %#v", items)
	}
}

func TestValidationRejectsInvalidExamples(t *testing.T) {
	ctx, db := openExamplesTestDatabase(t)
	vocabularyID := insertVocabulary(t, db, "犬")
	captureID := int64(10)
	negative := int64(-1)
	tests := []Input{
		{},
		{Sentence: "犬です。", Origin: "unknown"},
		{Sentence: "犬です。", Origin: OriginMined},
		{Sentence: "犬です。", MiningCaptureID: &captureID},
		{Sentence: "犬です。", SourceURL: "javascript:alert(1)"},
		{Sentence: "犬です。", SourcePositionMS: &negative},
		{Sentence: strings.Repeat("犬", maxSentenceRunes+1)},
		{Sentence: string([]byte{0xff})},
	}
	for index, input := range tests {
		if _, err := NewStore(db).Create(ctx, vocabularyID, input); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("case %d error = %v, want ErrInvalidInput", index, err)
		}
	}
	if _, err := NewStore(db).Create(ctx, 0, Input{Sentence: "犬です。"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid vocabulary error = %v, want ErrInvalidInput", err)
	}
}

func openExamplesTestDatabase(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "examples.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return ctx, db
}

func insertVocabulary(t *testing.T, db *sql.DB, expression string) int64 {
	t.Helper()
	result, err := db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, created_at, updated_at)
		VALUES (?, ?, 1, 1)`, expression, expression)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertAcceptedCapture(t *testing.T, db *sql.DB, vocabularyID int64, contextText string) int64 {
	t.Helper()
	result, err := db.Exec(`
		INSERT INTO mining_captures (
			raw_text, expression, normalized_expression, context_text, source_kind,
			capture_nonce, request_hash, status, vocabulary_id,
			created_at
		) VALUES ('猫', '猫', '猫', ?, 'manual', ?, ?, 'accepted', ?, 1)`,
		contextText, strings.Repeat("1", 32), strings.Repeat("a", 64), vocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func attachCaptureMedia(t *testing.T, db *sql.DB, captureID int64, purpose, kind, mimeType, checksumCharacter string) int64 {
	t.Helper()
	result, err := db.Exec(
		"INSERT INTO media (kind, mime_type, sha256, created_at) VALUES (?, ?, ?, 1)",
		kind, mimeType, strings.Repeat(checksumCharacter, 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO media_content (media_id, content) VALUES (?, ?)", mediaID, []byte(kind)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO mining_capture_media (capture_id, purpose, media_id) VALUES (?, ?, ?)",
		captureID, purpose, mediaID,
	); err != nil {
		t.Fatal(err)
	}
	return mediaID
}
