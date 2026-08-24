package backups

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tomasmik/goi/internal/database"
)

type fakeDrive struct {
	configured    bool
	connected     bool
	disconnectErr error
	uploadErr     error
	uploadResult  *RemoteBackup
	uploads       []string
	files         []RemoteBackup
	currentFiles  []RemoteBackup
	deleted       []string
	download      []byte
	downloads     int
}

type blockingUploadDrive struct {
	*fakeDrive
	started chan struct{}
	release chan struct{}
}

func (f *fakeDrive) Configured() bool                              { return f.configured }
func (f *fakeDrive) Connected() bool                               { return f.connected }
func (f *fakeDrive) CallbackURL() string                           { return "" }
func (f *fakeDrive) EnvironmentManaged() bool                      { return false }
func (f *fakeDrive) SaveClient(io.Reader) error                    { return nil }
func (f *fakeDrive) RemoveClient() error                           { return nil }
func (f *fakeDrive) AuthorizationURL() (string, error)             { return "https://example.com", nil }
func (f *fakeDrive) Connect(context.Context, string, string) error { f.connected = true; return nil }
func (f *fakeDrive) Disconnect() error {
	if f.disconnectErr != nil {
		return f.disconnectErr
	}
	f.connected = false
	return nil
}
func (f *fakeDrive) Upload(_ context.Context, _ string, name string) (RemoteBackup, error) {
	if f.uploadErr != nil {
		return RemoteBackup{}, f.uploadErr
	}
	f.uploads = append(f.uploads, name)
	if f.uploadResult != nil {
		return *f.uploadResult, nil
	}
	return RemoteBackup{ID: "remote1", Name: name}, nil
}
func (f *fakeDrive) List(context.Context) ([]RemoteBackup, error) { return f.files, nil }
func (f *fakeDrive) ListCurrent(context.Context) ([]RemoteBackup, error) {
	return f.currentFiles, nil
}
func (f *fakeDrive) Download(_ context.Context, _ string, destination io.Writer) error {
	f.downloads++
	_, err := destination.Write(f.download)
	return err
}
func (f *fakeDrive) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *blockingUploadDrive) Upload(ctx context.Context, path, name string) (RemoteBackup, error) {
	close(f.started)
	select {
	case <-ctx.Done():
		return RemoteBackup{}, ctx.Err()
	case <-f.release:
		return f.fakeDrive.Upload(ctx, path, name)
	}
}

func TestNextWakeUsesConfiguredLocalHourOncePerDay(t *testing.T) {
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	defer db.Close()
	store := NewStore(db)
	if err := store.UpdateSettings(t.Context(), Settings{Enabled: true, Hour: 3, KeepLocal: true, RetentionDays: DefaultRetentionDays}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE user_settings SET time_zone = 'Europe/Vilnius' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	service := NewService(ServiceConfig{Store: store})
	location, err := time.LoadLocation("Europe/Vilnius")
	if err != nil {
		t.Fatal(err)
	}

	before := time.Date(2026, 8, 5, 2, 0, 0, 0, location)
	wake, scheduled, err := service.nextWake(t.Context(), before)
	if err != nil {
		t.Fatal(err)
	}
	if !scheduled || !wake.Equal(time.Date(2026, 8, 5, 3, 0, 0, 0, location)) {
		t.Fatalf("nextWake() = %v, %v", wake, scheduled)
	}

	after := time.Date(2026, 8, 5, 4, 0, 0, 0, location)
	wake, scheduled, err = service.nextWake(t.Context(), after)
	if err != nil {
		t.Fatal(err)
	}
	if !scheduled || !wake.Equal(after) {
		t.Fatalf("overdue nextWake() = %v, %v", wake, scheduled)
	}
	claimed, err := store.ClaimScheduledDate(t.Context(), "2026-08-05")
	if err != nil || !claimed {
		t.Fatalf("ClaimScheduledDate() = %v, %v", claimed, err)
	}
	wake, _, err = service.nextWake(t.Context(), after)
	if err != nil {
		t.Fatal(err)
	}
	if !wake.Equal(time.Date(2026, 8, 6, 3, 0, 0, 0, location)) {
		t.Fatalf("next day's wake = %v", wake)
	}
}

func TestScheduleRechecksBeforeAChangedTimezoneCouldBeMissed(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	wake, due := boundedScheduleWake(now, now.Add(12*time.Hour), true)
	if due || !wake.Equal(now.Add(time.Minute)) {
		t.Fatalf("boundedScheduleWake() = %v, %v", wake, due)
	}
	wake, due = boundedScheduleWake(now, now, true)
	if !due || !wake.Equal(now) {
		t.Fatalf("due boundedScheduleWake() = %v, %v", wake, due)
	}
}

func TestRetentionCutoffUsesLocalCalendarDays(t *testing.T) {
	now := time.Date(2026, 3, 30, 1, 30, 0, 0, time.UTC)
	cutoff, err := retentionCutoff(now, "Europe/Vilnius", 3)
	if err != nil {
		t.Fatal(err)
	}
	location, err := time.LoadLocation("Europe/Vilnius")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 3, 28, 0, 0, 0, 0, location)
	if !cutoff.Equal(want) {
		t.Fatalf("retention cutoff = %v, want %v", cutoff, want)
	}
}

