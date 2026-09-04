package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/justinas/nosurf"

	"github.com/tomasmik/goi/internal/backups"
	"github.com/tomasmik/goi/internal/captureapi"
	"github.com/tomasmik/goi/internal/database"
	appimports "github.com/tomasmik/goi/internal/imports"
	"github.com/tomasmik/goi/internal/mining"
	"github.com/tomasmik/goi/internal/vocabulary"
	internalweb "github.com/tomasmik/goi/internal/web"
)

func TestStatusWriterDefaultsToOK(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &statusWriter{ResponseWriter: recorder}

	if got := writer.Status(); got != http.StatusOK {
		t.Fatalf("Status() = %d, want %d", got, http.StatusOK)
	}
}

func TestServerRefusesToStartAfterPublishedRestoreCleanupFailure(t *testing.T) {
	cause := errors.New("could not consume pending bundle")
	if err := pendingRestoreStartupError(true, cause); !errors.Is(err, cause) {
		t.Fatalf("pendingRestoreStartupError() = %v", err)
	}
	if err := pendingRestoreStartupError(false, cause); err != nil {
		t.Fatalf("unapplied restore prevented startup: %v", err)
	}
	retryable := fmt.Errorf("restore: %w", backups.ErrPendingRestoreRetry)
	if err := pendingRestoreStartupError(false, retryable); !errors.Is(err, backups.ErrPendingRestoreRetry) {
		t.Fatalf("retryable restore failure did not prevent startup: %v", err)
	}
	lockBusy := fmt.Errorf("restore: %w", database.ErrDatabaseInUse)
	if err := pendingRestoreStartupError(false, lockBusy); !errors.Is(err, database.ErrDatabaseInUse) {
		t.Fatalf("lock contention did not prevent startup: %v", err)
	}
}

func TestStatusWriterKeepsFirstStatus(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &statusWriter{ResponseWriter: recorder}

	writer.WriteHeader(http.StatusCreated)
	writer.WriteHeader(http.StatusInternalServerError)

	if got := writer.Status(); got != http.StatusCreated {
		t.Fatalf("Status() = %d, want %d", got, http.StatusCreated)
	}
	if got := recorder.Code; got != http.StatusCreated {
		t.Fatalf("response status = %d, want %d", got, http.StatusCreated)
	}
}

