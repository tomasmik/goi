package backups

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	internalweb "github.com/tomasmik/goi/internal/web"
)

type blockingListDrive struct {
	*fakeDrive
}

type fakeDriveWithSetup struct {
	*fakeDrive
	callback string
	saved    string
	removed  bool
}

func (f *fakeDriveWithSetup) CallbackURL() string {
	return f.callback
}

func (f *fakeDriveWithSetup) EnvironmentManaged() bool {
	return false
}

func (f *fakeDriveWithSetup) SaveClient(source io.Reader) error {
	contents, err := io.ReadAll(source)
	if err == nil {
		f.saved = string(contents)
		f.configured = true
	}
	return err
}

func (f *fakeDriveWithSetup) RemoveClient() error {
	f.removed = true
	f.configured = false
	return nil
}

type readCountingBody struct {
	reads int
}

func (body *readCountingBody) Read([]byte) (int, error) {
	body.reads++
	return 0, errors.New("body should not be read")
}

func (d *blockingListDrive) List(ctx context.Context) ([]RemoteBackup, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRestoreUploadRejectsDeclaredOversizeBeforeReading(t *testing.T) {
	handler, _, _, dataDir, _ := newHandlerTest(t)
	body := &readCountingBody{}
	request := httptest.NewRequest(http.MethodPost, "/settings/backups/restore/upload", body)
	request.ContentLength = RestoreUploadRequestLimit + 1
	request.Header.Set("Content-Type", "multipart/form-data; boundary=goi-test")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
	if body.reads != 0 {
		t.Fatalf("oversized body reads = %d, want 0", body.reads)
	}
	if !strings.Contains(response.Body.String(), "The backup upload is too large or invalid.") {
		t.Fatalf("response does not explain the rejected upload: %s", response.Body.String())
	}
	assertNoPendingRestore(t, dataDir)
}

func TestGoogleClientSetupPageAndUpload(t *testing.T) {
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	t.Cleanup(func() { db.Close() })
	store := NewStore(db)
	drive := &fakeDriveWithSetup{
		fakeDrive: &fakeDrive{},
		callback:  "https://goi.example/settings/backups/google/callback",
	}
	service := NewService(ServiceConfig{DataDir: dataDir, DatabasePath: databasePath, Store: store, Drive: drive})
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, service, drive, dataDir, renderer).Routes(router)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/settings/backups", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{
		drive.callback,
		`action="/settings/backups/google/client"`,
		"Save connection file",
		"Web application",
		`name="retention_days" min="1" max="3" value="1"`,
		"Each completed backup is verified.",
		filepath.Join(dataDir, "backups"),
		"persistent storage",
	} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("setup page does not contain %q: %s", expected, response.Body.String())
		}
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("client", "client_secret.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, `{"web":{"client_id":"client","client_secret":"secret"}}`); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/settings/backups/google/client", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/settings/backups?result=google_client_saved" {
		t.Fatalf("POST status = %d, location = %q, body = %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if drive.saved == "" || !drive.configured {
		t.Fatalf("saved client = %q, configured = %v", drive.saved, drive.configured)
	}
}

func TestBackupPageUsesConfiguredLocalDirectory(t *testing.T) {
	dataDir := t.TempDir()
	backupDir := filepath.Join(t.TempDir(), "goi-backups")
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	t.Cleanup(func() { db.Close() })
	store := NewStore(db)
	service := NewService(ServiceConfig{
		DataDir: dataDir, DatabasePath: databasePath, BackupDir: backupDir, Store: store,
	})
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, service, nil, dataDir, renderer).Routes(router)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/settings/backups", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), backupDir) {
		t.Fatalf("backup page does not show configured directory: %s", response.Body.String())
	}
}

func TestRestoreLocalRequiresServerSideConfirmation(t *testing.T) {
	handler, service, _, dataDir, databasePath := newHandlerTest(t)
	name := "goi-local" + BundleSuffix
	path := filepath.Join(dataDir, "backups", name)
	if err := CreateBundle(t.Context(), databasePath, path, service.config.Now()); err != nil {
		t.Fatal(err)
	}

	response := postForm(handler, "/settings/backups/restore/local/"+name, nil)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
	if !strings.Contains(response.Body.String(), "Confirm the local restore") {
		t.Fatalf("response does not explain confirmation requirement: %s", response.Body.String())
	}
	assertNoPendingRestore(t, dataDir)
	download := httptest.NewRecorder()
	handler.ServeHTTP(download, httptest.NewRequest(http.MethodGet, "/settings/backups/local/"+name, nil))
	if download.Code != http.StatusOK || download.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("backup download = %d, Cache-Control %q", download.Code, download.Header().Get("Cache-Control"))
	}
}