func TestQueueManualExposesBusyStateImmediately(t *testing.T) {
	service := NewService(ServiceConfig{})
	if err := service.QueueManual(); err != nil {
		t.Fatal(err)
	}
	if !service.Busy() {
		t.Fatal("queued manual backup is not reported as busy")
	}
	if err := service.QueueManual(); !errors.Is(err, ErrBackupRunning) {
		t.Fatalf("second QueueManual() error = %v", err)
	}
	service.finishJob()
	if service.Busy() {
		t.Fatal("finished manual backup is still busy")
	}
}

func TestServiceRunCleansInterruptedBundleFiles(t *testing.T) {
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	defer db.Close()
	directory := filepath.Join(dataDir, "backups")
	stale := filepath.Join(directory, ".bundle-interrupted")
	if err := os.MkdirAll(stale, 0o750); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	staleTime := now.Add(-interruptedBundleStaleAfter - time.Minute)
	if err := os.Chtimes(stale, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}
	service := NewService(ServiceConfig{
		DataDir: dataDir, Store: NewStore(db), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return now },
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		service.Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := os.Stat(stale)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("interrupted bundle workspace was not removed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("backup worker did not stop")
	}
}

func TestManualBackupCreatesDownloadableLocalBundle(t *testing.T) {
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	defer db.Close()
	store := NewStore(db)
	now := time.Date(2026, 8, 5, 10, 0, 0, 123, time.UTC)
	service := NewService(ServiceConfig{
		DataDir: dataDir, DatabasePath: databasePath, Store: store,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now },
	})

	service.perform(t.Context(), "manual")
	state, err := store.State(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "success" || state.LocalName == "" || state.RemoteID != "" {
		t.Fatalf("backup state = %+v", state)
	}
	path, err := LocalPath(service.config.BackupDir, state.LocalName)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateBundle(t.Context(), path); err != nil {
		t.Fatal(err)
	}
}

func TestManualBackupUsesConfiguredDirectory(t *testing.T) {
	dataDir := t.TempDir()
	backupDir := filepath.Join(t.TempDir(), "goi-backups")
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	defer db.Close()
	store := NewStore(db)
	service := NewService(ServiceConfig{
		DataDir: dataDir, DatabasePath: databasePath, BackupDir: backupDir, Store: store,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	service.perform(t.Context(), "manual")
	state, err := store.State(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "success" || state.LocalName == "" {
		t.Fatalf("backup state = %+v", state)
	}
	if _, err := LocalPath(backupDir, state.LocalName); err != nil {
		t.Fatalf("configured backup missing: %v", err)
	}
	if files, err := os.ReadDir(filepath.Join(dataDir, "backups")); err == nil && len(files) != 0 {
		t.Fatalf("default backup directory contains %d files", len(files))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := service.QueueLocalRestore(t.Context(), state.LocalName); err != nil {
		t.Fatalf("queue configured backup restore: %v", err)
	}
	restore, err := ReadRestoreStatus(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if restore.State != "pending" {
		t.Fatalf("restore status = %+v", restore)
	}
	if err := CancelPendingRestore(dataDir); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareLocalDirectoryCreatesWritableDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "backups")
	if err := PrepareLocalDirectory(directory); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("backup directory contains write-test files: %v", entries)
	}
}

func TestGoogleDriveFailureKeepsLocalRecoveryCopy(t *testing.T) {
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	defer db.Close()
	store := NewStore(db)
	if err := store.UpdateSettings(t.Context(), Settings{Hour: 3, GoogleDrive: true, RetentionDays: DefaultRetentionDays}); err != nil {
		t.Fatal(err)
	}
	drive := &fakeDrive{configured: true, connected: true, uploadErr: errors.New("offline")}
	service := NewService(ServiceConfig{
		DataDir: dataDir, DatabasePath: databasePath, Store: store, Drive: drive,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	service.perform(t.Context(), "manual")
	state, err := store.State(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "failed" || state.LocalName == "" || state.ErrorMessage != "offline" {
		t.Fatalf("backup state = %+v", state)
	}
	if _, err := LocalPath(service.config.BackupDir, state.LocalName); err != nil {
		t.Fatalf("local recovery copy missing: %v", err)
	}
}

func TestRepeatedGoogleDriveFailuresKeepConfiguredDaysOfLocalCopies(t *testing.T) {
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	defer db.Close()
	store := NewStore(db)
	if err := store.UpdateSettings(t.Context(), Settings{Hour: 3, GoogleDrive: true, RetentionDays: 3}); err != nil {
		t.Fatal(err)
	}
	drive := &fakeDrive{configured: true, connected: true, uploadErr: errors.New("offline")}
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	service := NewService(ServiceConfig{
		DataDir: dataDir, DatabasePath: databasePath, Store: store, Drive: drive,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now },
	})

	for attempt := 0; attempt < 5; attempt++ {
		service.perform(t.Context(), "manual")
		now = now.AddDate(0, 0, 1)
	}
	files, err := ListLocal(service.config.BackupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("local backup count = %d, want 3", len(files))
	}
	oldestRetained := now.AddDate(0, 0, -3).UTC().Format("20060102T150405")
	if !strings.Contains(files[len(files)-1].Name, oldestRetained) {
		t.Fatalf("oldest retained backup = %q, want timestamp %s", files[len(files)-1].Name, oldestRetained)
	}
}

func TestDriveDisconnectIsRejectedWhileBackupIsRunning(t *testing.T) {
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	defer db.Close()
	store := NewStore(db)
	if err := store.UpdateSettings(t.Context(), Settings{Hour: 3, GoogleDrive: true, RetentionDays: DefaultRetentionDays}); err != nil {
		t.Fatal(err)
	}
	drive := &blockingUploadDrive{
		fakeDrive: &fakeDrive{configured: true, connected: true},
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	service := NewService(ServiceConfig{
		DataDir: dataDir, DatabasePath: databasePath, Store: store, Drive: drive,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	workerDone := make(chan struct{})
	go func() {
		service.Run(ctx)
		close(workerDone)
	}()
	if err := service.QueueManual(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drive.started:
	case <-time.After(5 * time.Second):
		t.Fatal("backup did not reach Google Drive upload")
	}

	if err := service.DisconnectDrive(t.Context()); !errors.Is(err, ErrBackupRunning) {
		t.Fatalf("DisconnectDrive() error = %v, want ErrBackupRunning", err)
	}
	if !drive.Connected() {
		t.Fatal("busy disconnect removed Google Drive credentials")
	}
	files, err := ListLocal(service.config.BackupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("local backups during upload = %d, want 1", len(files))
	}

	close(drive.release)
	deadline := time.Now().Add(5 * time.Second)
	for service.Busy() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if service.Busy() {
		t.Fatal("backup did not finish")
	}
	if err := service.DisconnectDrive(t.Context()); err != nil {
		t.Fatal(err)
	}
	if drive.Connected() {
		t.Fatal("Google Drive remained connected after backup finished")
	}
	settings, err := store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if settings.GoogleDrive || !settings.KeepLocal {
		t.Fatalf("destination = %+v, want local", settings)
	}
	cancel()
	select {
	case <-workerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("backup worker did not stop")
	}
}

func TestQueueLocalRestoreUsesOnlyManagedBackupFiles(t *testing.T) {
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	defer db.Close()
	store := NewStore(db)
	service := NewService(ServiceConfig{DataDir: dataDir, DatabasePath: databasePath, Store: store})
	name := "goi-local" + BundleSuffix
	path := filepath.Join(dataDir, "backups", name)
	if err := CreateBundle(t.Context(), databasePath, path, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := service.QueueLocalRestore(t.Context(), name); err != nil {
		t.Fatal(err)
	}
	status, err := ReadRestoreStatus(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "pending" {
		t.Fatalf("restore status = %+v", status)
	}
	if err := CancelPendingRestore(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := service.QueueLocalRestore(t.Context(), "../"+name); err == nil {
		t.Fatal("QueueLocalRestore() accepted path traversal")
	}
}

func TestGoogleDriveOnlyRemovesLocalCopyAfterUpload(t *testing.T) {
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	defer db.Close()
	store := NewStore(db)
	if err := store.UpdateSettings(t.Context(), Settings{Hour: 3, GoogleDrive: true, RetentionDays: DefaultRetentionDays}); err != nil {
		t.Fatal(err)
	}
	drive := &fakeDrive{configured: true, connected: true}
	service := NewService(ServiceConfig{
		DataDir: dataDir, DatabasePath: databasePath, Store: store, Drive: drive,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	service.perform(t.Context(), "manual")
	state, err := store.State(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "success" || state.LocalName != "" || state.RemoteID != "remote1" || len(drive.uploads) != 1 {
		t.Fatalf("backup state = %+v, uploads = %v", state, drive.uploads)
	}
	files, err := ListLocal(service.config.BackupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("local backups = %+v", files)
	}
}

func TestGoogleDriveOnlyKeepsLocalCopyWhenUploadReturnsInvalidID(t *testing.T) {
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	defer db.Close()
	store := NewStore(db)
	if err := store.UpdateSettings(t.Context(), Settings{Hour: 3, GoogleDrive: true, RetentionDays: DefaultRetentionDays}); err != nil {
		t.Fatal(err)
	}
	drive := &fakeDrive{
		configured: true,
		connected:  true,
		uploadResult: &RemoteBackup{
			ID: "",
		},
	}
	service := NewService(ServiceConfig{
		DataDir: dataDir, DatabasePath: databasePath, Store: store, Drive: drive,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	service.perform(t.Context(), "manual")
	state, err := store.State(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "failed" || state.LocalName == "" || !strings.Contains(state.ErrorMessage, "invalid backup reference") {
		t.Fatalf("backup state = %+v", state)
	}
	if _, err := LocalPath(service.config.BackupDir, state.LocalName); err != nil {
		t.Fatalf("local recovery copy missing: %v", err)
	}
}

func TestStoreRecoversInterruptedBackup(t *testing.T) {
	db, err := database.Open(t.Context(), filepath.Join(t.TempDir(), "vocab.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	if err := store.Begin(t.Context(), "manual", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverInterrupted(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := store.State(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "failed" || state.ErrorMessage == "" {
		t.Fatalf("recovered state = %+v", state)
	}
}

func TestPruneLocalBeforeKeepsFilesOnAndAfterCutoff(t *testing.T) {
	dataDir := t.TempDir()
	directory := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 9; index++ {
		name := filepath.Join(directory, "goi-"+time.Unix(int64(index), 0).UTC().Format("20060102T150405Z")+BundleSuffix)
		if err := os.WriteFile(name, []byte("backup"), 0o640); err != nil {
			t.Fatal(err)
		}
		when := time.Unix(int64(index), 0)
		if err := os.Chtimes(name, when, when); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := time.Unix(2, 0)
	if err := PruneLocalBefore(directory, cutoff); err != nil {
		t.Fatal(err)
	}
	files, err := ListLocal(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 7 {
		t.Fatalf("local backup count = %d", len(files))
	}
	if files[len(files)-1].CreatedAt.Before(cutoff) {
		t.Fatalf("oldest backup = %v, cutoff = %v", files[len(files)-1].CreatedAt, cutoff)
	}
}

func TestRemotePruningDeletesOnlyCurrentInstallationBackups(t *testing.T) {
	drive := &fakeDrive{
		files: []RemoteBackup{
			{ID: "other-1"},
			{ID: "other-2"},
		},
	}
	cutoff := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	for index := 1; index <= 9; index++ {
		drive.currentFiles = append(drive.currentFiles, RemoteBackup{
			ID: fmt.Sprintf("current-%d", index), CreatedAt: cutoff.AddDate(0, 0, index-3),
		})
	}
	if err := pruneRemote(t.Context(), drive, cutoff); err != nil {
		t.Fatal(err)
	}
	want := []string{"current-1", "current-2"}
	if !slices.Equal(drive.deleted, want) {
		t.Fatalf("deleted backups = %v, want %v", drive.deleted, want)
	}
}
