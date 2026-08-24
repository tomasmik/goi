package lessons

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/database"
	"github.com/tomasmik/goi/internal/examples"
	"github.com/tomasmik/goi/internal/reviews"
	internalweb "github.com/tomasmik/goi/internal/web"
)

func lessonTestRouter(t *testing.T, store *Store, reviewStore *reviews.Store) http.Handler {
	t.Helper()
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, reviewStore, renderer).Routes(router)
	return router
}

func TestMissingLessonRendersRecoveryPage(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "missing-lesson.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	router := lessonTestRouter(t, NewStore(db), reviews.NewStore(db))
	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/lessons/session/999", nil),
		httptest.NewRequest(http.MethodPost, "/lessons/session/999/review", strings.NewReader("")),
	}
	for _, request := range requests {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `href="/lessons"`) {
			t.Fatalf("missing lesson response = %d, %s", response.Code, response.Body.String())
		}
	}
}

func TestLessonReviewNotReadyRendersRecoveryPage(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "lesson-not-ready.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	sessionID, err := store.Start(ctx, insertUnlearnedVocabulary(t, db, 2))
	if err != nil {
		t.Fatal(err)
	}
	router := lessonTestRouter(t, store, reviews.NewStore(db))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/lessons/session/%d/review", sessionID), nil))

	body := response.Body.String()
	if response.Code != http.StatusConflict || !strings.Contains(body, "Open every word in this lesson batch") {
		t.Fatalf("response = %d, body = %s", response.Code, body)
	}
	for _, expected := range []string{`class="site-header"`, fmt.Sprintf(`href="/lessons/session/%d"`, sessionID), "Back to lesson"} {
		if !strings.Contains(body, expected) {
			t.Errorf("recovery page does not contain %q: %s", expected, body)
		}
	}
}

func TestLessonShowsExampleContext(t *testing.T) {
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	renderer.Render(response, "lesson-session.html", SessionPage{
		Title: "Lesson",
		Session: Session{
			Status:     "active",
			Phase:      "study",
			BatchCount: 1,
			Items:      []StudyItem{{Expression: "食べる"}},
			StudyItem: StudyItem{
				Expression: "食べる",
				AudioID:    7,
				PictureID:  8,
				Example: examples.Example{
					Sentence:            "昨日、寿司を食べた。",
					Translation:         "I ate sushi yesterday.",
					SourceTitle:         "Japanese vlog",
					SourceLink:          "https://example.com/watch",
					SourcePositionLabel: "1:24",
					BeforeTarget:        "昨日、寿司を",
					MatchedTarget:       "食べた",
					AfterTarget:         "。",
					HasTarget:           true,
				},
			},
		},
	})

	body := response.Body.String()
	for _, expected := range []string{"昨日、寿司を", "食べた", "I ate sushi yesterday.", "Japanese vlog", "1:24", `href="https://example.com/watch"`, `src="/media/7"`, `src="/media/8"`, `class="lesson-controls"`} {
		if !strings.Contains(body, expected) {
			t.Errorf("lesson example does not contain %q", expected)
		}
	}
}

func TestStudyNavigationReturnsUpdatedFragment(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "lesson-handler.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	ids := insertUnlearnedVocabulary(t, db, 2)
	store := NewStore(db)
	sessionID, err := store.Start(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Current(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	router := lessonTestRouter(t, store, reviews.NewStore(db))

	form := url.Values{"csrf_token": {""}}
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/lessons/session/%d/word/1", sessionID), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Goi-Fragment", "lesson")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `id="study-stage"`) || !strings.Contains(body, "Word 2 of 2") {
		t.Fatalf("lesson fragment = %s", body)
	}
	if strings.Contains(body, "<!doctype html>") {
		t.Fatal("lesson navigation unexpectedly rendered a full page")
	}
}

func TestCompletedLessonRedirectsDashboard(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "completed-lesson.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec("INSERT INTO lesson_sessions (status, phase) VALUES ('completed', 'review')")
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/lessons/session/%d", sessionID), nil)
	response := httptest.NewRecorder()
	lessonTestRouter(t, NewStore(db), reviews.NewStore(db)).ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard" {
		t.Fatalf("completed lesson response = %d, location %q", response.Code, response.Header().Get("Location"))
	}
}

