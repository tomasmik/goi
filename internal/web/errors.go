package web

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
)

// UserErrorMessage returns text only from errors that explicitly mark it as safe to expose.
func UserErrorMessage(err error) (string, bool) {
	var userError interface {
		UserMessage() string
	}
	if !errors.As(err, &userError) {
		return "", false
	}
	return userError.UserMessage(), true
}

// InternalError logs the underlying failure while returning only safe text to the client.
func InternalError(w http.ResponseWriter, r *http.Request, message string, err error) {
	LogError(r, message, err)
	http.Error(w, message, http.StatusInternalServerError)
}

func LogError(r *http.Request, message string, err error) {
	attributes := []any{"error", err}
	if requestID := middleware.GetReqID(r.Context()); requestID != "" {
		attributes = append(attributes, "request_id", requestID)
	}
	slog.ErrorContext(r.Context(), message, attributes...)
}
