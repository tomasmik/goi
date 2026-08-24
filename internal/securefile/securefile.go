package securefile

import (
	"errors"
	"os"
	"path/filepath"
)

func Write(path string, contents []byte) error {
	directoryPath := filepath.Dir(path)
	if err := os.MkdirAll(directoryPath, 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directoryPath, ".goi-private-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if _, err := temporary.Write(contents); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if err := temporary.Sync(); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(directoryPath)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
