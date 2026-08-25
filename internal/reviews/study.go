package reviews

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const maximumStudySelectionSize = 100

func (s *Store) StudyCounts(ctx context.Context) (StudyCounts, error) {
	var counts StudyCounts
	windowHours := 12
	if err := s.db.QueryRowContext(ctx, "SELECT lesson_window_hours FROM user_settings WHERE id = 1").Scan(&windowHours); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return StudyCounts{}, fmt.Errorf("load study window: %w", err)
	}
	now := time.Now().UTC()
	queries := []struct {
		name        string
		destination *int
		statement   string
		arguments   []any
	}{
		{
			name:        "recent lessons",
			destination: &counts.RecentLessons,
			statement: `SELECT COUNT(*)
			 FROM vocabulary
			 WHERE status = 'active' AND lesson_completed_at >= ?`,
			arguments: []any{now.Add(-time.Duration(windowHours) * time.Hour).Unix()},
		},
		{
			name:        "recent mistakes",
			destination: &counts.RecentMistakes,
			statement: `SELECT COUNT(DISTINCT rsi.vocabulary_id)
			 FROM review_results rr
			 JOIN review_session_items rsi ON rsi.id = rr.session_item_id
			 JOIN review_sessions rs ON rs.id = rsi.session_id
			 JOIN vocabulary v ON v.id = rsi.vocabulary_id
			 LEFT JOIN mistake_visibility mv ON mv.vocabulary_id = rsi.vocabulary_id
			 WHERE rs.kind = 'normal'
			   AND rr.srs_applied = 1
			   AND rr.outcome = 'failure'
			   AND rr.voided_at IS NULL
			   AND rr.created_at >= ?
			   AND v.status = 'active'
			   AND (
			       mv.hidden_at IS NULL
			       OR rr.created_at > mv.hidden_at
			   )`,
			arguments: []any{now.Add(-24 * time.Hour).Unix()},
		},
		{
			name:        "current words",
			destination: &counts.Current,
			statement: `SELECT COUNT(*)
			 FROM srs_states ss
			 JOIN vocabulary v ON v.id = ss.vocabulary_id
			 WHERE v.status = 'active' AND ss.stage <= 1`,
		},
	}
	for _, query := range queries {
		if err := s.db.QueryRowContext(ctx, query.statement, query.arguments...).Scan(query.destination); err != nil {
			return StudyCounts{}, fmt.Errorf("count %s: %w", query.name, err)
		}
	}
	leechCount, err := s.LeechCount(ctx, now)
	if err != nil {
		return StudyCounts{}, err
	}
	counts.Leeches = leechCount
	return counts, nil
}

func (s *Store) LeechCount(ctx context.Context, _ time.Time) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM leech_states WHERE active = 1`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count leeches: %w", err)
	}
	return count, nil
}

func (s *Store) Leeches(ctx context.Context) ([]LeechItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.id, v.expression, v.pronunciation, ls.failures_since_mark,
		       ls.correct_streak, v.status = 'suspended'
		FROM leech_states ls
		JOIN vocabulary v ON v.id = ls.vocabulary_id
		WHERE ls.active = 1 AND v.status IN ('active', 'suspended')
		ORDER BY v.status = 'suspended' DESC, ls.failures_since_mark DESC, v.expression`)
	if err != nil {
		return nil, fmt.Errorf("load leeches: %w", err)
	}
	defer rows.Close()
	leeches := make([]LeechItem, 0)
	for rows.Next() {
		var item LeechItem
		if err := rows.Scan(&item.ID, &item.Expression, &item.Pronunciation, &item.FailuresSinceMark, &item.CorrectStreak, &item.Suspended); err != nil {
			return nil, fmt.Errorf("scan leech: %w", err)
		}
		leeches = append(leeches, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate leeches: %w", err)
	}
	return leeches, nil
}

func (s *Store) studyIDs(ctx context.Context, source string, selected []int64, limit int) ([]int64, error) {
	var query string
	var args []any
	switch source {
	case "recent-lessons":
		windowHours := 12
		if err := s.db.QueryRowContext(ctx, "SELECT lesson_window_hours FROM user_settings WHERE id = 1").Scan(&windowHours); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("load study window: %w", err)
		}
		query = `
			SELECT id
			FROM vocabulary
			WHERE status = 'active' AND lesson_completed_at >= ?
			ORDER BY lesson_completed_at DESC, id DESC
			LIMIT ?`
		args = append(args, time.Now().UTC().Add(-time.Duration(windowHours)*time.Hour).Unix(), limit)
	case "recent-mistakes":
		query = `
			SELECT rsi.vocabulary_id
			FROM review_results rr
			JOIN review_session_items rsi ON rsi.id = rr.session_item_id
			JOIN review_sessions rs ON rs.id = rsi.session_id
			JOIN vocabulary v ON v.id = rsi.vocabulary_id
			LEFT JOIN mistake_visibility mv ON mv.vocabulary_id = rsi.vocabulary_id
			WHERE rs.kind = 'normal'
			  AND rr.srs_applied = 1
			  AND rr.outcome = 'failure'
			  AND rr.voided_at IS NULL
			  AND rr.created_at >= ?
			  AND v.status = 'active'
			  AND (
			      mv.hidden_at IS NULL
			      OR rr.created_at > mv.hidden_at
			  )
			GROUP BY rsi.vocabulary_id
			ORDER BY MAX(rr.created_at) DESC
			LIMIT ?`
		args = append(args, time.Now().UTC().Add(-24*time.Hour).Unix(), limit)
	case "current":
		query = `
			SELECT ss.vocabulary_id
			FROM srs_states ss
			JOIN vocabulary v ON v.id = ss.vocabulary_id
			WHERE v.status = 'active' AND ss.stage <= 1
			ORDER BY ss.stage, ss.due_at
			LIMIT ?`
		args = append(args, limit)
	case "leeches":
		query = `
			SELECT ls.vocabulary_id
			FROM leech_states ls
			JOIN vocabulary v ON v.id = ls.vocabulary_id
			WHERE ls.active = 1 AND v.status IN ('active', 'suspended')
			ORDER BY v.status = 'suspended' DESC, ls.failures_since_mark DESC, ls.vocabulary_id
			LIMIT ?`
		args = append(args, limit)
	case "selected":
		if len(selected) > maximumStudySelectionSize {
			return nil, stateErrorf("select at most %d words", maximumStudySelectionSize)
		}
		seen := make(map[int64]struct{}, len(selected))
		ids := make([]int64, 0, len(selected))
		for _, id := range selected {
			if id <= 0 {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			return nil, stateError("select at least one word")
		}
		query = `
			SELECT v.id
			FROM vocabulary v
			LEFT JOIN leech_states ls ON ls.vocabulary_id = v.id
			WHERE (v.status = 'active' OR (v.status = 'suspended' AND ls.active = 1))
			  AND v.id IN (` + placeholders(len(ids)) + `)
			ORDER BY v.id LIMIT ?`
		for _, id := range ids {
			args = append(args, id)
		}
		args = append(args, limit)
	default:
		return nil, fmt.Errorf("unsupported study source %q", source)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("find %s study words: %w", source, err)
	}
	defer rows.Close()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan %s study word: %w", source, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s study words: %w", source, err)
	}
	return ids, nil
}

func placeholders(count int) string {
	values := make([]string, count)
	for i := range values {
		values[i] = "?"
	}
	return strings.Join(values, ",")
}
