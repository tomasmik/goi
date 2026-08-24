package backups

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	installationIDFile     = "installation-id"
	maxInstallationIDBytes = 128
)

func LoadOrCreateInstallationID(dataDir string) (string, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return "", fmt.Errorf("create data directory: %w", err)
	}
	path := filepath.Join(dataDir, installationIDFile)
	installationID, err := readInstallationID(path)
	if err == nil {
		if err := syncDirectory(dataDir); err != nil {
			return "", fmt.Errorf("sync installation ID directory: %w", err)
		}
		return installationID, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("create installation ID: %w", err)
	}
	installationID = hex.EncodeToString(random)
	temporary, err := os.CreateTemp(dataDir, ".installation-id-*")
	if err != nil {
		return "", fmt.Errorf("create installation ID file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o640); err != nil {
		temporary.Close()
		return "", fmt.Errorf("secure installation ID file: %w", err)
	}
	if _, err := temporary.WriteString(installationID + "\n"); err != nil {
		temporary.Close()
		return "", fmt.Errorf("write installation ID: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", fmt.Errorf("sync installation ID: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close installation ID: %w", err)
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			removeErr := os.Remove(temporaryPath)
			syncErr := syncDirectory(dataDir)
			if removeErr != nil || syncErr != nil {
				return "", fmt.Errorf("finalize concurrent installation ID: %w", errors.Join(removeErr, syncErr))
			}
			persistedID, readErr := readInstallationID(path)
			if readErr != nil {
				return "", fmt.Errorf("read concurrently created installation ID: %w", readErr)
			}
			return persistedID, nil
		}
		return "", fmt.Errorf("save installation ID: %w", err)
	}
	removeErr := os.Remove(temporaryPath)
	syncErr := syncDirectory(dataDir)
	if removeErr != nil || syncErr != nil {
		return "", fmt.Errorf("finalize installation ID: %w", errors.Join(removeErr, syncErr))
	}
	return installationID, nil
}

func readInstallationID(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("installation ID is not a regular file")
	}
	if info.Size() > maxInstallationIDBytes {
		return "", errors.New("installation ID file is too large")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read installation ID: %w", err)
	}
	if len(contents) > maxInstallationIDBytes {
		return "", errors.New("installation ID file is too large")
	}
	installationID := strings.TrimSpace(string(contents))
	if !validInstallationID(installationID) {
		return "", errors.New("installation ID is invalid")
	}
	return installationID, nil
}

func validInstallationID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16 && value == strings.ToLower(value)
}
