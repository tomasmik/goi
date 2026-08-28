package wanikani

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/tomasmik/goi/internal/database"
)

func openTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(t.Context(), filepath.Join(t.TempDir(), "goi.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestStoreCompletesSyncAndSeparatesSeenSubjects(t *testing.T) {
	db := openTestDatabase(t)
	store := NewStore(db)
	initial, err := store.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if initial.UserID != "" || initial.SubjectCount != 0 || !initial.CursorAt.IsZero() {
		t.Fatalf("initial status = %+v", initial)
	}

	user := User{ID: "user-1", Username: "turtle", Level: 12, MaxLevelGranted: 60}
	if changed, err := store.ConfigureAccount(t.Context(), user); err != nil || changed {
		t.Fatalf("ConfigureAccount() = %v, %v", changed, err)
	}
	cursor := time.Date(2026, 8, 27, 10, 30, 0, 123, time.UTC)
	if err := store.CompleteSync(t.Context(), user, cursor, cursor.Add(time.Second), []SubjectMapping{
		{ID: 10, Expression: "食べる"},
		{ID: 11, Expression: "おやつ"},
		{ID: 10, Expression: "食べる"},
	}); err != nil {
		t.Fatal(err)
	}

	status, err := store.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.UserID != user.ID || status.Username != user.Username || status.UserLevel != user.Level ||
		status.SubjectCount != 2 || !status.CursorAt.Equal(cursor) || !status.LastSuccessAt.Equal(cursor.Add(time.Second).Truncate(time.Second)) {
		t.Fatalf("status = %+v", status)
	}
	replacement := user
	replacement.Username = "turtle-renamed"
	replacement.Level = 13
	if changed, err := store.ConfigureAccount(t.Context(), replacement); err != nil || changed {
		t.Fatalf("ConfigureAccount(replacement) = %v, %v", changed, err)
	}
	status, err = store.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.Username != replacement.Username || status.UserLevel != replacement.Level || status.SubjectCount != 2 || !status.CursorAt.Equal(cursor) {
		t.Fatalf("same-account replacement status = %+v", status)
	}
	unseen, err := store.UnseenSubjectIDs(t.Context(), []int64{9, 10, 11, 12})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(unseen, []int64{9, 12}) {
		t.Fatalf("unseen = %v, want [9 12]", unseen)
	}
}

func TestStoreAccountChangeAndDisconnectKeepVocabulary(t *testing.T) {
	db := openTestDatabase(t)
	store := NewStore(db)
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, known_elsewhere_at, created_at, updated_at)
		VALUES ('食べる', '食べる', ?, ?, ?)`, now.Unix(), now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	first := User{ID: "first", Username: "one", Level: 10, MaxLevelGranted: 60}
	if _, err := store.ConfigureAccount(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSync(t.Context(), first, now, now, []SubjectMapping{{ID: 1, Expression: "食べる"}}); err != nil {
		t.Fatal(err)
	}
	second := User{ID: "second", Username: "two", Level: 3, MaxLevelGranted: 3}
	changed, err := store.ConfigureAccount(t.Context(), second)
	if err != nil || !changed {
		t.Fatalf("ConfigureAccount(second) = %v, %v", changed, err)
	}
	status, err := store.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.SubjectCount != 0 || !status.CursorAt.IsZero() || status.UserID != second.ID {
		t.Fatalf("changed account status = %+v", status)
	}
	if err := store.Clear(t.Context()); err != nil {
		t.Fatal(err)
	}
	var vocabularyCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary WHERE known_elsewhere_at IS NOT NULL").Scan(&vocabularyCount); err != nil {
		t.Fatal(err)
	}
	if vocabularyCount != 1 {
		t.Fatalf("known vocabulary count = %d, want 1", vocabularyCount)
	}
}
