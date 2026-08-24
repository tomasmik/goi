package reviews

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestLeechLifecycleMarksSuspendsAndRecovers(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	insertLeechSettings(t, db, 5, 3, 3)
	vocabularyID := firstReviewVocabularyID(t, db)
	now := time.Unix(1000, 0).UTC()

	for index := range 5 {
		recordLeechResult(t, ctx, db, vocabularyID, false, true, now.Add(time.Duration(index)*time.Second))
	}
	state := readLeechState(t, ctx, db, vocabularyID)
	if !state.active || !state.everLeech || state.failuresSinceMark != 0 {
		t.Fatalf("marked state = %+v", state)
	}

	for index := range 3 {
		recordLeechResult(t, ctx, db, vocabularyID, false, false, now.Add(time.Duration(10+index)*time.Second))
	}
	state = readLeechState(t, ctx, db, vocabularyID)
	if !state.active || !state.autoSuspendedAt.Valid || state.failuresSinceMark != 3 {
		t.Fatalf("suspended state = %+v", state)
	}
	var vocabularyStatus string
	var suspendedAt sql.NullInt64
	if err := db.QueryRow(`
		SELECT v.status, s.suspended_at
		FROM vocabulary v JOIN srs_states s ON s.vocabulary_id = v.id
		WHERE v.id = ?`, vocabularyID).Scan(&vocabularyStatus, &suspendedAt); err != nil {
		t.Fatal(err)
	}
	if vocabularyStatus != "suspended" || !suspendedAt.Valid {
		t.Fatalf("automatic suspension = status %q, suspended at %v", vocabularyStatus, suspendedAt)
	}

	for index := range 3 {
		recordLeechResult(t, ctx, db, vocabularyID, true, false, now.Add(time.Duration(20+index)*time.Second))
	}
	state = readLeechState(t, ctx, db, vocabularyID)
	if state.active || !state.everLeech || !state.clearedAt.Valid {
		t.Fatalf("recovered state = %+v", state)
	}
	if err := db.QueryRow(`
		SELECT v.status, s.suspended_at
		FROM vocabulary v JOIN srs_states s ON s.vocabulary_id = v.id
		WHERE v.id = ?`, vocabularyID).Scan(&vocabularyStatus, &suspendedAt); err != nil {
		t.Fatal(err)
	}
	if vocabularyStatus != "active" || suspendedAt.Valid {
		t.Fatalf("recovered review state = status %q, suspended at %v", vocabularyStatus, suspendedAt)
	}
}

func TestUndoRebuildsLeechState(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	insertLeechSettings(t, db, 1, 3, 3)
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
	leech := readLeechState(t, ctx, db, state.VocabularyID)
	if !leech.active {
		t.Fatal("failed review did not mark the word as a leech")
	}
	if err := store.Undo(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	leech = readLeechState(t, ctx, db, state.VocabularyID)
	if leech.active || leech.everLeech || leech.failuresTowardLeech != 0 {
		t.Fatalf("leech state after undo = %+v", leech)
	}
}

func TestUndoFailureThatSuspendedLeechResumesReviews(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	insertLeechSettings(t, db, 1, 1, 3)
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
	if err := store.Continue(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	state, err = store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "wrong"); err != nil {
		t.Fatal(err)
	}

	leech := readLeechState(t, ctx, db, state.VocabularyID)
	if !leech.autoSuspendedAt.Valid || leech.failuresSinceMark != 1 {
		t.Fatalf("leech before undo = %+v", leech)
	}
	if err := store.Undo(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	leech = readLeechState(t, ctx, db, state.VocabularyID)
	if !leech.active || leech.autoSuspendedAt.Valid || leech.failuresSinceMark != 0 {
		t.Fatalf("leech after undo = %+v", leech)
	}
	assertReviewStatus(t, db, state.VocabularyID, "active", false)
}

func TestUndoSuccessThatClearedLeechRestoresIt(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	insertLeechSettings(t, db, 1, 3, 1)
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
	if err := store.Continue(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	completeCurrentItem(t, ctx, store, sessionID)

	leech := readLeechState(t, ctx, db, state.VocabularyID)
	if leech.active || !leech.clearedAt.Valid {
		t.Fatalf("leech before undo = %+v", leech)
	}
	if err := store.Undo(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	leech = readLeechState(t, ctx, db, state.VocabularyID)
	if !leech.active || !leech.everLeech || leech.clearedAt.Valid || leech.correctStreak != 0 {
		t.Fatalf("leech after undo = %+v", leech)
	}
}

func TestSuspendedLeechesRemainAvailableForPractice(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	insertLeechSettings(t, db, 1, 1, 3)
	vocabularyID := firstReviewVocabularyID(t, db)
	now := time.Unix(1000, 0).UTC()
	recordLeechResult(t, ctx, db, vocabularyID, false, true, now)
	recordLeechResult(t, ctx, db, vocabularyID, false, false, now.Add(time.Second))

	store := NewStore(db)
	items, err := store.Leeches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != vocabularyID || !items[0].Suspended {
		t.Fatalf("suspended leeches = %+v", items)
	}
	if _, err := store.StartExtraSource(ctx, "leeches", nil); err != nil {
		t.Fatalf("start suspended leech practice: %v", err)
	}
}

func TestManuallySuspendedLeechCanBePracticedIndividually(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	insertLeechSettings(t, db, 1, 3, 3)
	vocabularyID := firstReviewVocabularyID(t, db)
	recordLeechResult(t, ctx, db, vocabularyID, false, true, time.Unix(1000, 0).UTC())
	if _, err := db.Exec(`UPDATE vocabulary SET status = 'suspended' WHERE id = ?`, vocabularyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE srs_states SET suspended_at = 1001 WHERE vocabulary_id = ?`, vocabularyID); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	items, err := store.Leeches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].Suspended {
		t.Fatalf("manually suspended leeches = %+v", items)
	}
	if _, err := store.StartExtraSource(ctx, "selected", []int64{vocabularyID}); err != nil {
		t.Fatalf("practice manually suspended leech: %v", err)
	}
}

func insertLeechSettings(t *testing.T, db *sql.DB, markAfter, suspendAfter, clearAfter int) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO user_settings (
			id, retry_count, leech_failure_threshold, leech_suspend_threshold, leech_recovery_streak
		) VALUES (1, 1, ?, ?, ?)`, markAfter, suspendAfter, clearAfter); err != nil {
		t.Fatal(err)
	}
}

func firstReviewVocabularyID(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT id FROM vocabulary ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func recordLeechResult(t *testing.T, ctx context.Context, db *sql.DB, vocabularyID int64, success, srsApplied bool, now time.Time) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := updateLeechAfterResultTx(ctx, tx, vocabularyID, success, srsApplied, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func readLeechState(t *testing.T, ctx context.Context, db *sql.DB, vocabularyID int64) leechState {
	t.Helper()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	state, err := loadLeechStateTx(ctx, tx, vocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func assertReviewStatus(t *testing.T, db *sql.DB, vocabularyID int64, wantStatus string, wantSuspended bool) {
	t.Helper()
	var status string
	var suspendedAt sql.NullInt64
	if err := db.QueryRow(`
		SELECT v.status, s.suspended_at
		FROM vocabulary v
		JOIN srs_states s ON s.vocabulary_id = v.id
		WHERE v.id = ?`, vocabularyID).Scan(&status, &suspendedAt); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus || suspendedAt.Valid != wantSuspended {
		t.Fatalf("review status = %q, suspended %t; want %q, suspended %t", status, suspendedAt.Valid, wantStatus, wantSuspended)
	}
}
