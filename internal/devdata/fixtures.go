package devdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tomasmik/goi/internal/srs"
	"github.com/tomasmik/goi/internal/vocabulary"
)

type Scenario string

const (
	ScenarioLessons Scenario = "lessons"
	ScenarioReviews Scenario = "reviews"
	ScenarioMixed   Scenario = "mixed"
	ScenarioQA      Scenario = "qa"
)

type Summary struct {
	Vocabulary        int
	LessonsAvailable  int
	KnownElsewhere    int
	Due               int
	Future            int
	Evergreen         int
	ReviewSessions    int
	PendingCaptures   int
	AcceptedCaptures  int
	DiscardedCaptures int
	Examples          int
	MediaAssets       int
}

type fixture struct {
	expression    string
	pronunciation string
	meaning       string
}

var fixtures = [...]fixture{
	{expression: "食べる", pronunciation: "たべる", meaning: "to eat"},
	{expression: "飲む", pronunciation: "のむ", meaning: "to drink"},
	{expression: "見る", pronunciation: "みる", meaning: "to see"},
	{expression: "聞く", pronunciation: "きく", meaning: "to listen"},
	{expression: "話す", pronunciation: "はなす", meaning: "to speak"},
	{expression: "読む", pronunciation: "よむ", meaning: "to read"},
	{expression: "書く", pronunciation: "かく", meaning: "to write"},
	{expression: "行く", pronunciation: "いく", meaning: "to go"},
	{expression: "来る", pronunciation: "くる", meaning: "to come"},
	{expression: "帰る", pronunciation: "かえる", meaning: "to return"},
	{expression: "大きい", pronunciation: "おおきい", meaning: "big"},
	{expression: "小さい", pronunciation: "ちいさい", meaning: "small"},
	{expression: "新しい", pronunciation: "あたらしい", meaning: "new"},
	{expression: "古い", pronunciation: "ふるい", meaning: "old"},
	{expression: "早い", pronunciation: "はやい", meaning: "early"},
	{expression: "遅い", pronunciation: "おそい", meaning: "late"},
	{expression: "今日", pronunciation: "きょう", meaning: "today"},
	{expression: "明日", pronunciation: "あした", meaning: "tomorrow"},
	{expression: "友達", pronunciation: "ともだち", meaning: "friend"},
	{expression: "難しい", pronunciation: "むずかしい", meaning: "difficult"},
}

func Populate(ctx context.Context, db *sql.DB, scenario Scenario, now time.Time) (Summary, error) {
	if scenario != ScenarioLessons && scenario != ScenarioReviews && scenario != ScenarioMixed && scenario != ScenarioQA {
		return Summary{}, fmt.Errorf("unsupported scenario %q", scenario)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Summary{}, fmt.Errorf("begin fixture transaction: %w", err)
	}
	defer tx.Rollback()

	var existing int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM vocabulary").Scan(&existing); err != nil {
		return Summary{}, fmt.Errorf("count existing vocabulary: %w", err)
	}
	if existing != 0 {
		return Summary{}, errors.New("test database is not empty")
	}

	now = now.UTC()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_settings (id, time_zone)
		VALUES (1, 'UTC')`); err != nil {
		return Summary{}, fmt.Errorf("insert test settings: %w", err)
	}

	vocabularyStore := vocabulary.NewStore(db)
	ids := make([]int64, 0, len(fixtures))
	idsByExpression := make(map[string]int64, len(fixtures))
	for _, word := range fixtures {
		id, err := vocabularyStore.CreateInTx(ctx, tx, vocabulary.CreateInput{
			Expression:    word.expression,
			Pronunciation: word.pronunciation,
			Meanings:      []string{word.meaning},
			SourceLabel:   "devdata",
		})
		if err != nil {
			return Summary{}, fmt.Errorf("create fixture %q: %w", word.expression, err)
		}
		ids = append(ids, id)
		idsByExpression[word.expression] = id
	}

	if scenario != ScenarioLessons {
		if err := insertReviewHistory(ctx, tx, ids, now); err != nil {
			return Summary{}, err
		}
	}
	switch scenario {
	case ScenarioReviews:
		if err := populateReviews(ctx, tx, ids, now); err != nil {
			return Summary{}, err
		}
	case ScenarioMixed, ScenarioQA:
		if err := populateMixed(ctx, tx, ids, now); err != nil {
			return Summary{}, err
		}
	}
	if scenario == ScenarioQA {
		if err := populateQA(ctx, db, tx, idsByExpression, now); err != nil {
			return Summary{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return Summary{}, fmt.Errorf("commit fixture transaction: %w", err)
	}
	return LoadSummary(ctx, db, now)
}

func populateReviews(ctx context.Context, tx *sql.Tx, ids []int64, now time.Time) error {
	for index, id := range ids {
		stage := srs.Stage(index % int(srs.StageEvergreen))
		dueAt := now.Add(-time.Minute)
		if index == len(ids)-2 {
			stage = srs.StageEvergreen
			dueAt = time.Time{}
		}
		if index == len(ids)-1 {
			stage = srs.StageNew
		}
		if err := activateWord(ctx, tx, id, stage, dueAt, now); err != nil {
			return err
		}
	}
	return nil
}

func populateMixed(ctx context.Context, tx *sql.Tx, ids []int64, now time.Time) error {
	dueStages := []srs.Stage{srs.StageNew, srs.StageOne, srs.StageFour, srs.StageSix, srs.StageSeven}
	for index, stage := range dueStages {
		if err := activateWord(ctx, tx, ids[10+index], stage, now.Add(-time.Minute), now); err != nil {
			return err
		}
	}

	future := []struct {
		stage srs.Stage
		dueAt time.Time
	}{
		{stage: srs.StageTwo, dueAt: now.AddDate(0, 0, 1)},
		{stage: srs.StageFive, dueAt: now.AddDate(0, 0, 2)},
		{stage: srs.StageSeven, dueAt: now.AddDate(0, 0, 4)},
	}
	for index, item := range future {
		if err := activateWord(ctx, tx, ids[15+index], item.stage, item.dueAt, now); err != nil {
			return err
		}
	}
	if err := activateWord(ctx, tx, ids[18], srs.StageEvergreen, time.Time{}, now); err != nil {
		return err
	}
	return activateWord(ctx, tx, ids[19], srs.StageNew, now.Add(-time.Minute), now)
}

func activateWord(ctx context.Context, tx *sql.Tx, id int64, stage srs.Stage, dueAt, now time.Time) error {
	learnedAt := now.Add(-2 * time.Hour).Unix()
	if _, err := tx.ExecContext(ctx, `
		UPDATE vocabulary
		SET status = 'active', lesson_completed_at = ?, updated_at = ?
		WHERE id = ?`, learnedAt, now.Unix(), id); err != nil {
		return fmt.Errorf("activate fixture %d: %w", id, err)
	}
	var due any
	if !dueAt.IsZero() {
		due = dueAt.Unix()
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO srs_states (vocabulary_id, stage, due_at)
		VALUES (?, ?, ?)`, id, stage, due); err != nil {
		return fmt.Errorf("schedule fixture %d: %w", id, err)
	}
	return nil
}

