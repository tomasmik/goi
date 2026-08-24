package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/database"
	internalweb "github.com/tomasmik/goi/internal/web"
)

func TestCredentialsAuthenticate(t *testing.T) {
	store := NewStore(" study-owner ", "correct horse battery staple")
	tests := []struct {
		name     string
		username string
		password string
		wantErr  bool
	}{
		{name: "matching credentials", username: "study-owner", password: "correct horse battery staple"},
		{name: "username whitespace", username: " study-owner ", password: "correct horse battery staple"},
		{name: "wrong username", username: "someone-else", password: "correct horse battery staple", wantErr: true},
		{name: "wrong password", username: "study-owner", password: "wrong", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := store.Authenticate(test.username, test.password)
			if (err != nil) != test.wantErr {
				t.Fatalf("Authenticate() error = %v, want error %t", err, test.wantErr)
			}
		})
	}
}

func TestSafeReturnTo(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{value: "/mining/capture", want: "/mining/capture"},
		{value: "/dashboard", want: "/dashboard"},
		{value: "/vocabulary/42?view=history", want: "/vocabulary/42?view=history"},
		{value: "/login"},
		{value: "https://example.com/mining/capture"},
		{value: "//example.com/mining/capture"},
		{value: `\\example.com\mining\capture`},
		{value: "/%5cexample.com/mining/capture"},
		{value: "/%0d%0aX-Header:%20value"},
		{value: "/%2f%2fexample.com/mining/capture"},
		{value: "/%6cogin"},
		{value: "/dashboard\nX-Header: value"},
	}
	for _, test := range tests {
		if got := safeReturnTo(test.value); got != test.want {
			t.Errorf("safeReturnTo(%q) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestLoginReturnsToRequestedPage(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "auth.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionManager(db, false)
	handler := NewHandler(NewStore("owner", "secret"), sessions, renderer, true)
	router := chi.NewRouter()
	handler.Routes(router)
	router.Get("/vocabulary/42", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	app := sessions.LoadAndSave(handler.Require(router))

	request := httptest.NewRequest(http.MethodGet, "/vocabulary/42?view=history", nil)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login?return_to=%2Fvocabulary%2F42%3Fview%3Dhistory" {
		t.Fatalf("protected-page redirect = %d %q", response.Code, response.Header().Get("Location"))
	}

	form := url.Values{
		"username":  {"owner"},
		"password":  {"secret"},
		"return_to": {"/vocabulary/42?view=history"},
	}
	request = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/vocabulary/42?view=history" {
		t.Fatalf("login redirect = %d %q, body = %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}

	form.Set("return_to", "https://example.com/")
	request = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard" {
		t.Fatalf("unsafe return redirect = %d %q", response.Code, response.Header().Get("Location"))
	}
}

func TestRejectedLoginPreservesOnlyTheUsername(t *testing.T) {
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(NewStore("owner", "secret"), nil, renderer, true)
	form := url.Values{
		"username":  {"reading-owner"},
		"password":  {"wrong-secret"},
		"return_to": {"/vocabulary"},
	}
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	handler.login(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `name="username" value="reading-owner"`) {
		t.Fatalf("response = %d, body = %s", response.Code, body)
	}
	if strings.Contains(body, "wrong-secret") || !strings.Contains(body, `value="/vocabulary"`) {
		t.Fatalf("login error exposed the password or lost its safe return path: %s", body)
	}
}

func TestLoginSessionDoesNotPersistCredentialVerifier(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "auth.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}

	login := func(store *Store, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		sessions := NewSessionManager(db, false)
		handler := NewHandler(store, sessions, renderer, true)
		router := chi.NewRouter()
		handler.Routes(router)
		router.Get("/protected", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
		app := sessions.LoadAndSave(handler.Require(router))

		var request *http.Request
		if cookie == nil {
			form := url.Values{"username": {"owner"}, "password": {"secret"}}
			request = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			request = httptest.NewRequest(http.MethodGet, "/protected", nil)
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)
		return response
	}

	response := login(NewStore("owner", "secret"), nil)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, want %d", response.Code, http.StatusSeeOther)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies = %d, want 1", len(cookies))
	}

	var sessionData []byte
	if err := db.QueryRow("SELECT data FROM web_sessions").Scan(&sessionData); err != nil {
		t.Fatal(err)
	}
	legacyVerifier := sha256.Sum256([]byte("owner\x00secret"))
	if bytes.Contains(sessionData, []byte(hex.EncodeToString(legacyVerifier[:]))) {
		t.Fatal("persisted session contains a reusable credential verifier")
	}

	response = login(NewStore("owner", "secret"), cookies[0])
	if response.Code != http.StatusNoContent {
		t.Fatalf("session after restart status = %d, want %d", response.Code, http.StatusNoContent)
	}
	response = login(NewStore("owner", "changed"), cookies[0])
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login?return_to=%2Fprotected" {
		t.Fatalf("session after credential change = %d %q", response.Code, response.Header().Get("Location"))
	}
}

