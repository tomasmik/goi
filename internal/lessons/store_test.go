package lessons

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomasmik/goi/internal/database"
	"github.com/tomasmik/goi/internal/examples"
	"github.com/tomasmik/goi/internal/reviews"
)

func TestLessonCurrentLoadsPreferredExample(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "lesson-example.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	vocabularyID := insertUnlearnedVocabulary(t, db, 1)[0]
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
	lessonID, err := store.Start(ctx, []int64{vocabularyID})
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.Current(ctx, lessonID)
	if err != nil {
		t.Fatal(err)
	}
	if session.StudyItem.Example.ID != created.ID || !session.StudyItem.Example.HasTarget {
		t.Fatalf("lesson example = %+v, want example %d with highlighted target", session.StudyItem.Example, created.ID)
	}
}

func TestLessonCurrentLoadsAttachedMedia(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "lesson-media.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	vocabularyID := insertUnlearnedVocabulary(t, db, 1)[0]
	audioID := attachLessonTestMedia(t, db, vocabularyID, "pronunciation", "audio", "audio/mpeg", "a")
	pictureID := attachLessonTestMedia(t, db, vocabularyID, "picture", "image", "image/png", "b")
	if _, err := db.Exec("INSERT INTO user_settings (id, audio_enabled) VALUES (1, 1)"); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	lessonID, err := store.Start(ctx, []int64{vocabularyID})
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.Current(ctx, lessonID)
	if err != nil {
		t.Fatal(err)
	}
	if session.StudyItem.AudioID != audioID || session.StudyItem.PictureID != pictureID {
		t.Fatalf("lesson media = audio %d, picture %d; want %d and %d", session.StudyItem.AudioID, session.StudyItem.PictureID, audioID, pictureID)
	}
	if !session.AudioEnabled {
		t.Fatal("lesson did not load the automatic audio setting")
	}
}

func attachLessonTestMedia(t *testing.T, db *sql.DB, vocabularyID int64, purpose, kind, mimeType, checksumCharacter string) int64 {
	t.Helper()
	result, err := db.Exec(
		"INSERT INTO media (kind, mime_type, sha256, created_at) VALUES (?, ?, ?, 1)",
		kind,
		mimeType,
		strings.Repeat(checksumCharacter, 64),
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
		"INSERT INTO vocabulary_media (vocabulary_id, purpose, media_id) VALUES (?, ?, ?)",
		vocabularyID,
		purpose,
		mediaID,
	); err != nil {
		t.Fatal(err)
	}
	return mediaID
}

