package wanikani

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	internalweb "github.com/tomasmik/goi/internal/web"
)

func testSettingsHandler(t *testing.T, api http.Handler) (*Service, *Store, *Credentials, http.Handler) {
	t.Helper()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	service, store, credentials, _ := newTestService(t, api, now)
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(service, renderer).Routes(router)
	return service, store, credentials, router
}

func postSettingsForm(handler http.Handler, path string, values url.Values) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestSettingsDisconnectedConnectAndConnectedPageHideToken(t *testing.T) {
	api := &serviceAPI{userID: "user-1", username: "turtle", level: 12, maxLevel: 60, subjects: map[string]string{}}
	_, _, credentials, handler := testSettingsHandler(t, api)

	disconnected := httptest.NewRecorder()
	handler.ServeHTTP(disconnected, httptest.NewRequest(http.MethodGet, "/settings/wanikani", nil))
	for _, expected := range []string{"Connect WaniKani", "read-only", "every 12 hours", "remain known"} {
		if !strings.Contains(disconnected.Body.String(), expected) {
			t.Errorf("disconnected page does not contain %q", expected)
		}
	}

	connected := postSettingsForm(handler, "/settings/wanikani/connect", url.Values{"token": {testToken}})
	if connected.Code != http.StatusSeeOther || connected.Header().Get("Location") != "/settings/wanikani?result=connected" {
		t.Fatalf("connect response = %d, location %q", connected.Code, connected.Header().Get("Location"))
	}
	if _, exists, err := credentials.Load(); err != nil || !exists {
		t.Fatalf("saved credential exists = %v, error = %v", exists, err)
	}

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/settings/wanikani?result=connected", nil))
	for _, expected := range []string{"Connected as turtle", "WaniKani level 12", "Sync now", "Imported vocabulary remains known"} {
		if !strings.Contains(page.Body.String(), expected) {
			t.Errorf("connected page does not contain %q", expected)
		}
	}
	if strings.Contains(page.Body.String(), testToken) {
		t.Fatal("connected page contains the personal access token")
	}
}
