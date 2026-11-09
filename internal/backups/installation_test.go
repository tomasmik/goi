package backups

import (
	"path/filepath"
	"sync"
	"testing"
)

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
