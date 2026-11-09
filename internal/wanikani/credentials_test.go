package wanikani

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialsSaveReplaceLoadAndDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials", TokenFilename)
	credentials := NewCredentials(path)
	for _, token := range []string{strings.Repeat("a", 36), strings.Repeat("b", 36)} {
		if err := credentials.Save("  " + token + "\n"); err != nil {
			t.Fatal(err)
		}
		loaded, exists, err := credentials.Load()
		if err != nil || !exists || loaded != token {
			t.Fatalf("Load() = %q, %v, %v", loaded, exists, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("credential mode = %o, want 600", info.Mode().Perm())
		}
	}
	if err := credentials.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := credentials.Load(); err != nil || exists {
		t.Fatalf("Load() after delete exists = %v, error = %v", exists, err)
	}
}
