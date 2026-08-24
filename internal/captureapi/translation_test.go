package captureapi

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/examplegen"
)

type fakeTranslator struct {
	text   string
	result examplegen.Translation
	err    error
}

func (*fakeTranslator) TranslationAvailable() bool { return true }

func (translator *fakeTranslator) Translate(_ context.Context, text string) (examplegen.Translation, error) {
	translator.text = text
	return translator.result, translator.err
}

func TestTranslateReturnsConfiguredProviderResult(t *testing.T) {
	translator := &fakeTranslator{result: examplegen.Translation{Text: "I am reading a book.", Provider: "microsoft-translator"}}
	router, token := translationTestRouter(t, translator)
	request := httptest.NewRequest(http.MethodPost, "/api/extension/v1/translate", strings.NewReader(`{"text":"本を読んでいます。"}`))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != "{\"translation\":\"I am reading a book.\",\"provider\":\"microsoft-translator\"}\n" {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if translator.text != "本を読んでいます。" {
		t.Fatalf("translated text = %q", translator.text)
	}
}

func TestTranslateRejectsUnavailableAndInvalidRequests(t *testing.T) {
	router, token := translationTestRouter(t, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/extension/v1/translate", strings.NewReader(`{"text":"猫"}`))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable response = %d %s", response.Code, response.Body.String())
	}

	translator := &fakeTranslator{}
	router, token = translationTestRouter(t, translator)
	request = httptest.NewRequest(http.MethodPost, "/api/extension/v1/translate", strings.NewReader(`{"text":"","extra":true}`))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || translator.text != "" {
		t.Fatalf("invalid response = %d %s, translated = %q", response.Code, response.Body.String(), translator.text)
	}
}

func translationTestRouter(t *testing.T, translator examplegen.Translator) (http.Handler, string) {
	t.Helper()
	ctx, db := openCaptureAPITestDatabase(t)
	store := NewStore(db)
	token, err := store.Create(ctx, "Translation browser")
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, &fakeCaptureCreator{}, nil, nil, nil, translator, false).Routes(router)
	return router, token.Plaintext
}