func TestLessonBatchesActivateIndependently(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "lesson.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	ids := insertUnlearnedVocabulary(t, db, 12)
	lessonStore := NewStore(db)
	reviewStore := reviews.NewStore(db)
	sessionID, err := lessonStore.Start(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}

	session, err := lessonStore.Current(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.BatchCount != 3 || len(session.Items) != BatchSize {
		t.Fatalf("initial batch = %d items across %d batches, want 5 items across 3 batches", len(session.Items), session.BatchCount)
	}

	for batch := 0; batch < 3; batch++ {
		for position := range session.Items {
			if err := lessonStore.SelectStudyItem(ctx, sessionID, position); err != nil {
				t.Fatal(err)
			}
		}
		session, err = lessonStore.Current(ctx, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if err := lessonStore.MarkCurrentViewed(ctx, sessionID); err != nil {
			t.Fatal(err)
		}
		reviewID, err := reviewStore.StartLesson(ctx, sessionID, itemIDs(session.Items))
		if err != nil {
			t.Fatal(err)
		}
		reviewing, err := lessonStore.Current(ctx, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if reviewing.Phase != "review" || reviewing.ReviewSessionID != reviewID {
			t.Fatalf("batch %d review link = phase %q, review %d; want review %d", batch+1, reviewing.Phase, reviewing.ReviewSessionID, reviewID)
		}
		completeLessonReview(t, ctx, reviewStore, reviewID)
		if err := lessonStore.CompleteReviewedBatch(ctx, sessionID); err != nil {
			t.Fatal(err)
		}

		session, err = lessonStore.Current(ctx, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		wantActive := (batch + 1) * BatchSize
		if batch == 2 {
			wantActive = 12
		}
		var active int
		if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary WHERE status = 'active'").Scan(&active); err != nil {
			t.Fatal(err)
		}
		if active != wantActive {
			t.Fatalf("after batch %d active words = %d, want %d", batch+1, active, wantActive)
		}
		if batch < 2 && (session.Phase != "study" || session.CurrentBatch != batch+1) {
			t.Fatalf("after batch %d session = phase %q, batch %d", batch+1, session.Phase, session.CurrentBatch)
		}
	}
	if session.Status != "completed" || session.Phase != "review" || session.Completed != 12 {
		t.Fatalf("final lesson = status %q, phase %q, completed %d", session.Status, session.Phase, session.Completed)
	}
}

func TestLessonActivationUsesReviewCompletionTime(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "lesson-completion-time.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	lessonStore := NewStore(db)
	reviewStore := reviews.NewStore(db)
	vocabularyID := insertUnlearnedVocabulary(t, db, 1)[0]
	lessonID, err := lessonStore.Start(ctx, []int64{vocabularyID})
	if err != nil {
		t.Fatal(err)
	}
	session, err := lessonStore.Current(ctx, lessonID)
	if err != nil {
		t.Fatal(err)
	}
	if err := lessonStore.MarkCurrentViewed(ctx, lessonID); err != nil {
		t.Fatal(err)
	}
	reviewID, err := reviewStore.StartLesson(ctx, lessonID, itemIDs(session.Items))
	if err != nil {
		t.Fatal(err)
	}
	completeLessonReview(t, ctx, reviewStore, reviewID)
	const completedAt int64 = 1_700_000_000
	if _, err := db.Exec("UPDATE review_sessions SET completed_at = ? WHERE id = ?", completedAt, reviewID); err != nil {
		t.Fatal(err)
	}
	if err := lessonStore.CompleteReviewedBatch(ctx, lessonID); err != nil {
		t.Fatal(err)
	}
	completed, err := lessonStore.Current(ctx, lessonID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "completed" {
		t.Fatalf("lesson status = %q, want completed", completed.Status)
	}

	var learnedAt, dueAt, itemCompletedAt int64
	if err := db.QueryRow(`
		SELECT v.lesson_completed_at, ss.due_at, lsi.review_completed_at
		FROM vocabulary v
		JOIN srs_states ss ON ss.vocabulary_id = v.id
		JOIN lesson_session_items lsi ON lsi.vocabulary_id = v.id
		WHERE v.id = ?`, vocabularyID).Scan(
		&learnedAt, &dueAt, &itemCompletedAt,
	); err != nil {
		t.Fatal(err)
	}
	if learnedAt != completedAt || itemCompletedAt != completedAt {
		t.Fatalf("completion timestamps = learned %d, item %d; want %d", learnedAt, itemCompletedAt, completedAt)
	}
	if wantDue := completedAt + int64(4*time.Hour/time.Second); dueAt != wantDue {
		t.Fatalf("due_at = %d, want %d", dueAt, wantDue)
	}
}

func TestActiveLessonReservesVocabulary(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "lesson-reservation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	ids := insertUnlearnedVocabulary(t, db, 2)
	store := NewStore(db)
	sessionID, err := store.Start(ctx, ids[:1])
	if err != nil {
		t.Fatal(err)
	}
	activeSessionID, found, err := store.ActiveSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !found || activeSessionID != sessionID {
		t.Fatalf("active lesson = %d, %t; want %d, true", activeSessionID, found, sessionID)
	}

	available, err := store.AvailablePage(ctx, maximumAvailablePageSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 1 || available[0].ID != ids[1] {
		t.Fatalf("available vocabulary = %+v, want only %d", available, ids[1])
	}
	next, err := store.NextBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0] != ids[1] {
		t.Fatalf("next batch = %v, want [%d]", next, ids[1])
	}

	if _, err := store.Start(ctx, ids[1:]); err == nil || !strings.Contains(err.Error(), "current lesson") {
		t.Fatalf("second start error = %v, want current lesson conflict", err)
	}
	var sessionCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM lesson_sessions").Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if sessionCount != 1 {
		t.Fatalf("lesson session count = %d, want 1", sessionCount)
	}
}

func TestReturnToQueueReleasesUnfinishedVocabulary(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "return-lesson.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	ids := insertUnlearnedVocabulary(t, db, 2)
	store := NewStore(db)
	sessionID, err := store.Start(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReturnToQueue(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	available, err := store.AvailablePage(ctx, maximumAvailablePageSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != len(ids) {
		t.Fatalf("available vocabulary = %+v, want %d returned words", available, len(ids))
	}
	if _, found, err := store.ActiveSession(ctx); err != nil || found {
		t.Fatalf("active lesson after return = found %t, error %v", found, err)
	}
}

func TestReturnToQueueDoesNotAbandonReviewPhase(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "return-review-lesson.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	ids := insertUnlearnedVocabulary(t, db, 1)
	store := NewStore(db)
	sessionID, err := store.Start(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE lesson_sessions SET phase = 'review' WHERE id = ?`, sessionID); err != nil {
		t.Fatal(err)
	}

	if err := store.ReturnToQueue(ctx, sessionID); err == nil {
		t.Fatal("returning a lesson in review phase succeeded")
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM lesson_sessions WHERE id = ?`, sessionID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("lesson status = %q, want active", status)
	}
}

func TestAvailablePageBoundsItemsWithoutSelectingForTheUser(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "lesson-pages.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	ids := insertUnlearnedVocabulary(t, db, 105)
	store := NewStore(db)

	count, err := store.AvailableCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != len(ids) {
		t.Fatalf("available count = %d, want %d", count, len(ids))
	}
	first, err := store.AvailablePage(ctx, maximumAvailablePageSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AvailablePage(ctx, maximumAvailablePageSize, maximumAvailablePageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != maximumAvailablePageSize || len(second) != 5 || second[0].ID != ids[100] {
		t.Fatalf("available pages = %d and %+v", len(first), second)
	}
	for _, item := range append(first, second...) {
		if item.Selected {
			t.Fatalf("available item %d was selected without user input", item.ID)
		}
	}
	if _, err := store.AvailablePage(ctx, maximumAvailablePageSize+1, 0); err == nil {
		t.Fatal("oversized lesson page was accepted")
	}
	if _, err := store.Start(ctx, make([]int64, maximumAvailablePageSize+1)); err == nil || !strings.Contains(err.Error(), "at most 100") {
		t.Fatalf("oversized lesson error = %v, want selection limit", err)
	}
}

func TestKnownElsewhereVocabularyNeverEntersLessons(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "known-elsewhere.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	id := insertUnlearnedVocabulary(t, db, 1)[0]
	if _, err := db.Exec("UPDATE vocabulary SET known_elsewhere_at = ? WHERE id = ?", time.Now().UTC().Unix(), id); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	available, err := store.AvailablePage(ctx, maximumAvailablePageSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 0 {
		t.Fatalf("available vocabulary = %+v, want none", available)
	}
	if _, err := store.NextBatch(ctx); err == nil || !strings.Contains(err.Error(), "no new words") {
		t.Fatalf("next batch error = %v, want no new words", err)
	}
	if _, err := store.Start(ctx, []int64{id}); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("start error = %v, want unavailable word", err)
	}
	var sessionCount, srsCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM lesson_sessions").Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM srs_states").Scan(&srsCount); err != nil {
		t.Fatal(err)
	}
	if sessionCount != 0 || srsCount != 0 {
		t.Fatalf("created %d lesson sessions and %d SRS states", sessionCount, srsCount)
	}
}

func TestSparseVocabularyNeverEntersLessons(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "lesson-sparse.sqlite"))
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
	now := time.Now().UTC().Unix()
	result, err := db.Exec(`
		INSERT INTO vocabulary (
			expression, normalized_expression, pronunciation, normalized_pronunciation,
			status, created_at, updated_at
		) VALUES ('既知語', '既知語', '', '', 'unlearned', ?, ?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)

	count, err := store.AvailableCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("available lesson count = %d, want 0", count)
	}
	items, err := store.AvailablePage(ctx, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("available sparse vocabulary = %+v", items)
	}
	if _, err := store.Start(ctx, []int64{id}); err == nil {
		t.Fatal("lesson started with sparse vocabulary")
	}
	var sessions int
	if err := db.QueryRow("SELECT COUNT(*) FROM lesson_sessions").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("failed lesson start left %d sessions", sessions)
	}
}

func TestCurrentDoesNotRecordStudyViews(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "abandoned-lesson.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	ids := insertUnlearnedVocabulary(t, db, 2)
	sessionID, err := store.Start(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Current(ctx, sessionID); err != nil {
		t.Fatal(err)
	}

	var viewedAt sql.NullInt64
	if err := db.QueryRow("SELECT study_viewed_at FROM lesson_session_items WHERE session_id = ? AND position = 1", sessionID).Scan(&viewedAt); err != nil {
		t.Fatal(err)
	}
	if viewedAt.Valid {
		t.Fatalf("reading a lesson recorded study view at %d", viewedAt.Int64)
	}
}

func TestAbandonedLessonDoesNotSynchronizeCompletedReview(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "abandoned-review.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	vocabularyID := insertUnlearnedVocabulary(t, db, 1)[0]
	lessonResult, err := db.Exec(`
		INSERT INTO lesson_sessions (status, phase, current_batch)
		VALUES ('abandoned', 'review', 0)`)
	if err != nil {
		t.Fatal(err)
	}
	lessonID, err := lessonResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO lesson_session_items (session_id, vocabulary_id, position, batch_number)
		VALUES (?, ?, 0, 0)`, lessonID, vocabularyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO review_sessions (kind, status, completed_at, lesson_session_id)
		VALUES ('extra', 'completed', 200, ?)`, lessonID); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	if err := store.CompleteReviewedBatch(ctx, lessonID); err != nil {
		t.Fatal(err)
	}
	session, err := store.Current(ctx, lessonID)
	if err != nil {
		t.Fatal(err)
	}
	if session.Status != "abandoned" || session.Phase != "review" {
		t.Fatalf("lesson = status %q, phase %q; want abandoned review", session.Status, session.Phase)
	}
	var vocabularyStatus string
	var reviewCompletedAt sql.NullInt64
	var srsCount int
	if err := db.QueryRow(`
		SELECT v.status, lsi.review_completed_at,
		       (SELECT COUNT(*) FROM srs_states ss WHERE ss.vocabulary_id = v.id)
		FROM vocabulary v
		JOIN lesson_session_items lsi ON lsi.vocabulary_id = v.id
		WHERE v.id = ?`, vocabularyID).Scan(
		&vocabularyStatus, &reviewCompletedAt, &srsCount,
	); err != nil {
		t.Fatal(err)
	}
	if vocabularyStatus != "unlearned" || reviewCompletedAt.Valid || srsCount != 0 {
		t.Fatalf(
			"abandoned lesson mutated: vocabulary %q, item completion %v, SRS rows %d",
			vocabularyStatus, reviewCompletedAt, srsCount,
		)
	}
}

func TestStaleLessonCompletionPreservesActiveSRSState(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "stale-lesson.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	id := insertUnlearnedVocabulary(t, db, 1)[0]
	if _, err := db.Exec("UPDATE vocabulary SET status = 'active', lesson_completed_at = 100 WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO srs_states (vocabulary_id, stage, due_at, last_reviewed_at)
		VALUES (?, 4, 900, 800)`, id); err != nil {
		t.Fatal(err)
	}

	lessonResult, err := db.Exec(`
		INSERT INTO lesson_sessions (status, phase, current_batch)
		VALUES ('active', 'review', 0)`)
	if err != nil {
		t.Fatal(err)
	}
	lessonID, err := lessonResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO lesson_session_items (session_id, vocabulary_id, position, batch_number)
		VALUES (?, ?, 0, 0)`, lessonID, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO review_sessions (kind, status, completed_at, lesson_session_id)
		VALUES ('extra', 'completed', 300, ?)`, lessonID); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	if err := store.CompleteReviewedBatch(ctx, lessonID); err != nil {
		t.Fatal(err)
	}
	session, err := store.Current(ctx, lessonID)
	if err != nil {
		t.Fatal(err)
	}
	if session.Status != "completed" {
		t.Fatalf("lesson status = %q, want completed", session.Status)
	}
	var status string
	var stage, dueAt, lastReviewedAt int64
	if err := db.QueryRow(`
		SELECT v.status, ss.stage, ss.due_at, ss.last_reviewed_at
		FROM vocabulary v
		JOIN srs_states ss ON ss.vocabulary_id = v.id
		WHERE v.id = ?`, id).Scan(&status, &stage, &dueAt, &lastReviewedAt); err != nil {
		t.Fatal(err)
	}
	if status != "active" || stage != 4 || dueAt != 900 || lastReviewedAt != 800 {
		t.Fatalf("vocabulary state = status %q, stage %d, due %d, reviewed %d", status, stage, dueAt, lastReviewedAt)
	}
}

func completeLessonReview(t *testing.T, ctx context.Context, store *reviews.Store, sessionID int64) {
	t.Helper()
	for {
		state, err := store.State(ctx, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if state.Status == "completed" {
			return
		}
		answer := "to eat"
		if state.PromptType == "pronunciation" {
			answer = "たべる"
		}
		if _, err := store.Answer(ctx, sessionID, state.PromptID, answer); err != nil {
			t.Fatal(err)
		}
	}
}

func itemIDs(items []StudyItem) []int64 {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func insertUnlearnedVocabulary(t *testing.T, db *sql.DB, count int) []int64 {
	t.Helper()
	ids := make([]int64, 0, count)
	now := time.Now().UTC().Unix()
	for index := 0; index < count; index++ {
		expression := fmt.Sprintf("word-%d", index)
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
		if _, err := db.Exec(`INSERT INTO meanings (vocabulary_id, position, text, normalized_text) VALUES (?, 0, 'to eat', 'to eat')`, id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}
