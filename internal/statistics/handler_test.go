package statistics

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/database"
	internalweb "github.com/tomasmik/goi/internal/web"
)

func TestPageHidesReviewActionWithoutRecentMistakes(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "statistics-handler.sqlite"))
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
	router := chi.NewRouter()
	NewHandler(NewStore(db, time.UTC), renderer).Routes(router)

	request := httptest.NewRequest(http.MethodGet, "/statistics", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "<title>Progress | Goi</title>") {
		t.Fatalf("statistics page title is inconsistent: %s", body)
	}
	if strings.Contains(body, `action="/study/recent-mistakes"`) {
		t.Fatal("statistics page contains recent-mistakes action without mistakes")
	}
}

func TestMalformedMistakeActionRendersRecoveryPage(t *testing.T) {
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(nil, renderer).Routes(router)
	request := httptest.NewRequest(
		http.MethodPost,
		"/statistics/mistakes/7/visibility",
		strings.NewReader(strings.Repeat("x", (64<<10)+1)),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusBadRequest || !strings.Contains(body, "The mistake action form is too large or invalid.") {
		t.Fatalf("response = %d, body = %s", response.Code, body)
	}
	for _, expected := range []string{`class="site-header"`, `href="/statistics"`, "Back to progress"} {
		if !strings.Contains(body, expected) {
			t.Errorf("rendered error does not contain %q: %s", expected, body)
		}
	}
}

func TestHideUnavailableMistakeReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "missing-mistake-handler.sqlite"))
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
	router := chi.NewRouter()
	NewHandler(NewStore(db, time.UTC), renderer).Routes(router)

	nonMistakeID := insertMistakeVocabulary(t, db, "食べる")
	staleMistakeID := insertMistakeVocabulary(t, db, "見る")
	insertNormalFailure(t, db, staleMistakeID, time.Now().UTC().Add(-25*time.Hour).Unix())
	for _, test := range []struct {
		name string
		id   int64
	}{
		{name: "missing", id: 999},
		{name: "non-mistake", id: nonMistakeID},
		{name: "stale", id: staleMistakeID},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/statistics/mistakes/%d/visibility", test.id), nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusNotFound, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), `href="/statistics"`) {
				t.Fatalf("missing activity recovery link: %s", response.Body.String())
			}
		})
	}
}
