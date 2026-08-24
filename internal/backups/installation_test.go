package backups

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestInstallationIDPersists(t *testing.T) {
	dataDir := t.TempDir()
	first, err := LoadOrCreateInstallationID(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateInstallationID(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !validInstallationID(first) {
		t.Fatalf("installation IDs = %q, %q", first, second)
	}
	info, err := os.Stat(filepath.Join(dataDir, installationIDFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("installation ID mode = %o", info.Mode().Perm())
	}
}

func TestInstallationIDRejectsInvalidPersistedValue(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, installationIDFile), []byte("shared\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateInstallationID(dataDir); err == nil {
		t.Fatal("LoadOrCreateInstallationID() accepted an invalid ID")
	}
}

func TestInstallationIDRejectsOversizedPersistedValue(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, installationIDFile)
	if err := os.WriteFile(path, make([]byte, maxInstallationIDBytes+1), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateInstallationID(dataDir); err == nil {
		t.Fatal("LoadOrCreateInstallationID() accepted an oversized ID file")
	}
}

func TestConcurrentInstallationIDCreationReturnsPersistedIdentity(t *testing.T) {
	dataDir := t.TempDir()
	const workers = 32
	start := make(chan struct{})
	results := make(chan string, workers)
	errors := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			installationID, err := LoadOrCreateInstallationID(dataDir)
			if err != nil {
				errors <- err
				return
			}
			results <- installationID
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	persisted, err := readInstallationID(filepath.Join(dataDir, installationIDFile))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for installationID := range results {
		count++
		if installationID != persisted {
			t.Fatalf("concurrent installation ID = %q, persisted = %q", installationID, persisted)
		}
	}
	if count != workers {
		t.Fatalf("installation ID results = %d, want %d", count, workers)
	}
}
