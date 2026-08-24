package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Store struct {
	db *sql.DB
}

type Values struct {
	TimeZone            string
	LessonWindowHours   int
	ExtraStudyLimit     int
	RetryCount          int
	ReviewMode          string
	ReviewOrder         string
	ReviewCardOrder     string
	ReviewAutoAdvance   bool
	LeechFailureCount   int
	LeechSuspendCount   int
	LeechRecoveryStreak int
	SixMonthReview      bool
	Theme               string
	AudioEnabled        bool
}

type validationError string

func (err validationError) Error() string {
	return string(err)
}

func (err validationError) UserMessage() string {
	return string(err)
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Ensure(ctx context.Context, defaultTimeZone string) error {
	if _, err := time.LoadLocation(defaultTimeZone); err != nil {
		return fmt.Errorf("load default timezone %q: %w", defaultTimeZone, err)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO user_settings (id, time_zone, theme)
		VALUES (1, ?, 'light')
		ON CONFLICT(id) DO NOTHING`, defaultTimeZone)
	if err != nil {
		return fmt.Errorf("ensure user settings: %w", err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context) (Values, error) {
	var values Values
	var audio, reviewAutoAdvance, sixMonthReview int
	err := s.db.QueryRowContext(ctx, `
		SELECT time_zone, lesson_window_hours, extra_study_limit, retry_count,
		       review_mode, review_order, review_card_order, review_auto_advance,
		       leech_failure_threshold, leech_suspend_threshold, leech_recovery_streak,
		       six_month_review_enabled, theme, audio_enabled
		FROM user_settings WHERE id = 1`).Scan(
		&values.TimeZone, &values.LessonWindowHours, &values.ExtraStudyLimit, &values.RetryCount,
		&values.ReviewMode, &values.ReviewOrder, &values.ReviewCardOrder, &reviewAutoAdvance,
		&values.LeechFailureCount, &values.LeechSuspendCount, &values.LeechRecoveryStreak,
		&sixMonthReview, &values.Theme, &audio,
	)
	if err != nil {
		return Values{}, fmt.Errorf("load user settings: %w", err)
	}
	values.SixMonthReview = sixMonthReview == 1
	values.ReviewAutoAdvance = reviewAutoAdvance == 1
	values.AudioEnabled = audio == 1
	return values, nil
}

func (s *Store) Update(ctx context.Context, values Values) error {
	if _, err := time.LoadLocation(values.TimeZone); err != nil {
		return validationError("invalid timezone")
	}
	if values.LessonWindowHours != 4 && values.LessonWindowHours != 8 && values.LessonWindowHours != 12 && values.LessonWindowHours != 24 {
		return validationError("recent lesson window must be 4, 8, 12, or 24 hours")
	}
	if values.ExtraStudyLimit < 1 || values.ExtraStudyLimit > 100 {
		return validationError("extra-study list size must be between 1 and 100")
	}
	if values.RetryCount < 1 || values.RetryCount > 5 {
		return validationError("answer attempts must be between 1 and 5")
	}
	if values.ReviewMode != "typed" && values.ReviewMode != "self_grade" {
		return validationError("review mode must be typed answers or self grading")
	}
	if values.ReviewOrder != "stage_ascending" && values.ReviewOrder != "stage_descending" && values.ReviewOrder != "random" {
		return validationError("review order is invalid")
	}
	if values.ReviewCardOrder != "together" && values.ReviewCardOrder != "spaced" {
		return validationError("review card order must be together or spaced")
	}
	if values.LeechFailureCount < 1 || values.LeechFailureCount > 100 {
		return validationError("leech failure count must be between 1 and 100")
	}
	if values.LeechSuspendCount < 1 || values.LeechSuspendCount > 100 {
		return validationError("leech suspension count must be between 1 and 100")
	}
	if values.LeechRecoveryStreak < 1 || values.LeechRecoveryStreak > 100 {
		return validationError("leech recovery streak must be between 1 and 100")
	}
	if values.Theme != "system" && values.Theme != "light" && values.Theme != "dark" {
		return validationError("theme must be system, light, or dark")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE user_settings
		SET time_zone = ?, lesson_window_hours = ?, extra_study_limit = ?, retry_count = ?,
		    review_mode = ?, review_order = ?, review_card_order = ?, review_auto_advance = ?,
		    leech_failure_threshold = ?, leech_suspend_threshold = ?, leech_recovery_streak = ?,
		    six_month_review_enabled = ?, theme = ?, audio_enabled = ?
		WHERE id = 1`, values.TimeZone, values.LessonWindowHours, values.ExtraStudyLimit, values.RetryCount,
		values.ReviewMode, values.ReviewOrder, values.ReviewCardOrder, boolInt(values.ReviewAutoAdvance),
		values.LeechFailureCount, values.LeechSuspendCount, values.LeechRecoveryStreak,
		boolInt(values.SixMonthReview), values.Theme, boolInt(values.AudioEnabled))
	if err != nil {
		return fmt.Errorf("update user settings: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read updated user settings count: %w", err)
	}
	if updated == 0 {
		return errors.New("user settings do not exist")
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
