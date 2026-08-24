package media

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/database"
)

func TestHandlerServesPrivateMedia(t *testing.T) {
	db, err := database.Open(t.Context(), filepath.Join(t.TempDir(), "media.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := SaveInTx(t.Context(), tx, Upload{
		Kind:     KindAudio,
		MimeType: "audio/mpeg",
		Content:  []byte("audio"),
	}, time.Unix(123, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	NewHandler(db).Routes(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/media/"+strconv.FormatInt(id, 10), nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := response.Body.String(); got != "audio" {
		t.Fatalf("body = %q", got)
	}
}

func TestDuplicateMediaKeepsOneAttribution(t *testing.T) {
	db, err := database.Open(t.Context(), filepath.Join(t.TempDir(), "media.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := SaveInTx(t.Context(), tx, Upload{
		Kind: KindAudio, MimeType: "audio/mpeg", Content: []byte("same audio"),
	}, time.Unix(123, 0))
	if err != nil {
		t.Fatal(err)
	}
	attributed := Upload{
		Kind:        KindAudio,
		MimeType:    "audio/mpeg",
		Content:     []byte("same audio"),
		SourceName:  "First source",
		SourceURL:   "https://example.com/first",
		LicenseName: "CC0",
		LicenseURL:  "https://creativecommons.org/publicdomain/zero/1.0/",
	}
	secondID, err := SaveInTx(t.Context(), tx, attributed, time.Unix(124, 0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = SaveInTx(t.Context(), tx, Upload{
		Kind:        KindAudio,
		MimeType:    "audio/mpeg",
		Content:     []byte("same audio"),
		SourceName:  "Different source",
		SourceURL:   "https://example.com/different",
		LicenseName: "Different licence",
		LicenseURL:  "https://example.com/licence",
	}, time.Unix(125, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if firstID != secondID {
		t.Fatalf("duplicate media IDs = %d and %d", firstID, secondID)
	}

	var sourceName, sourceURL, licenseName, licenseURL string
	if err := db.QueryRow(`
		SELECT source_name, source_url, license_name, license_url
		FROM media WHERE id = ?`, firstID).Scan(
		&sourceName, &sourceURL, &licenseName, &licenseURL,
	); err != nil {
		t.Fatal(err)
	}
	if sourceName != attributed.SourceName || sourceURL != attributed.SourceURL ||
		licenseName != attributed.LicenseName || licenseURL != attributed.LicenseURL {
		t.Fatalf("attribution = %q, %q, %q, %q", sourceName, sourceURL, licenseName, licenseURL)
	}
}

func TestHandlerRejectsInvalidAndMissingIDs(t *testing.T) {
	db, err := database.Open(t.Context(), filepath.Join(t.TempDir(), "media.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(db).Routes(router)

	for _, path := range []string{"/media/nope", "/media/0", "/media/999"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d", path, response.Code)
		}
	}
}