func TestLoginRateLimitReturnsTooManyRequests(t *testing.T) {
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(NewStore("owner", "secret"), nil, renderer, true)
	form := url.Values{
		"username":  {"owner"},
		"password":  {"wrong"},
		"return_to": {"/vocabulary/42"},
	}

	for attempt := 1; attempt <= loginBurst+1; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.RemoteAddr = "192.0.2.1:1234"
		response := httptest.NewRecorder()
		handler.login(response, request)
		if attempt <= loginBurst {
			if response.Code != http.StatusOK {
				t.Fatalf("attempt %d status = %d, want %d", attempt, response.Code, http.StatusOK)
			}
			continue
		}
		if response.Code != http.StatusTooManyRequests {
			t.Fatalf("rate-limited status = %d, want %d", response.Code, http.StatusTooManyRequests)
		}
		if response.Header().Get("Retry-After") != "60" {
			t.Fatalf("Retry-After = %q", response.Header().Get("Retry-After"))
		}
		if !strings.Contains(response.Body.String(), `value="/vocabulary/42"`) {
			t.Fatalf("rate-limited form lost return path: %s", response.Body.String())
		}
	}
}

func TestExistingLoginClientDoesNotScanFullLimiterMap(t *testing.T) {
	handler := NewHandler(NewStore("owner", "secret"), nil, nil, true)
	request := httptest.NewRequest(http.MethodPost, "/login", nil)
	request.RemoteAddr = "192.0.2.1:1234"
	if !handler.allowLogin(request) {
		t.Fatal("first login attempt was rate limited")
	}

	existing := handler.loginLimits["192.0.2.1"]
	handler.loginLimits["expired"] = &loginLimit{
		limiter:  existing.limiter,
		lastSeen: time.Now().Add(-loginClientTTL - time.Minute),
	}
	for index := 0; len(handler.loginLimits) < maxLoginClients; index++ {
		handler.loginLimits["client-"+strconv.Itoa(index)] = &loginLimit{
			limiter:  existing.limiter,
			lastSeen: time.Now(),
		}
	}

	if !handler.allowLogin(request) {
		t.Fatal("existing client was unexpectedly rate limited")
	}
	if _, found := handler.loginLimits["expired"]; !found {
		t.Fatal("existing-client lookup swept the limiter map")
	}
}

func TestLoginLimiterIgnoresForwardedClientAddresses(t *testing.T) {
	handler := NewHandler(NewStore("owner", "secret"), nil, nil, true)
	for attempt := 1; attempt <= loginBurst+1; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/login", nil)
		request.RemoteAddr = "192.0.2.1:1234"
		request.Header.Set("X-Forwarded-For", "198.51.100."+strconv.Itoa(attempt))
		request.Header.Set("X-Real-IP", "203.0.113."+strconv.Itoa(attempt))
		request.Header.Set("True-Client-IP", "203.0.113."+strconv.Itoa(attempt))

		allowed := handler.allowLogin(request)
		if attempt <= loginBurst && !allowed {
			t.Fatalf("attempt %d was rate limited", attempt)
		}
		if attempt == loginBurst+1 && allowed {
			t.Fatal("forwarded client addresses created separate limiter buckets")
		}
	}
}
