package backups

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	internalweb "github.com/tomasmik/goi/internal/web"
)

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

func TestDisconnectGoogleSwitchesDestinationToLocalFirst(t *testing.T) {
	handler, service, drive, _, _ := newHandlerTest(t)
	if err := service.config.Store.UpdateSettings(t.Context(), Settings{Enabled: true, Hour: 8, GoogleDrive: true, KeepLocal: true, RetentionDays: 1}); err != nil {
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
