package dashboard

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/database"
	"github.com/tomasmik/goi/internal/reviews"
	internalweb "github.com/tomasmik/goi/internal/web"
)

func TestDashboardShowsCompletedNormalReviewSummary(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "review-completion.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	reviewStore, sessionID := completeDashboardReview(t, ctx, db)
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(newDashboardTestStore(db, time.UTC), renderer)
	path := "/dashboard?completed_review=" + stringID(sessionID)
	response := httptest.NewRecorder()
	handler.Dashboard(response, httptest.NewRequest(http.MethodGet, path, nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{
		`role="status" aria-labelledby="review-completion-title"`,
		"Reviews complete",
		"1 word reviewed",
		"2 of 2 prompts correct first try",
		"Next review",
		"data-local-time",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("dashboard completion does not contain %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "retries completed") {
		t.Fatalf("dashboard invents retries for a first-try review: %s", body)
	}

	if err := reviewStore.Undo(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.Dashboard(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("reopened review status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "Reviews complete") {
		t.Fatalf("dashboard shows stale completion after undo: %s", response.Body.String())
	}
}

func TestDashboardIgnoresUnknownCompletedReview(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "unknown-review-completion.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(newDashboardTestStore(db, time.UTC), renderer)

	for _, value := range []string{"not-a-session", "999"} {
		response := httptest.NewRecorder()
		handler.Dashboard(response, httptest.NewRequest(http.MethodGet, "/dashboard?completed_review="+value, nil))
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "Reviews complete") {
			t.Fatalf("completed_review=%q response = %d, body = %s", value, response.Code, response.Body.String())
		}
	}
}

func TestDashboardRendersActiveLessonAction(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "resume-actions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	lessonResult, err := db.Exec("INSERT INTO lesson_sessions (status) VALUES ('active')")
	if err != nil {
		t.Fatal(err)
	}
	lessonID, err := lessonResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		INSERT INTO review_sessions (kind, status, lesson_session_id)
		VALUES ('extra', 'paused', ?)`, lessonID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		UPDATE lesson_sessions
		SET phase = 'review'
		WHERE id = ?`, lessonID); err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(newDashboardTestStore(db, time.UTC), renderer)
	request := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	response := httptest.NewRecorder()

	handler.Dashboard(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := dashboardActions(t, response.Body.String())
	for _, expected := range []string{
		`href="/lessons">Choose a lesson`,
		`href="/lessons/session/` + stringID(lessonID) + `">Continue lesson`,
		`<strong class="action-count">1</strong>`,
		`<h2>Lesson in progress</h2>`,
		`href="/lessons">Lesson queue`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("dashboard does not contain %q: %s", expected, body)
		}
	}
	for _, unexpected := range []string{"Resume reviews", "Start reviews", ">Learn <", "Capture a word"} {
		if strings.Contains(body, unexpected) {
			t.Fatalf("dashboard contains %q while a lesson is active", unexpected)
		}
	}
}

func TestDashboardRendersStartActionsWithoutSessions(t *testing.T) {
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()

	renderer.Render(response, "dashboard.html", Page{
		Title: "Dashboard",
		Summary: Summary{
			DueReviews:       1,
			AvailableLessons: 1,
		},
	})

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := dashboardActions(t, response.Body.String())
	for _, expected := range []string{"Start reviews", ">Learn <", "Choose words"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("dashboard does not contain %q: %s", expected, body)
		}
	}
	for _, unexpected := range []string{"Resume reviews", "Continue lesson"} {
		if strings.Contains(body, unexpected) {
			t.Fatalf("dashboard contains %q without a resumable session", unexpected)
		}
	}
}

func TestDashboardOmitsUpcomingReviewsWhenNoneAreScheduled(t *testing.T) {
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()

	renderer.Render(response, "dashboard.html", Page{Title: "Dashboard"})

	if strings.Contains(response.Body.String(), `id="upcoming-reviews"`) {
		t.Fatalf("dashboard renders an empty upcoming review section: %s", response.Body.String())
	}
}

func TestDashboardEmptyStateGuidesFirstRun(t *testing.T) {
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()

	renderer.Render(response, "dashboard.html", Page{Title: "Dashboard"})

	body := response.Body.String()
	for _, expected := range []string{
		"Capture a word",
		"Import vocabulary",
		"Install the extension",
		"Mine from webpages and video.",
		`href="/mining/capture"`,
		`href="/settings/extension"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("empty dashboard does not contain %q: %s", expected, body)
		}
	}
	if strings.Contains(body, `href="/vocabulary/new"`) {
		t.Fatalf("empty dashboard bypasses dictionary lookup: %s", body)
	}
	for _, unexpected := range []string{"Memory stages", "Words learned", "Next reviews"} {
		if strings.Contains(body, unexpected) {
			t.Fatalf("empty dashboard contains %q: %s", unexpected, body)
		}
	}
	if !strings.Contains(body, "Recent mistakes") || !strings.Contains(body, "No recent mistakes.") {
		t.Fatalf("empty dashboard omits recent-mistakes state: %s", body)
	}
}

func TestDashboardLinksToPendingMiningCaptures(t *testing.T) {
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()

	renderer.Render(response, "dashboard.html", Page{
		Title:   "Dashboard",
		Summary: Summary{PendingCaptures: 2},
	})

	body := response.Body.String()
	if !strings.Contains(body, `<h2>Mining captures</h2>`) || !strings.Contains(body, `<strong class="action-count">2</strong>`) || !strings.Contains(body, `href="/mining"`) {
		t.Fatalf("dashboard does not link to pending mining captures: %s", body)
	}
}

func TestDashboardWarnsOnlyForUnhealthyAutomaticBackups(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "backup-warning.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db, time.UTC, nil, nil, nil)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	for _, test := range []struct {
		name        string
		enabled     int
		status      string
		lastSuccess any
		errorText   string
		want        string
	}{
		{name: "disabled", enabled: 0, status: "failed", errorText: "disk full"},
		{name: "healthy", enabled: 1, status: "success", lastSuccess: now.Add(-24 * time.Hour).Unix()},
		{name: "failed", enabled: 1, status: "failed", lastSuccess: now.Add(-24 * time.Hour).Unix(), errorText: "disk full", want: "latest automatic backup failed"},
		{name: "overdue", enabled: 1, status: "success", lastSuccess: now.Add(-72 * time.Hour).Unix(), want: "Automatic backups are overdue"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := db.Exec(`UPDATE backup_settings SET enabled = ? WHERE id = 1`, test.enabled); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE backup_state SET status = ?, last_success_at = ?, error_message = ? WHERE id = 1`, test.status, test.lastSuccess, test.errorText); err != nil {
				t.Fatal(err)
			}
			var summary Summary
			if err := store.loadBackupWarning(ctx, now, time.UTC, &summary); err != nil {
				t.Fatal(err)
			}
			if test.want == "" && summary.BackupWarning != "" {
				t.Fatalf("warning = %q", summary.BackupWarning)
			}
			if test.want != "" && !strings.Contains(summary.BackupWarning, test.want) {
				t.Fatalf("warning = %q, want %q", summary.BackupWarning, test.want)
			}
		})
	}
}

