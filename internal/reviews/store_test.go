package reviews

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomasmik/goi/internal/database"
	"github.com/tomasmik/goi/internal/examples"
	"github.com/tomasmik/goi/internal/srs"
	"github.com/tomasmik/goi/internal/vocabulary"
)

func TestReviewStateLoadsPreferredExample(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	var vocabularyID int64
	if err := db.QueryRow("SELECT id FROM vocabulary").Scan(&vocabularyID); err != nil {
		t.Fatal(err)
	}
	created, err := examples.NewStore(db).Create(ctx, vocabularyID, examples.Input{
		Origin:        examples.OriginManual,
		Sentence:      "昨日、寿司を食べた。",
		Translation:   "I ate sushi yesterday.",
		TargetSurface: "食べた",
	})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Example.ID != created.ID || !state.Example.HasTarget {
		t.Fatalf("review example = %+v, want example %d with highlighted target", state.Example, created.ID)
	}
}

func TestReviewStateLoadsAttachedPicture(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	var vocabularyID int64
	if err := db.QueryRow("SELECT id FROM vocabulary").Scan(&vocabularyID); err != nil {
		t.Fatal(err)
	}
	pictureID := attachReviewTestPicture(t, db, vocabularyID)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.PictureID != pictureID {
		t.Fatalf("review picture = %d, want %d", state.PictureID, pictureID)
	}
}

func attachReviewTestPicture(t *testing.T, db *sql.DB, vocabularyID int64) int64 {
	t.Helper()
	result, err := db.Exec(
		"INSERT INTO media (kind, mime_type, sha256, created_at) VALUES ('image', 'image/png', ?, 1)",
		strings.Repeat("c", 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO media_content (media_id, content) VALUES (?, ?)", mediaID, []byte("image")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO vocabulary_media (vocabulary_id, purpose, media_id) VALUES (?, 'picture', ?)",
		vocabularyID,
		mediaID,
	); err != nil {
		t.Fatal(err)
	}
	return mediaID
}

func TestCorrectAnswerWaitsForConfirmation(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}

	if result, err := store.CheckAnswer(ctx, sessionID, before.PromptID, "wrong"); err != nil || result != Incorrect {
		t.Fatalf("wrong CheckAnswer() = %q, %v", result, err)
	}
	if result, err := store.CheckAnswer(ctx, sessionID, before.PromptID, answerForState(before)); err != nil || result == Incorrect {
		t.Fatalf("correct CheckAnswer() = %q, %v", result, err)
	}
	afterCheck, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if afterCheck.PromptID != before.PromptID || afterCheck.Attempts != 0 {
		t.Fatalf("answer check advanced prompt: before %+v, after %+v", before, afterCheck)
	}
	if _, err := store.ConfirmAnswer(ctx, sessionID, before.PromptID, answerForState(before)); err != nil {
		t.Fatal(err)
	}
	afterConfirm, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if afterConfirm.PromptID == before.PromptID {
		t.Fatal("confirmed answer did not advance the prompt")
	}
	var attempts int
	var promptStatus string
	if err := db.QueryRow("SELECT attempt_count, status FROM review_prompts WHERE id = ?", before.PromptID).Scan(&attempts, &promptStatus); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || promptStatus != "passed" {
		t.Fatalf("confirmed prompt = %d attempts, status %q; want 1/passed", attempts, promptStatus)
	}
}

func TestConfirmAnswerRejectsIncorrectAnswer(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.ConfirmAnswer(ctx, sessionID, state.PromptID, "wrong")
	if err == nil || !strings.Contains(err.Error(), "no longer correct") {
		t.Fatalf("ConfirmAnswer() error = %v", err)
	}
	var attempts int
	if err := db.QueryRow("SELECT attempt_count FROM review_prompts WHERE id = ?", state.PromptID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("rejected confirmation recorded %d attempts", attempts)
	}
}

func TestAddSynonymAcceptsTheRejectedMeaningFromTheSamePrompt(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state := moveToPromptType(t, ctx, store, sessionID, "meaning")
	promptID := state.PromptID
	vocabularyID := state.VocabularyID
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "  consume  "); err != nil {
		t.Fatal(err)
	}

	state, err = store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.RejectedAnswer != "consume" || state.Attempts != 1 {
		t.Fatalf("rejected answer = %q after %d attempts, want consume after 1", state.RejectedAnswer, state.Attempts)
	}
	var rejectedRevision int64
	if err := db.QueryRow(`
		SELECT last_incorrect_content_revision
		FROM review_prompts
		WHERE id = ?`, state.PromptID).Scan(&rejectedRevision); err != nil {
		t.Fatal(err)
	}
	if rejectedRevision != 1 {
		t.Fatalf("rejected content revision = %d, want 1", rejectedRevision)
	}
	added, err := store.AddSynonym(ctx, sessionID, state.PromptID, state.RejectedAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if added != "consume" {
		t.Fatalf("added synonym = %q, want consume", added)
	}

	state, err = store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "completed" {
		t.Fatalf("review status after synonym = %q, want completed", state.Status)
	}
	var rejectedAnswer string
	var promptStatus string
	var attempts int
	if err := db.QueryRow(`
		SELECT last_incorrect_answer, last_incorrect_content_revision, status, attempt_count
		FROM review_prompts
		WHERE id = ?`, promptID).Scan(&rejectedAnswer, &rejectedRevision, &promptStatus, &attempts); err != nil {
		t.Fatal(err)
	}
	if rejectedAnswer != "" || rejectedRevision != 0 || promptStatus != "passed" || attempts != 2 {
		t.Fatalf("corrected prompt = rejected %q at revision %d, status %q, attempts %d", rejectedAnswer, rejectedRevision, promptStatus, attempts)
	}
	var firstAttemptCorrect, promptCount int
	if err := db.QueryRow(`
		SELECT first_attempt_correct_count, prompt_count
		FROM review_results
		WHERE session_item_id = (SELECT session_item_id FROM review_prompts WHERE id = ?)`, promptID,
	).Scan(&firstAttemptCorrect, &promptCount); err != nil {
		t.Fatal(err)
	}
	if firstAttemptCorrect != 1 || promptCount != 2 {
		t.Fatalf("corrected review accuracy = %d/%d, want 1/2", firstAttemptCorrect, promptCount)
	}
	var text, normalized string
	var revision int64
	if err := db.QueryRow(`
		SELECT text, normalized_text
		FROM meanings
		WHERE vocabulary_id = ? AND position = 1`, vocabularyID).Scan(&text, &normalized); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT content_revision FROM vocabulary WHERE id = ?", vocabularyID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if text != "consume" || normalized != "consume" || revision != 2 {
		t.Fatalf("stored synonym = %q/%q, revision %d", text, normalized, revision)
	}
}

func TestPunctuationOnlyAnswerIsNotOfferedAsASynonym(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state := moveToPromptType(t, ctx, store, sessionID, "meaning")
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "！！！"); err != nil {
		t.Fatal(err)
	}

	state, err = store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.RejectedAnswer != "" {
		t.Fatalf("rejected punctuation answer = %q, want empty", state.RejectedAnswer)
	}
}