func TestRequestLoggerIncludesRequestID(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	handler := middleware.RequestID(requestLogger(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	request := httptest.NewRequest(http.MethodPost, "/settings", nil)
	request.Header.Set(middleware.RequestIDHeader, "test-request-id")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if logOutput := output.String(); !strings.Contains(logOutput, `"request_id":"test-request-id"`) {
		t.Fatalf("request log does not contain request ID: %s", logOutput)
	}
}

func TestSecurityHeadersScopeHSTSToCurrentHost(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	secureResponse := httptest.NewRecorder()
	securityHeaders(true)(next).ServeHTTP(secureResponse, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := secureResponse.Header().Get("Strict-Transport-Security"); got != "max-age=31536000" {
		t.Fatalf("secure HSTS = %q", got)
	}
	if got := secureResponse.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Fatalf("Content-Security-Policy = %q", got)
	}

	insecureResponse := httptest.NewRecorder()
	securityHeaders(false)(next).ServeHTTP(insecureResponse, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := insecureResponse.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("insecure HSTS = %q", got)
	}
}

func TestRequestBodyLimitRejectsOversizedDefaultRequest(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		}
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/settings",
		strings.NewReader(strings.Repeat("x", int(defaultRequestBodyLimit)+1)),
	)
	recorder := httptest.NewRecorder()

	limitRequestBody(next).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("response status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestRequestBodyLimitUsesRouteSpecificLimits(t *testing.T) {
	tests := []struct {
		path string
		want int64
	}{
		{path: "/imports/anki/upload", want: appimports.MaxArchiveBytes + 10<<20},
		{path: "/vocabulary", want: vocabularyRequestBodyLimit},
		{path: "/vocabulary/known", want: vocabulary.KnownWordsBodyLimit},
		{path: "/vocabulary/42", want: vocabularyRequestBodyLimit},
		{path: "/vocabulary/42/action", want: defaultRequestBodyLimit},
		{path: "/mining/captures", want: miningRequestBodyLimit},
		{path: "/mining/captures/42", want: miningRequestBodyLimit},
		{path: "/mining/captures/42/accept", want: mining.CaptureMediaBodyLimit},
		{path: "/mining/captures/42/attach", want: miningRequestBodyLimit},
		{path: "/mining/captures/42/attach-candidate", want: mining.CaptureMediaBodyLimit},
		{path: "/mining/captures/42/media", want: mining.CaptureMediaBodyLimit},
		{path: "/mining/captures/42/media/9/delete", want: miningRequestBodyLimit},
		{path: "/mining/captures/42/discard", want: miningRequestBodyLimit},
		{path: "/mining/captures/42/restore", want: miningRequestBodyLimit},
		{path: "/mining/captures/42/delete", want: miningRequestBodyLimit},
		{path: "/api/extension/v1/captures", want: miningRequestBodyLimit},
		{path: "/api/extension/v1/captures/42/media", want: captureapi.CaptureMediaBodyLimit},
		{path: "/api/extension/v1/coverage", want: captureapi.CoverageBodyLimit},
		{path: "/settings/jmdict/refresh", want: defaultRequestBodyLimit},
		{path: "/settings/jiten/refresh", want: defaultRequestBodyLimit},
		{path: "/settings/backups/restore/upload", want: backups.RestoreUploadRequestLimit},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, nil)
			if got := requestBodyLimit(request); got != test.want {
				t.Fatalf("requestBodyLimit() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestRequestTimeoutUsesRouteSpecificDurations(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   time.Duration
	}{
		{method: http.MethodGet, path: "/dashboard", want: defaultRequestTimeout},
		{method: http.MethodPost, path: "/api/extension/v1/translate", want: translationRequestTimeout},
		{method: http.MethodPost, path: "/vocabulary", want: mediaRequestTimeout},
		{method: http.MethodPost, path: "/vocabulary/42", want: mediaRequestTimeout},
		{method: http.MethodPost, path: "/api/extension/v1/captures/42/media", want: mediaRequestTimeout},
		{method: http.MethodPost, path: "/mining/captures/42/accept", want: mediaRequestTimeout},
		{method: http.MethodPost, path: "/mining/captures/42/attach-candidate", want: mediaRequestTimeout},
		{method: http.MethodPost, path: "/mining/captures/42/media", want: mediaRequestTimeout},
		{method: http.MethodPost, path: "/mining/captures/42/attach", want: defaultRequestTimeout},
		{method: http.MethodPost, path: "/imports/anki/upload", want: longRequestTimeout},
		{method: http.MethodPost, path: "/imports/anki/42/apply", want: longRequestTimeout},
		{method: http.MethodPost, path: "/settings/backups/restore/upload", want: longRequestTimeout},
		{method: http.MethodPost, path: "/settings/backups/restore/google/file1", want: longRequestTimeout},
		{method: http.MethodPost, path: "/settings/jmdict/refresh", want: longRequestTimeout},
		{method: http.MethodPost, path: "/settings/jiten/refresh", want: longRequestTimeout},
		{method: http.MethodGet, path: "/settings/backups/local/goi-test.goi-backup.zip", want: 0},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			if got := timeoutForRequest(request); got != test.want {
				t.Fatalf("timeoutForRequest() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestRequestTimeoutAllowsStreamingBackupDownloads(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("backup"))
	})
	handler := requestTimeoutFor(next, timeoutForRequest)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/settings/backups/local/goi-test.goi-backup.zip",
		nil,
	))
	if response.Code != http.StatusOK || response.Body.String() != "backup" {
		t.Fatalf("streaming response = %d %q", response.Code, response.Body.String())
	}
}

func TestRequestTimeoutReturnsWhileHandlerIsBlocked(t *testing.T) {
	release := make(chan struct{})
	writeResult := make(chan error, 1)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, err := w.Write([]byte("late response"))
		writeResult <- err
	})
	handler := requestTimeoutFor(next, func(*http.Request) time.Duration {
		return 20 * time.Millisecond
	})

	response := httptest.NewRecorder()
	started := time.Now()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	elapsed := time.Since(started)
	close(release)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if response.Body.String() != "request timed out\n" {
		t.Fatalf("response body = %q", response.Body.String())
	}
	if elapsed >= time.Second {
		t.Fatalf("timeout response took %s", elapsed)
	}
	select {
	case err := <-writeResult:
		if !errors.Is(err, http.ErrHandlerTimeout) {
			t.Fatalf("late handler write error = %v, want %v", err, http.ErrHandlerTimeout)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked handler did not finish after release")
	}
}

