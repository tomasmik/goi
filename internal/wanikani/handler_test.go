package wanikani

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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

func TestSettingsInvalidTokenIsNotSavedOrRendered(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	_, _, credentials, handler := testSettingsHandler(t, api)
	response := postSettingsForm(handler, "/settings/wanikani/connect", url.Values{"token": {testToken}})
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "did not accept that token") {
		t.Fatalf("response = %d, %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), testToken) {
		t.Fatal("validation response contains the rejected token")
	}
	if _, exists, err := credentials.Load(); err != nil || exists {
		t.Fatalf("credential exists = %v, error = %v", exists, err)
	}
}

func TestSettingsSyncQueuesAndDisconnectPreservesVocabulary(t *testing.T) {
	api := &serviceAPI{userID: "user-1", username: "turtle", level: 3, maxLevel: 3, subjects: map[string]string{}}
	_, store, credentials, handler := testSettingsHandler(t, api)
	if response := postSettingsForm(handler, "/settings/wanikani/connect", url.Values{"token": {testToken}}); response.Code != http.StatusSeeOther {
		t.Fatalf("connect response = %d", response.Code)
	}
	if _, err := store.db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, known_elsewhere_at, created_at, updated_at)
		VALUES ('食べる', '食べる', 1, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	syncResponse := postSettingsForm(handler, "/settings/wanikani/sync", nil)
	if syncResponse.Code != http.StatusSeeOther || syncResponse.Header().Get("Location") != "/settings/wanikani?result=sync_requested" {
		t.Fatalf("sync response = %d, location %q", syncResponse.Code, syncResponse.Header().Get("Location"))
	}
	api.mu.Lock()
	assignmentRequests := len(api.assignmentQuery)
	api.mu.Unlock()
	if assignmentRequests != 0 {
		t.Fatalf("manual request started %d synchronous imports", assignmentRequests)
	}

	unconfirmed := postSettingsForm(handler, "/settings/wanikani/disconnect", nil)
	if unconfirmed.Code != http.StatusUnprocessableEntity || !strings.Contains(unconfirmed.Body.String(), "Imported vocabulary will remain known") {
		t.Fatalf("unconfirmed disconnect = %d, %s", unconfirmed.Code, unconfirmed.Body.String())
	}
	disconnected := postSettingsForm(handler, "/settings/wanikani/disconnect", url.Values{"confirmed": {"1"}})
	if disconnected.Code != http.StatusSeeOther || disconnected.Header().Get("Location") != "/settings/wanikani?result=disconnected" {
		t.Fatalf("disconnect response = %d, location %q", disconnected.Code, disconnected.Header().Get("Location"))
	}
	if _, exists, err := credentials.Load(); err != nil || exists {
		t.Fatalf("credential exists after disconnect = %v, error = %v", exists, err)
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM vocabulary WHERE known_elsewhere_at IS NOT NULL").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("known vocabulary after disconnect = %d, want 1", count)
	}
}

func TestSettingsCredentialPathCannotBeSymlinked(t *testing.T) {
	api := &serviceAPI{userID: "user-1", username: "turtle", level: 3, maxLevel: 3, subjects: map[string]string{}}
	service, _, credentials, _ := newTestService(t, api, time.Now())
	target := credentials.path + "-target"
	if err := os.WriteFile(target, []byte(testToken), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, credentials.path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Connect(t.Context(), testToken); err == nil || !strings.Contains(fmt.Sprint(err), "save WaniKani credential") {
		t.Fatalf("Connect() error = %v", err)
	}
}
