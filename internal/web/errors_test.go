package web

import (
	"errors"
	"fmt"
	"testing"
)

type testUserError string

func (err testUserError) Error() string       { return string(err) }
func (err testUserError) UserMessage() string { return string(err) }

func TestUserErrorMessage(t *testing.T) {
	wrapped := fmt.Errorf("operation failed: %w", testUserError("try again"))
	if message, ok := UserErrorMessage(wrapped); !ok || message != "try again" {
		t.Fatalf("UserErrorMessage() = %q, %t", message, ok)
	}
	if _, ok := UserErrorMessage(errors.New("database path")); ok {
		t.Fatal("ordinary error was classified as safe")
	}
}