func TestAddSynonymRejectsTheAnswerFromAStaleReviewTab(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state := moveToPromptType(t, ctx, store, sessionID, "meaning")
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "consume"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "devour"); err != nil {
		t.Fatal(err)
	}

	if _, err := store.AddSynonym(ctx, sessionID, state.PromptID, "consume"); err == nil ||
		!strings.Contains(err.Error(), "rejected answer has changed") {
		t.Fatalf("stale rejected answer error = %v", err)
	}

	var meaningCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM meanings WHERE vocabulary_id = ?", state.VocabularyID).Scan(&meaningCount); err != nil {
		t.Fatal(err)
	}
	if meaningCount != 1 {
		t.Fatalf("meanings after stale submission = %d, want 1", meaningCount)
	}
	var rejectedAnswer string
	if err := db.QueryRow("SELECT last_incorrect_answer FROM review_prompts WHERE id = ?", state.PromptID).Scan(&rejectedAnswer); err != nil {
		t.Fatal(err)
	}
	if rejectedAnswer != "devour" {
		t.Fatalf("latest rejected answer = %q, want devour", rejectedAnswer)
	}
}

func TestAddSynonymFromFinalCorrectionChangesTheResultToSuccess(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	if _, err := db.Exec("INSERT INTO user_settings (id, retry_count) VALUES (1, 1)"); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state := moveToPromptType(t, ctx, store, sessionID, "meaning")
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "consume"); err != nil {
		t.Fatal(err)
	}
	state, err = store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Feedback || state.RejectedAnswer != "consume" {
		t.Fatalf("final correction state = feedback %t, rejected %q", state.Feedback, state.RejectedAnswer)
	}
	if _, err := store.AddSynonym(ctx, sessionID, state.PromptID, state.RejectedAnswer); err != nil {
		t.Fatal(err)
	}

	var outcome string
	if err := db.QueryRow("SELECT outcome FROM review_results").Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "success" {
		t.Fatalf("review outcome after adding synonym = %q, want success", outcome)
	}
}

func TestAddSynonymRejectsVocabularyEditedAfterTheWrongAnswer(t *testing.T) {
	for _, test := range []struct {
		name         string
		finalFailure bool
	}{
		{name: "retry"},
		{name: "final correction", finalFailure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, db := openReviewTestDatabase(t)
			insertDueVocabulary(t, db)
			if test.finalFailure {
				if _, err := db.Exec("INSERT INTO user_settings (id, retry_count) VALUES (1, 1)"); err != nil {
					t.Fatal(err)
				}
			}
			store := NewStore(db)
			sessionID, err := store.StartNormal(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			state := moveToPromptType(t, ctx, store, sessionID, "meaning")
			if _, err := store.Answer(ctx, sessionID, state.PromptID, "consume"); err != nil {
				t.Fatal(err)
			}

			vocabularyStore := vocabulary.NewStore(db)
			item, err := vocabularyStore.Get(ctx, state.VocabularyID)
			if err != nil {
				t.Fatal(err)
			}
			if err := vocabularyStore.Update(ctx, item.ID, item.ContentRevision, vocabulary.CreateInput{
				Expression:    item.Expression,
				Pronunciation: item.Pronunciation,
				Meanings:      []string{"to dine"},
			}); err != nil {
				t.Fatal(err)
			}

			state, err = store.State(ctx, sessionID)
			if err != nil {
				t.Fatal(err)
			}
			if state.RejectedAnswer != "consume" || state.Feedback != test.finalFailure {
				t.Fatalf("stale review state = rejected %q, feedback %t", state.RejectedAnswer, state.Feedback)
			}
			if _, err := store.AddSynonym(ctx, sessionID, state.PromptID, state.RejectedAnswer); err == nil || !strings.Contains(err.Error(), "word changed") {
				t.Fatalf("stale synonym error = %v", err)
			}

			var meaning, rejectedAnswer string
			var contentRevision, rejectedRevision int64
			if err := db.QueryRow(`
				SELECT v.content_revision, m.text,
				       rp.last_incorrect_answer, rp.last_incorrect_content_revision
				FROM vocabulary v
				JOIN meanings m ON m.vocabulary_id = v.id
				JOIN review_session_items rsi ON rsi.vocabulary_id = v.id
				JOIN review_prompts rp ON rp.session_item_id = rsi.id
				WHERE v.id = ? AND rp.id = ?`, state.VocabularyID, state.PromptID).Scan(
				&contentRevision, &meaning, &rejectedAnswer, &rejectedRevision,
			); err != nil {
				t.Fatal(err)
			}
			if contentRevision != item.ContentRevision+1 || meaning != "to dine" ||
				rejectedAnswer != "consume" || rejectedRevision != item.ContentRevision {
				t.Fatalf(
					"stale rejection changed data: revision %d, meaning %q, answer %q/%d",
					contentRevision, meaning, rejectedAnswer, rejectedRevision,
				)
			}
		})
	}
}

func TestAddSynonymHandlesConcurrentSubmissionsOnce(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state := moveToPromptType(t, ctx, store, sessionID, "meaning")
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "consume"); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := store.AddSynonym(ctx, sessionID, state.PromptID, "consume")
			results <- err
		}()
	}
	close(start)
	succeeded := 0
	for range 2 {
		if err := <-results; err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful concurrent synonym additions = %d, want 1", succeeded)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM meanings WHERE vocabulary_id = ?", state.VocabularyID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("meanings after concurrent additions = %d, want 2", count)
	}
}

func TestAddSynonymRespectsTheVocabularyMeaningLimit(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	if _, err := db.Exec(`
		UPDATE meanings
		SET text = ?, normalized_text = ?`, strings.Repeat("b", 1801), strings.Repeat("b", 1801)); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state := moveToPromptType(t, ctx, store, sessionID, "meaning")
	if _, err := store.Answer(ctx, sessionID, state.PromptID, strings.Repeat("a", 200)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddSynonym(ctx, sessionID, state.PromptID, strings.Repeat("a", 200)); err == nil || !strings.Contains(err.Error(), "too much meaning text") {
		t.Fatalf("meaning limit error = %v", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM meanings").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("meanings after rejected addition = %d, want 1", count)
	}
}

func TestAddSynonymRejectsPronunciationPrompts(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state := moveToPromptType(t, ctx, store, sessionID, "pronunciation")
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "wrong"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddSynonym(ctx, sessionID, state.PromptID, "wrong"); err == nil || !strings.Contains(err.Error(), "meaning answers") {
		t.Fatalf("pronunciation synonym error = %v", err)
	}
	var meaningCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM meanings").Scan(&meaningCount); err != nil {
		t.Fatal(err)
	}
	if meaningCount != 1 {
		t.Fatalf("meanings after pronunciation request = %d, want 1", meaningCount)
	}
}

func TestAddSynonymRejectsOtherSessionsAndStalePrompts(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state := moveToPromptType(t, ctx, store, sessionID, "meaning")
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "consume"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddSynonym(ctx, sessionID+100, state.PromptID, "consume"); err == nil || !strings.Contains(err.Error(), "no longer available") {
		t.Fatalf("cross-session synonym error = %v", err)
	}
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "to eat"); err != nil {
		t.Fatal(err)
	}
	var rejectedAnswer string
	var rejectedRevision int64
	if err := db.QueryRow(`
		SELECT last_incorrect_answer, last_incorrect_content_revision
		FROM review_prompts
		WHERE id = ?`, state.PromptID).Scan(&rejectedAnswer, &rejectedRevision); err != nil {
		t.Fatal(err)
	}
	if rejectedAnswer != "" || rejectedRevision != 0 {
		t.Fatalf("passed prompt retained rejected answer %q/%d", rejectedAnswer, rejectedRevision)
	}
	if _, err := store.AddSynonym(ctx, sessionID, state.PromptID, "consume"); err == nil {
		t.Fatal("stale prompt added a synonym")
	}
	var meaningCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM meanings").Scan(&meaningCount); err != nil {
		t.Fatal(err)
	}
	if meaningCount != 1 {
		t.Fatalf("meanings after rejected synonym requests = %d, want 1", meaningCount)
	}
}

