package settings

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/justinas/nosurf"

	"github.com/tomasmik/goi/internal/database"
	"github.com/tomasmik/goi/internal/dictionary/jmdict"
	internalweb "github.com/tomasmik/goi/internal/web"
)

type fakeDictionaryManager struct {
	status     jmdict.ManagerStatus
	refreshes  int
	refreshErr error
}

func (f *fakeDictionaryManager) Status() jmdict.ManagerStatus {
	return f.status
}

func (f *fakeDictionaryManager) Refresh(context.Context) error {
	f.refreshes++
	return f.refreshErr
}

func TestValuesFromRequest(t *testing.T) {
	form := url.Values{
		"time_zone":             {"Asia/Tokyo"},
		"lesson_window_hours":   {"12"},
		"extra_study_limit":     {"8"},
		"retry_count":           {"3"},
		"review_mode":           {"self_grade"},
		"review_order":          {"stage_descending"},
		"review_card_order":     {"spaced"},
		"review_auto_advance":   {"on"},
		"leech_failure_count":   {"5"},
		"leech_suspend_count":   {"3"},
		"leech_recovery_streak": {"3"},
		"six_month_review":      {"on"},
		"theme":                 {"dark"},
		"audio_enabled":         {"on"},
	}
	request := formRequest(t, form)

	values, err := valuesFromRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	want := Values{
		TimeZone:            "Asia/Tokyo",
		LessonWindowHours:   12,
		ExtraStudyLimit:     8,
		RetryCount:          3,
		ReviewMode:          "self_grade",
		ReviewOrder:         "stage_descending",
		ReviewCardOrder:     "spaced",
		ReviewAutoAdvance:   true,
		LeechFailureCount:   5,
		LeechSuspendCount:   3,
		LeechRecoveryStreak: 3,
		SixMonthReview:      true,
		Theme:               "dark",
		AudioEnabled:        true,
	}
	if values != want {
		t.Fatalf("valuesFromRequest() = %+v, want %+v", values, want)
	}
}

func TestValuesFromRequestRejectsInvalidNumbers(t *testing.T) {
	tests := []struct {
		name  string
		field string
		want  string
	}{
		{name: "lesson window", field: "lesson_window_hours", want: "recent lesson window must be a number"},
		{name: "study list size", field: "extra_study_limit", want: "extra-study list size must be a number"},
		{name: "answer attempts", field: "retry_count", want: "answer attempts must be a number"},
		{name: "leech failures", field: "leech_failure_count", want: "leech failure count must be a number"},
		{name: "leech suspension", field: "leech_suspend_count", want: "leech suspension count must be a number"},
		{name: "leech recovery", field: "leech_recovery_streak", want: "leech recovery streak must be a number"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			form := url.Values{
				"time_zone":             {"UTC"},
				"lesson_window_hours":   {"12"},
				"extra_study_limit":     {"8"},
				"retry_count":           {"3"},
				"leech_failure_count":   {"5"},
				"leech_suspend_count":   {"3"},
				"leech_recovery_streak": {"3"},
				"theme":                 {"light"},
			}
			form.Set(test.field, "not-a-number")

			values, err := valuesFromRequest(formRequest(t, form))
			if err == nil || err.Error() != test.want {
				t.Fatalf("valuesFromRequest() error = %v, want %q", err, test.want)
			}
			if test.field != "lesson_window_hours" && values.LessonWindowHours != 12 {
				t.Fatalf("lesson window = %d, want 12", values.LessonWindowHours)
			}
			if test.field != "extra_study_limit" && values.ExtraStudyLimit != 8 {
				t.Fatalf("extra-study list size = %d, want 8", values.ExtraStudyLimit)
			}
			if test.field != "retry_count" && values.RetryCount != 3 {
				t.Fatalf("answer attempts = %d, want 3", values.RetryCount)
			}
		})
	}
}

