package backups

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrBackupRunning = errors.New("a backup is already queued or running")

type ServiceConfig struct {
	DataDir      string
	DatabasePath string
	BackupDir    string
	Store        *Store
	Drive        DriveClient
	Logger       *slog.Logger
	Now          func() time.Time
}

type Service struct {
	config ServiceConfig

	manual          chan struct{}
	settingsChanged chan struct{}
	mu              sync.Mutex
	busy            bool
}

func NewService(config ServiceConfig) *Service {
	if config.BackupDir == "" && config.DataDir != "" {
		config.BackupDir = filepath.Join(config.DataDir, "backups")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Service{
		config:          config,
		manual:          make(chan struct{}, 1),
		settingsChanged: make(chan struct{}, 1),
	}
}

func (s *Service) QueueManual() error {
	if !s.startJob() {
		return ErrBackupRunning
	}
	s.manual <- struct{}{}
	return nil
}

func (s *Service) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

func (s *Service) SignalSettingsChanged() {
	select {
	case s.settingsChanged <- struct{}{}:
	default:
	}
}

func (s *Service) Run(ctx context.Context) {
	if err := cleanupInterruptedBundles(s.config.BackupDir, s.config.Now()); err != nil {
		s.config.Logger.Warn("clean interrupted backup files", "error", err)
	}
	if err := s.config.Store.RecoverInterrupted(ctx); err != nil {
		s.config.Logger.Error("recover backup worker", "error", err)
	}
	for {
		wakeAt, scheduled, err := s.nextWake(ctx, s.config.Now())
		if err != nil {
			s.config.Logger.Warn("load backup schedule", "error", err)
			wakeAt = s.config.Now().Add(time.Minute)
		}
		wakeAt, scheduled = boundedScheduleWake(s.config.Now(), wakeAt, scheduled)
		var timer *time.Timer
		var timerChannel <-chan time.Time
		if !wakeAt.IsZero() {
			delay := wakeAt.Sub(s.config.Now())
			if delay < 0 {
				delay = 0
			}
			timer = time.NewTimer(delay)
			timerChannel = timer.C
		}

		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-s.settingsChanged:
			if timer != nil {
				timer.Stop()
			}
			continue
		case <-s.manual:
			if timer != nil {
				timer.Stop()
			}
			s.perform(ctx, "manual")
			s.finishJob()
		case <-timerChannel:
			if scheduled {
				s.runScheduled(ctx)
			}
		}
	}
}

func boundedScheduleWake(now, wakeAt time.Time, scheduled bool) (time.Time, bool) {
	if !scheduled {
		return wakeAt, false
	}
	refreshAt := now.Add(time.Minute)
	if wakeAt.After(refreshAt) {
		return refreshAt, false
	}
	return wakeAt, true
}

func (s *Service) runScheduled(ctx context.Context) {
	if !s.startJob() {
		return
	}
	defer s.finishJob()

	settings, err := s.config.Store.Settings(ctx)
	if err != nil || !settings.Enabled {
		return
	}
	location, err := time.LoadLocation(settings.TimeZone)
	if err != nil {
		s.config.Logger.Warn("load backup timezone", "error", err)
		return
	}
	date := s.config.Now().In(location).Format("2006-01-02")
	claimed, err := s.config.Store.ClaimScheduledDate(ctx, date)
	if err != nil {
		s.config.Logger.Warn("claim scheduled backup", "error", err)
		return
	}
	if claimed {
		s.perform(ctx, "scheduled")
	}
}

func (s *Service) perform(ctx context.Context, trigger string) {
	startedAt := s.config.Now()
	if err := s.config.Store.Begin(ctx, trigger, startedAt); err != nil {
		s.config.Logger.Error("start backup", "error", err)
		return
	}
	settings, err := s.config.Store.Settings(ctx)
	if err != nil {
		s.fail(ctx, err, "")
		return
	}
	name := "goi-" + startedAt.UTC().Format("20060102T150405.000000000Z") + BundleSuffix
	localPath := filepath.Join(s.config.BackupDir, name)
	if err := CreateBundle(ctx, s.config.DatabasePath, localPath, startedAt); err != nil {
		s.fail(ctx, err, "")
		return
	}
	cutoff, err := retentionCutoff(startedAt, settings.TimeZone, settings.RetentionDays)
	if err != nil {
		s.config.Logger.Warn("calculate backup retention", "error", err)
	} else if err := PruneLocalBefore(s.config.BackupDir, cutoff); err != nil {
		s.config.Logger.Warn("prune local backups", "error", err)
	}
	localName := name
	remoteID := ""
	if settings.GoogleDrive {
		drive := s.config.Drive
		if drive == nil {
			s.fail(ctx, errors.New("Google Drive is not configured; the local backup was kept"), localName)
			return
		}
		if !drive.Connected() {
			s.fail(ctx, errors.New("Google Drive is not connected; the local backup was kept"), localName)
			return
		}
		uploaded, err := drive.Upload(ctx, localPath, name)
		if err != nil {
			s.fail(ctx, err, localName)
			return
		}
		if strings.TrimSpace(uploaded.ID) == "" {
			s.fail(ctx, errors.New("Google Drive returned an invalid backup reference; the local backup was kept"), localName)
			return
		}
		remoteID = uploaded.ID
		if !cutoff.IsZero() {
			if err := pruneRemote(ctx, drive, cutoff); err != nil {
				s.config.Logger.Warn("prune Google Drive backups", "error", err)
			}
		}
		if !settings.KeepLocal {
			if err := os.Remove(localPath); err != nil {
				s.fail(ctx, fmt.Errorf("remove uploaded local backup: %w", err), localName)
				return
			}
			if err := syncDirectory(filepath.Dir(localPath)); err != nil {
				s.fail(ctx, fmt.Errorf("sync local backup removal: %w", err), "")
				return
			}
			localName = ""
		}
	}
	if err := s.config.Store.Succeed(ctx, s.config.Now(), localName, remoteID); err != nil {
		s.config.Logger.Error("record successful backup", "error", err)
	}
}

