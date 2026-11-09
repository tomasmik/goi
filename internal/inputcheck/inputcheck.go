package inputcheck

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxSourceURLBytes = 2048

func Text(value string, maxRunes int, name string) (string, error) {
	if !utf8.ValidString(value) {
		return "", errors.New(name + " must be valid UTF-8")
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.TrimSpace(value)
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\t' {
			return "", errors.New(name + " contains a control character")
		}
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return "", fmt.Errorf("%s must be at most %d characters", name, maxRunes)
	}
	return value, nil
}

func SourceURL(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", errors.New("source URL must be valid UTF-8")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > MaxSourceURLBytes {
		return "", fmt.Errorf("source URL must be at most %d bytes", MaxSourceURLBytes)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("source URL must use http or https")
	}
	parsed.User = nil
	return parsed.String(), nil
}

func CopyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