func TestDictionaryRefreshRunsAndRedirects(t *testing.T) {
	dictionary := &fakeDictionaryManager{}
	router := chi.NewRouter()
	NewHandler(nil, nil, dictionary, false).Routes(router)
	request := httptest.NewRequest(http.MethodPost, "/settings/jmdict/refresh", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/settings?jmdict_refresh=updated" {
		t.Fatalf("response = %d, location = %q", response.Code, response.Header().Get("Location"))
	}
	if dictionary.refreshes != 1 {
		t.Fatalf("refresh calls = %d", dictionary.refreshes)
	}
}

func TestDictionaryRefreshReportsFailure(t *testing.T) {
	dictionary := &fakeDictionaryManager{refreshErr: errors.New("download failed")}
	router := chi.NewRouter()
	NewHandler(nil, nil, dictionary, false).Routes(router)
	request := httptest.NewRequest(http.MethodPost, "/settings/jmdict/refresh", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/settings?jmdict_refresh=failed" {
		t.Fatalf("response = %d, location = %q", response.Code, response.Header().Get("Location"))
	}
}

func TestDictionaryRefreshRequiresCSRFToken(t *testing.T) {
	dictionary := &fakeDictionaryManager{}
	router := chi.NewRouter()
	NewHandler(nil, nil, dictionary, false).Routes(router)
	request := httptest.NewRequest(http.MethodPost, "/settings/jmdict/refresh", nil)
	response := httptest.NewRecorder()

	nosurf.New(router).ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("response status = %d", response.Code)
	}
	if dictionary.refreshes != 0 {
		t.Fatalf("refresh calls = %d", dictionary.refreshes)
	}
}

func TestSettingsPageShowsDictionaryStatusAndAttribution(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "settings-handler.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	if err := store.Ensure(ctx, "UTC"); err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	dictionary := &fakeDictionaryManager{status: jmdict.ManagerStatus{
		Available: true,
		Metadata: jmdict.Metadata{Source: jmdict.Source{
			Version: "1.10", Created: "2026-07-26", DownloadedAt: time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC),
		}},
		LastCheck:      time.Date(2026, 7, 26, 11, 12, 0, 0, time.UTC),
		LastSuccess:    time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC),
		LastErrorCode:  "network_timeout",
		RefreshRunning: true,
	}}
	router := chi.NewRouter()
	NewHandler(store, renderer, dictionary, true).Routes(router)
	request := httptest.NewRequest(http.MethodGet, "/settings?jmdict_refresh=updated", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, value := range []string{
		"Dictionary is up to date.", "Refreshing", "1.10", "2026-07-26",
		"2026-07-26 11:12 UTC", "network timeout", "JMdict/EDRDG", "CC BY-SA 4.0",
		`action="/settings/jmdict/refresh"`, `href="https://www.edrdg.org/"`,
		`href="https://www.edrdg.org/edrdg/licence.html"`,
		`action="/logout"`,
		`type="radio" name="theme" value="system"`,
		`type="radio" name="theme" value="light" checked`,
		`type="radio" name="theme" value="dark"`,
		`type="radio" name="review_mode" value="typed" checked`,
		`type="radio" name="review_mode" value="self_grade"`,
		`type="radio" name="review_order" value="stage_ascending" checked`,
		`type="radio" name="review_order" value="stage_descending"`,
		`type="radio" name="review_order" value="random"`,
		`type="radio" name="review_card_order" value="together" checked`,
		`type="radio" name="review_card_order" value="spaced"`,
		`name="review_auto_advance"`,
		"Self grade",
		"Advance after a correct typed answer",
		"Play audio automatically",
		`name="six_month_review"`,
		"Add a 6-month review",
		`aria-label="Settings sections"`,
		`href="/settings/examples"`,
		"Translation and examples",
	} {
		if !strings.Contains(body, value) {
			t.Fatalf("settings page does not contain %q: %s", value, body)
		}
	}
	if strings.Contains(body, `<select name="theme">`) {
		t.Fatal("settings page hides the theme choices in a select")
	}
}

