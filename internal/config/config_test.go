package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAllowsEnabledAuthOverHTTP(t *testing.T) {
	t.Setenv("APP_AUTH_MODE", "true")
	t.Setenv("APP_AUTH_USERNAME", "study-owner")
	t.Setenv("APP_AUTH_PASSWORD", "correct horse battery staple")
	t.Setenv("APP_BASE_URL", "http://localhost:8080")
	t.Setenv("APP_TIME_ZONE", "UTC")

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.AuthEnabled || loaded.BaseURL != "http://localhost:8080" {
		t.Fatalf("config = auth %t, base URL %q", loaded.AuthEnabled, loaded.BaseURL)
	}
}

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

func TestLoadRejectsDatabasePathAtJMdictCache(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("APP_DATA_DIR", dataDir)
	t.Setenv("APP_DATABASE_PATH", filepath.Join(dataDir, "jmdict.sqlite"))

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted the JMdict cache as the application database")
	}
}

func TestLoadRejectsJitenCacheAndAliases(t *testing.T) {
	for _, name := range []string{"jiten.sqlite", "jiten.sqlite-wal", "jiten.sqlite-shm", "jiten.sqlite-journal"} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			cache := filepath.Join(directory, name)
			if err := os.WriteFile(cache, []byte("preserve"), 0o600); err != nil {
				t.Fatal(err)
			}
			symlink, hardlink := filepath.Join(directory, "symlink"), filepath.Join(directory, "hardlink")
			if err := os.Symlink(cache, symlink); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(cache, hardlink); err != nil {
				t.Fatal(err)
			}
			t.Setenv("APP_DATA_DIR", directory)
			for _, path := range []string{cache, symlink, hardlink} {
				t.Setenv("APP_DATABASE_PATH", path)
				if _, err := Load(); err == nil {
					t.Fatalf("accepted cache alias %q", path)
				}
			}
		})
	}
}

func TestLoadRejectsDatabasePathInAnkiStaging(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("APP_DATA_DIR", dataDir)
	t.Setenv("APP_DATABASE_PATH", filepath.Join(dataDir, "imports", "run-database.sqlite"))

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an application database inside Anki staging")
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

func TestLoadRejectsDatabasePathThroughBackupDirectoryAlias(t *testing.T) {
	dataDir := t.TempDir()
	backupDir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "backup-alias")
	if err := os.Symlink(backupDir, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_DATA_DIR", dataDir)
	t.Setenv("APP_DATABASE_PATH", filepath.Join(alias, "goi-live.goi-backup.zip"))
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a database through a managed-backup symlink")
	}
}

func TestLoadUsesConfiguredBackupDirectory(t *testing.T) {
	dataDir := t.TempDir()
	backupDir := filepath.Join(t.TempDir(), "goi-backups")
	t.Setenv("APP_DATA_DIR", dataDir)
	t.Setenv("APP_BACKUP_DIR", backupDir)

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BackupDir != backupDir {
		t.Fatalf("backup directory = %q, want %q", loaded.BackupDir, backupDir)
	}
}

func TestLoadDefaultsBackupDirectoryUnderDataDirectory(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("APP_DATA_DIR", dataDir)

	loaded, err := LoadStorage()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dataDir, "backups")
	if loaded.BackupDir != want {
		t.Fatalf("backup directory = %q, want %q", loaded.BackupDir, want)
	}
}

func TestLoadRejectsDatabaseInsideConfiguredBackupDirectory(t *testing.T) {
	backupDir := t.TempDir()
	t.Setenv("APP_DATA_DIR", t.TempDir())
	t.Setenv("APP_BACKUP_DIR", backupDir)
	t.Setenv("APP_DATABASE_PATH", filepath.Join(backupDir, "vocab.sqlite"))

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted the application database inside APP_BACKUP_DIR")
	}
}

func TestLoadResolvesStoragePathAliases(t *testing.T) {
	dataDir := t.TempDir()
	aliasRoot := t.TempDir()
	alias := filepath.Join(aliasRoot, "data")
	if err := os.Symlink(dataDir, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_DATA_DIR", dataDir)
	t.Setenv("APP_DATABASE_PATH", filepath.Join(alias, "jmdict.sqlite"))

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a symlink alias of the JMdict cache")
	}
}

