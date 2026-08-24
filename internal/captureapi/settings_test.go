package captureapi

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/database"
	internalweb "github.com/tomasmik/goi/internal/web"
)

func TestInvalidExtensionTokenRendersRecoveryPage(t *testing.T) {
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewSettingsHandler(nil, renderer, nil, "").Routes(router)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/settings/extension/tokens/not-an-id/revoke", nil))

	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `href="/settings/extension"`) {
		t.Fatalf("invalid token response = %d, %s", response.Code, response.Body.String())
	}
}

func TestMalformedTokenActionRendersRecoveryPage(t *testing.T) {
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewSettingsHandler(nil, renderer, nil, "").Routes(router)
	request := httptest.NewRequest(
		http.MethodPost,
		"/settings/extension/tokens/7/revoke",
		strings.NewReader(strings.Repeat("x", (16<<10)+1)),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusBadRequest || !strings.Contains(body, "The token action form is too large or invalid.") {
		t.Fatalf("response = %d, body = %s", response.Code, body)
	}
	for _, expected := range []string{`class="site-header"`, `href="/settings/extension"`, "Back to extension settings"} {
		if !strings.Contains(body, expected) {
			t.Errorf("rendered error does not contain %q: %s", expected, body)
		}
	}
}

func TestExtensionSettingsDownloadContainsLoadableManifest(t *testing.T) {
	router := chi.NewRouter()
	NewSettingsHandler(nil, nil, nil, "").Routes(router)
	request := httptest.NewRequest(http.MethodGet, "/settings/extension/download", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("download response = %d, Content-Type = %q", response.Code, response.Header().Get("Content-Type"))
	}
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	manifestFound := false
	licenseFound := false
	requiredFiles := map[string]bool{
		"goi-browser-extension/background/capture-delivery.js":        false,
		"goi-browser-extension/background/connection-manager.js":      false,
		"goi-browser-extension/background/youtube-transcript-page.js": false,
		"goi-browser-extension/player/player.html":                    false,
		"goi-browser-extension/player/player.css":                     false,
		"goi-browser-extension/player/player.js":                      false,
		"goi-browser-extension/player/player-state.js":                false,
		"goi-browser-extension/options/options.html":                  false,
		"goi-browser-extension/options/options.css":                   false,
		"goi-browser-extension/options/options.js":                    false,
	}
	for _, file := range archive.File {
		if file.Name == "goi-browser-extension/manifest.json" {
			manifestFound = true
		}
		if file.Name == "goi-browser-extension/LICENSE" {
			licenseFound = true
		}
		if _, required := requiredFiles[file.Name]; required {
			requiredFiles[file.Name] = true
		}
		if strings.Contains(file.Name, "README") || strings.Contains(file.Name, "/tests/") || strings.HasSuffix(file.Name, ".go") {
			t.Fatalf("development file included in extension download: %s", file.Name)
		}
	}
	if !manifestFound {
		t.Fatal("extension download does not contain manifest.json")
	}
	if !licenseFound {
		t.Fatal("extension download does not contain LICENSE")
	}
	for name, found := range requiredFiles {
		if !found {
			t.Errorf("extension download does not contain %s", name)
		}
	}
}

func TestExtensionSettingsCreatesShowsAndRevokesToken(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "extension-settings.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	router := chi.NewRouter()
	NewSettingsHandler(store, renderer, nil, "").Routes(router)
	version, minimumChrome := extensionRelease()
	request := httptest.NewRequest(http.MethodGet, "/settings/extension", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("setup response = %d, body = %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{"Set up the extension", "Version " + version, "Chrome " + minimumChrome + " or newer", "Updating later"} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("extension setup does not contain %q: %s", expected, response.Body.String())
		}
	}
	if strings.Contains(response.Body.String(), "<details") {
		t.Fatalf("extension setup hides ordinary instructions in a disclosure: %s", response.Body.String())
	}

	form := url.Values{"name": {"Reading browser"}}
	request = httptest.NewRequest(http.MethodPost, "/settings/extension/tokens", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("create response = %d, body = %s", response.Code, response.Body.String())
	}
	displayPath := response.Header().Get("Location")
	if !strings.HasPrefix(displayPath, "/settings/extension?created=") {
		t.Fatalf("create location = %q", displayPath)
	}
	displayCookie := response.Header().Get("Set-Cookie")
	if !strings.Contains(displayCookie, createdTokenCookieName+"=") || !strings.Contains(displayCookie, "HttpOnly") {
		t.Fatalf("created token display cookie = %q", displayCookie)
	}
	request = httptest.NewRequest(http.MethodGet, displayPath, nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("display response = %d, body = %s", response.Code, response.Body.String())
	}
	match := regexp.MustCompile(`goi_ext_v1_[A-Za-z0-9_-]{43}`).FindString(response.Body.String())
	if match == "" {
		t.Fatalf("created token missing from response: %s", response.Body.String())
	}
	for _, unexpected := range []string{"Set up the extension", "Create token", "Reading browser", "Extension access"} {
		if strings.Contains(response.Body.String(), unexpected) {
			t.Fatalf("connection screen repeats %q: %s", unexpected, response.Body.String())
		}
	}
	if count := strings.Count(response.Body.String(), `aria-label="Goi server address"`); count != 1 {
		t.Fatalf("connection screen has %d server address fields, want 1: %s", count, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("sensitive response headers = %q, %q", response.Header().Get("Cache-Control"), response.Header().Get("Referrer-Policy"))
	}

	tokens, err := store.List(ctx)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("tokens = %#v, error = %v", tokens, err)
	}
	request = httptest.NewRequest(http.MethodGet, displayPath, nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), match) || strings.Contains(response.Body.String(), "Reading browser") {
		t.Fatalf("reloaded display response = %d, body = %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/settings/extension", nil)
	request.Header.Set("Cookie", strings.Split(displayCookie, ";")[0])
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), match) {
		t.Fatalf("returning settings response = %d, body = %s", response.Code, response.Body.String())
	}

	revokePath := "/settings/extension/tokens/" + strconv.FormatInt(tokens[0].ID, 10) + "/revoke"
	request = httptest.NewRequest(http.MethodPost, revokePath, strings.NewReader(""))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Confirm removing this token before continuing.") {
		t.Fatalf("unconfirmed revoke response = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `href="/settings/extension"`) {
		t.Fatalf("unconfirmed revoke has no recovery link: %s", response.Body.String())
	}
	if _, err := store.Authenticate(ctx, match); err != nil {
		t.Fatalf("unconfirmed revoke invalidated token: %v", err)
	}

	request = httptest.NewRequest(http.MethodPost, revokePath, strings.NewReader("confirmed=1"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Cookie", strings.Split(displayCookie, ";")[0])
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/settings/extension" {
		t.Fatalf("revoke response = %d, location = %q", response.Code, response.Header().Get("Location"))
	}
	if !strings.Contains(response.Header().Get("Set-Cookie"), createdTokenCookieName+"=") || !strings.Contains(response.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("revoke did not clear created token cookie: %q", response.Header().Get("Set-Cookie"))
	}
	if _, err := store.Authenticate(ctx, match); err != ErrUnauthorized {
		t.Fatalf("revoked token authentication error = %v", err)
	}
	request = httptest.NewRequest(http.MethodGet, displayPath, nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), match) || strings.Contains(response.Body.String(), "token can no longer be shown") {
		t.Fatalf("revoked token remains available: %s", response.Body.String())
	}
}

