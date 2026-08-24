package statistics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Store struct {
	db       *sql.DB
	location *time.Location
}

type Summary struct {
	Streak        int
	WeeklyReviews int
	WeeklyCorrect int
	WeeklyTotal   int
}

type WeeklyActivity struct {
	Reviews int
	Correct int
	Total   int
}

type Mistake struct {
	ID            int64
	Expression    string
	Pronunciation string
	Meaning       string
}

func NewStore(db *sql.DB, location *time.Location) *Store {
	if location == nil {
		location = time.UTC
	}
	return &Store{db: db, location: location}
}

func (s *Store) Summary(ctx context.Context, now time.Time) (Summary, error) {
	var summary Summary
	location, err := s.currentLocation(ctx)
	if err != nil {
		return Summary{}, err
	}
	weekly, err := s.weeklyActivity(ctx, localWeekStart(now, location))
	if err != nil {
		return Summary{}, err
	}
	summary.WeeklyReviews = weekly.Reviews
	summary.WeeklyCorrect = weekly.Correct
	summary.WeeklyTotal = weekly.Total
	summary.Streak, err = s.studyStreak(ctx, now, location)
	if err != nil {
		return Summary{}, err
	}
	return summary, nil
}

func (s *Store) WeeklyActivity(ctx context.Context, now time.Time) (WeeklyActivity, error) {
	location, err := s.currentLocation(ctx)
	if err != nil {
		return WeeklyActivity{}, err
	}
	return s.weeklyActivity(ctx, localWeekStart(now, location))
}

func (s *Store) weeklyActivity(ctx context.Context, weekStart time.Time) (WeeklyActivity, error) {
	var activity WeeklyActivity
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(rr.first_attempt_correct_count), 0),
		       COALESCE(SUM(rr.prompt_count), 0)
		FROM review_results rr JOIN review_session_items rsi ON rsi.id = rr.session_item_id
		JOIN review_sessions rs ON rs.id = rsi.session_id
		WHERE rs.kind = 'normal' AND rr.srs_applied = 1 AND rr.voided_at IS NULL AND rr.created_at >= ?`, weekStart.Unix()).Scan(
		&activity.Reviews, &activity.Correct, &activity.Total); err != nil {
		return WeeklyActivity{}, fmt.Errorf("load weekly review summary: %w", err)
	}
	return activity, nil
}

func (s *Store) RecentMistakes(ctx context.Context, now time.Time) ([]Mistake, error) {
	return s.RecentMistakesLimit(ctx, now, 0)
}

func (s *Store) RecentMistakesLimit(ctx context.Context, now time.Time, limit int) ([]Mistake, error) {
	if limit < 0 {
		return nil, errors.New("recent mistake limit cannot be negative")
	}
	query := `
		SELECT v.id, v.expression,
		       v.pronunciation,
		       COALESCE((SELECT m.text FROM meanings m WHERE m.vocabulary_id = v.id AND m.position = 0), '')
		FROM review_results rr
		JOIN review_session_items rsi ON rsi.id = rr.session_item_id
		JOIN review_sessions rs ON rs.id = rsi.session_id
		JOIN vocabulary v ON v.id = rsi.vocabulary_id
		LEFT JOIN mistake_visibility mv ON mv.vocabulary_id = v.id
		WHERE v.status = 'active'
		  AND rs.kind = 'normal' AND rr.srs_applied = 1 AND rr.outcome = 'failure' AND rr.voided_at IS NULL
		  AND rr.created_at >= ?
		  AND (
		      mv.hidden_at IS NULL
		      OR rr.created_at > mv.hidden_at
		  )
		GROUP BY v.id
		ORDER BY MAX(rr.created_at) DESC`
	args := []any{now.UTC().Add(-24 * time.Hour).Unix()}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load recent mistakes: %w", err)
	}
	defer rows.Close()
	mistakes := make([]Mistake, 0)
	for rows.Next() {
		var mistake Mistake
		if err := rows.Scan(&mistake.ID, &mistake.Expression, &mistake.Pronunciation, &mistake.Meaning); err != nil {
			return nil, fmt.Errorf("scan recent mistake: %w", err)
		}
		mistakes = append(mistakes, mistake)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent mistakes: %w", err)
	}
	return mistakes, nil
}

func (s *Store) HideMistake(ctx context.Context, id int64) error {
	now := time.Now().UTC().Unix()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO mistake_visibility (vocabulary_id, hidden_at)
		SELECT v.id, ?
		FROM vocabulary v
		LEFT JOIN mistake_visibility mv ON mv.vocabulary_id = v.id
		WHERE v.id = ?
		  AND v.status = 'active'
		  AND EXISTS (
			SELECT 1
			FROM review_results rr
			JOIN review_session_items rsi ON rsi.id = rr.session_item_id
			JOIN review_sessions rs ON rs.id = rsi.session_id
			WHERE rsi.vocabulary_id = v.id
			  AND rs.kind = 'normal'
			  AND rr.srs_applied = 1
			  AND rr.outcome = 'failure'
			  AND rr.voided_at IS NULL
			  AND rr.created_at >= ?
			  AND (
			      mv.hidden_at IS NULL
			      OR rr.created_at > mv.hidden_at
			  )
		  )
		ON CONFLICT(vocabulary_id) DO UPDATE
		SET hidden_at = excluded.hidden_at`, now, id, now-int64(24*time.Hour/time.Second))
	if err != nil {
		return fmt.Errorf("hide mistake: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check hidden mistake: %w", err)
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) studyStreak(ctx context.Context, now time.Time, location *time.Location) (int, error) {
	dayStart := localDayStart(now, location)
	for streak := 0; ; streak++ {
		nextDay := dayStart.AddDate(0, 0, 1)
		studied, err := s.studiedBetween(ctx, dayStart, nextDay)
		if err != nil {
			return 0, err
		}
		if !studied {
			return streak, nil
		}
		dayStart = dayStart.AddDate(0, 0, -1)
	}
}

func (s *Store) studiedBetween(ctx context.Context, start, end time.Time) (bool, error) {
	var studied bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM review_results rr
			JOIN review_session_items rsi ON rsi.id = rr.session_item_id
			JOIN review_sessions rs ON rs.id = rsi.session_id
			WHERE rr.created_at >= ? AND rr.created_at < ?
			  AND rr.srs_applied = 1 AND rr.voided_at IS NULL
			  AND rs.kind = 'normal'
		) OR EXISTS (
			SELECT 1
			FROM lesson_session_items
			WHERE review_completed_at >= ? AND review_completed_at < ?
		)`, start.Unix(), end.Unix(), start.Unix(), end.Unix()).Scan(&studied)
	if err != nil {
		return false, fmt.Errorf("check study day: %w", err)
	}
	return studied, nil
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

func localDayStart(now time.Time, location *time.Location) time.Time {
	local := now.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
}

func localWeekStart(now time.Time, location *time.Location) time.Time {
	local := localDayStart(now, location)
	daysSinceMonday := (int(local.Weekday()) + 6) % 7
	return local.AddDate(0, 0, -daysSinceMonday)
}
