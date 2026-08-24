package securefile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCreatesAndReplacesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	for _, contents := range [][]byte{[]byte("first"), []byte("second")} {
		if err := Write(path, contents); err != nil {
			t.Fatal(err)
		}
		stored, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(stored) != string(contents) {
			t.Fatalf("contents = %q, want %q", stored, contents)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("permissions = %o, want 600", info.Mode().Perm())
		}
	}
}