func TestBackupTimesUseTheConfiguredTimezone(t *testing.T) {
	value := time.Date(2026, 8, 5, 12, 30, 0, 0, time.UTC)
	if got := formatTime(value, "Europe/Vilnius"); got != "2026-08-05 15:30" {
		t.Fatalf("formatted backup time = %q", got)
	}
}

func TestParseBackupHourAcceptsTimeAndLegacyValues(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int
	}{
		{value: "08:00", want: 8},
		{value: "23:00", want: 23},
		{value: "8", want: 8},
	} {
		got, err := parseBackupHour(test.value)
		if err != nil || got != test.want {
			t.Fatalf("parseBackupHour(%q) = %d, %v; want %d", test.value, got, err, test.want)
		}
	}
	for _, value := range []string{"", "08:30", "08:00:00", "morning"} {
		if _, err := parseBackupHour(value); err == nil {
			t.Fatalf("parseBackupHour(%q) succeeded", value)
		}
	}
}

func TestBackupSettingsSaveRetentionDays(t *testing.T) {
	handler, service, _, _, _ := newHandlerTest(t)
	response := postForm(handler, "/settings/backups", url.Values{
		"enabled":        {"on"},
		"hour":           {"06:00"},
		"keep_local":     {"on"},
		"retention_days": {"2"},
	})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusSeeOther, response.Body.String())
	}
	settings, err := service.config.Store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Enabled || settings.Hour != 6 || !settings.KeepLocal || settings.GoogleDrive || settings.RetentionDays != 2 {
		t.Fatalf("saved settings = %+v", settings)
	}

	response = postForm(handler, "/settings/backups", url.Values{
		"hour":           {"06:00"},
		"keep_local":     {"on"},
		"retention_days": {"4"},
	})
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid retention status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
	settings, err = service.config.Store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if settings.RetentionDays != 2 {
		t.Fatalf("retention changed after invalid form: %+v", settings)
	}
}

func TestBackupPageStillRendersWhenDriveListingTimesOut(t *testing.T) {
	_, service, drive, dataDir, _ := newHandlerTest(t)
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	remoteDrive := &blockingListDrive{fakeDrive: drive}
	backupHandler := NewHandler(service.config.Store, service, remoteDrive, dataDir, renderer)
	backupHandler.driveListTimeout = time.Millisecond
	router := chi.NewRouter()
	backupHandler.Routes(router)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/settings/backups", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if !strings.Contains(body, "Could not list Google Drive backups") {
		t.Fatalf("response does not explain the Drive listing failure: %s", body)
	}
	if !strings.Contains(body, "Back up now") || !strings.Contains(body, "Restore from a file") {
		t.Fatalf("local recovery controls are missing after Drive timeout: %s", body)
	}
}

func TestBackupPageShowsPreservedLastSuccess(t *testing.T) {
	handler, service, _, _, _ := newHandlerTest(t)
	lastSuccess := time.Date(2026, 8, 5, 12, 30, 0, 0, time.UTC)
	if _, err := service.config.Store.db.ExecContext(t.Context(), `
		UPDATE backup_state
		SET status = 'idle', last_attempt_at = NULL, last_success_at = ?
		WHERE id = 1`, lastSuccess.Unix()); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/settings/backups", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if !strings.Contains(body, "Last success: 2026-08-05 12:30.") {
		t.Fatalf("preserved success is missing: %s", body)
	}
	if strings.Contains(body, "No backups have run yet.") {
		t.Fatalf("response incorrectly reports no backup history: %s", body)
	}
}

