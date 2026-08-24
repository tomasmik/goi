package statistics

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomasmik/goi/internal/database"
)

func TestStudyStreakUsesConfiguredCalendarDays(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "statistics.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 24, 18, 0, 0, 0, time.UTC)
	insertLessonActivity(t, db, "today", now, true)
	insertLessonActivity(t, db, "today again", now.Add(-time.Hour), true)
	insertLessonActivity(t, db, "yesterday", now.AddDate(0, 0, -1), true)
	insertLessonActivity(t, db, "viewed only", now.AddDate(0, 0, -2), false)

	streak, err := NewStore(db, time.UTC).studyStreak(ctx, now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if streak != 2 {
		t.Fatalf("streak = %d, want 2", streak)
	}
}

func TestStudyStreakIncludesNormalReviews(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "statistics.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 24, 18, 0, 0, 0, time.UTC)
	vocabularyID := insertMistakeVocabulary(t, db, "食べる")
	insertNormalFailure(t, db, vocabularyID, now.Unix())

	streak, err := NewStore(db, time.UTC).studyStreak(ctx, now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if streak != 1 {
		t.Fatalf("streak = %d, want 1", streak)
	}
}

func TestStudyStreakUsesConfiguredTimeZoneAcrossDaylightSaving(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "statistics.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_settings (id, time_zone) VALUES (1, ?)`, location.String()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.March, 9, 0, 30, 0, 0, location)
	insertLessonActivity(t, db, "today", time.Date(2026, time.March, 9, 0, 15, 0, 0, location), true)
	insertLessonActivity(t, db, "yesterday", time.Date(2026, time.March, 8, 1, 30, 0, 0, location), true)

	summary, err := NewStore(db, time.UTC).Summary(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Streak != 2 {
		t.Fatalf("streak = %d, want 2", summary.Streak)
	}
}

func TestSummaryRejectsInvalidConfiguredTimeZone(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "timezone.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO user_settings (id, time_zone)
		VALUES (1, 'not/a-time-zone')`); err != nil {
		t.Fatal(err)
	}

	_, err = NewStore(db, time.UTC).Summary(ctx, time.Now())
	if err == nil || !strings.Contains(err.Error(), `load configured time zone "not/a-time-zone"`) {
		t.Fatalf("Summary() error = %v", err)
	}
}

func TestRecentMistakesExcludeWordsOutsideActiveStudy(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "recent-mistakes.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	wordResult, err := db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, status, created_at, updated_at)
		VALUES ('食べる', '食べる', 'active', ?, ?)`, now.Unix(), now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	vocabularyID, err := wordResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	sessionResult, err := db.Exec(`
		INSERT INTO review_sessions (kind, status, completed_at)
		VALUES ('normal', 'completed', ?)`, now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := sessionResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	itemResult, err := db.Exec(`
		INSERT INTO review_session_items (session_id, vocabulary_id, position, status)
		VALUES (?, ?, 0, 'completed')`, sessionID, vocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := itemResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO review_results (
			session_item_id, outcome, stage_before, stage_after, created_at,
			mistake_visibility_existed_before
		)
		VALUES (?, 'failure', 0, 0, ?, 0)`, itemID, now.Unix()); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db, time.UTC)
	mistakes, err := store.RecentMistakes(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(mistakes) != 1 {
		t.Fatalf("active recent mistakes = %d, want 1", len(mistakes))
	}
	if _, err := db.Exec("DELETE FROM vocabulary WHERE id = ?", vocabularyID); err != nil {
		t.Fatal(err)
	}
	mistakes, err = store.RecentMistakes(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(mistakes) != 0 {
		t.Fatalf("deleted recent mistakes = %d, want 0", len(mistakes))
	}
}

func TestHideMistakeOnlyDismissesExistingFailures(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "mistake-visibility.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	vocabularyID := insertMistakeVocabulary(t, db, "食べる")
	initialFailure := time.Now().UTC().Add(-time.Minute).Unix()
	insertNormalFailure(t, db, vocabularyID, initialFailure)
	store := NewStore(db, time.UTC)
	if err := store.HideMistake(ctx, vocabularyID); err != nil {
		t.Fatal(err)
	}
	mistakes, err := store.RecentMistakes(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(mistakes) != 0 {
		t.Fatalf("mistakes after hide = %+v, want none", mistakes)
	}

	var hiddenAt int64
	if err := db.QueryRow("SELECT hidden_at FROM mistake_visibility WHERE vocabulary_id = ?", vocabularyID).Scan(&hiddenAt); err != nil {
		t.Fatal(err)
	}
	insertNormalFailure(t, db, vocabularyID, hiddenAt+1)
	mistakes, err = store.RecentMistakes(ctx, time.Unix(hiddenAt+1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(mistakes) != 1 || mistakes[0].ID != vocabularyID {
		t.Fatalf("mistakes after newer failure = %+v, want vocabulary %d", mistakes, vocabularyID)
	}
}

func TestRecentMistakesLimitReturnsMostRecent(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "limited-mistakes.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	olderID := insertMistakeVocabulary(t, db, "older")
	newerID := insertMistakeVocabulary(t, db, "newer")
	insertNormalFailure(t, db, olderID, now.Add(-2*time.Minute).Unix())
	insertNormalFailure(t, db, newerID, now.Add(-time.Minute).Unix())

	mistakes, err := NewStore(db, time.UTC).RecentMistakesLimit(ctx, now, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(mistakes) != 1 || mistakes[0].ID != newerID {
		t.Fatalf("limited mistakes = %+v, want vocabulary %d", mistakes, newerID)
	}
	if _, err := NewStore(db, time.UTC).RecentMistakesLimit(ctx, now, -1); err == nil {
		t.Fatal("RecentMistakesLimit() accepted a negative limit")
	}
}

func TestHideMistakeRejectsUnavailableVocabulary(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "unavailable-mistake.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db, time.UTC)
	if err := store.HideMistake(ctx, 999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing vocabulary error = %v, want sql.ErrNoRows", err)
	}
	nonMistakeID := insertMistakeVocabulary(t, db, "食べる")
	if err := store.HideMistake(ctx, nonMistakeID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("non-mistake vocabulary error = %v, want sql.ErrNoRows", err)
	}
	staleMistakeID := insertMistakeVocabulary(t, db, "見る")
	insertNormalFailure(t, db, staleMistakeID, time.Now().UTC().Add(-25*time.Hour).Unix())
	if err := store.HideMistake(ctx, staleMistakeID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale mistake error = %v, want sql.ErrNoRows", err)
	}
	deletedMistakeID := insertMistakeVocabulary(t, db, "読む")
	insertNormalFailure(t, db, deletedMistakeID, time.Now().UTC().Unix())
	if _, err := db.Exec("DELETE FROM vocabulary WHERE id = ?", deletedMistakeID); err != nil {
		t.Fatal(err)
	}
	if err := store.HideMistake(ctx, deletedMistakeID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted vocabulary error = %v, want sql.ErrNoRows", err)
	}
}

func insertLessonActivity(t *testing.T, db *sql.DB, expression string, completedAt time.Time, reviewed bool) {
	t.Helper()
	result, err := db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, status, created_at, updated_at)
		VALUES (?, ?, 'active', ?, ?)`, expression, expression, completedAt.Unix(), completedAt.Unix())
	if err != nil {
		t.Fatal(err)
	}
	vocabularyID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	sessionResult, err := db.Exec(`
		INSERT INTO lesson_sessions (status, phase)
		VALUES ('completed', 'review')`)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := sessionResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	var reviewCompletedAt any
	if reviewed {
		reviewCompletedAt = completedAt.Unix()
	}
	if _, err := db.Exec(`
		INSERT INTO lesson_session_items (
			session_id, vocabulary_id, position, review_completed_at
		)
		VALUES (?, ?, 0, ?)`, sessionID, vocabularyID, reviewCompletedAt); err != nil {
		t.Fatal(err)
	}
}

func insertMistakeVocabulary(t *testing.T, db *sql.DB, expression string) int64 {
	t.Helper()
	now := time.Now().UTC().Unix()
	result, err := db.Exec(`
		INSERT INTO vocabulary (
			expression, normalized_expression, pronunciation, normalized_pronunciation,
			status, created_at, updated_at
		)
		VALUES (?, ?, 'たべる', 'たべる', 'active', ?, ?)`, expression, expression, now, now)
	if err != nil {
		t.Fatal(err)
	}
	vocabularyID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO meanings (vocabulary_id, position, text, normalized_text)
		VALUES (?, 0, 'to eat', 'to eat')`, vocabularyID); err != nil {
		t.Fatal(err)
	}
	return vocabularyID
}

func insertNormalFailure(t *testing.T, db *sql.DB, vocabularyID, createdAt int64) {
	t.Helper()
	sessionResult, err := db.Exec(`
		INSERT INTO review_sessions (kind, status, completed_at)
		VALUES ('normal', 'completed', ?)`, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := sessionResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	itemResult, err := db.Exec(`
		INSERT INTO review_session_items (session_id, vocabulary_id, position, status)
		VALUES (?, ?, 0, 'completed')`, sessionID, vocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := itemResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO review_results (
			session_item_id, outcome, stage_before, stage_after, created_at,
			mistake_visibility_existed_before
		)
		VALUES (?, 'failure', 0, 0, ?, 0)`, itemID, createdAt); err != nil {
		t.Fatal(err)
	}
}