func LoadSummary(ctx context.Context, db *sql.DB, now time.Time) (Summary, error) {
	var summary Summary
	queries := []struct {
		name  string
		value *int
		query string
		args  []any
	}{
		{name: "vocabulary", value: &summary.Vocabulary, query: "SELECT COUNT(*) FROM vocabulary"},
		{name: "lessons available", value: &summary.LessonsAvailable, query: "SELECT COUNT(*) FROM vocabulary WHERE status = 'unlearned' AND known_elsewhere_at IS NULL"},
		{name: "known elsewhere", value: &summary.KnownElsewhere, query: "SELECT COUNT(*) FROM vocabulary WHERE known_elsewhere_at IS NOT NULL"},
		{name: "due", value: &summary.Due, query: `
			SELECT COUNT(*) FROM srs_states ss
			JOIN vocabulary v ON v.id = ss.vocabulary_id
			WHERE v.status = 'active' AND ss.suspended_at IS NULL
			  AND ss.due_at IS NOT NULL AND ss.due_at <= ?`, args: []any{now.UTC().Unix()}},
		{name: "future", value: &summary.Future, query: `
			SELECT COUNT(*) FROM srs_states ss
			JOIN vocabulary v ON v.id = ss.vocabulary_id
			WHERE v.status = 'active' AND ss.suspended_at IS NULL
			  AND ss.due_at IS NOT NULL AND ss.due_at > ?`, args: []any{now.UTC().Unix()}},
		{name: "evergreen", value: &summary.Evergreen, query: `
			SELECT COUNT(*) FROM srs_states ss
			JOIN vocabulary v ON v.id = ss.vocabulary_id
			WHERE v.status = 'active' AND ss.stage = ? AND ss.due_at IS NULL`, args: []any{srs.StageEvergreen}},
		{name: "review sessions", value: &summary.ReviewSessions, query: "SELECT COUNT(*) FROM review_sessions WHERE kind = 'normal'"},
		{name: "pending captures", value: &summary.PendingCaptures, query: "SELECT COUNT(*) FROM mining_captures WHERE status = 'pending'"},
		{name: "accepted captures", value: &summary.AcceptedCaptures, query: "SELECT COUNT(*) FROM mining_captures WHERE status = 'accepted'"},
		{name: "discarded captures", value: &summary.DiscardedCaptures, query: "SELECT COUNT(*) FROM mining_captures WHERE status = 'discarded'"},
		{name: "examples", value: &summary.Examples, query: "SELECT COUNT(*) FROM vocabulary_examples"},
		{name: "media assets", value: &summary.MediaAssets, query: "SELECT COUNT(*) FROM media"},
	}
	for _, query := range queries {
		if err := db.QueryRowContext(ctx, query.query, query.args...).Scan(query.value); err != nil {
			return Summary{}, fmt.Errorf("count %s: %w", query.name, err)
		}
	}
	return summary, nil
}
