package dashboard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tomasmik/goi/internal/lessons"
	"github.com/tomasmik/goi/internal/reviews"
	"github.com/tomasmik/goi/internal/srs"
	"github.com/tomasmik/goi/internal/statistics"
)

type Store struct {
	db         *sql.DB
	location   *time.Location
	lessons    *lessons.Store
	reviews    *reviews.Store
	statistics *statistics.Store
}

type Summary struct {
	ActiveLessonSessionID int64
	ActiveReviewSessionID int64
	ActiveReviewCompleted int
	ActiveReviewTotal     int
	AvailableLessons      int
	DueReviews            int
	VocabularyCount       int
	ActiveWords           int
	LearnedToday          int
	LearnedWeek           int
	LearnedMonth          int
	Leeches               int
	PendingCaptures       int
	WeeklyReviews         int
	WeeklyCorrect         int
	WeeklyTotal           int
	MistakeItems          []MistakeItem
	UpcomingReviews       []UpcomingReviewBatch
	StageCounts           []StageCount
	BackupWarning         string
}

type UpcomingReviewBatch struct {
	DateTime string
	Label    string
	Count    int
}

type StageCount struct {
	Label string
	Count int
}

var stageGroups = [...]struct {
	label string
	first srs.Stage
	last  srs.Stage
}{
	{label: "New", first: srs.StageNew, last: srs.StageThree},
	{label: "Learning", first: srs.StageFour, last: srs.StageFive},
	{label: "Familiar", first: srs.StageSix, last: srs.StageSix},
	{label: "Mature", first: srs.StageSeven, last: srs.StageEight},
	{label: "Burned", first: srs.StageEvergreen, last: srs.StageEvergreen},
}

type MistakeItem struct {
	ID         int64
	Expression string
}

func NewStore(
	db *sql.DB,
	location *time.Location,
	lessons *lessons.Store,
	reviews *reviews.Store,
	statistics *statistics.Store,
) *Store {
	if location == nil {
		location = time.UTC
	}
	return &Store{
		db:         db,
		location:   location,
		lessons:    lessons,
		reviews:    reviews,
		statistics: statistics,
	}
}