func TestRequestTimeoutPreservesSuccessfulResponse(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Test", "complete")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("saved"))
	})
	handler := requestTimeoutFor(next, func(*http.Request) time.Duration {
		return time.Second
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil))

	if response.Code != http.StatusCreated || response.Body.String() != "saved" {
		t.Fatalf("response = %d %q, want 201 saved", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Test") != "complete" {
		t.Fatalf("X-Test = %q", response.Header().Get("X-Test"))
	}
}

func TestOuterMiddlewareKeepsHeadersAndLogsTimeoutStatus(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	release := make(chan struct{})
	blocked := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	})
	handler := securityHeaders(false)(middleware.RequestID(requestLogger(logger)(requestTimeoutFor(
		blocked,
		func(*http.Request) time.Duration { return time.Millisecond },
	))))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/settings", nil))
	close(release)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers missing from timeout response: %v", response.Header())
	}
	if logOutput := output.String(); !strings.Contains(logOutput, `"status":503`) {
		t.Fatalf("timeout status missing from request log: %s", logOutput)
	}
}

func TestCSRFFailureRendersRecoveryPage(t *testing.T) {
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	request := httptest.NewRequest(http.MethodPost, "/settings", nil)
	response := httptest.NewRecorder()
	protected := nosurf.New(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request without a CSRF token reached the protected handler")
	}))
	protected.SetFailureHandler(csrfFailureHandler(logger, renderer))

	protected.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	body := response.Body.String()
	if !strings.Contains(body, "Request rejected") || !strings.Contains(body, "Back to study") {
		t.Fatalf("response does not contain recovery UI: %s", body)
	}
}

func TestCSRFRecoveryTarget(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		host      string
		referer   string
		wantURL   string
		wantLabel string
	}{
		{
			name:      "same-origin form",
			path:      "/vocabulary/7",
			host:      "goi.example",
			referer:   "https://goi.example/vocabulary/7/edit?from=list",
			wantURL:   "/vocabulary/7/edit?from=list",
			wantLabel: "Back to previous page",
		},
		{
			name:      "external referer",
			path:      "/settings",
			host:      "goi.example",
			referer:   "https://example.com/settings",
			wantURL:   "/dashboard",
			wantLabel: "Back to study",
		},
		{
			name:      "sign in",
			path:      "/login",
			host:      "goi.example",
			referer:   "https://goi.example/login?return_to=%2Fvocabulary",
			wantURL:   "/login?return_to=%2Fvocabulary",
			wantLabel: "Back to sign in",
		},
		{
			name:      "sign in with unrelated same-origin referer",
			path:      "/login",
			host:      "goi.example",
			referer:   "https://goi.example/settings",
			wantURL:   "/login",
			wantLabel: "Back to sign in",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, nil)
			request.Host = test.host
			request.Header.Set("Referer", test.referer)
			target, label := csrfRecoveryTarget(request)
			if target != test.wantURL || label != test.wantLabel {
				t.Fatalf("csrfRecoveryTarget() = %q, %q; want %q, %q", target, label, test.wantURL, test.wantLabel)
			}
		})
	}
}