func TestRejectedMeaningCandidateIsBounded(t *testing.T) {
	if got := rejectedMeaningCandidate("meaning", strings.Repeat("a", maxRejectedMeaningRunes+1)); got != "" {
		t.Fatalf("oversized rejected answer = %q", got)
	}
	if got := rejectedMeaningCandidate("meaning", "line\nbreak"); got != "" {
		t.Fatalf("control-character rejected answer = %q", got)
	}
	if got := rejectedMeaningCandidate("pronunciation", "wrong"); got != "" {
		t.Fatalf("pronunciation rejected answer = %q", got)
	}
}

func TestReviewFailureRequeuesAndCanComplete(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "review.sqlite")
	db, err := database.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	insertDueVocabulary(t, db)

	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 10)
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatalf("load initial review: %v", err)
	}
	firstPromptID := state.PromptID
	var failedItemID int64
	if err := db.QueryRow("SELECT session_item_id FROM review_prompts WHERE id = ?", firstPromptID).Scan(&failedItemID); err != nil {
		t.Fatal(err)
	}
	wantPromptTypes := reviewPromptTypes(t, db, failedItemID)
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := store.Answer(ctx, sessionID, firstPromptID, "wrong"); err != nil {
			t.Fatalf("wrong attempt %d: %v", attempt+1, err)
		}
	}
	state, err = store.State(ctx, sessionID)
	if err != nil {
		t.Fatalf("load failed review: %v", err)
	}
	if !state.Feedback {
		t.Fatal("review did not enter feedback state")
	}
	if err := store.Continue(ctx, sessionID); err != nil {
		t.Fatalf("continue failed review: %v", err)
	}
	var reinforcementID int64
	if err := db.QueryRow(`
		SELECT id FROM review_session_items
		WHERE session_id = ? AND srs_applied = 0
		ORDER BY id DESC LIMIT 1`, sessionID).Scan(&reinforcementID); err != nil {
		t.Fatal(err)
	}
	if got := reviewPromptTypes(t, db, reinforcementID); strings.Join(got, ",") != strings.Join(wantPromptTypes, ",") {
		t.Fatalf("reinforcement prompt order = %v, want %v", got, wantPromptTypes)
	}

	state, err = store.State(ctx, sessionID)
	if err != nil {
		t.Fatalf("load requeued review: %v", err)
	}
	if state.UndoAvailable {
		t.Fatal("undo is shown while reinforcement is pending")
	}
	for state.Status != "completed" {
		answer := "to eat"
		if state.PromptType == "pronunciation" {
			answer = "たべる"
		}
		if _, err := store.Answer(ctx, sessionID, state.PromptID, answer); err != nil {
			t.Fatalf("answer requeued prompt: %v", err)
		}
		state, err = store.State(ctx, sessionID)
		if err != nil {
			t.Fatalf("load review after answer: %v", err)
		}
		if state.Feedback {
			t.Fatalf("requeued correct answer unexpectedly entered feedback")
		}
	}
}

