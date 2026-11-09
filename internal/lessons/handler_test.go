package lessons

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/database"
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
