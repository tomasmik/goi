package main

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/justinas/nosurf"

	"github.com/tomasmik/goi/internal/backups"
	"github.com/tomasmik/goi/internal/captureapi"
	appimports "github.com/tomasmik/goi/internal/imports"
	"github.com/tomasmik/goi/internal/vocabulary"
	internalweb "github.com/tomasmik/goi/internal/web"
)

func withoutBrowserSession(sessionless, browser http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sessionlessPath(r.URL.Path) {
			sessionless.ServeHTTP(w, r)
			return
		}
		browser.ServeHTTP(w, r)
	})
}

func sessionlessPath(path string) bool {
	return path == "/healthz" ||
		path == "/readyz" ||
		strings.HasPrefix(path, "/static/") ||
		path == "/api/extension/v1/status" ||
		path == "/api/extension/v1/captures" ||
		extensionCaptureMediaPath(path) ||
		path == "/api/extension/v1/coverage" ||
		path == "/api/extension/v1/dictionary" ||
		path == "/api/extension/v1/known" ||
		path == "/api/extension/v1/translate"
}

func extensionCaptureMediaPath(path string) bool {
	const prefix = "/api/extension/v1/captures/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/media") {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/media")
	return id != "" && !strings.Contains(id, "/")
}

func cacheControl(value string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", value)
			next.ServeHTTP(w, r)
		})
	}
}

const contentSecurityPolicy = "default-src 'self';" +
	" script-src 'self';" +
	" style-src 'self' 'unsafe-inline';" +
	" img-src 'self' data:;" +
	" media-src 'self';" +
	" object-src 'none';" +
	" base-uri 'self';" +
	" form-action 'self';" +
	" frame-ancestors 'none'"

func securityHeaders(secure bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if secure {
				w.Header().Set("Strict-Transport-Security", "max-age=31536000")
			}
			w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
			w.Header().Set("Referrer-Policy", "same-origin")
			w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			next.ServeHTTP(w, r)
		})
	}
}

const (
	defaultRequestBodyLimit    int64 = 1 << 20
	vocabularyRequestBodyLimit int64 = 25 << 20
	miningRequestBodyLimit     int64 = 64 << 10
	defaultRequestTimeout            = 15 * time.Second
	translationRequestTimeout        = 65 * time.Second
	mediaRequestTimeout              = 2 * time.Minute
	longRequestTimeout               = 30 * time.Minute
)

func requestTimeout(next http.Handler) http.Handler {
	return requestTimeoutFor(next, timeoutForRequest)
}

func requestTimeoutFor(next http.Handler, duration func(*http.Request) time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timeout := duration(r)
		if timeout <= 0 {
			next.ServeHTTP(w, r)
			return
		}
		http.TimeoutHandler(next, timeout, "request timed out\n").ServeHTTP(w, r)
	})
}

func csrfFailureHandler(logger *slog.Logger, renderer *internalweb.Renderer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.WarnContext(r.Context(), "csrf validation failed", "reason", nosurf.Reason(r), "request_id", middleware.GetReqID(r.Context()))
		returnURL, returnLabel := csrfRecoveryTarget(r)
		renderer.RenderStatus(w, http.StatusBadRequest, "not-found.html", internalweb.NotFoundPage{
			Title:       "Request rejected",
			Heading:     "Request rejected",
			Message:     "Reload the previous page and try again.",
			ReturnURL:   returnURL,
			ReturnLabel: returnLabel,
		})
	})
}

func csrfRecoveryTarget(r *http.Request) (string, string) {
	referer, err := url.Parse(r.Referer())
	if err == nil &&
		(referer.Scheme == "http" || referer.Scheme == "https") &&
		referer.User == nil &&
		strings.EqualFold(referer.Host, r.Host) {
		path := referer.EscapedPath()
		if path == "" {
			path = "/"
		}
		if strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//") {
			if referer.RawQuery != "" {
				path += "?" + referer.RawQuery
			}
			if r.URL.Path == "/login" {
				if referer.Path == "/login" {
					return path, "Back to sign in"
				}
			} else {
				return path, "Back to previous page"
			}
		}
	}
	if r.URL.Path == "/login" {
		return "/login", "Back to sign in"
	}
	return "/dashboard", "Back to study"
}

func timeoutForRequest(r *http.Request) time.Duration {
	if r.Method == http.MethodPost && r.URL.Path == "/api/extension/v1/translate" {
		return translationRequestTimeout
	}
	if backupDownloadPath(r) {
		return 0
	}
	if strings.HasPrefix(r.URL.Path, "/imports/anki/") {
		return longRequestTimeout
	}
	if strings.HasPrefix(r.URL.Path, "/settings/backups/restore/") {
		return longRequestTimeout
	}
	if r.Method == http.MethodPost && r.URL.Path == "/settings/jmdict/refresh" {
		return longRequestTimeout
	}
	if r.Method == http.MethodPost && extensionCaptureMediaPath(r.URL.Path) {
		return mediaRequestTimeout
	}
	if r.Method == http.MethodPost && (r.URL.Path == "/vocabulary" || strings.HasPrefix(r.URL.Path, "/vocabulary/")) {
		return mediaRequestTimeout
	}
	return defaultRequestTimeout
}

func backupDownloadPath(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	const prefix = "/settings/backups/local/"
	name := strings.TrimPrefix(r.URL.Path, prefix)
	return name != r.URL.Path && name != "" && !strings.Contains(name, "/")
}

func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, requestBodyLimit(r))
		next.ServeHTTP(w, r)
	})
}

func requestBodyLimit(r *http.Request) int64 {
	if r.Method != http.MethodPost {
		return defaultRequestBodyLimit
	}
	if r.URL.Path == "/imports/anki/upload" {
		return appimports.MaxArchiveBytes + 10<<20
	}
	if r.URL.Path == "/settings/backups/restore/upload" {
		return backups.RestoreUploadRequestLimit
	}
	if r.URL.Path == "/mining/captures" || strings.HasPrefix(r.URL.Path, "/mining/captures/") {
		return miningRequestBodyLimit
	}
	if r.URL.Path == "/api/extension/v1/captures" {
		return miningRequestBodyLimit
	}
	if extensionCaptureMediaPath(r.URL.Path) {
		return captureapi.CaptureMediaBodyLimit
	}
	if r.URL.Path == "/api/extension/v1/coverage" {
		return captureapi.CoverageBodyLimit
	}
	if r.URL.Path == "/vocabulary/known" {
		return vocabulary.KnownWordsBodyLimit
	}
	if r.URL.Path == "/vocabulary" {
		return vocabularyRequestBodyLimit
	}
	if remainder, found := strings.CutPrefix(r.URL.Path, "/vocabulary/"); found && !strings.Contains(remainder, "/") {
		return vocabularyRequestBodyLimit
	}
	return defaultRequestBodyLimit
}
