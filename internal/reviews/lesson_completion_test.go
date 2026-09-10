package reviews

import (
	"context"
	"database/sql"
	"testing"
)

func TestLessonFinalAnswerRollsBackIfActivationFails(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	store := NewStore(db)
	lessonID, reviewID, vocabularyID := createLessonReview(t, ctx, db, store)
	first, err := store.State(ctx, reviewID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmAnswer(ctx, reviewID, first.PromptID, answerForState(first)); err != nil {
		t.Fatal(err)
	}
	last, err := store.State(ctx, reviewID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_lesson_schedule BEFORE INSERT ON srs_states BEGIN SELECT RAISE(FAIL, 'schedule write failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmAnswer(ctx, reviewID, last.PromptID, answerForState(last)); err == nil {
		t.Fatal("final answer succeeded despite failed lesson activation")
	}
	after, err := store.State(ctx, reviewID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "active" || after.PromptID != last.PromptID || after.Attempts != last.Attempts {
		t.Fatalf("failed activation committed the final answer: before = %+v, after = %+v", last, after)
	}
	var lessonStatus, vocabularyStatus string
	var completedAt sql.NullInt64
	if err := db.QueryRow(`SELECT ls.status, v.status, lsi.review_completed_at
		FROM lesson_sessions ls JOIN lesson_session_items lsi ON lsi.session_id = ls.id
		JOIN vocabulary v ON v.id = lsi.vocabulary_id WHERE ls.id = ?`, lessonID).Scan(&lessonStatus, &vocabularyStatus, &completedAt); err != nil {
		t.Fatal(err)
	}
	if lessonStatus != "active" || vocabularyStatus != "unlearned" || completedAt.Valid {
		t.Fatalf("failed activation changed lesson state: %s / %s / %v", lessonStatus, vocabularyStatus, completedAt)
	}
	for _, table := range []string{"review_results", "srs_states"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("failed final answer left %d %s rows", count, table)
		}
	}
	if _, err := db.Exec("DROP TRIGGER reject_lesson_schedule"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmAnswer(ctx, reviewID, last.PromptID, answerForState(last)); err != nil {
		t.Fatal(err)
	}
	var reviewTime, lessonTime, learnedTime, dueTime int64
	if err := db.QueryRow(`SELECT rs.completed_at, lsi.review_completed_at, v.lesson_completed_at, ss.due_at
		FROM review_sessions rs JOIN lesson_session_items lsi ON lsi.session_id = rs.lesson_session_id
		JOIN vocabulary v ON v.id = lsi.vocabulary_id JOIN srs_states ss ON ss.vocabulary_id = v.id
		WHERE rs.id = ? AND v.id = ?`, reviewID, vocabularyID).Scan(&reviewTime, &lessonTime, &learnedTime, &dueTime); err != nil {
		t.Fatal(err)
	}
	if reviewTime != lessonTime || reviewTime != learnedTime || dueTime != reviewTime+4*60*60 {
		t.Fatalf("completion times = review %d, lesson %d, learned %d, due %d", reviewTime, lessonTime, learnedTime, dueTime)
	}
}

func TestLegacyLessonReconciliationCanRetryAndDoesNotRepeatActivation(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	store := NewStore(db)
	lessonID, reviewID, vocabularyID := createLessonReview(t, ctx, db, store)
	const completedAt = 1_700_000_000
	if _, err := db.Exec(`UPDATE review_sessions SET status = 'completed', completed_at = ? WHERE id = ?`, completedAt, reviewID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_legacy_schedule BEFORE INSERT ON srs_states BEGIN SELECT RAISE(FAIL, 'schedule write failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileCompletedLessons(ctx); err == nil {
		t.Fatal("reconciliation ignored the activation failure")
	}
	var status string
	var itemTime sql.NullInt64
	if err := db.QueryRow(`SELECT v.status, lsi.review_completed_at
		FROM vocabulary v JOIN lesson_session_items lsi ON lsi.vocabulary_id = v.id WHERE v.id = ?`, vocabularyID).Scan(&status, &itemTime); err != nil {
		t.Fatal(err)
	}
	if status != "unlearned" || itemTime.Valid {
		t.Fatalf("failed reconciliation left partial activation: %q / %v", status, itemTime)
	}
	if _, err := db.Exec("DROP TRIGGER reject_legacy_schedule"); err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileCompletedLessons(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT status FROM lesson_sessions WHERE id = ?", lessonID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("reconciled lesson status = %q", status)
	}
	var learnedAt, dueAt int64
	if err := db.QueryRow(`SELECT v.lesson_completed_at, ss.due_at FROM vocabulary v
		JOIN srs_states ss ON ss.vocabulary_id = v.id WHERE v.id = ?`, vocabularyID).Scan(&learnedAt, &dueAt); err != nil {
		t.Fatal(err)
	}
	if learnedAt != completedAt || dueAt != completedAt+4*60*60 {
		t.Fatalf("reconciled timing = %d / %d", learnedAt, dueAt)
	}
	if _, err := db.Exec("PRAGMA query_only = ON"); err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileCompletedLessons(ctx); err != nil {
		t.Fatalf("repeated reconciliation attempted to write: %v", err)
	}
}

func TestLegacyLessonReconciliationIgnoresAnOlderCompletedReview(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	store := NewStore(db)
	lessonID, reviewID, vocabularyID := createLessonReview(t, ctx, db, store)
	if _, err := db.Exec(`UPDATE review_sessions SET status = 'completed', completed_at = 100 WHERE id = ?`, reviewID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO review_sessions (kind, status, lesson_session_id) VALUES ('extra', 'active', ?)`, lessonID); err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileCompletedLessons(ctx); err != nil {
		t.Fatal(err)
	}
	var lessonStatus, vocabularyStatus string
	if err := db.QueryRow(`SELECT ls.status, v.status FROM lesson_sessions ls
		JOIN lesson_session_items lsi ON lsi.session_id = ls.id JOIN vocabulary v ON v.id = lsi.vocabulary_id
		WHERE ls.id = ? AND v.id = ?`, lessonID, vocabularyID).Scan(&lessonStatus, &vocabularyStatus); err != nil {
		t.Fatal(err)
	}
	if lessonStatus != "active" || vocabularyStatus != "unlearned" {
		t.Fatalf("older completion activated a newer lesson review: %s / %s", lessonStatus, vocabularyStatus)
	}
}

func createLessonReview(t *testing.T, ctx context.Context, db *sql.DB, store *Store) (lessonID, reviewID, vocabularyID int64) {
	t.Helper()
	vocabularyID = insertUnlearnedReviewVocabulary(t, db, "食べる")
	result, err := db.Exec(`INSERT INTO lesson_sessions (status, phase) VALUES ('active', 'study')`)
	if err != nil {
		t.Fatal(err)
	}
	lessonID, err = result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO lesson_session_items (session_id, vocabulary_id, position, batch_number, study_viewed_at)
		VALUES (?, ?, 0, 0, 1)`, lessonID, vocabularyID); err != nil {
		t.Fatal(err)
	}
	reviewID, err = store.StartLesson(ctx, lessonID, []int64{vocabularyID})
	if err != nil {
		t.Fatal(err)
	}
	return lessonID, reviewID, vocabularyID
}
