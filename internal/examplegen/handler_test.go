package examplegen

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	internalweb "github.com/tomasmik/goi/internal/web"
)

func TestSettingsHandlerTestsAndSavesSubmittedProvider(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("provider request = %s, authorization %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"sentence\":\"日本語を勉強します。\",\"translation\":\"I study Japanese.\",\"target_surface\":\"日本語\"}"}}]}`)
	}))
	defer provider.Close()

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
		"base_url":                {provider.URL + "/v1"},
		"model":                   {"test-model"},
		"api_key":                 {"test-key"},
		"sentence_instruction":    {DefaultInstructions().Sentence},
		"translation_instruction": {DefaultInstructions().Translation},
		"target_instruction":      {DefaultInstructions().TargetSurface},
	}
	request := httptest.NewRequest(http.MethodPost, "/settings/examples/test", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/settings/examples?tested=1" {
		t.Fatalf("test status = %d, location = %q, body = %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if !manager.Available() || !manager.Current().HasAPIKey {
		t.Fatal("working provider settings were not saved")
	}
}

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

func TestSettingsHandlerShowsDefaultsAndSavesConfiguration(t *testing.T) {
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

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/settings/examples", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{
		"Translation and examples", "Microsoft Translator", "Keys and Endpoint", "Translation instructions", "Japanese sentence", "Target word form",
		"OpenRouter", "https://openrouter.ai/api/v1", "full model ID",
		`href="/settings"`, `aria-current="page"`,
		DefaultInstructions().Sentence,
	} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("settings page does not contain %q: %s", expected, response.Body.String())
		}
	}

	form := url.Values{
		"base_url":                {"http://127.0.0.1:11434/v1"},
		"model":                   {"qwen3:4b"},
		"api_key":                 {"local-secret"},
		"sentence_instruction":    {"Write a short sentence."},
		"translation_instruction": {"Use natural English."},
		"target_instruction":      {"Return the exact inflected spelling."},
	}
	request := httptest.NewRequest(http.MethodPost, "/settings/examples", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/settings/examples?saved=1" {
		t.Fatalf("POST status = %d, location = %q, body = %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	got := manager.Current()
	if !manager.Available() || got.Model != "qwen3:4b" || !got.HasAPIKey || got.Instructions.Sentence != "Write a short sentence." {
		t.Fatalf("saved settings = %+v", got)
	}
}

func TestSettingsHandlerRemovesSavedAPIKeyImmediately(t *testing.T) {
	manager, err := NewManager(filepath.Join(t.TempDir(), SettingsFilename), ProviderSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Update(SettingsUpdate{
		BaseURL:      "http://127.0.0.1:11434/v1",
		Model:        "qwen3:4b",
		APIKey:       "secret",
		Instructions: DefaultInstructions(),
	}); err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewSettingsHandler(manager, renderer).Routes(router)
	page := httptest.NewRecorder()
	router.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/settings/examples", nil))
	if body := page.Body.String(); !strings.Contains(body, "Remove API key") || strings.Contains(body, `name="clear_api_key"`) {
		t.Fatalf("saved key controls are not explicit: %s", body)
	}

	unconfirmed := httptest.NewRecorder()
	router.ServeHTTP(unconfirmed, httptest.NewRequest(http.MethodPost, "/settings/examples/api-key/remove", nil))
	if unconfirmed.Code != http.StatusBadRequest || !manager.Current().HasAPIKey {
		t.Fatalf("unconfirmed removal status = %d, has key = %t", unconfirmed.Code, manager.Current().HasAPIKey)
	}

	request := httptest.NewRequest(http.MethodPost, "/settings/examples/api-key/remove", strings.NewReader("confirmed=1"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/settings/examples?saved=1" {
		t.Fatalf("status = %d, location = %q", response.Code, response.Header().Get("Location"))
	}
	if manager.Current().HasAPIKey {
		t.Fatal("saved API key was not removed")
	}
}

func TestSettingsHandlerPreservesSubmittedValuesAfterValidationError(t *testing.T) {
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
		"model":                   {"my-model"},
		"sentence_instruction":    {"My sentence instruction"},
		"translation_instruction": {"My translation instruction"},
		"target_instruction":      {"My target instruction"},
	}
	request := httptest.NewRequest(http.MethodPost, "/settings/examples", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{"must use HTTPS", "my-model", "My sentence instruction"} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("error page does not contain %q: %s", expected, response.Body.String())
		}
	}
}
