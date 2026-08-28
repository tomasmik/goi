package wanikani

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/tomasmik/goi/internal/securefile"
)

const TokenFilename = "wanikani-token"

var ErrInvalidToken = errors.New("invalid WaniKani personal access token")

type Credentials struct {
	path string
}

func NewCredentials(path string) *Credentials {
	return &Credentials{path: path}
}

func (c *Credentials) Load() (string, bool, error) {
	info, err := os.Lstat(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect WaniKani credential: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", false, errors.New("WaniKani credential is not a regular file")
	}
	if info.Size() < 1 || info.Size() > 4<<10 {
		return "", false, errors.New("WaniKani credential has an invalid size")
	}
	if err := os.Chmod(c.path, 0o600); err != nil {
		return "", false, fmt.Errorf("secure WaniKani credential: %w", err)
	}
	contents, err := os.ReadFile(c.path)
	if err != nil {
		return "", false, fmt.Errorf("read WaniKani credential: %w", err)
	}
	token, err := validateToken(string(contents))
	if err != nil {
		return "", false, fmt.Errorf("validate saved WaniKani credential: %w", err)
	}
	return token, true, nil
}

func (c *Credentials) Save(token string) error {
	token, err := validateToken(token)
	if err != nil {
		return err
	}
	info, err := os.Lstat(c.path)
	if err == nil && !info.Mode().IsRegular() {
		return errors.New("save WaniKani credential: destination is not a regular file")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect WaniKani credential: %w", err)
	}
	if err := securefile.Write(c.path, []byte(token+"\n")); err != nil {
		return fmt.Errorf("save WaniKani credential: %w", err)
	}
	return nil
}

func (c *Credentials) Delete() error {
	info, err := os.Lstat(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect WaniKani credential: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("WaniKani credential is not a regular file")
	}
	if err := os.Remove(c.path); err != nil {
		return fmt.Errorf("remove WaniKani credential: %w", err)
	}
	directory, err := os.Open(filepath.Dir(c.path))
	if err != nil {
		return fmt.Errorf("open WaniKani credential directory: %w", err)
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func validateToken(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 16 || len(value) > 4<<10 {
		return "", fmt.Errorf("%w: invalid length", ErrInvalidToken)
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return "", fmt.Errorf("%w: contains whitespace or a control character", ErrInvalidToken)
		}
	}
	return value, nil
}
