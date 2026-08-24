package web

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
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

func TestLogErrorIncludesRequestID(t *testing.T) {
	var output bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })

	handler := middleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		LogError(r, "request failed", errors.New("database failed"))
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(middleware.RequestIDHeader, "test-request-id")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if logOutput := output.String(); !strings.Contains(logOutput, `"request_id":"test-request-id"`) {
		t.Fatalf("error log does not contain request ID: %s", logOutput)
	}
}