func TestMalformedSettingsFormRendersRecoveryPage(t *testing.T) {
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(nil, renderer, nil, false).Routes(router)
	request := httptest.NewRequest(
		http.MethodPost,
		"/settings",
		strings.NewReader(strings.Repeat("x", (64<<10)+1)),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusBadRequest || !strings.Contains(body, "The settings form is too large or invalid.") {
		t.Fatalf("response = %d, body = %s", response.Code, body)
	}
	for _, expected := range []string{`class="site-header"`, `href="/settings"`, "Back to settings"} {
		if !strings.Contains(body, expected) {
			t.Errorf("rendered error does not contain %q: %s", expected, body)
		}
	}
}

func TestPostRedirectsToSavedSettingsPage(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "settings-save-handler.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	if err := store.Ensure(ctx, "UTC"); err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, renderer, &fakeDictionaryManager{}, false).Routes(router)
	form := url.Values{
		"time_zone":             {"Asia/Tokyo"},
		"lesson_window_hours":   {"12"},
		"extra_study_limit":     {"8"},
		"retry_count":           {"3"},
		"leech_failure_count":   {"5"},
		"leech_suspend_count":   {"3"},
		"leech_recovery_streak": {"3"},
		"six_month_review":      {"on"},
		"theme":                 {"dark"},
		"audio_enabled":         {"on"},
	}
	request := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/settings?saved=1" {
		t.Fatalf("response = %d, location = %q", response.Code, response.Header().Get("Location"))
	}
	if cookie := response.Header().Get("Set-Cookie"); !strings.Contains(cookie, "goi_theme=dark") {
		t.Fatalf("theme cookie = %q", cookie)
	}

	request = httptest.NewRequest(http.MethodGet, response.Header().Get("Location"), nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Saved.") {
		t.Fatalf("saved page = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestPostHidesStoreFailures(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "closed-settings-handler.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	if err := store.Ensure(ctx, "UTC"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, renderer, &fakeDictionaryManager{}, false).Routes(router)
	form := url.Values{
		"time_zone":             {"UTC"},
		"lesson_window_hours":   {"12"},
		"extra_study_limit":     {"8"},
		"retry_count":           {"3"},
		"leech_failure_count":   {"5"},
		"leech_suspend_count":   {"3"},
		"leech_recovery_streak": {"3"},
		"theme":                 {"light"},
	}
	request := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("response status = %d, body = %s", response.Code, response.Body.String())
	}
	if body := response.Body.String(); !strings.Contains(body, "could not update settings") || strings.Contains(body, "closed") {
		t.Fatalf("response exposed store failure: %s", body)
	}
}

func TestPostValidationReturnsUnprocessableEntity(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "settings-validation-handler.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	if err := store.Ensure(ctx, "UTC"); err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, renderer, &fakeDictionaryManager{}, false).Routes(router)

	tests := []struct {
		name   string
		change func(url.Values)
	}{
		{
			name: "malformed number",
			change: func(form url.Values) {
				form.Set("retry_count", "many")
			},
		},
		{
			name: "invalid stored value",
			change: func(form url.Values) {
				form.Set("time_zone", "not/a-time-zone")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			form := url.Values{
				"time_zone":             {"UTC"},
				"lesson_window_hours":   {"12"},
				"extra_study_limit":     {"8"},
				"retry_count":           {"3"},
				"leech_failure_count":   {"5"},
				"leech_suspend_count":   {"3"},
				"leech_recovery_streak": {"3"},
				"theme":                 {"light"},
			}
			test.change(form)
			request := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusUnprocessableEntity, response.Body.String())
			}
		})
	}
}

func formRequest(t *testing.T, form url.Values) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := request.ParseForm(); err != nil {
		t.Fatal(err)
	}
	return request
}