func TestGiveUpAfterARejectedAnswerUsesTheNormalFailurePath(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state := moveToPromptType(t, ctx, store, sessionID, "meaning")
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "consume"); err != nil {
		t.Fatal(err)
	}

	outcome, err := store.GiveUp(ctx, sessionID, state.PromptID)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.FinalFailure || outcome.Value != Incorrect {
		t.Fatalf("GiveUp() = %+v, want final incorrect result", outcome)
	}
	state, err = store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Feedback || state.Attempts != state.MaxAttempts || state.RejectedAnswer != "consume" {
		t.Fatalf(
			"revealed state = feedback %t, attempts %d/%d, rejected %q",
			state.Feedback, state.Attempts, state.MaxAttempts, state.RejectedAnswer,
		)
	}
	var resultCount int
	var resultOutcome string
	if err := db.QueryRow("SELECT COUNT(*), MIN(outcome) FROM review_results").Scan(&resultCount, &resultOutcome); err != nil {
		t.Fatal(err)
	}
	if resultCount != 1 || resultOutcome != "failure" {
		t.Fatalf("review results = %d %q, want one failure", resultCount, resultOutcome)
	}
	var failuresTowardLeech int
	if err := db.QueryRow("SELECT failures_toward_leech FROM leech_states WHERE vocabulary_id = ?", state.VocabularyID).Scan(&failuresTowardLeech); err != nil {
		t.Fatal(err)
	}
	if failuresTowardLeech != 1 {
		t.Fatalf("leech failures = %d, want 1", failuresTowardLeech)
	}
	if _, err := store.GiveUp(ctx, sessionID, state.PromptID); err == nil || !strings.Contains(err.Error(), "no longer active") {
		t.Fatalf("second GiveUp() error = %v, want stale prompt error", err)
	}
	if err := store.Continue(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	var reinforcementCount int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM review_session_items
		WHERE session_id = ? AND vocabulary_id = ? AND srs_applied = 0 AND status IN ('pending', 'current')`,
		sessionID, state.VocabularyID,
	).Scan(&reinforcementCount); err != nil {
		t.Fatal(err)
	}
	if reinforcementCount != 1 {
		t.Fatalf("reinforcement items = %d, want 1", reinforcementCount)
	}
}

func TestUndoAfterGiveUpRemovesTheFailure(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state := moveToPromptType(t, ctx, store, sessionID, "meaning")
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "consume"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GiveUp(ctx, sessionID, state.PromptID); err != nil {
		t.Fatal(err)
	}
	if err := store.Undo(ctx, sessionID); err != nil {
		t.Fatal(err)
	}

	var visibleResults int
	if err := db.QueryRow("SELECT COUNT(*) FROM review_results WHERE voided_at IS NULL").Scan(&visibleResults); err != nil {
		t.Fatal(err)
	}
	if visibleResults != 0 {
		t.Fatalf("visible review results = %d, want 0", visibleResults)
	}
	var failuresTowardLeech int
	if err := db.QueryRow("SELECT failures_toward_leech FROM leech_states WHERE vocabulary_id = ?", state.VocabularyID).Scan(&failuresTowardLeech); err != nil {
		t.Fatal(err)
	}
	if failuresTowardLeech != 0 {
		t.Fatalf("leech failures after undo = %d, want 0", failuresTowardLeech)
	}
}

func TestGiveUpRequiresARejectedTypedAnswer(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GiveUp(ctx, sessionID, state.PromptID); err == nil || !strings.Contains(err.Error(), "submit an answer") {
		t.Fatalf("GiveUp() before an answer error = %v", err)
	}
}

func TestContinueRejectsInactiveSession(t *testing.T) {
	for _, status := range []string{"paused", "abandoned"} {
		t.Run(status, func(t *testing.T) {
			ctx, db := openReviewTestDatabase(t)
			insertDueVocabulary(t, db)
			if _, err := db.Exec(`
				INSERT INTO user_settings (id, retry_count)
				VALUES (1, 1)`); err != nil {
				t.Fatal(err)
			}

			store := NewStore(db)
			sessionID, err := store.StartNormal(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			state, err := store.State(ctx, sessionID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Answer(ctx, sessionID, state.PromptID, "wrong"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("UPDATE review_sessions SET status = ? WHERE id = ?", status, sessionID); err != nil {
				t.Fatal(err)
			}

			err = store.Continue(ctx, sessionID)
			if err == nil || !strings.Contains(err.Error(), "no failed item ready") {
				t.Fatalf("Continue() error = %v", err)
			}
			var itemCount int
			if err := db.QueryRow("SELECT COUNT(*) FROM review_session_items WHERE session_id = ?", sessionID).Scan(&itemCount); err != nil {
				t.Fatal(err)
			}
			if itemCount != 1 {
				t.Fatalf("review items = %d, want 1", itemCount)
			}
		})
	}
}

func TestStartSessionAcceptsValidNormalReview(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	var vocabularyID int64
	if err := db.QueryRow("SELECT id FROM vocabulary").Scan(&vocabularyID); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	sessionID, err := store.startSession(ctx, "normal", []int64{vocabularyID}, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Kind != "normal" || state.Status != "active" || state.Total != 1 || state.VocabularyID != vocabularyID {
		t.Fatalf("normal review state = kind %q, status %q, total %d, word %d", state.Kind, state.Status, state.Total, state.VocabularyID)
	}
}

func TestNormalReviewLimitIsBounded(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	for index := range normalSessionLimit + 1 {
		insertDueVocabularyWithExpression(t, db, fmt.Sprintf("word-%d", index))
	}

	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, normalSessionLimit+1)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Total != normalSessionLimit {
		t.Fatalf("review items = %d, want %d", state.Total, normalSessionLimit)
	}
}

func TestRootedSuccessBecomesEvergreen(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	if _, err := db.Exec("UPDATE srs_states SET stage = ?", srs.StageSeven); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	completeCurrentItem(t, ctx, store, sessionID)

	var stage int
	var dueAt sql.NullInt64
	if err := db.QueryRow("SELECT stage, due_at FROM srs_states").Scan(&stage, &dueAt); err != nil {
		t.Fatal(err)
	}
	if stage != int(srs.StageEvergreen) || dueAt.Valid {
		t.Fatalf("SRS state = stage %d, due %v; want Evergreen with no due date", stage, dueAt)
	}
	var resultDue sql.NullInt64
	if err := db.QueryRow("SELECT due_after FROM review_results").Scan(&resultDue); err != nil {
		t.Fatal(err)
	}
	if resultDue.Valid {
		t.Fatalf("review result due date = %v, want NULL", resultDue)
	}
	dueCount, err := store.DueCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if dueCount != 0 {
		t.Fatalf("due reviews = %d, want 0", dueCount)
	}
	if _, err := store.StartNormal(ctx, 1); err == nil || !strings.Contains(err.Error(), "no reviews are due") {
		t.Fatalf("start reviews error = %v", err)
	}
}

func TestFourMonthSuccessSchedulesSixMonthReviewWhenEnabled(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	if _, err := db.Exec("UPDATE srs_states SET stage = ?", srs.StageSeven); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO user_settings (id, six_month_review_enabled) VALUES (1, 1)"); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	completeCurrentItem(t, ctx, store, sessionID)

	var stage int
	var dueAt int64
	if err := db.QueryRow("SELECT stage, due_at FROM srs_states").Scan(&stage, &dueAt); err != nil {
		t.Fatal(err)
	}
	minimumDue := before.AddDate(0, 6, 0).Unix()
	maximumDue := time.Now().UTC().AddDate(0, 6, 0).Unix() + 1
	if stage != int(srs.StageEight) || dueAt < minimumDue || dueAt > maximumDue {
		t.Fatalf("SRS state = stage %d, due %d; want six-month stage due between %d and %d", stage, dueAt, minimumDue, maximumDue)
	}
}

func TestDueCountAtUsesProvidedTime(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabularyWithExpression(t, db, "past")
	insertDueVocabularyWithExpression(t, db, "future")
	asOf := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`
		UPDATE srs_states
		SET due_at = CASE vocabulary_id
			WHEN (SELECT id FROM vocabulary WHERE expression = 'past') THEN ?
			ELSE ?
		END`, asOf.Add(-time.Minute).Unix(), asOf.Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	count, err := store.DueCountAt(ctx, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("due count at %v = %d, want 1", asOf, count)
	}
	count, err = store.DueCountAt(ctx, asOf.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("due count after future item = %d, want 2", count)
	}
}

func TestStartSessionRevalidatesNormalReviewVocabulary(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(context.Context, *sql.DB, int64) error
		want   string
	}{
		{
			name: "reset",
			mutate: func(ctx context.Context, db *sql.DB, id int64) error {
				return vocabulary.NewStore(db).ApplyAction(ctx, id, vocabulary.ActionReset)
			},
			want: "no longer active",
		},
		{
			name: "archived",
			mutate: func(ctx context.Context, db *sql.DB, id int64) error {
				return vocabulary.NewStore(db).ApplyAction(ctx, id, vocabulary.ActionArchive)
			},
			want: "no longer active",
		},
		{
			name: "deleted",
			mutate: func(ctx context.Context, db *sql.DB, id int64) error {
				return vocabulary.NewStore(db).ApplyAction(ctx, id, vocabulary.ActionDelete)
			},
			want: "no longer exists",
		},
		{
			name: "missing SRS state",
			mutate: func(ctx context.Context, db *sql.DB, id int64) error {
				_, err := db.ExecContext(ctx, "DELETE FROM srs_states WHERE vocabulary_id = ?", id)
				return err
			},
			want: "no SRS state",
		},
		{
			name: "suspended SRS state",
			mutate: func(ctx context.Context, db *sql.DB, id int64) error {
				_, err := db.ExecContext(ctx, "UPDATE srs_states SET suspended_at = ? WHERE vocabulary_id = ?", time.Now().UTC().Unix(), id)
				return err
			},
			want: "suspended from review",
		},
		{
			name: "not due",
			mutate: func(ctx context.Context, db *sql.DB, id int64) error {
				_, err := db.ExecContext(ctx, "UPDATE srs_states SET due_at = ? WHERE vocabulary_id = ?", time.Now().UTC().Add(time.Hour).Unix(), id)
				return err
			},
			want: "no longer due",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, db := openReviewTestDatabase(t)
			insertDueVocabulary(t, db)
			var vocabularyID int64
			if err := db.QueryRow("SELECT id FROM vocabulary").Scan(&vocabularyID); err != nil {
				t.Fatal(err)
			}
			if err := test.mutate(ctx, db, vocabularyID); err != nil {
				t.Fatal(err)
			}

			_, err := NewStore(db).startSession(ctx, "normal", []int64{vocabularyID}, 3, 0)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("start error = %v, want containing %q", err, test.want)
			}
			var sessionCount int
			if err := db.QueryRow("SELECT COUNT(*) FROM review_sessions").Scan(&sessionCount); err != nil {
				t.Fatal(err)
			}
			if sessionCount != 0 {
				t.Fatalf("review sessions = %d, want 0", sessionCount)
			}
		})
	}
}

func TestStartSessionRevalidatesExtraStudyVocabulary(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	var vocabularyID int64
	if err := db.QueryRow("SELECT id FROM vocabulary").Scan(&vocabularyID); err != nil {
		t.Fatal(err)
	}
	if err := vocabulary.NewStore(db).ApplyAction(ctx, vocabularyID, vocabulary.ActionArchive); err != nil {
		t.Fatal(err)
	}

	_, err := NewStore(db).startSession(ctx, "extra", []int64{vocabularyID}, 3, 0)
	if err == nil || !strings.Contains(err.Error(), "no longer active for extra study") {
		t.Fatalf("start error = %v", err)
	}
	var sessionCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM review_sessions").Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if sessionCount != 0 {
		t.Fatalf("review sessions = %d, want 0", sessionCount)
	}
}

func TestReviewUndoRestoresSRSAndReopensSession(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "review-undo.sqlite")
	db, err := database.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	insertDueVocabulary(t, db)

	var vocabularyID int64
	var dueBefore int64
	if err := db.QueryRow("SELECT vocabulary_id, due_at FROM srs_states LIMIT 1").Scan(&vocabularyID, &dueBefore); err != nil {
		t.Fatalf("load initial SRS state: %v", err)
	}
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 10)
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	var reviewedItemID int64
	if err := db.QueryRow(`
		SELECT id FROM review_session_items
		WHERE session_id = ? AND status = 'current'`, sessionID).Scan(&reviewedItemID); err != nil {
		t.Fatal(err)
	}
	wantPromptTypes := reviewPromptTypes(t, db, reviewedItemID)
	completeCurrentItem(t, ctx, store, sessionID)

	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatalf("load completed review: %v", err)
	}
	if state.Status != "completed" || !state.UndoAvailable {
		t.Fatalf("completed review state = status %q, undo %t", state.Status, state.UndoAvailable)
	}
	if err := store.Undo(ctx, sessionID); err != nil {
		t.Fatalf("undo review: %v", err)
	}
	state, err = store.State(ctx, sessionID)
	if err != nil {
		t.Fatalf("load reopened review: %v", err)
	}
	if state.Status != "active" || state.PromptID == 0 {
		t.Fatalf("reopened review state = status %q, prompt %d", state.Status, state.PromptID)
	}
	if state.Completed != 0 || state.Total != 1 || state.Remaining != 1 {
		t.Fatalf("reopened review progress = %d/%d with %d remaining", state.Completed, state.Total, state.Remaining)
	}
	var undoItemID int64
	if err := db.QueryRow(`
		SELECT id FROM review_session_items
		WHERE session_id = ? AND status = 'current'`, sessionID).Scan(&undoItemID); err != nil {
		t.Fatal(err)
	}
	if got := reviewPromptTypes(t, db, undoItemID); strings.Join(got, ",") != strings.Join(wantPromptTypes, ",") {
		t.Fatalf("undo prompt order = %v, want %v", got, wantPromptTypes)
	}
	var stage int
	var dueAfter sql.NullInt64
	if err := db.QueryRow("SELECT stage, due_at FROM srs_states WHERE vocabulary_id = ?", vocabularyID).Scan(&stage, &dueAfter); err != nil {
		t.Fatalf("load restored SRS state: %v", err)
	}
	if stage != 0 || !dueAfter.Valid || dueAfter.Int64 != dueBefore {
		t.Fatalf("restored SRS state = stage %d, due %v; want stage 0, due %d", stage, dueAfter, dueBefore)
	}
	var voided int
	if err := db.QueryRow("SELECT COUNT(*) FROM review_results WHERE voided_at IS NOT NULL").Scan(&voided); err != nil {
		t.Fatalf("count voided results: %v", err)
	}
	if voided != 1 {
		t.Fatalf("voided results = %d, want 1", voided)
	}
}

func TestReviewUndoCannotOverwriteANewerSession(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "stale-undo.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	insertDueVocabulary(t, db)

	store := NewStore(db)
	firstSessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	completeCurrentItem(t, ctx, store, firstSessionID)
	if _, err := db.Exec("UPDATE srs_states SET due_at = ?", time.Now().UTC().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	secondSessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	completeCurrentItem(t, ctx, store, secondSessionID)

	state, err := store.State(ctx, firstSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.UndoAvailable {
		t.Fatal("an older session offers undo after a newer review")
	}
	if err := store.Undo(ctx, firstSessionID); err == nil {
		t.Fatal("Undo() rewound an older session")
	}
	var stage int
	if err := db.QueryRow("SELECT stage FROM srs_states").Scan(&stage); err != nil {
		t.Fatal(err)
	}
	if stage != 2 {
		t.Fatalf("SRS stage = %d, want 2", stage)
	}
}

func TestReviewUndoRejectsArchivedVocabulary(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "archived-undo.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	insertDueVocabulary(t, db)

	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	completeCurrentItem(t, ctx, store, sessionID)
	var vocabularyID int64
	if err := db.QueryRow("SELECT id FROM vocabulary LIMIT 1").Scan(&vocabularyID); err != nil {
		t.Fatal(err)
	}
	if err := vocabulary.NewStore(db).ApplyAction(ctx, vocabularyID, vocabulary.ActionArchive); err != nil {
		t.Fatal(err)
	}

	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.UndoAvailable {
		t.Fatal("an archived word still offers undo")
	}
	if err := store.Undo(ctx, sessionID); err == nil {
		t.Fatal("Undo() reopened an archived word")
	}
}

func TestReviewUndoRejectsAbandonedSession(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)

	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	completeCurrentItem(t, ctx, store, sessionID)
	if _, err := db.Exec("UPDATE review_sessions SET status = 'abandoned' WHERE id = ?", sessionID); err != nil {
		t.Fatal(err)
	}

	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.UndoAvailable {
		t.Fatal("abandoned review offers undo")
	}
	if err := store.Undo(ctx, sessionID); err == nil || !strings.Contains(err.Error(), "not available for undo") {
		t.Fatalf("Undo() error = %v", err)
	}
	var status string
	var voided int
	if err := db.QueryRow("SELECT status FROM review_sessions WHERE id = ?", sessionID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM review_results WHERE voided_at IS NOT NULL").Scan(&voided); err != nil {
		t.Fatal(err)
	}
	if status != "abandoned" || voided != 0 {
		t.Fatalf("review after rejected undo = status %q, voided results %d", status, voided)
	}
}

func TestReviewUndoDoesNotReopenAlongsideAnotherStandaloneSession(t *testing.T) {
	for _, otherStatus := range []string{"active", "paused"} {
		t.Run(otherStatus, func(t *testing.T) {
			ctx, db := openReviewTestDatabase(t)
			insertDueVocabularyWithExpression(t, db, "first")
			insertDueVocabularyWithExpression(t, db, "second")
			store := NewStore(db)
			completedSessionID, err := store.StartNormal(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			completeCurrentItem(t, ctx, store, completedSessionID)
			otherSessionID, err := store.StartNormal(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			if otherStatus == "paused" {
				if err := store.Pause(ctx, otherSessionID); err != nil {
					t.Fatal(err)
				}
			}

			state, err := store.State(ctx, completedSessionID)
			if err != nil {
				t.Fatal(err)
			}
			if state.UndoAvailable {
				t.Fatal("completed session offers undo while another standalone session is open")
			}
			if err := store.Undo(ctx, completedSessionID); err == nil || !strings.Contains(err.Error(), "another review session") {
				t.Fatalf("Undo() error = %v", err)
			}
		})
	}
}

func TestLessonReviewUndoRequiresCurrentLessonLink(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	vocabularyID := insertUnlearnedReviewVocabulary(t, db, "食べる")
	now := time.Now().UTC().Unix()
	lessonResult, err := db.Exec(`
		INSERT INTO lesson_sessions (status, phase)
		VALUES ('active', 'study')`)
	if err != nil {
		t.Fatal(err)
	}
	lessonID, err := lessonResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO lesson_session_items (
			session_id, vocabulary_id, position, batch_number, study_viewed_at
		)
		VALUES (?, ?, 0, 0, ?)`, lessonID, vocabularyID, now); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	reviewID, err := store.StartLesson(ctx, lessonID, []int64{vocabularyID})
	if err != nil {
		t.Fatal(err)
	}
	completeCurrentItem(t, ctx, store, reviewID)
	state, err := store.State(ctx, reviewID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.UndoAvailable {
		t.Fatal("current lesson review does not offer undo for an unlearned word")
	}
	if err := store.Undo(ctx, reviewID); err != nil {
		t.Fatal(err)
	}
	completeCurrentItem(t, ctx, store, reviewID)

	if _, err := db.Exec(`
		UPDATE lesson_sessions
		SET status = 'completed'
		WHERE id = ?`, lessonID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE vocabulary SET status = 'active' WHERE id = ?", vocabularyID); err != nil {
		t.Fatal(err)
	}
	state, err = store.State(ctx, reviewID)
	if err != nil {
		t.Fatal(err)
	}
	if state.UndoAvailable {
		t.Fatal("detached completed lesson review still offers undo")
	}
	if err := store.Undo(ctx, reviewID); err == nil || !strings.Contains(err.Error(), "lesson review is no longer available") {
		t.Fatalf("Undo() error = %v", err)
	}
}

