package examplegen

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	internalweb "github.com/tomasmik/goi/internal/web"
)

func TestSettingsHandlerDoesNotSaveProviderThatFailsTesting(t *testing.T) {
	manager, err := NewManager(filepath.Join(t.TempDir(), SettingsFilename), ProviderSettings{})
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewSettingsHandler(manager, renderer).Routes(router)
	form := url.Values{
		"base_url":                {"http://remote.example/v1"},
		"model":                   {"test-model"},
		"sentence_instruction":    {DefaultInstructions().Sentence},
		"translation_instruction": {DefaultInstructions().Translation},
		"target_instruction":      {DefaultInstructions().TargetSurface},
	}
	request := httptest.NewRequest(http.MethodPost, "/settings/examples/test", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("test status = %d, body = %s", response.Code, response.Body.String())
	}
	if manager.Available() {
		t.Fatal("provider settings were saved after a failed test")
	}
}