func TestExtensionSettingsExplainsConfiguredPublicAddress(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "extension-public-address.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewSettingsHandler(
		NewStore(db),
		renderer,
		func(context.Context) (*time.Location, error) { return time.UTC, nil },
		"https://goi.example",
	).Routes(router)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/settings/extension", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `data-configured-url="https://goi.example"`) {
		t.Fatalf("response = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestExtensionSettingsKeepsTokenDisplayWhenListingFails(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "extension-settings-display.sqlite")
	db, err := database.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	created, err := store.Create(ctx, "Reading browser")
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	handler := NewSettingsHandler(store, renderer, nil, "")
	displayID := strings.Repeat("a", 32)
	handler.rememberCreatedToken(displayID, created)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/settings/extension?created="+displayID, nil)
	response := httptest.NewRecorder()
	handler.page(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("failed list status = %d, want %d", response.Code, http.StatusInternalServerError)
	}

	reopened, err := database.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	handler.tokens = NewStore(reopened)
	response = httptest.NewRecorder()
	handler.page(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), created.Plaintext) {
		t.Fatalf("recovered display response = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestExtensionSettingsRejectsEmptyTokenName(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "extension-settings-invalid.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewSettingsHandler(NewStore(db), renderer, nil, "").Routes(router)

	request := httptest.NewRequest(http.MethodPost, "/settings/extension/tokens", strings.NewReader("name="))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(body, "between 1 and 100 characters") {
		t.Fatalf("response = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(body, `name="name" maxlength="100" value=""`) || strings.Contains(body, `value="Chrome on this device"`) {
		t.Fatalf("invalid token form did not preserve the submitted name: %s", body)
	}
}
