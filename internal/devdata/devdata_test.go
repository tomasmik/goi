package devdata_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomasmik/goi/internal/coverage"
	"github.com/tomasmik/goi/internal/dashboard"
	"github.com/tomasmik/goi/internal/database"
	"github.com/tomasmik/goi/internal/devdata"
	"github.com/tomasmik/goi/internal/examples"
	"github.com/tomasmik/goi/internal/lessons"
	"github.com/tomasmik/goi/internal/mining"
	"github.com/tomasmik/goi/internal/reviews"
	"github.com/tomasmik/goi/internal/srs"
	"github.com/tomasmik/goi/internal/statistics"
	"github.com/tomasmik/goi/internal/vocabulary"
)

var fixtureNow = time.Now().UTC().Truncate(time.Second)

func TestPopulateScenarios(t *testing.T) {
	tests := []struct {
		scenario devdata.Scenario
		want     devdata.Summary
	}{
		{
			scenario: devdata.ScenarioLessons,
			want:     devdata.Summary{Vocabulary: 20, LessonsAvailable: 20},
		},
		{
			scenario: devdata.ScenarioReviews,
			want:     devdata.Summary{Vocabulary: 20, Due: 19, Evergreen: 1, ReviewSessions: 7},
		},
		{
			scenario: devdata.ScenarioMixed,
			want:     devdata.Summary{Vocabulary: 20, LessonsAvailable: 10, Due: 6, Future: 3, Evergreen: 1, ReviewSessions: 7},
		},
		{
			scenario: devdata.ScenarioQA,
			want: devdata.Summary{
				Vocabulary: 24, LessonsAvailable: 10, KnownElsewhere: 3,
				Due: 7, Future: 3, Evergreen: 1, ReviewSessions: 7,
				PendingCaptures: 3, AcceptedCaptures: 1, DiscardedCaptures: 1,
				Examples: 3, MediaAssets: 2,
			},
		},
	}
	for _, test := range tests {
		t.Run(string(test.scenario), func(t *testing.T) {
			ctx, db := openTestDatabase(t)
			got, err := devdata.Populate(ctx, db, test.scenario, fixtureNow)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("summary = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestQAScenarioFeedsBrowserAndExtensionFlows(t *testing.T) {
	ctx, db := openTestDatabase(t)
	if _, err := devdata.Populate(ctx, db, devdata.ScenarioQA, fixtureNow); err != nil {
		t.Fatal(err)
	}

	available, err := lessons.NewStore(db).AvailablePage(ctx, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 10 {
		t.Fatalf("available lessons = %d, want 10", len(available))
	}
	reviewStore := reviews.NewStore(db)
	due, err := reviewStore.DueCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if due != 7 {
		t.Fatalf("due reviews = %d, want 7", due)
	}
	sessionID, err := reviewStore.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	reviewState, err := reviewStore.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var firstStage srs.Stage
	if err := db.QueryRow(`SELECT stage FROM srs_states WHERE vocabulary_id = ?`, reviewState.VocabularyID).Scan(&firstStage); err != nil {
		t.Fatal(err)
	}
	if firstStage != srs.StageNew {
		t.Fatalf("first QA review stage = %d, want %d", firstStage, srs.StageNew)
	}
	known, err := vocabulary.NewStore(db).KnownCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if known != 14 {
		t.Fatalf("known vocabulary = %d, want 14", known)
	}

	analyzer, err := coverage.NewAnalyzer(vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	coverageResult, err := analyzer.Analyze(ctx, []coverage.Block{{ID: 1, Text: "日本語を勉強する。気をつける。"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{"日本語", "勉強する", "気をつける"} {
		if !hasKnownToken(coverageResult, expression) {
			t.Fatalf("coverage did not recognize %q: %#v", expression, coverageResult)
		}
	}

	miningStore := mining.NewStore(db)
	pending, err := miningStore.ListPage(ctx, mining.StatusPending, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 3 {
		t.Fatalf("pending captures = %d, want 3", len(pending))
	}
	_ = captureWithExpression(t, pending, "見つける")
	ambiguous := captureWithExpression(t, pending, "生")
	if ambiguous.SuggestedEntrySequence == nil || *ambiguous.SuggestedEntrySequence != 1579510 {
		t.Fatalf("ambiguous suggested entry = %#v", ambiguous.SuggestedEntrySequence)
	}
	existing := captureWithExpression(t, pending, "食べる")
	if existing.ExistingVocabularyID == nil || existing.SourcePositionMS == nil || *existing.SourcePositionMS != 12_500 {
		t.Fatalf("existing vocabulary capture = %#v", existing)
	}
	for _, capture := range pending {
		assertCaptureReplays(t, ctx, miningStore, capture)
	}

	accepted, err := miningStore.ListPage(ctx, mining.StatusAccepted, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted) != 1 || accepted[0].Revision != 2 || accepted[0].SentenceAudioID == 0 || accepted[0].VideoFrameID == 0 {
		t.Fatalf("accepted capture = %#v", accepted)
	}
	assertCaptureReplays(t, ctx, miningStore, accepted[0])
	if accepted[0].VocabularyID == nil {
		t.Fatal("accepted capture has no vocabulary")
	}
	minedExample, err := examples.NewStore(db).Preferred(ctx, *accepted[0].VocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	if minedExample.Origin != examples.OriginMined || minedExample.SentenceAudioID == 0 || minedExample.VideoFrameID == 0 {
		t.Fatalf("mined example = %#v", minedExample)
	}
	if minedExample.Sentence != "静かな部屋で本を読む。" || minedExample.SourceURL != "" || minedExample.SourcePositionMS == nil || *minedExample.SourcePositionMS != 42_000 {
		t.Fatalf("mined example provenance = %#v", minedExample)
	}

	lessonID, err := lessons.NewStore(db).Start(ctx, []int64{*accepted[0].VocabularyID})
	if err != nil {
		t.Fatal(err)
	}
	lesson, err := lessons.NewStore(db).Current(ctx, lessonID)
	if err != nil {
		t.Fatal(err)
	}
	if lesson.StudyItem.Expression != "読む" || lesson.StudyItem.Example.SentenceAudioID == 0 || lesson.StudyItem.Example.VideoFrameID == 0 {
		t.Fatalf("lesson item with mined media = %#v", lesson.StudyItem)
	}

	discarded, err := miningStore.ListPage(ctx, mining.StatusDiscarded, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(discarded) != 1 || discarded[0].Revision != 2 {
		t.Fatalf("discarded capture = %#v", discarded)
	}
	assertCaptureReplays(t, ctx, miningStore, discarded[0])

	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("QA fixture has a foreign-key violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestLessonScenarioFeedsLessonPicker(t *testing.T) {
	ctx, db := openTestDatabase(t)
	if _, err := devdata.Populate(ctx, db, devdata.ScenarioLessons, fixtureNow); err != nil {
		t.Fatal(err)
	}
	items, err := lessons.NewStore(db).AvailablePage(ctx, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 20 {
		t.Fatalf("available lessons = %d, want 20", len(items))
	}
	if items[0].Expression != "食べる" || items[19].Expression != "難しい" {
		t.Fatalf("lesson order = %q ... %q", items[0].Expression, items[19].Expression)
	}
}

func TestReviewScenarioFeedsReviewAndHistoryViews(t *testing.T) {
	ctx, db := openTestDatabase(t)
	if _, err := devdata.Populate(ctx, db, devdata.ScenarioReviews, fixtureNow); err != nil {
		t.Fatal(err)
	}

	reviewStore := reviews.NewStore(db)
	due, err := reviewStore.DueCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if due != 19 {
		t.Fatalf("due reviews = %d, want 19", due)
	}
	leeches, err := reviewStore.Leeches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(leeches) != 1 || leeches[0].Expression != "難しい" {
		t.Fatalf("leeches = %+v", leeches)
	}

	statisticsStore := statistics.NewStore(db, time.UTC)
	summary, err := statisticsStore.Summary(ctx, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Streak != 7 {
		t.Fatalf("statistics = %+v", summary)
	}
	mistakes, err := statisticsStore.RecentMistakes(ctx, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(mistakes) != 1 || mistakes[0].Expression != "難しい" {
		t.Fatalf("recent mistakes = %+v", mistakes)
	}

	dashboardSummary, err := dashboard.NewStore(
		db,
		time.UTC,
		lessons.NewStore(db),
		reviews.NewStore(db),
		statisticsStore,
	).Summary(ctx, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboardSummary.StageCounts) != 5 {
		t.Fatalf("stage groups = %+v", dashboardSummary.StageCounts)
	}
	for _, group := range dashboardSummary.StageCounts {
		if group.Count == 0 {
			t.Fatalf("empty stage group in review scenario: %+v", dashboardSummary.StageCounts)
		}
	}

	var evergreenDue sql.NullInt64
	if err := db.QueryRow(`
		SELECT ss.due_at
		FROM srs_states ss
		JOIN vocabulary v ON v.id = ss.vocabulary_id
		WHERE v.expression = '友達'`).Scan(&evergreenDue); err != nil {
		t.Fatal(err)
	}
	if evergreenDue.Valid {
		t.Fatalf("Evergreen due date = %v, want NULL", evergreenDue)
	}
}

func TestMixedScenarioIncludesLessonsAndReviewForecast(t *testing.T) {
	ctx, db := openTestDatabase(t)
	if _, err := devdata.Populate(ctx, db, devdata.ScenarioMixed, fixtureNow); err != nil {
		t.Fatal(err)
	}
	available, err := lessons.NewStore(db).AvailablePage(ctx, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 10 {
		t.Fatalf("available lessons = %d, want 10", len(available))
	}
	summary, err := dashboard.NewStore(
		db,
		time.UTC,
		lessons.NewStore(db),
		reviews.NewStore(db),
		statistics.NewStore(db, time.UTC),
	).Summary(ctx, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if summary.DueReviews != 6 {
		t.Fatalf("due reviews = %d, want 6", summary.DueReviews)
	}
	future := 0
	for _, batch := range summary.UpcomingReviews {
		future += batch.Count
	}
	if future != 3 {
		t.Fatalf("future review forecast = %+v", summary.UpcomingReviews)
	}
}

func TestManageTestVocabulary(t *testing.T) {
	ctx, db := openTestDatabase(t)
	if _, err := devdata.Populate(ctx, db, devdata.ScenarioMixed, fixtureNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE srs_states SET due_at = ? WHERE stage < ?", fixtureNow.AddDate(0, 0, 7).Unix(), srs.StageEvergreen); err != nil {
		t.Fatal(err)
	}
	updated, err := devdata.MakeDue(ctx, db, 3, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if updated != 3 {
		t.Fatalf("updated due words = %d, want 3", updated)
	}
	summary, err := devdata.LoadSummary(ctx, db, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Due != 3 || summary.Evergreen != 1 {
		t.Fatalf("summary after due update = %+v", summary)
	}

	if err := devdata.SetStage(ctx, db, 1, srs.StageSix, fixtureNow); err != nil {
		t.Fatal(err)
	}
	assertWordState(t, db, 1, "active", int(srs.StageSix), true)
	if err := devdata.SetStage(ctx, db, 1, srs.StageEvergreen, fixtureNow); err != nil {
		t.Fatal(err)
	}
	assertWordState(t, db, 1, "active", int(srs.StageEvergreen), false)

	if err := devdata.Unlearn(ctx, db, 1, fixtureNow); err != nil {
		t.Fatal(err)
	}
	if err := devdata.Unlearn(ctx, db, 1, fixtureNow); err != nil {
		t.Fatal(err)
	}
	assertWordState(t, db, 1, "unlearned", -1, false)

	entries, err := devdata.List(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 20 || entries[18].StageName != "Burned" {
		t.Fatalf("test data list = %+v", entries)
	}
}

func TestUnlearnClearsKnownElsewhere(t *testing.T) {
	ctx, db := openTestDatabase(t)
	if _, err := devdata.Populate(ctx, db, devdata.ScenarioQA, fixtureNow); err != nil {
		t.Fatal(err)
	}
	var id, revision int64
	if err := db.QueryRow("SELECT id, content_revision FROM vocabulary WHERE expression = '勉強する'").Scan(&id, &revision); err != nil {
		t.Fatal(err)
	}
	if err := devdata.Unlearn(ctx, db, id, fixtureNow); err != nil {
		t.Fatal(err)
	}
	var status string
	var updatedRevision int64
	var knownElsewhere sql.NullInt64
	if err := db.QueryRow("SELECT status, known_elsewhere_at, content_revision FROM vocabulary WHERE id = ?", id).Scan(&status, &knownElsewhere, &updatedRevision); err != nil {
		t.Fatal(err)
	}
	if status != "unlearned" || knownElsewhere.Valid || updatedRevision != revision+1 {
		t.Fatalf("unlearned known vocabulary = status %q, known elsewhere %v, revision %d", status, knownElsewhere, updatedRevision)
	}

	if err := db.QueryRow("SELECT id, content_revision FROM vocabulary WHERE expression = '気をつける'").Scan(&id, &revision); err != nil {
		t.Fatal(err)
	}
	if err := devdata.SetStage(ctx, db, id, srs.StageTwo, fixtureNow); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT status, known_elsewhere_at, content_revision FROM vocabulary WHERE id = ?", id).Scan(&status, &knownElsewhere, &updatedRevision); err != nil {
		t.Fatal(err)
	}
	if status != "active" || knownElsewhere.Valid || updatedRevision != revision+1 {
		t.Fatalf("staged known vocabulary = status %q, known elsewhere %v, revision %d", status, knownElsewhere, updatedRevision)
	}
}

func TestMutationsRejectActiveStudy(t *testing.T) {
	ctx, db := openTestDatabase(t)
	if _, err := devdata.Populate(ctx, db, devdata.ScenarioMixed, fixtureNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO lesson_sessions (status) VALUES ('active')"); err != nil {
		t.Fatal(err)
	}
	err := devdata.SetStage(ctx, db, 1, srs.StageOne, fixtureNow)
	if err == nil || !strings.Contains(err.Error(), "finish the active lesson or review") {
		t.Fatalf("SetStage() error = %v", err)
	}
	if _, err := devdata.MakeDue(ctx, db, 1, fixtureNow); err == nil || !strings.Contains(err.Error(), "finish the active lesson or review") {
		t.Fatalf("MakeDue() error = %v", err)
	}
	if err := devdata.Unlearn(ctx, db, 1, fixtureNow); err == nil || !strings.Contains(err.Error(), "finish the active lesson or review") {
		t.Fatalf("Unlearn() error = %v", err)
	}
}

func TestStageValidation(t *testing.T) {
	ctx, db := openTestDatabase(t)
	if _, err := devdata.Populate(ctx, db, devdata.ScenarioLessons, fixtureNow); err != nil {
		t.Fatal(err)
	}
	if err := devdata.SetStage(ctx, db, 0, srs.StageNew, fixtureNow); err == nil {
		t.Fatal("SetStage() accepted ID 0")
	}
	if err := devdata.SetStage(ctx, db, 1, srs.Stage(10), fixtureNow); err == nil {
		t.Fatal("SetStage() accepted stage 10")
	}
	if err := devdata.SetStage(ctx, db, 999, srs.StageNew, fixtureNow); err == nil {
		t.Fatal("SetStage() accepted a missing word")
	}
}

func openTestDatabase(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "devdata.sqlite"))
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

func assertWordState(t *testing.T, db *sql.DB, id int64, wantStatus string, wantStage int, wantDue bool) {
	t.Helper()
	var status string
	var stage, dueAt sql.NullInt64
	if err := db.QueryRow(`
		SELECT v.status, ss.stage, ss.due_at
		FROM vocabulary v
		LEFT JOIN srs_states ss ON ss.vocabulary_id = v.id
		WHERE v.id = ?`, id).Scan(&status, &stage, &dueAt); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus || stage.Valid != (wantStage >= 0) || stage.Valid && int(stage.Int64) != wantStage || dueAt.Valid != wantDue {
		t.Fatalf("word state = status %q, stage %v, due %v", status, stage, dueAt)
	}
}

func hasKnownToken(result coverage.Result, expression string) bool {
	for _, block := range result.Blocks {
		for _, token := range block.Tokens {
			if token.Expression == expression && token.Status == coverage.StatusKnown {
				return true
			}
		}
	}
	return false
}

func captureWithExpression(t *testing.T, captures []mining.Capture, expression string) mining.Capture {
	t.Helper()
	for _, capture := range captures {
		if capture.Expression == expression {
			return capture
		}
	}
	t.Fatalf("capture %q not found in %#v", expression, captures)
	return mining.Capture{}
}

func assertCaptureReplays(t *testing.T, ctx context.Context, store *mining.Store, capture mining.Capture) {
	t.Helper()
	replayed, wasReplay, err := store.Create(ctx, mining.CreateInput{
		RawText:                capture.RawText,
		Expression:             capture.Expression,
		ContextText:            capture.ContextText,
		SourceKind:             capture.SourceKind,
		SourceTitle:            capture.SourceTitle,
		SourceURL:              capture.SourceURL,
		SourcePositionMS:       capture.SourcePositionMS,
		SuggestedEntrySequence: capture.SuggestedEntrySequence,
		CaptureNonce:           capture.CaptureNonce,
	})
	if err != nil {
		t.Fatalf("replay capture %d: %v", capture.ID, err)
	}
	if !wasReplay || replayed.ID != capture.ID {
		t.Fatalf("replay capture %d = id %d, replay %t", capture.ID, replayed.ID, wasReplay)
	}
}
