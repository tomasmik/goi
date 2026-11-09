package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tomasmik/goi/internal/database"
)

func TestRunDoesNotClearLockedDatabaseDirectory(t *testing.T) {
	dataDirectory := filepath.Join(t.TempDir(), "test-data")
	databasePath, err := prepareDataDirectory(dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(dataDirectory, "keep.txt")
	if err := os.WriteFile(sentinelPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := database.AcquireLock(databasePath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	if err := run(context.Background(), []string{"qa"}, &bytes.Buffer{}, dataDirectory); err == nil {
		t.Fatal("run rebuilt a locked test database")
	}
	contents, err := os.ReadFile(sentinelPath)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("sentinel contents = %q, error = %v", contents, err)
	}
}

func TestPrepareDataDirectoryRejectsBroadTargets(t *testing.T) {
	for _, target := range []string{"", string(filepath.Separator), "."} {
		if _, err := prepareDataDirectory(target); err == nil {
			t.Fatalf("prepareDataDirectory(%q) succeeded", target)
		}
	}
}

func TestPrepareDataDirectoryRejectsUnmarkedNonemptyDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "unrelated")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	keepPath := filepath.Join(directory, "keep.txt")
	if err := os.WriteFile(keepPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := prepareDataDirectory(directory); err == nil {
		t.Fatal("prepareDataDirectory accepted an unmarked nonempty directory")
	}
	contents, err := os.ReadFile(keepPath)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("sentinel contents = %q, error = %v", contents, err)
	}
}