func TestLegacyStudyNavigationDoesNotMutateSession(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "lesson-legacy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	ids := insertUnlearnedVocabulary(t, db, 2)
	store := NewStore(db)
	sessionID, err := store.Start(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	router := lessonTestRouter(t, store, reviews.NewStore(db))

	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/lessons/session/%d/word/1", sessionID), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusSeeOther)
	}
	var position int
	if err := db.QueryRow("SELECT study_position FROM lesson_sessions WHERE id = ?", sessionID).Scan(&position); err != nil {
		t.Fatal(err)
	}
	if position != 0 {
		t.Fatalf("legacy GET changed study position to %d", position)
	}
}

func TestLessonPickerShowsActiveSession(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "active-lesson-handler.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	ids := insertUnlearnedVocabulary(t, db, 1)
	store := NewStore(db)
	sessionID, err := store.Start(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	router := lessonTestRouter(t, store, reviews.NewStore(db))

	request := httptest.NewRequest(http.MethodGet, "/lessons", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	for _, expected := range []string{
		"Current lesson",
		fmt.Sprintf(`href="/lessons/session/%d"`, sessionID),
		fmt.Sprintf(`action="/lessons/session/%d/return"`, sessionID),
		"Continue lesson",
		"Return words to queue",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("lesson picker does not contain %q: %s", expected, body)
		}
	}
	if strings.Contains(body, `action="/lessons/start?page=`) {
		t.Fatalf("lesson picker offers a second lesson while one is active: %s", body)
	}
}

func TestReturnLessonToQueue(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "return-lesson-handler.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	ids := insertUnlearnedVocabulary(t, db, 2)
	sessionID, err := store.Start(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	router := lessonTestRouter(t, store, reviews.NewStore(db))

	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/lessons/session/%d/return", sessionID), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/lessons" {
		t.Fatalf("return response = %d, %q", response.Code, response.Header().Get("Location"))
	}
	var status string
	if err := db.QueryRow("SELECT status FROM lesson_sessions WHERE id = ?", sessionID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "abandoned" {
		t.Fatalf("lesson status = %q, want abandoned", status)
	}
	count, err := store.AvailableCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("available words = %d, want 2", count)
	}
}

func TestStartNextRedirectsToActiveSessionWhenNoWordsRemain(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "active-start-next.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	sessionID, err := store.Start(ctx, insertUnlearnedVocabulary(t, db, 1))
	if err != nil {
		t.Fatal(err)
	}
	router := lessonTestRouter(t, store, reviews.NewStore(db))

	request := httptest.NewRequest(http.MethodPost, "/lessons/start-next", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	wantLocation := fmt.Sprintf("/lessons/session/%d", sessionID)
	if location := response.Header().Get("Location"); location != wantLocation {
		t.Fatalf("location = %q, want %q", location, wantLocation)
	}
}

func TestStartHidesStoreFailures(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "closed-lesson-handler.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	router := lessonTestRouter(t, NewStore(db), reviews.NewStore(db))
	form := url.Values{"vocabulary_id": {"1"}}

	request := httptest.NewRequest(http.MethodPost, "/lessons/start", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if body := response.Body.String(); !strings.Contains(body, "could not start lesson") || strings.Contains(body, "closed") {
		t.Fatalf("response exposed store failure: %s", body)
	}
}

func TestStartUsesClientErrorStatuses(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "lesson-status.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	router := lessonTestRouter(t, NewStore(db), reviews.NewStore(db))

	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "malformed selection", body: "%zz", want: http.StatusBadRequest},
		{name: "empty selection", body: "", want: http.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/lessons/start", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestStartPreservesPickerSelectionAfterValidationError(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "lesson-selection.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	ids := insertUnlearnedVocabulary(t, db, 6)
	router := lessonTestRouter(t, NewStore(db), reviews.NewStore(db))
	form := url.Values{"vocabulary_id": {strconv.FormatInt(ids[5], 10), "999999"}}

	request := httptest.NewRequest(http.MethodPost, "/lessons/start", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusConflict, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, fmt.Sprintf(`value="%d" checked`, ids[5])) {
		t.Fatalf("submitted word was not kept selected: %s", body)
	}
	if strings.Contains(body, fmt.Sprintf(`value="%d" checked`, ids[0])) {
		t.Fatalf("default selection replaced the submitted selection: %s", body)
	}
}
