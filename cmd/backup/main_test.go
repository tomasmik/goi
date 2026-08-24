package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tomasmik/goi/internal/database"
)

func TestRunValidatesArgumentsBeforeChangingFiles(t *testing.T) {
	output := filepath.Join(t.TempDir(), "backup.sqlite")
	t.Setenv("APP_DATA_DIR", t.TempDir())
	t.Setenv("APP_AUTH_MODE", "false")

	err := run(context.Background(), "backup", "", output, 0)
	if err == nil || !strings.Contains(err.Error(), "-keep") {
		t.Fatalf("run() error = %v, want invalid retention", err)
	}
}

func TestRunRejectsUnsupportedModeBeforeLoadingConfiguration(t *testing.T) {
	t.Setenv("APP_TIME_ZONE", "not-a-timezone")

	err := run(context.Background(), "unexpected", "", "", 7)
	if err == nil || !strings.Contains(err.Error(), "unsupported mode") {
		t.Fatalf("run() error = %v, want unsupported mode", err)
	}
}

func TestRunBackupIgnoresServerOnlyConfiguration(t *testing.T) {
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "vocab.sqlite")
	t.Setenv("APP_DATA_DIR", dataDir)
	t.Setenv("APP_DATABASE_PATH", databasePath)
	t.Setenv("APP_TIME_ZONE", "not-a-timezone")
	t.Setenv("APP_BASE_URL", "://invalid")
	t.Setenv("APP_AUTH_MODE", "not-a-boolean")
	t.Setenv("APP_TRUST_PROXY", "not-a-boolean")

	db, err := database.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(context.Background(), db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(dataDir, "backups", "emergency.sqlite")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := run(ctx, "backup", "", outputPath, 1); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outputPath, outputPath + ".sha256"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("inspect backup artifact %q: %v", path, err)
		}
	}
}
