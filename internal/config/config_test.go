package config

import (
	"path/filepath"
	"testing"
)

func TestLoadNormalizesBaseURLOrigin(t *testing.T) {
	t.Setenv("APP_BASE_URL", "  HTTPS://Vocab.Example.COM:0443/  ")

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BaseURL != "https://vocab.example.com" || !loaded.SecureCookies {
		t.Fatalf("base URL = %q, secure cookies = %t", loaded.BaseURL, loaded.SecureCookies)
	}
}

func TestLoadRejectsInvalidBaseURL(t *testing.T) {
	for _, value := range []string{
		"localhost:8080",
		"http://:8080",
		"ftp://vocab.example.com",
		"https://user@vocab.example.com",
		"https://vocab.example.com/goi",
		"https://vocab.example.com?",
		"https://vocab.example.com?mode=study",
		"https://vocab.example.com#study",
		"http://localhost:0",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("APP_BASE_URL", value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted APP_BASE_URL %q", value)
			}
		})
	}
}

func TestLoadCanonicalizesBaseURLPorts(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{value: "http://Example.COM:080", want: "http://example.com"},
		{value: "https://Example.COM:8443", want: "https://example.com:8443"},
		{value: "http://[2001:0DB8::1]:8080", want: "http://[2001:db8::1]:8080"},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv("APP_BASE_URL", test.value)
			loaded, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if loaded.BaseURL != test.want {
				t.Fatalf("base URL = %q, want %q", loaded.BaseURL, test.want)
			}
		})
	}
}

func TestLoadRejectsDatabasePathOwnedByBackups(t *testing.T) {
	dataDir := t.TempDir()
	for _, relative := range []string{
		filepath.Join("backups", "goi-old.goi-backup.zip"),
		"pending-restore.goi-backup.zip",
		"restore-status.json",
		"restore-queue",
		"restore-queue.lock",
		"restore-receipt.json",
		"google-drive.json",
		"google-drive-client.json",
		"example-generation.json",
		"installation-id",
		"wanikani-token",
		"failed-restore-20260805T120000Z.goi-backup.zip",
	} {
		t.Run(relative, func(t *testing.T) {
			t.Setenv("APP_DATA_DIR", dataDir)
			t.Setenv("APP_DATABASE_PATH", filepath.Join(dataDir, relative))
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted backup-owned database path %q", relative)
			}
		})
	}
}

func TestLoadRejectsUnsafeLLMBaseURL(t *testing.T) {
	for _, value := range []string{
		"http://models.example/v1",
		"ftp://models.example/v1",
		"https://user@models.example/v1",
		"https://models.example/v1?mode=fast",
		"https://models.example/v1#models",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("APP_LLM_BASE_URL", value)
			t.Setenv("APP_LLM_MODEL", "free-model")
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted APP_LLM_BASE_URL %q", value)
			}
		})
	}
}

func TestLoadStorageIgnoresServerOnlyConfiguration(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("APP_DATA_DIR", dataDir)
	t.Setenv("APP_DATABASE_PATH", filepath.Join(dataDir, "maintenance.sqlite"))
	t.Setenv("APP_TIME_ZONE", "not-a-timezone")
	t.Setenv("APP_BASE_URL", "://invalid")
	t.Setenv("APP_AUTH_MODE", "not-a-boolean")
	t.Setenv("APP_TRUST_PROXY", "not-a-boolean")
	t.Setenv("APP_LLM_BASE_URL", "http://models.example")
	t.Setenv("APP_LLM_MODEL", "")

	loaded, err := LoadStorage()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DataDir != dataDir ||
		loaded.DatabasePath != filepath.Join(dataDir, "maintenance.sqlite") ||
		loaded.BackupDir != filepath.Join(dataDir, "backups") {
		t.Fatalf("storage config = %#v", loaded)
	}
}