func (s *Store) Summary(ctx context.Context, now time.Time) (Summary, error) {
	var summary Summary
	location, err := s.currentLocation(ctx)
	if err != nil {
		return Summary{}, err
	}
	weekStart := localWeekStart(now, location)
	summary.AvailableLessons, err = s.lessons.AvailableCount(ctx)
	if err != nil {
		return Summary{}, fmt.Errorf("load available lessons: %w", err)
	}
	summary.DueReviews, err = s.reviews.DueCountAt(ctx, now)
	if err != nil {
		return Summary{}, fmt.Errorf("load due reviews: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM vocabulary WHERE status = 'active'").Scan(&summary.ActiveWords); err != nil {
		return Summary{}, fmt.Errorf("load active words: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM vocabulary").Scan(&summary.VocabularyCount); err != nil {
		return Summary{}, fmt.Errorf("load vocabulary count: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM mining_captures WHERE status = 'pending'").Scan(&summary.PendingCaptures); err != nil {
		return Summary{}, fmt.Errorf("load pending mining captures: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE((
			SELECT id FROM lesson_sessions
			WHERE status = 'active'
			ORDER BY id DESC LIMIT 1
		), 0)`).Scan(&summary.ActiveLessonSessionID); err != nil {
		return Summary{}, fmt.Errorf("load active lesson: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(rs.id, 0),
		       COALESCE((
		           SELECT SUM(completed)
		           FROM (
		               SELECT MIN(CASE WHEN rsi2.status = 'completed' THEN 1 ELSE 0 END) completed
		               FROM review_session_items rsi2
		               WHERE rsi2.session_id = rs.id
		               GROUP BY rsi2.vocabulary_id
		           )
		       ), 0),
		       COUNT(DISTINCT rsi.vocabulary_id)
		FROM (SELECT id FROM review_sessions
		      WHERE kind = 'normal' AND lesson_session_id IS NULL AND status IN ('active', 'paused')
		      ORDER BY id DESC LIMIT 1) rs
		LEFT JOIN review_session_items rsi ON rsi.session_id = rs.id`).Scan(
		&summary.ActiveReviewSessionID, &summary.ActiveReviewCompleted, &summary.ActiveReviewTotal,
	); err != nil {
		return Summary{}, fmt.Errorf("load active reviews: %w", err)
	}
	summary.Leeches, err = s.reviews.LeechCount(ctx, now.UTC())
	if err != nil {
		return Summary{}, err
	}
	weekly, err := s.statistics.WeeklyActivity(ctx, now)
	if err != nil {
		return Summary{}, err
	}
	summary.WeeklyReviews = weekly.Reviews
	summary.WeeklyCorrect = weekly.Correct
	summary.WeeklyTotal = weekly.Total
	dayStart := localDayStart(now, location)
	periods := []struct {
		name  string
		start time.Time
		end   time.Time
		value *int
	}{
		{name: "today", start: dayStart, end: dayStart.AddDate(0, 0, 1), value: &summary.LearnedToday},
		{name: "week", start: weekStart, end: weekStart.AddDate(0, 0, 7), value: &summary.LearnedWeek},
		{name: "month", start: time.Date(dayStart.Year(), dayStart.Month(), 1, 0, 0, 0, 0, location), end: time.Date(dayStart.Year(), dayStart.Month()+1, 1, 0, 0, 0, 0, location), value: &summary.LearnedMonth},
	}
	for _, period := range periods {
		if err := s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM vocabulary
			WHERE lesson_completed_at >= ? AND lesson_completed_at < ?`, period.start.Unix(), period.end.Unix()).Scan(period.value); err != nil {
			return Summary{}, fmt.Errorf("load learned words %s: %w", period.name, err)
		}
	}
	upcoming, err := s.upcomingReviews(ctx, now, location)
	if err != nil {
		return Summary{}, err
	}
	summary.UpcomingReviews = upcoming
	stageCounts, err := s.StageCounts(ctx)
	if err != nil {
		return Summary{}, err
	}
	summary.StageCounts = stageCounts
	mistakes, err := s.statistics.RecentMistakesLimit(ctx, now, 8)
	if err != nil {
		return Summary{}, err
	}
	summary.MistakeItems = make([]MistakeItem, 0, len(mistakes))
	for _, mistake := range mistakes {
		summary.MistakeItems = append(summary.MistakeItems, MistakeItem{
			ID:         mistake.ID,
			Expression: mistake.Expression,
		})
	}
	if err := s.loadBackupWarning(ctx, now, location, &summary); err != nil {
		return Summary{}, err
	}
	return summary, nil
}

func (s *Store) loadBackupWarning(ctx context.Context, now time.Time, location *time.Location, summary *Summary) error {
	var enabled int
	var status, message string
	var lastSuccess sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT settings.enabled, state.status, state.last_success_at, state.error_message
		FROM backup_settings settings
		JOIN backup_state state ON state.id = settings.id
		WHERE settings.id = 1`).Scan(&enabled, &status, &lastSuccess, &message)
	if err != nil {
		return fmt.Errorf("load backup health: %w", err)
	}
	if enabled == 0 {
		return nil
	}
	if status == "failed" {
		if lastSuccess.Valid {
			summary.BackupWarning = "The latest automatic backup failed. Last successful backup: " + time.Unix(lastSuccess.Int64, 0).In(location).Format("Jan 2, 15:04") + ". " + message
		} else {
			summary.BackupWarning = "The latest automatic backup failed. No successful automatic backup has completed. " + message
		}
		return nil
	}
	if lastSuccess.Valid && now.Sub(time.Unix(lastSuccess.Int64, 0)) > 48*time.Hour {
		summary.BackupWarning = "Automatic backups are overdue. Last successful backup: " + time.Unix(lastSuccess.Int64, 0).In(location).Format("Jan 2, 15:04") + "."
	}
	return nil
}

func (s *Store) upcomingReviews(ctx context.Context, now time.Time, location *time.Location) ([]UpcomingReviewBatch, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ss.due_at FROM srs_states ss INDEXED BY srs_states_due
		JOIN vocabulary v ON v.id = ss.vocabulary_id
		WHERE v.status = 'active'
		  AND ss.suspended_at IS NULL
		  AND ss.due_at IS NOT NULL
		  AND ss.due_at > ?
		ORDER BY ss.due_at`, now.Unix())
	if err != nil {
		return nil, fmt.Errorf("load upcoming reviews: %w", err)
	}
	defer rows.Close()

	const batchLimit = 3
	batches := make([]UpcomingReviewBatch, 0, batchLimit)
	labelCounts := make(map[string]int, batchLimit)
	zoneLabels := make([]string, 0, batchLimit)
	currentHour := ""
	for rows.Next() {
		var dueAt int64
		if err := rows.Scan(&dueAt); err != nil {
			return nil, fmt.Errorf("scan upcoming review: %w", err)
		}
		due := time.Unix(dueAt, 0).In(location)
		hour := reviewHourKey(due)
		if hour != currentHour {
			if len(batches) == batchLimit {
				break
			}
			label := upcomingReviewLabel(due, now.In(location))
			batches = append(batches, UpcomingReviewBatch{
				DateTime: due.Format(time.RFC3339),
				Label:    label,
			})
			labelCounts[label]++
			zoneLabels = append(zoneLabels, due.Format("MST"))
			currentHour = hour
		}
		batches[len(batches)-1].Count++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate upcoming reviews: %w", err)
	}
	for index := range batches {
		if labelCounts[batches[index].Label] > 1 {
			batches[index].Label += " " + zoneLabels[index]
		}
	}
	return batches, nil
}

func (s *Store) currentLocation(ctx context.Context) (*time.Location, error) {
	var timeZone string
	err := s.db.QueryRowContext(ctx, "SELECT time_zone FROM user_settings WHERE id = 1").Scan(&timeZone)
	if errors.Is(err, sql.ErrNoRows) {
		return s.location, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load configured time zone: %w", err)
	}
	location, err := time.LoadLocation(timeZone)
	if err != nil {
		return nil, fmt.Errorf("load configured time zone %q: %w", timeZone, err)
	}
	return location, nil
}

func (s *Store) StageCounts(ctx context.Context) ([]StageCount, error) {
	counts := make([]int, len(stageGroups))
	rows, err := s.db.QueryContext(ctx, `
		SELECT ss.stage, COUNT(*) FROM srs_states ss
		JOIN vocabulary v ON v.id = ss.vocabulary_id
		WHERE v.status = 'active' GROUP BY ss.stage ORDER BY ss.stage`)
	if err != nil {
		return nil, fmt.Errorf("load SRS stages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var stage, count int
		if err := rows.Scan(&stage, &count); err != nil {
			return nil, fmt.Errorf("scan SRS stage: %w", err)
		}
		for index, group := range stageGroups {
			if srs.Stage(stage) >= group.first && srs.Stage(stage) <= group.last {
				counts[index] += count
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate SRS stages: %w", err)
	}
	result := make([]StageCount, 0, len(counts))
	for index, count := range counts {
		result = append(result, StageCount{Label: stageGroups[index].label, Count: count})
	}
	return result, nil
}

func localDayStart(now time.Time, location *time.Location) time.Time {
	local := now.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
}

func localWeekStart(now time.Time, location *time.Location) time.Time {
	local := localDayStart(now, location)
	daysSinceMonday := (int(local.Weekday()) + 6) % 7
	return local.AddDate(0, 0, -daysSinceMonday)
}

func upcomingReviewLabel(due, now time.Time) string {
	if reviewHourKey(due) == reviewHourKey(now) {
		return "Within the hour"
	}
	today := localDayStart(now, now.Location())
	dueDay := localDayStart(due, due.Location())
	switch {
	case dueDay.Equal(today):
		return "Today · " + due.Format("15:00")
	case dueDay.Equal(today.AddDate(0, 0, 1)):
		return "Tomorrow · " + due.Format("15:00")
	default:
		return due.Format("Mon, Jan 2 · 15:00")
	}
}

func reviewHourKey(value time.Time) string {
	return value.Format("2006-01-02T15Z07:00")
}