func TestWithoutBrowserSessionRoutesOnlyStatelessPathsDirectly(t *testing.T) {
	sessionless := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Handler", "sessionless")
	})
	browser := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Handler", "browser")
	})
	handler := withoutBrowserSession(sessionless, browser)

	tests := []struct {
		path string
		want string
	}{
		{path: "/static/css/app.css", want: "sessionless"},
		{path: "/healthz", want: "sessionless"},
		{path: "/readyz", want: "sessionless"},
		{path: "/api/extension/v1/status", want: "sessionless"},
		{path: "/api/extension/v1/captures", want: "sessionless"},
		{path: "/api/extension/v1/captures/42/media", want: "sessionless"},
		{path: "/api/extension/v1/coverage", want: "sessionless"},
		{path: "/api/extension/v1/dictionary", want: "sessionless"},
		{path: "/api/extension/v1/known", want: "sessionless"},
		{path: "/api/extension/v1/translate", want: "sessionless"},
		{path: "/login", want: "browser"},
		{path: "/settings", want: "browser"},
		{path: "/api/extension/v1/captures/extra", want: "browser"},
		{path: "/api/extension/v1/captures/42/other/media", want: "browser"},
		{path: "/static-old/app.css", want: "browser"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if got := response.Header().Get("X-Handler"); got != test.want {
				t.Fatalf("handler = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCacheControlSetsExplicitPolicy(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	for _, value := range []string{"no-cache", "no-store"} {
		response := httptest.NewRecorder()
		cacheControl(value)(next).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
		if got := response.Header().Get("Cache-Control"); got != value {
			t.Fatalf("Cache-Control = %q, want %q", got, value)
		}
	}
}

func TestMiningBodyLimitRunsBeforeCSRFFormParsing(t *testing.T) {
	var token string
	postReached := false
	endpoint := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			token = nosurf.Token(r)
			w.WriteHeader(http.StatusOK)
			return
		}
		postReached = true
		w.WriteHeader(http.StatusNoContent)
	})
	csrf := nosurf.New(endpoint)
	csrf.SetIsTLSFunc(func(*http.Request) bool { return false })
	var csrfFailure error
	csrf.SetFailureHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		csrfFailure = nosurf.Reason(r)
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
	}))
	handler := limitRequestBody(csrf)

	getRequest := httptest.NewRequest(http.MethodGet, "/mining/captures", nil)
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusOK || token == "" {
		t.Fatalf("GET response = %d, token empty = %t", getResponse.Code, token == "")
	}
	cookies := getResponse.Result().Cookies()

	validForm := url.Values{"csrf_token": {token}}
	validRequest := httptest.NewRequest(http.MethodPost, "/mining/captures", strings.NewReader(validForm.Encode()))
	validRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	validRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	for _, cookie := range cookies {
		validRequest.AddCookie(cookie)
	}
	validResponse := httptest.NewRecorder()
	handler.ServeHTTP(validResponse, validRequest)
	if validResponse.Code != http.StatusNoContent || !postReached {
		t.Fatalf("valid POST response = %d, endpoint reached = %t, CSRF failure = %v", validResponse.Code, postReached, csrfFailure)
	}
	postReached = false
	csrfFailure = nil

	form := url.Values{
		"csrf_token": {token},
		"padding":    {strings.Repeat("x", int(miningRequestBodyLimit))},
	}
	postRequest := httptest.NewRequest(http.MethodPost, "/mining/captures", strings.NewReader(form.Encode()))
	postRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	for _, cookie := range cookies {
		postRequest.AddCookie(cookie)
	}
	postResponse := httptest.NewRecorder()
	handler.ServeHTTP(postResponse, postRequest)

	if postReached {
		t.Fatal("oversized mining request reached the endpoint after CSRF parsing")
	}
	if postResponse.Code != http.StatusBadRequest {
		t.Fatalf("POST response = %d, want %d", postResponse.Code, http.StatusBadRequest)
	}
}