func TestReviewScheduleRedirectsLegacyDates(t *testing.T) {
	handler := NewHandler(nil, nil)
	router := chi.NewRouter()
	router.Get("/dashboard/reviews/{date}", handler.ReviewScheduleRedirect)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/dashboard/reviews/2026-07-27", nil))

	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusSeeOther)
	}
	if location := response.Header().Get("Location"); location != "/dashboard#upcoming-reviews" {
		t.Fatalf("redirect location = %q", location)
	}
}

func TestReviewScheduleRejectsInvalidLegacyDates(t *testing.T) {
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(nil, renderer)
	router := chi.NewRouter()
	router.Get("/dashboard/reviews/{date}", handler.ReviewScheduleRedirect)
	for _, path := range []string{
		"/dashboard/reviews/not-a-date",
		"/dashboard/reviews/2026-02-30",
	} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
			}
			if location := response.Header().Get("Location"); location != "" {
				t.Fatalf("unexpected redirect to %q", location)
			}
			if !strings.Contains(response.Body.String(), `href="/dashboard"`) {
				t.Fatalf("missing dashboard recovery link: %s", response.Body.String())
			}
		})
	}
}

func stringID(id int64) string {
	return strconv.FormatInt(id, 10)
}

func dashboardActions(t *testing.T, body string) string {
	t.Helper()
	const marker = `<section class="dashboard-actions">`
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatalf("dashboard actions not found: %s", body)
	}
	end := strings.Index(body[start:], "</section>")
	if end < 0 {
		t.Fatalf("dashboard actions are not closed: %s", body)
	}
	return body[start : start+end]
}

func completeDashboardReview(t *testing.T, ctx context.Context, db *sql.DB) (*reviews.Store, int64) {
	t.Helper()
	now := time.Now().UTC().Unix()
	result, err := db.Exec(`
		INSERT INTO vocabulary (
			expression, normalized_expression, pronunciation, normalized_pronunciation,
			status, created_at, updated_at
		)
		VALUES ('食べる', '食べる', 'たべる', 'たべる', 'active', ?, ?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	vocabularyID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO meanings (vocabulary_id, position, text, normalized_text) VALUES (?, 0, 'to eat', 'to eat')`, vocabularyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO srs_states (vocabulary_id, stage, due_at) VALUES (?, 0, ?)`, vocabularyID, now-1); err != nil {
		t.Fatal(err)
	}

	store := reviews.NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	for attempts := 0; attempts < 10; attempts++ {
		state, err := store.State(ctx, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if state.Status == "completed" {
			return store, sessionID
		}
		answer := state.Meanings[0]
		if state.PromptType == "pronunciation" {
			answer = state.Pronunciation
		}
		if _, err := store.Answer(ctx, sessionID, state.PromptID, answer); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("review did not complete")
	return nil, 0
}