func TestExtraStudyDoesNotChangeSRS(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "extra-study.sqlite")
	db, err := database.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	insertDueVocabulary(t, db)
	var beforeStage int
	var beforeDue int64
	if err := db.QueryRow("SELECT stage, due_at FROM srs_states LIMIT 1").Scan(&beforeStage, &beforeDue); err != nil {
		t.Fatalf("load SRS state: %v", err)
	}

	store := NewStore(db)
	if _, err := store.StudyCounts(ctx); err != nil {
		t.Fatalf("load study counts: %v", err)
	}
	if _, err := store.Leeches(ctx); err != nil {
		t.Fatalf("load leeches: %v", err)
	}
	sessionID, err := store.StartExtraSource(ctx, "current", nil)
	if err != nil {
		t.Fatalf("start extra study: %v", err)
	}
	completeCurrentItem(t, ctx, store, sessionID)
	var afterStage int
	var afterDue int64
	if err := db.QueryRow("SELECT stage, due_at FROM srs_states LIMIT 1").Scan(&afterStage, &afterDue); err != nil {
		t.Fatalf("load SRS state after extra study: %v", err)
	}
	if afterStage != beforeStage || afterDue != beforeDue {
		t.Fatalf("extra study changed SRS from %d/%d to %d/%d", beforeStage, beforeDue, afterStage, afterDue)
	}
	var applied int
	if err := db.QueryRow("SELECT srs_applied FROM review_results LIMIT 1").Scan(&applied); err != nil {
		t.Fatalf("load extra result: %v", err)
	}
	if applied != 0 {
		t.Fatalf("extra result srs_applied = %d, want 0", applied)
	}
	if err := store.Undo(ctx, sessionID); err != nil {
		t.Fatalf("undo extra study: %v", err)
	}
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatalf("load extra study after undo: %v", err)
	}
	if state.Status != "active" || state.PromptID == 0 {
		t.Fatalf("extra study after undo = status %q, prompt %d", state.Status, state.PromptID)
	}
	if err := db.QueryRow("SELECT stage, due_at FROM srs_states LIMIT 1").Scan(&afterStage, &afterDue); err != nil {
		t.Fatalf("load SRS state after extra-study undo: %v", err)
	}
	if afterStage != beforeStage || afterDue != beforeDue {
		t.Fatalf("extra-study undo changed SRS from %d/%d to %d/%d", beforeStage, beforeDue, afterStage, afterDue)
	}
}

