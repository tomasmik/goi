package backups

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	DefaultRetentionDays = 1
	MaxRetentionDays     = 3
)

type Settings struct {
	Enabled       bool
	Hour          int
	GoogleDrive   bool
	KeepLocal     bool
	RetentionDays int
	TimeZone      string
}

type State struct {
	Status            string
	Trigger           string
	LastAttemptAt     time.Time
	LastSuccessAt     time.Time
	LastScheduledDate string
	LocalName         string
	RemoteID          string
	ErrorMessage      string
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Settings(ctx context.Context) (Settings, error) {
	var values Settings
	var enabled, googleDrive, keepLocal int
	err := s.db.QueryRowContext(ctx, `
		SELECT b.enabled, b.hour, b.google_drive, b.keep_local, b.retention_days, u.time_zone
		FROM backup_settings b
		JOIN user_settings u ON u.id = b.id
		WHERE b.id = 1`).Scan(&enabled, &values.Hour, &googleDrive, &keepLocal, &values.RetentionDays, &values.TimeZone)
	if err != nil {
		return Settings{}, fmt.Errorf("load backup settings: %w", err)
	}
	values.Enabled = enabled == 1
	values.GoogleDrive = googleDrive == 1
	values.KeepLocal = keepLocal == 1
	return values, nil
}

func (s *Store) UpdateSettings(ctx context.Context, values Settings) error {
	if values.Hour < 0 || values.Hour > 23 {
		return errors.New("backup hour must be between 0 and 23")
	}
	if !values.KeepLocal && !values.GoogleDrive {
		return errors.New("at least one backup destination is required")
	}
	if values.RetentionDays < 1 || values.RetentionDays > MaxRetentionDays {
		return fmt.Errorf("backup retention must be between 1 and %d days", MaxRetentionDays)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE backup_settings
		SET enabled = ?, hour = ?, google_drive = ?, keep_local = ?, retention_days = ?
		WHERE id = 1`, boolInt(values.Enabled), values.Hour, boolInt(values.GoogleDrive), boolInt(values.KeepLocal), values.RetentionDays)
	if err != nil {
		return fmt.Errorf("update backup settings: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check updated backup settings: %w", err)
	}
	if updated != 1 {
		return errors.New("backup settings do not exist")
	}
	return nil
}

func (s *Store) State(ctx context.Context) (State, error) {
	var state State
	var attempt, success sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT status, trigger, last_attempt_at, last_success_at, last_scheduled_date,
		       local_name, remote_id, error_message
		FROM backup_state WHERE id = 1`).Scan(
		&state.Status, &state.Trigger, &attempt, &success, &state.LastScheduledDate,
		&state.LocalName, &state.RemoteID, &state.ErrorMessage,
	)
	if err != nil {
		return State{}, fmt.Errorf("load backup state: %w", err)
	}
	state.LastAttemptAt = unixTime(attempt)
	state.LastSuccessAt = unixTime(success)
	return state, nil
}

func (s *Store) Begin(ctx context.Context, trigger string, now time.Time) error {
	if trigger != "manual" && trigger != "scheduled" {
		return errors.New("invalid backup trigger")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE backup_state
		SET status = 'running', trigger = ?, last_attempt_at = ?, local_name = '',
		    remote_id = '', error_message = ''
		WHERE id = 1`, trigger, now.Unix())
	if err != nil {
		return fmt.Errorf("start backup: %w", err)
	}
	return nil
}

func (s *Store) Succeed(ctx context.Context, now time.Time, localName, remoteID string) error {
	if localName == "" && remoteID == "" {
		return errors.New("successful backup has no stored copy")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE backup_state
		SET status = 'success', last_success_at = ?, local_name = ?, remote_id = ?, error_message = ''
		WHERE id = 1`, now.Unix(), localName, remoteID)
	if err != nil {
		return fmt.Errorf("record successful backup: %w", err)
	}
	return nil
}

func (s *Store) Fail(ctx context.Context, message, localName string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE backup_state
		SET status = 'failed', local_name = ?, remote_id = '', error_message = ?
		WHERE id = 1`, localName, message)
	if err != nil {
		return fmt.Errorf("record failed backup: %w", err)
	}
	return nil
}

func (s *Store) RecoverInterrupted(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE backup_state
		SET status = 'failed', error_message = 'The server stopped before this backup finished.'
		WHERE id = 1 AND status = 'running'`)
	if err != nil {
		return fmt.Errorf("recover interrupted backup: %w", err)
	}
	return nil
}

func (s *Store) ClaimScheduledDate(ctx context.Context, date string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE backup_state
		SET last_scheduled_date = ?
		WHERE id = 1 AND last_scheduled_date <> ?`, date, date)
	if err != nil {
		return false, fmt.Errorf("claim scheduled backup date: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("check scheduled backup claim: %w", err)
	}
	return updated == 1, nil
}

func unixTime(value sql.NullInt64) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return time.Unix(value.Int64, 0).UTC()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
