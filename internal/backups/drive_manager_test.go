package backups

import (
	"path/filepath"
	"strings"
	"testing"
)

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