func TestBackupPageIdentifiesGoogleDriveBackups(t *testing.T) {
	handler, _, drive, _, _ := newHandlerTest(t)
	currentInstallation := "0123456789abcdef0123456789abcdef"
	otherInstallation := "fedcba9876543210fedcba9876543210"
	drive.files = []RemoteBackup{
		{
			ID:                  "remote-current",
			Name:                "goi-20260805T123012.000000000Z" + BundleSuffix,
			Size:                2048,
			CreatedAt:           time.Date(2026, 8, 5, 12, 30, 12, 0, time.UTC),
			AppProperties:       map[string]string{installationProperty: currentInstallation},
			CurrentInstallation: true,
		},
		{
			ID:            "remote-other",
			Name:          "goi-20260805T123011.000000000Z" + BundleSuffix,
			Size:          1024,
			CreatedAt:     time.Date(2026, 8, 5, 12, 30, 11, 0, time.UTC),
			AppProperties: map[string]string{installationProperty: otherInstallation},
		},
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/settings/backups", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	for _, value := range []string{
		"goi-20260805T123012.000000000Z" + BundleSuffix,
		"goi-20260805T123011.000000000Z" + BundleSuffix,
		"2026-08-05 12:30:12 UTC",
		"2026-08-05 12:30:11 UTC",
		"This server",
		"Another Goi server",
		"/settings/backups/restore/google/remote-current",
		"/settings/backups/restore/google/remote-other",
	} {
		if !strings.Contains(body, value) {
			t.Fatalf("response does not contain %q: %s", value, body)
		}
	}
	if strings.Contains(body, currentInstallation) || strings.Contains(body, otherInstallation) {
		t.Fatalf("response exposes a raw installation ID: %s", body)
	}
}

func TestBackupFormsRejectUnreadableBodiesBeforeChangingState(t *testing.T) {
	tests := []struct {
		name   string
		target string
		prefix string
		check  func(*testing.T, *Service, *fakeDrive, string)
	}{
		{
			name:   "settings",
			target: "/settings/backups",
			prefix: "enabled=on&hour=8&google_drive=on&padding=",
			check: func(t *testing.T, service *Service, _ *fakeDrive, _ string) {
				settings, err := service.config.Store.Settings(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if settings.Enabled || settings.Hour != 3 || settings.GoogleDrive || !settings.KeepLocal {
					t.Fatalf("settings changed after invalid form: %+v", settings)
				}
			},
		},
		{
			name:   "local restore",
			target: "/settings/backups/restore/local/example" + BundleSuffix,
			prefix: "confirmed=1&padding=",
			check: func(t *testing.T, _ *Service, _ *fakeDrive, dataDir string) {
				assertNoPendingRestore(t, dataDir)
			},
		},
		{
			name:   "Drive restore",
			target: "/settings/backups/restore/google/remote-1",
			prefix: "confirmed=1&padding=",
			check: func(t *testing.T, _ *Service, drive *fakeDrive, dataDir string) {
				if drive.downloads != 0 {
					t.Fatalf("Drive downloads = %d, want 0", drive.downloads)
				}
				assertNoPendingRestore(t, dataDir)
			},
		},
		{
			name:   "Drive disconnect",
			target: "/settings/backups/google/disconnect",
			prefix: "confirmed=1&padding=",
			check: func(t *testing.T, _ *Service, drive *fakeDrive, _ string) {
				if !drive.Connected() {
					t.Fatal("Google Drive was disconnected after an invalid form")
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, service, drive, dataDir, _ := newHandlerTest(t)
			body := test.prefix + strings.Repeat("x", backupFormBodyLimit)
			request := httptest.NewRequest(http.MethodPost, test.target, strings.NewReader(body))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
			}
			if !strings.Contains(response.Body.String(), "The backup form is too large or invalid.") {
				t.Fatalf("response does not explain the invalid form: %s", response.Body.String())
			}
			test.check(t, service, drive, dataDir)
		})
	}
}

func TestRestoreGoogleRequiresServerSideConfirmation(t *testing.T) {
	handler, _, drive, dataDir, _ := newHandlerTest(t)

	response := postForm(handler, "/settings/backups/restore/google/remote-1", nil)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
	if !strings.Contains(response.Body.String(), "Confirm the Google Drive restore") {
		t.Fatalf("response does not explain confirmation requirement: %s", response.Body.String())
	}
	if drive.downloads != 0 {
		t.Fatalf("Drive downloads = %d, want 0", drive.downloads)
	}
	assertNoPendingRestore(t, dataDir)
}

func TestDisconnectGoogleRequiresServerSideConfirmation(t *testing.T) {
	handler, service, drive, _, _ := newHandlerTest(t)
	if err := service.config.Store.UpdateSettings(t.Context(), Settings{Hour: 3, GoogleDrive: true, RetentionDays: DefaultRetentionDays}); err != nil {
		t.Fatal(err)
	}

	response := postForm(handler, "/settings/backups/google/disconnect", nil)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
	if !drive.Connected() {
		t.Fatal("Google Drive was disconnected without confirmation")
	}
	settings, err := service.config.Store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !settings.GoogleDrive || settings.KeepLocal {
		t.Fatalf("destination = %+v, want Google Drive only", settings)
	}
}

func TestDisconnectGoogleSwitchesDestinationToLocalFirst(t *testing.T) {
	handler, service, drive, _, _ := newHandlerTest(t)
	if err := service.config.Store.UpdateSettings(t.Context(), Settings{Enabled: true, Hour: 8, GoogleDrive: true, KeepLocal: true, RetentionDays: DefaultRetentionDays}); err != nil {
		t.Fatal(err)
	}

	response := postForm(handler, "/settings/backups/google/disconnect", url.Values{"confirmed": {"1"}})

	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusSeeOther, response.Body.String())
	}
	if drive.Connected() {
		t.Fatal("Google Drive is still connected")
	}
	settings, err := service.config.Store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if settings.GoogleDrive || !settings.KeepLocal || !settings.Enabled || settings.Hour != 8 {
		t.Fatalf("settings = %+v", settings)
	}
}