func (s *Service) fail(ctx context.Context, cause error, localName string) {
	s.config.Logger.Warn("backup failed", "error", cause)
	if err := s.config.Store.Fail(ctx, cause.Error(), localName); err != nil {
		s.config.Logger.Error("record failed backup", "error", err)
	}
}

func (s *Service) DisconnectDrive(ctx context.Context) error {
	if !s.startJob() {
		return ErrBackupRunning
	}
	defer s.finishJob()

	settings, err := s.config.Store.Settings(ctx)
	if err != nil {
		return err
	}
	if settings.GoogleDrive {
		settings.GoogleDrive = false
		settings.KeepLocal = true
		if err := s.config.Store.UpdateSettings(ctx, settings); err != nil {
			return err
		}
		s.SignalSettingsChanged()
	}
	if s.config.Drive == nil {
		return nil
	}
	return s.config.Drive.Disconnect()
}

func (s *Service) startJob() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return false
	}
	s.busy = true
	return true
}

func (s *Service) finishJob() {
	s.mu.Lock()
	s.busy = false
	s.mu.Unlock()
}

func (s *Service) nextWake(ctx context.Context, now time.Time) (time.Time, bool, error) {
	settings, err := s.config.Store.Settings(ctx)
	if err != nil {
		return time.Time{}, false, err
	}
	if !settings.Enabled {
		return time.Time{}, false, nil
	}
	state, err := s.config.Store.State(ctx)
	if err != nil {
		return time.Time{}, false, err
	}
	location, err := time.LoadLocation(settings.TimeZone)
	if err != nil {
		return time.Time{}, false, err
	}
	localNow := now.In(location)
	date := localNow.Format("2006-01-02")
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), settings.Hour, 0, 0, 0, location)
	if state.LastScheduledDate != date {
		if localNow.Before(today) {
			return today, true, nil
		}
		return now, true, nil
	}
	tomorrow := today.AddDate(0, 0, 1)
	return tomorrow, true, nil
}

func pruneRemote(ctx context.Context, drive DriveClient, cutoff time.Time) error {
	files, err := drive.ListCurrent(ctx)
	if err != nil {
		return err
	}
	var pruneErrors []error
	for _, file := range files {
		if file.CreatedAt.IsZero() || !file.CreatedAt.Before(cutoff) {
			continue
		}
		if err := drive.Delete(ctx, file.ID); err != nil {
			pruneErrors = append(pruneErrors, err)
		}
	}
	return errors.Join(pruneErrors...)
}

func retentionCutoff(now time.Time, timeZone string, days int) (time.Time, error) {
	if days < 1 || days > MaxRetentionDays {
		return time.Time{}, fmt.Errorf("backup retention must be between 1 and %d days", MaxRetentionDays)
	}
	location, err := time.LoadLocation(timeZone)
	if err != nil {
		return time.Time{}, fmt.Errorf("load backup timezone: %w", err)
	}
	local := now.In(location)
	startOfToday := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	return startOfToday.AddDate(0, 0, -(days - 1)), nil
}

func (s *Service) backupDirectory() string {
	return s.config.BackupDir
}

func (s *Service) QueueDriveRestore(ctx context.Context, id string) error {
	drive := s.config.Drive
	if drive == nil {
		return errors.New("Google Drive is not configured")
	}
	if !drive.Connected() {
		return errors.New("Google Drive is not connected")
	}
	temporary, err := os.CreateTemp(s.config.DataDir, ".remote-restore-*.zip")
	if err != nil {
		return fmt.Errorf("create remote restore download: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := drive.Download(ctx, id, temporary); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	input, err := os.Open(temporaryPath)
	if err != nil {
		return err
	}
	defer input.Close()
	return QueueRestore(ctx, s.config.DataDir, input)
}

func (s *Service) QueueLocalRestore(ctx context.Context, name string) error {
	path, err := LocalPath(s.config.BackupDir, name)
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return QueueRestore(ctx, s.config.DataDir, file)
}

func (s *Service) QueueUploadedRestore(ctx context.Context, source io.Reader) error {
	return QueueRestore(ctx, s.config.DataDir, source)
}
