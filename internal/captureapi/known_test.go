package captureapi

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/vocabulary"
)

type fakeKnownVocabulary struct {
	expression string
	result     vocabulary.AddKnownResult
	err        error
}

func (store *fakeKnownVocabulary) AddKnown(_ context.Context, expression string) (vocabulary.AddKnownResult, error) {
	store.expression = expression
	return store.result, store.err
}

func TestMarkKnownAddsWordWithoutMining(t *testing.T) {
	known := &fakeKnownVocabulary{result: vocabulary.AddKnownResult{Created: 1}}
	router, token := knownTestRouter(t, known)
	response := serveKnownAPI(router, token, `{"expression":"育てる"}`)

	if response.Code != http.StatusOK || response.Body.String() != "{\"state\":\"marked_known\"}\n" {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if known.expression != "育てる" {
		t.Fatalf("expression = %q", known.expression)
	}
}

func TestMarkKnownReportsExistingState(t *testing.T) {
	for _, test := range []struct {
		name   string
		result vocabulary.AddKnownResult
		state  string
	}{
		{name: "already known", result: vocabulary.AddKnownResult{AlreadyKnown: 1}, state: "already_known"},
		{name: "active lesson", result: vocabulary.AddKnownResult{SkippedActiveLesson: 1}, state: "in_lessons"},
	} {
		t.Run(test.name, func(t *testing.T) {
			router, token := knownTestRouter(t, &fakeKnownVocabulary{result: test.result})
			response := serveKnownAPI(router, token, `{"expression":"育てる"}`)
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.state) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestMarkKnownValidatesInput(t *testing.T) {
	known := &fakeKnownVocabulary{err: vocabulary.ErrInvalidInput}
	router, token := knownTestRouter(t, known)

	response := serveKnownAPI(router, token, `{"expression":""}`)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "invalid_word") {
		t.Fatalf("invalid word response = %d %s", response.Code, response.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/api/extension/v1/known", strings.NewReader(`{"expression":"育てる"}`))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized response = %d %s", response.Code, response.Body.String())
	}
}

func knownTestRouter(t *testing.T, known KnownVocabulary) (http.Handler, string) {
	t.Helper()
	ctx, db := openCaptureAPITestDatabase(t)
	tokens := NewStore(db)
	token, err := tokens.Create(ctx, "Test browser")
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(tokens, &fakeCaptureCreator{}, nil, nil, known, nil, false).Routes(router)
	return router, token.Plaintext
}

func serveKnownAPI(router http.Handler, token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/extension/v1/known", strings.NewReader(body))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