func TestDisconnectGoogleFailureStillLeavesSafeLocalDestination(t *testing.T) {
	handler, service, drive, _, _ := newHandlerTest(t)
	if err := service.config.Store.UpdateSettings(t.Context(), Settings{Hour: 3, GoogleDrive: true, RetentionDays: DefaultRetentionDays}); err != nil {
		t.Fatal(err)
	}
	drive.disconnectErr = errors.New("credential file is busy")

	response := postForm(handler, "/settings/backups/google/disconnect", url.Values{"confirmed": {"1"}})

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	settings, err := service.config.Store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if settings.GoogleDrive || !settings.KeepLocal {
		t.Fatalf("destination = %+v, want local", settings)
	}
	if !drive.Connected() {
		t.Fatal("failed disconnect unexpectedly removed credentials")
	}
}

func TestDisconnectGoogleWaitsForRunningBackup(t *testing.T) {
	handler, service, drive, _, _ := newHandlerTest(t)
	if err := service.config.Store.UpdateSettings(t.Context(), Settings{Hour: 3, GoogleDrive: true, RetentionDays: DefaultRetentionDays}); err != nil {
		t.Fatal(err)
	}
	if err := service.QueueManual(); err != nil {
		t.Fatal(err)
	}
	defer service.finishJob()

	response := postForm(handler, "/settings/backups/google/disconnect", url.Values{"confirmed": {"1"}})

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusConflict)
	}
	if !drive.Connected() {
		t.Fatal("Google Drive was disconnected while a backup was queued")
	}
	settings, err := service.config.Store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !settings.GoogleDrive || settings.KeepLocal {
		t.Fatalf("destination = %+v, want Google Drive only", settings)
	}
}

func TestQueuedBackupNoticeFollowsWorkerState(t *testing.T) {
	if got := backupNotice("queued", "idle", true); got != "Backup queued. This page will update when it finishes." {
		t.Fatalf("busy notice = %q", got)
	}
	if got := backupNotice("queued", "running", false); got != "Backup queued. This page will update when it finishes." {
		t.Fatalf("running notice = %q", got)
	}
	if got := backupNotice("queued", "success", false); got != "Backup completed." {
		t.Fatalf("success notice = %q", got)
	}
	if got := backupNotice("queued", "failed", false); got != "" {
		t.Fatalf("failed notice = %q", got)
	}
}

func newHandlerTest(t *testing.T) (http.Handler, *Service, *fakeDrive, string, string) {
	t.Helper()
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	db := openBackupTestDatabase(t, databasePath)
	t.Cleanup(func() { db.Close() })
	store := NewStore(db)
	drive := &fakeDrive{configured: true, connected: true}
	service := NewService(ServiceConfig{
		DataDir: dataDir, DatabasePath: databasePath, Store: store, Drive: drive,
	})
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, service, drive, dataDir, renderer).Routes(router)
	return router, service, drive, dataDir, databasePath
}

func postForm(handler http.Handler, target string, values url.Values) *httptest.ResponseRecorder {
	if values == nil {
		values = url.Values{}
	}
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertNoPendingRestore(t *testing.T, dataDir string) {
	t.Helper()
	status, err := ReadRestoreStatus(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if status.State == "pending" {
		t.Fatal("restore was queued without confirmation")
	}
}