func TestExtraStudyDoesNotReuseLessonReview(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "separate-extra-study.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	insertDueVocabulary(t, db)

	lessonResult, err := db.Exec("INSERT INTO lesson_sessions (status) VALUES ('active')")
	if err != nil {
		t.Fatal(err)
	}
	lessonID, err := lessonResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	lessonWordResult, err := db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, status, created_at, updated_at)
		VALUES ('見る', '見る', 'unlearned', ?, ?)`,
		time.Now().UTC().Unix(),
		time.Now().UTC().Unix(),
	)
	if err != nil {
		t.Fatal(err)
	}
	lessonWordID, err := lessonWordResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO lesson_session_items (session_id, vocabulary_id, position, batch_number)
		VALUES (?, ?, 0, 0)`, lessonID, lessonWordID); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	var unrelatedID int64
	if err := db.QueryRow("SELECT id FROM vocabulary WHERE status = 'active' LIMIT 1").Scan(&unrelatedID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartLesson(ctx, lessonID, []int64{unrelatedID}); err == nil {
		t.Fatal("lesson review accepted a word outside the current batch")
	}
	if _, err := store.StartLesson(ctx, lessonID, []int64{lessonWordID}); err == nil || !strings.Contains(err.Error(), "view every word") {
		t.Fatalf("unviewed lesson review error = %v", err)
	}
	if _, err := db.Exec(`
		UPDATE lesson_session_items SET study_viewed_at = ?
		WHERE session_id = ?`, time.Now().UTC().Unix(), lessonID); err != nil {
		t.Fatal(err)
	}
	lessonReviewID, err := store.StartLesson(ctx, lessonID, []int64{lessonWordID})
	if err != nil {
		t.Fatal(err)
	}
	extraStudyID, err := store.StartExtraSource(ctx, "current", nil)
	if err != nil {
		t.Fatal(err)
	}
	if extraStudyID == lessonReviewID {
		t.Fatal("standalone extra study reused a lesson review")
	}
	var linkedLesson sql.NullInt64
	if err := db.QueryRow(
		"SELECT lesson_session_id FROM review_sessions WHERE id = ?",
		extraStudyID,
	).Scan(&linkedLesson); err != nil {
		t.Fatal(err)
	}
	if linkedLesson.Valid {
		t.Fatalf("standalone extra study is linked to lesson %d", linkedLesson.Int64)
	}
}

