package backups

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoogleDriveManagerSavesAndRemovesDownloadedClient(t *testing.T) {
	directory := t.TempDir()
	clientPath := filepath.Join(directory, GoogleDriveClientFilename)
	callback := "https://goi.example/settings/backups/google/callback"
	config := GoogleDriveManagerConfig{
		Drive: GoogleDriveConfig{
			RedirectURL:    callback,
			CredentialPath: filepath.Join(directory, "google-drive.json"),
			InstallationID: testInstallationID,
		},
		ClientConfigPath: clientPath,
	}
	manager, err := NewGoogleDriveManager(config)
	if err != nil {
		t.Fatal(err)
	}
	if manager.Configured() || manager.Connected() || manager.CallbackURL() != callback || manager.EnvironmentManaged() {
		t.Fatalf("new manager state = configured %v, connected %v, callback %q, environment %v",
			manager.Configured(), manager.Connected(), manager.CallbackURL(), manager.EnvironmentManaged())
	}

	withoutCallback := downloadedClientJSON(t, "https://other.example/callback")
	if err := manager.SaveClient(strings.NewReader(withoutCallback)); err == nil || !strings.Contains(err.Error(), callback) {
		t.Fatalf("SaveClient() callback error = %v", err)
	}
	if manager.Configured() {
		t.Fatal("manager was configured after rejecting the callback")
	}

	if err := manager.SaveClient(strings.NewReader(downloadedClientJSON(t, callback))); err != nil {
		t.Fatal(err)
	}
	if !manager.Configured() || manager.Connected() {
		t.Fatalf("saved manager state = configured %v, connected %v", manager.Configured(), manager.Connected())
	}
	info, err := os.Stat(clientPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("client permissions = %o, want 600", info.Mode().Perm())
	}
	contents, err := os.ReadFile(clientPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != `{"client_id":"client-id","client_secret":"client-secret"}` {
		t.Fatalf("stored client = %s", contents)
	}

	reloaded, err := NewGoogleDriveManager(config)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Configured() {
		t.Fatal("reloaded manager is not configured")
	}
	if err := reloaded.RemoveClient(); err != nil {
		t.Fatal(err)
	}
	if reloaded.Configured() {
		t.Fatal("manager remained configured after client removal")
	}
	if _, err := os.Stat(clientPath); !os.IsNotExist(err) {
		t.Fatalf("client file remains after removal: %v", err)
	}
}

func TestGoogleDriveManagerEnvironmentClientCannotBeChangedInApp(t *testing.T) {
	path := filepath.Join(t.TempDir(), GoogleDriveClientFilename)
	manager, err := NewGoogleDriveManager(GoogleDriveManagerConfig{
		Drive: GoogleDriveConfig{
			ClientID:       " client-id ",
			ClientSecret:   " client-secret ",
			RedirectURL:    "https://goi.example/settings/backups/google/callback",
			CredentialPath: filepath.Join(t.TempDir(), "google-drive.json"),
			InstallationID: testInstallationID,
		},
		ClientConfigPath: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !manager.Configured() || !manager.EnvironmentManaged() {
		t.Fatalf("environment manager state = configured %v, environment %v", manager.Configured(), manager.EnvironmentManaged())
	}
	if err := manager.SaveClient(strings.NewReader(downloadedClientJSON(t, manager.CallbackURL()))); err == nil {
		t.Fatal("SaveClient() replaced an environment-managed client")
	}
	if err := manager.RemoveClient(); err == nil {
		t.Fatal("RemoveClient() removed an environment-managed client")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("environment client wrote a settings file: %v", err)
	}
}

func TestGoogleDriveManagerRejectsDesktopClientJSON(t *testing.T) {
	manager, err := NewGoogleDriveManager(GoogleDriveManagerConfig{
		Drive: GoogleDriveConfig{
			RedirectURL:    "https://goi.example/settings/backups/google/callback",
			CredentialPath: filepath.Join(t.TempDir(), "google-drive.json"),
			InstallationID: testInstallationID,
		},
		ClientConfigPath: filepath.Join(t.TempDir(), GoogleDriveClientFilename),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SaveClient(strings.NewReader(`{"installed":{"client_id":"client","client_secret":"secret"}}`)); err == nil || !strings.Contains(err.Error(), "Web application") {
		t.Fatalf("desktop client error = %v", err)
	}
}

func downloadedClientJSON(t *testing.T, callback string) string {
	t.Helper()
	contents, err := json.Marshal(map[string]any{
		"web": map[string]any{
			"client_id":     "client-id",
			"client_secret": "client-secret",
			"redirect_uris": []string{callback},
			"auth_uri":      "https://accounts.google.com/o/oauth2/auth",
			"token_uri":     "https://oauth2.googleapis.com/token",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
