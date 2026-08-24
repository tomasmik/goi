package database

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func writeNewFileSynced(path string, data []byte, mode fs.FileMode) (returnErr error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	complete := false
	closed := false
	defer func() {
		if !complete {
			var cleanupErrors []error
			if !closed {
				if err := file.Close(); err != nil {
					cleanupErrors = append(cleanupErrors, fmt.Errorf("close incomplete file: %w", err))
				}
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("remove incomplete file: %w", err))
			}
			returnErr = errors.Join(returnErr, errors.Join(cleanupErrors...))
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	closeErr := file.Close()
	closed = true
	if closeErr != nil {
		return closeErr
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	complete = true
	return nil
}

func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	return file.Close()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		return errors.Join(err, directory.Close())
	}
	return directory.Close()
}

func syncParentDirectories(paths ...string) error {
	directories := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		directories[filepath.Dir(path)] = struct{}{}
	}
	for directory := range directories {
		if err := syncDirectory(directory); err != nil {
			return fmt.Errorf("sync directory %q: %w", directory, err)
		}
	}
	return nil
}