func TestHiddenMistakesStayOutOfExtraStudy(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "hidden-mistake.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	insertDueVocabulary(t, db)

	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := store.Answer(ctx, sessionID, state.PromptID, "wrong"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`
		INSERT INTO mistake_visibility (vocabulary_id, leech_hidden_at)
		VALUES (?, ?)`, state.VocabularyID, time.Now().UTC().Unix()); err != nil {
		t.Fatal(err)
	}

	counts, err := store.StudyCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.RecentMistakes != 1 {
		t.Fatalf("recent mistake count after hiding leech = %d, want 1", counts.RecentMistakes)
	}
	ids, err := store.studyIDs(ctx, "recent-mistakes", nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != state.VocabularyID {
		t.Fatalf("recent mistake study IDs after hiding leech = %v, want [%d]", ids, state.VocabularyID)
	}

	if _, err := db.Exec(
		"UPDATE mistake_visibility SET hidden_at = ? WHERE vocabulary_id = ?",
		time.Now().UTC().Unix(),
		state.VocabularyID,
	); err != nil {
		t.Fatal(err)
	}
	counts, err = store.StudyCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.RecentMistakes != 0 {
		t.Fatalf("recent mistake count = %d, want 0", counts.RecentMistakes)
	}
	ids, err = store.studyIDs(ctx, "recent-mistakes", nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("recent mistake study IDs = %v, want none", ids)
	}
}

func TestNormalFailureMakesHiddenMistakeVisibleAgain(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	if _, err := db.Exec(`
		INSERT INTO user_settings (id, retry_count)
		VALUES (1, 1)`); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	if _, err := db.Exec(`
		INSERT INTO mistake_visibility (vocabulary_id, hidden_at, leech_hidden_at)
		VALUES (?, ?, ?)`, state.VocabularyID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "wrong"); err != nil {
		t.Fatal(err)
	}

	var hiddenAt, leechHiddenAt sql.NullInt64
	if err := db.QueryRow(`
		SELECT hidden_at, leech_hidden_at
		FROM mistake_visibility
		WHERE vocabulary_id = ?`, state.VocabularyID).Scan(&hiddenAt, &leechHiddenAt); err != nil {
		t.Fatal(err)
	}
	if hiddenAt.Valid {
		t.Fatalf("hidden_at = %d after a newer failure, want NULL", hiddenAt.Int64)
	}
	if leechHiddenAt.Valid {
		t.Fatalf("leech_hidden_at = %d after a same-second failure, want NULL", leechHiddenAt.Int64)
	}
}

func TestReviewUndoRestoresMistakeVisibilitySnapshot(t *testing.T) {
	tests := []struct {
		name          string
		exists        bool
		hiddenAt      sql.NullInt64
		leechHiddenAt sql.NullInt64
	}{
		{
			name:          "existing values",
			exists:        true,
			hiddenAt:      sql.NullInt64{Int64: 101, Valid: true},
			leechHiddenAt: sql.NullInt64{Int64: 103, Valid: true},
		},
		{name: "existing all null", exists: true},
		{name: "no row"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, db := openReviewTestDatabase(t)
			insertDueVocabulary(t, db)
			if _, err := db.Exec(`
				INSERT INTO user_settings (id, retry_count)
				VALUES (1, 1)`); err != nil {
				t.Fatal(err)
			}
			var vocabularyID int64
			if err := db.QueryRow("SELECT id FROM vocabulary LIMIT 1").Scan(&vocabularyID); err != nil {
				t.Fatal(err)
			}
			if test.exists {
				if _, err := db.Exec(`
					INSERT INTO mistake_visibility (vocabulary_id, hidden_at, leech_hidden_at)
					VALUES (?, ?, ?)`, vocabularyID, nullableInt64(test.hiddenAt),
					nullableInt64(test.leechHiddenAt)); err != nil {
					t.Fatal(err)
				}
			}

			store := NewStore(db)
			sessionID, err := store.StartNormal(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			state, err := store.State(ctx, sessionID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Answer(ctx, sessionID, state.PromptID, "wrong"); err != nil {
				t.Fatal(err)
			}

			var existedBefore sql.NullInt64
			var hiddenBefore, leechHiddenBefore sql.NullInt64
			if err := db.QueryRow(`
				SELECT mistake_visibility_existed_before, mistake_hidden_before,
				       mistake_leech_hidden_before
				FROM review_results
				WHERE session_item_id IN (
					SELECT id FROM review_session_items WHERE session_id = ?
				)`, sessionID).Scan(
				&existedBefore, &hiddenBefore, &leechHiddenBefore,
			); err != nil {
				t.Fatal(err)
			}
			if !existedBefore.Valid || (existedBefore.Int64 == 1) != test.exists ||
				!nullableInt64Equal(hiddenBefore, test.hiddenAt) ||
				!nullableInt64Equal(leechHiddenBefore, test.leechHiddenAt) {
				t.Fatalf(
					"snapshot = existed %v, hidden %v, leech %v",
					existedBefore, hiddenBefore, leechHiddenBefore,
				)
			}
			if err := store.Undo(ctx, sessionID); err != nil {
				t.Fatal(err)
			}

			var hiddenAfter, leechHiddenAfter sql.NullInt64
			err = db.QueryRow(`
				SELECT hidden_at, leech_hidden_at
				FROM mistake_visibility
				WHERE vocabulary_id = ?`, vocabularyID).Scan(
				&hiddenAfter, &leechHiddenAfter,
			)
			if !test.exists {
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("visibility row after undo error = %v, want no row", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !nullableInt64Equal(hiddenAfter, test.hiddenAt) ||
				!nullableInt64Equal(leechHiddenAfter, test.leechHiddenAt) {
				t.Fatalf(
					"restored visibility = hidden %v, leech %v",
					hiddenAfter, leechHiddenAfter,
				)
			}
		})
	}
}

func TestReviewUndoRejectsMistakeVisibilityChangedAfterFailure(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	if _, err := db.Exec(`
		INSERT INTO user_settings (id, retry_count)
		VALUES (1, 1)`); err != nil {
		t.Fatal(err)
	}
	var vocabularyID int64
	if err := db.QueryRow("SELECT id FROM vocabulary LIMIT 1").Scan(&vocabularyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO mistake_visibility (vocabulary_id, hidden_at)
		VALUES (?, 100)`, vocabularyID); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "wrong"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		UPDATE mistake_visibility SET hidden_at = 200 WHERE vocabulary_id = ?`, vocabularyID); err != nil {
		t.Fatal(err)
	}
	state, err = store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.UndoAvailable {
		t.Fatal("review offers undo after mistake visibility changed")
	}
	if err := store.Undo(ctx, sessionID); err == nil || !strings.Contains(err.Error(), "mistake visibility has changed") {
		t.Fatalf("Undo() error = %v", err)
	}
	var hiddenAt int64
	var voidedAt sql.NullInt64
	if err := db.QueryRow("SELECT hidden_at FROM mistake_visibility WHERE vocabulary_id = ?", vocabularyID).Scan(&hiddenAt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT voided_at FROM review_results").Scan(&voidedAt); err != nil {
		t.Fatal(err)
	}
	if hiddenAt != 200 || voidedAt.Valid {
		t.Fatalf("rejected undo changed state: hidden %d, voided %v", hiddenAt, voidedAt)
	}
}

func TestUndoFailedReviewDoesNotLeaveAnUncountedItem(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "undo-failure.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	insertDueVocabulary(t, db)
	if _, err := db.Exec("INSERT INTO user_settings (id) VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE user_settings SET retry_count = 1 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "wrong"); err != nil {
		t.Fatal(err)
	}
	if err := store.Undo(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	completeCurrentItem(t, ctx, store, sessionID)
	state, err = store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "completed" || state.Completed != state.Total {
		t.Fatalf("after failed undo, state = status %q completed %d/%d", state.Status, state.Completed, state.Total)
	}
}

func TestReviewResultStoresPromptLevelAccuracy(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "prompt-metrics.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "wrong"); err != nil {
		t.Fatal(err)
	}
	state, err = store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Answer(ctx, sessionID, state.PromptID, answerForState(state)); err != nil {
		t.Fatal(err)
	}
	state, err = store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Answer(ctx, sessionID, state.PromptID, answerForState(state)); err != nil {
		t.Fatal(err)
	}
	state, err = store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "completed" || state.FirstTryCorrect != 1 || state.PromptCount != 2 {
		t.Fatalf("completed state = status %q, accuracy %d/%d", state.Status, state.FirstTryCorrect, state.PromptCount)
	}
	var correct, prompts int
	if err := db.QueryRow("SELECT first_attempt_correct_count, prompt_count FROM review_results LIMIT 1").Scan(&correct, &prompts); err != nil {
		t.Fatal(err)
	}
	if correct != 1 || prompts != 2 {
		t.Fatalf("prompt metrics = %d/%d, want 1/2", correct, prompts)
	}
}

func TestFailedReviewResultCountsOnlyAttemptedPrompts(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	if _, err := db.Exec("INSERT INTO user_settings (id, retry_count) VALUES (1, 1)"); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := store.Answer(ctx, sessionID, state.PromptID, "wrong")
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.FinalFailure {
		t.Fatalf("answer outcome = %+v, want final failure", outcome)
	}
	var correct, prompts int
	if err := db.QueryRow("SELECT first_attempt_correct_count, prompt_count FROM review_results").Scan(&correct, &prompts); err != nil {
		t.Fatal(err)
	}
	if correct != 0 || prompts != 1 {
		t.Fatalf("failed prompt metrics = %d/%d, want 0/1", correct, prompts)
	}
}

func TestSelectedStudyRejectsAnOversizedSelection(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	selected := make([]int64, maximumStudySelectionSize+1)
	for index := range selected {
		selected[index] = int64(index + 1)
	}

	_, err := NewStore(db).studyIDs(ctx, "selected", selected, 10)
	if err == nil || !strings.Contains(err.Error(), "at most 100") {
		t.Fatalf("oversized selection error = %v, want selection limit", err)
	}
}

func answerForState(state State) string {
	if state.PromptType == "pronunciation" {
		return "たべる"
	}
	return "to eat"
}

func moveToPromptType(t *testing.T, ctx context.Context, store *Store, sessionID int64, promptType string) State {
	t.Helper()
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.PromptType == promptType {
		return state
	}
	if _, err := store.Answer(ctx, sessionID, state.PromptID, answerForState(state)); err != nil {
		t.Fatal(err)
	}
	state, err = store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.PromptType != promptType {
		t.Fatalf("current prompt type = %q, want %q", state.PromptType, promptType)
	}
	return state
}

func completeCurrentItem(t *testing.T, ctx context.Context, store *Store, sessionID int64) {
	t.Helper()
	for {
		state, err := store.State(ctx, sessionID)
		if err != nil {
			t.Fatalf("load review item: %v", err)
		}
		if state.Status == "completed" {
			return
		}
		answer := "to eat"
		if state.PromptType == "pronunciation" {
			answer = "たべる"
		}
		if _, err := store.Answer(ctx, sessionID, state.PromptID, answer); err != nil {
			t.Fatalf("answer review item: %v", err)
		}
	}
}

func reviewPromptTypes(t *testing.T, db *sql.DB, itemID int64) []string {
	t.Helper()
	rows, err := db.Query(`
		SELECT prompt_type FROM review_prompts
		WHERE session_item_id = ?
		ORDER BY position`, itemID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var promptTypes []string
	for rows.Next() {
		var promptType string
		if err := rows.Scan(&promptType); err != nil {
			t.Fatal(err)
		}
		promptTypes = append(promptTypes, promptType)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return promptTypes
}

func insertDueVocabulary(t *testing.T, db *sql.DB) {
	insertDueVocabularyWithExpression(t, db, "食べる")
}

func insertDueVocabularyWithExpression(t *testing.T, db *sql.DB, expression string) {
	t.Helper()
	now := time.Now().UTC().Unix()
	result, err := db.Exec(`
		INSERT INTO vocabulary (
			expression, normalized_expression, pronunciation, normalized_pronunciation,
			status, created_at, updated_at
		)
		VALUES (?, ?, 'たべる', 'たべる', 'active', ?, ?)`, expression, expression, now, now)
	if err != nil {
		t.Fatalf("insert vocabulary: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("get vocabulary ID: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO meanings (vocabulary_id, position, text, normalized_text) VALUES (?, 0, 'to eat', 'to eat')`, id); err != nil {
		t.Fatalf("insert meaning: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO srs_states (vocabulary_id, stage, due_at) VALUES (?, 0, ?)`, id, now-1); err != nil {
		t.Fatalf("insert SRS state: %v", err)
	}
}

func insertUnlearnedReviewVocabulary(t *testing.T, db *sql.DB, expression string) int64 {
	t.Helper()
	now := time.Now().UTC().Unix()
	result, err := db.Exec(`
		INSERT INTO vocabulary (
			expression, normalized_expression, pronunciation, normalized_pronunciation,
			status, created_at, updated_at
		)
		VALUES (?, ?, 'たべる', 'たべる', 'unlearned', ?, ?)`, expression, expression, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO meanings (vocabulary_id, position, text, normalized_text)
		VALUES (?, 0, 'to eat', 'to eat')`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func openReviewTestDatabase(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "review-validation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return ctx, db
}
