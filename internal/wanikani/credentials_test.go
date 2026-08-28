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

func TestCredentialsRejectSymlinkAndInvalidToken(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte(strings.Repeat("a", 36)), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, TokenFilename)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	credentials := NewCredentials(path)
	if _, _, err := credentials.Load(); err == nil {
		t.Fatal("Load() accepted a symlink")
	}
	if err := credentials.Delete(); err == nil {
		t.Fatal("Delete() accepted a symlink")
	}
	if err := NewCredentials(filepath.Join(directory, "other")).Save("short"); err == nil {
		t.Fatal("Save() accepted a short token")
	}
}
