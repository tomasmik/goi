package dashboard

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomasmik/goi/internal/database"
	"github.com/tomasmik/goi/internal/lessons"
	"github.com/tomasmik/goi/internal/reviews"
	"github.com/tomasmik/goi/internal/statistics"
)

func TestSummaryLoadsStudyMetrics(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "dashboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, status, created_at, updated_at)
		VALUES ('食べる', '食べる', 'active', ?, ?)`, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO srs_states (vocabulary_id, stage, due_at) VALUES (?, 0, ?)`, id, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO mining_captures (
			raw_text, expression, normalized_expression, source_kind, capture_nonce,
			request_hash, status, created_at
		) VALUES ('猫', '猫', '猫', 'manual', ?, ?, 'pending', ?)`,
		strings.Repeat("1", 32), strings.Repeat("a", 64), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	summary, err := newDashboardTestStore(db, time.UTC).Summary(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if summary.ActiveWords != 1 || summary.Leeches != 0 || summary.WeeklyReviews != 0 || summary.PendingCaptures != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestSummaryExcludesWordsReservedByActiveLesson(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "reserved-lessons.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	_, err = db.Exec(`
		INSERT INTO vocabulary (
			expression, normalized_expression, pronunciation, normalized_pronunciation,
			status, known_elsewhere_at, created_at, updated_at
		)
		VALUES ('reserved', 'reserved', 'よやく', 'よやく', 'unlearned', NULL, ?, ?),
		       ('available', 'available', 'りよう', 'りよう', 'unlearned', NULL, ?, ?),
		       ('external', 'external', '', '', 'unlearned', ?, ?, ?)`,
		now, now, now, now, now, now, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO meanings (vocabulary_id, position, text, normalized_text)
		SELECT id, 0, 'test meaning', 'test meaning'
		FROM vocabulary
		WHERE normalized_expression IN ('reserved', 'available')`); err != nil {
		t.Fatal(err)
	}
	var reservedID int64
	if err := db.QueryRow("SELECT id FROM vocabulary WHERE normalized_expression = 'reserved'").Scan(&reservedID); err != nil {
		t.Fatal(err)
	}
	lessonResult, err := db.Exec("INSERT INTO lesson_sessions (status) VALUES ('active')")
	if err != nil {
		t.Fatal(err)
	}
	lessonID, err := lessonResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO lesson_session_items (session_id, vocabulary_id, position)
		VALUES (?, ?, 0)`, lessonID, reservedID); err != nil {
		t.Fatal(err)
	}

	summary, err := newDashboardTestStore(db, time.UTC).Summary(ctx, time.Unix(now, 0))
	if err != nil {
		t.Fatal(err)
	}
	if summary.AvailableLessons != 1 {
		t.Fatalf("available lessons = %d, want 1", summary.AvailableLessons)
	}
	if summary.ActiveLessonSessionID != lessonID {
		t.Fatalf("active lesson session = %d, want %d", summary.ActiveLessonSessionID, lessonID)
	}
}

func TestScheduleStagesAndLearningPeriods(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "schedule.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	day := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)
	insertDashboardWord(t, db, "one", 0, day.Add(2*time.Hour).Unix(), now.Add(-2*time.Hour))
	insertDashboardWord(t, db, "two", 3, day.AddDate(0, 0, 1).Add(14*time.Hour).Unix(), now.AddDate(0, 0, -2))
	insertDashboardWord(t, db, "three", 9, nil, now.AddDate(0, 0, -10))

	store := newDashboardTestStore(db, time.UTC)
	summary, err := store.Summary(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if summary.LearnedToday != 1 || summary.LearnedWeek != 2 || summary.LearnedMonth != 3 {
		t.Fatalf("learned periods = %d/%d/%d", summary.LearnedToday, summary.LearnedWeek, summary.LearnedMonth)
	}
	if len(summary.UpcomingReviews) != 1 || summary.UpcomingReviews[0].Label != "Tomorrow · 14:00" || summary.UpcomingReviews[0].Count != 1 {
		t.Fatalf("upcoming reviews = %+v", summary.UpcomingReviews)
	}
	if len(summary.StageCounts) != 5 ||
		summary.StageCounts[0].Label != "New" || summary.StageCounts[0].Count != 2 ||
		summary.StageCounts[4].Label != "Burned" || summary.StageCounts[4].Count != 1 {
		t.Fatalf("stage counts = %+v", summary.StageCounts)
	}

}

func TestUpcomingReviewsUsesNextThreeNonEmptyHours(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "upcoming-reviews.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 24, 10, 30, 0, 0, time.UTC)
	insertDashboardWord(t, db, "already due", 0, now.Unix(), now)
	insertDashboardWord(t, db, "within the hour", 0, now.Add(15*time.Minute).Unix(), now)
	insertDashboardWord(t, db, "next hour one", 0, now.Add(35*time.Minute).Unix(), now)
	insertDashboardWord(t, db, "next hour two", 0, now.Add(75*time.Minute).Unix(), now)
	insertDashboardWord(t, db, "tomorrow", 0, time.Date(2026, 7, 25, 9, 15, 0, 0, time.UTC).Unix(), now)
	insertDashboardWord(t, db, "after limit", 0, time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC).Unix(), now)

	batches, err := newDashboardTestStore(db, time.UTC).upcomingReviews(ctx, now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	expected := []UpcomingReviewBatch{
		{DateTime: "2026-07-24T10:45:00Z", Label: "Within the hour", Count: 1},
		{DateTime: "2026-07-24T11:05:00Z", Label: "Today · 11:00", Count: 2},
		{DateTime: "2026-07-25T09:15:00Z", Label: "Tomorrow · 09:00", Count: 1},
	}
	if len(batches) != len(expected) {
		t.Fatalf("upcoming reviews = %+v", batches)
	}
	for index := range expected {
		if batches[index] != expected[index] {
			t.Fatalf("upcoming review %d = %+v, want %+v", index, batches[index], expected[index])
		}
	}
}

func TestUpcomingReviewsSeparatesRepeatedDaylightSavingHours(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "daylight-saving-reviews.sqlite"))
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

	now := time.Date(2026, 11, 1, 4, 30, 0, 0, time.UTC)
	insertDashboardWord(t, db, "daylight hour", 0, time.Date(2026, 11, 1, 5, 15, 0, 0, time.UTC).Unix(), now)
	insertDashboardWord(t, db, "standard hour", 0, time.Date(2026, 11, 1, 6, 15, 0, 0, time.UTC).Unix(), now)

	batches, err := newDashboardTestStore(db, location).upcomingReviews(ctx, now, location)
	if err != nil {
		t.Fatal(err)
	}
	expected := []UpcomingReviewBatch{
		{DateTime: "2026-11-01T01:15:00-04:00", Label: "Today · 01:00 EDT", Count: 1},
		{DateTime: "2026-11-01T01:15:00-05:00", Label: "Today · 01:00 EST", Count: 1},
	}
	if len(batches) != len(expected) {
		t.Fatalf("upcoming reviews = %+v", batches)
	}
	for index := range expected {
		if batches[index] != expected[index] {
			t.Fatalf("upcoming review %d = %+v, want %+v", index, batches[index], expected[index])
		}
	}
}

func TestStageCountsUseNamedGroups(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "stage-groups.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	expressions := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}
	for stage, expression := range expressions {
		var dueAt any = time.Now().Add(time.Hour).Unix()
		if stage == 9 {
			dueAt = nil
		}
		insertDashboardWord(t, db, expression, stage, dueAt, time.Now())
	}

	counts, err := newDashboardTestStore(db, time.UTC).StageCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expected := []StageCount{
		{Label: "New", Count: 4},
		{Label: "Learning", Count: 2},
		{Label: "Familiar", Count: 1},
		{Label: "Mature", Count: 2},
		{Label: "Burned", Count: 1},
	}
	if len(counts) != len(expected) {
		t.Fatalf("stage groups = %+v", counts)
	}
	for index := range expected {
		if counts[index] != expected[index] {
			t.Fatalf("stage group %d = %+v, want %+v", index, counts[index], expected[index])
		}
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

	_, err = newDashboardTestStore(db, time.UTC).Summary(ctx, time.Now())
	if err == nil || !strings.Contains(err.Error(), `load configured time zone "not/a-time-zone"`) {
		t.Fatalf("Summary() error = %v", err)
	}
}

func insertDashboardWord(t *testing.T, db *sql.DB, expression string, stage int, dueAt any, learnedAt time.Time) {
	t.Helper()
	now := time.Now().UTC().Unix()
	result, err := db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, status, lesson_completed_at, created_at, updated_at)
		VALUES (?, ?, 'active', ?, ?, ?)`, expression, expression, learnedAt.Unix(), now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO srs_states (vocabulary_id, stage, due_at) VALUES (?, ?, ?)`, id, stage, dueAt); err != nil {
		t.Fatal(err)
	}
}

func newDashboardTestStore(db *sql.DB, location *time.Location) *Store {
	return NewStore(
		db,
		location,
		lessons.NewStore(db),
		reviews.NewStore(db),
		statistics.NewStore(db, location),
	)
}
