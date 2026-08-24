package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tomasmik/goi/internal/database"
)

func TestRunBuildsAndListsMixedScenario(t *testing.T) {
	dataDirectory := filepath.Join(t.TempDir(), "test-data")
	var output bytes.Buffer
	if err := run(context.Background(), []string{"mixed"}, &output, dataDirectory); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Scenario: mixed") ||
		!strings.Contains(output.String(), "Lessons available: 10") ||
		!strings.Contains(output.String(), "Due: 6") {
		t.Fatalf("mixed output = %q", output.String())
	}

	output.Reset()
	if err := run(context.Background(), []string{"list"}, &output, dataDirectory); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "難しい") || !strings.Contains(output.String(), "Burned") {
		t.Fatalf("list output = %q", output.String())
	}
}

func TestRunBuildsQAScenario(t *testing.T) {
	dataDirectory := filepath.Join(t.TempDir(), "test-data")
	var output bytes.Buffer
	if err := run(context.Background(), []string{"qa"}, &output, dataDirectory); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"Scenario: qa",
		"Vocabulary: 24",
		"Known elsewhere: 3",
		"Mining captures: 3 pending, 1 accepted, 1 discarded",
		"Examples: 3",
		"Media assets: 2",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("QA output does not contain %q: %s", expected, output.String())
		}
	}

	output.Reset()
	if err := run(context.Background(), []string{"list"}, &output, dataDirectory); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "KNOWN ELSEWHERE") || !strings.Contains(output.String(), "勉強する") || !strings.Contains(output.String(), "yes") {
		t.Fatalf("QA list output = %q", output.String())
	}
}

func TestRunRejectsInvalidCommands(t *testing.T) {
	dataDirectory := filepath.Join(t.TempDir(), "test-data")
	tests := [][]string{
		nil,
		{"unknown"},
		{"due", "-count", "wrong"},
		{"stage", "-id", "1"},
		{"unlearn"},
		{"qa", "extra"},
	}
	for _, args := range tests {
		if err := run(context.Background(), args, &bytes.Buffer{}, dataDirectory); err == nil {
			t.Fatalf("run(%q) succeeded", args)
		}
	}
}

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

func TestRunRejectsSymlinkedRebuildDirectory(t *testing.T) {
	victim := t.TempDir()
	keepPath := filepath.Join(victim, "keep.txt")
	if err := os.WriteFile(keepPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "test-data")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}

	if err := run(context.Background(), []string{"mixed"}, &bytes.Buffer{}, link); err == nil {
		t.Fatal("run accepted a symlinked rebuild directory")
	}
	if contents, err := os.ReadFile(keepPath); err != nil || string(contents) != "keep" {
		t.Fatalf("victim contents = %q, error = %v", contents, err)
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