func TestLoadRejectsWhitespaceOnlyAuthUsername(t *testing.T) {
	t.Setenv("APP_AUTH_MODE", "true")
	t.Setenv("APP_AUTH_USERNAME", "   ")
	t.Setenv("APP_AUTH_PASSWORD", "secret")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a whitespace-only authentication username")
	}
}

func TestLoadDisablesAuthByDefault(t *testing.T) {
	t.Setenv("APP_AUTH_MODE", "false")
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AuthEnabled {
		t.Fatal("authentication is enabled")
	}
	if loaded.ListenAddr != "127.0.0.1:8080" {
		t.Fatalf("default listener = %q", loaded.ListenAddr)
	}
}

func TestLoadDisablesLLMWhenBaseURLIsBlank(t *testing.T) {
	t.Setenv("APP_LLM_BASE_URL", "")
	t.Setenv("APP_LLM_MODEL", "ignored-model")
	t.Setenv("APP_LLM_API_KEY", "ignored-key")

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LLMBaseURL != "" || loaded.LLMModel != "" || loaded.LLMAPIKey != "" {
		t.Fatalf("LLM config = %#v", loaded)
	}
}

func TestLoadAcceptsLoopbackLLMOverHTTP(t *testing.T) {
	t.Setenv("APP_LLM_BASE_URL", "  http://127.0.0.1:11434/v1/  ")
	t.Setenv("APP_LLM_MODEL", " qwen3:4b ")
	t.Setenv("APP_LLM_API_KEY", "local-secret")

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LLMBaseURL != "http://127.0.0.1:11434/v1" || loaded.LLMModel != "qwen3:4b" || loaded.LLMAPIKey != "local-secret" {
		t.Fatalf("LLM config = base URL %q, model %q, API key %q", loaded.LLMBaseURL, loaded.LLMModel, loaded.LLMAPIKey)
	}
}

func TestLoadAcceptsRemoteLLMOverHTTPSWithoutAPIKey(t *testing.T) {
	t.Setenv("APP_LLM_BASE_URL", "https://models.example/v1")
	t.Setenv("APP_LLM_MODEL", "free-model")
	t.Setenv("APP_LLM_API_KEY", "")

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LLMBaseURL != "https://models.example/v1" || loaded.LLMModel != "free-model" || loaded.LLMAPIKey != "" {
		t.Fatalf("LLM config = base URL %q, model %q, API key %q", loaded.LLMBaseURL, loaded.LLMModel, loaded.LLMAPIKey)
	}
}

func TestLoadRejectsEnabledLLMWithoutModel(t *testing.T) {
	t.Setenv("APP_LLM_BASE_URL", "https://models.example/v1")
	t.Setenv("APP_LLM_MODEL", "   ")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an enabled LLM without a model")
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

func TestLoadAcceptsGoogleDriveOAuthPair(t *testing.T) {
	t.Setenv("APP_GOOGLE_DRIVE_CLIENT_ID", " client-id ")
	t.Setenv("APP_GOOGLE_DRIVE_CLIENT_SECRET", "client-secret")

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.GoogleDriveClientID != "client-id" || loaded.GoogleDriveClientSecret != "client-secret" {
		t.Fatalf("Google Drive config = %q, %q", loaded.GoogleDriveClientID, loaded.GoogleDriveClientSecret)
	}
}

func TestLoadRejectsIncompleteGoogleDriveOAuthPair(t *testing.T) {
	for _, missing := range []string{"id", "secret"} {
		t.Run(missing, func(t *testing.T) {
			if missing != "id" {
				t.Setenv("APP_GOOGLE_DRIVE_CLIENT_ID", "client-id")
			}
			if missing != "secret" {
				t.Setenv("APP_GOOGLE_DRIVE_CLIENT_SECRET", "client-secret")
			}
			if _, err := Load(); err == nil {
				t.Fatal("Load() accepted an incomplete Google Drive OAuth pair")
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
