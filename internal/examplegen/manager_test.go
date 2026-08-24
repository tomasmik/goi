package examplegen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerSavesProviderAndInstructions(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsFilename)
	manager, err := NewManager(path, ProviderSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if manager.Available() {
		t.Fatal("new manager is available without a provider")
	}

	instructions := Instructions{
		Sentence:      "Use language suitable for a beginner.",
		Translation:   "Keep the English translation literal.",
		TargetSurface: "Return the conjugated target spelling.",
	}
	if err := manager.Update(SettingsUpdate{
		BaseURL:      "http://127.0.0.1:11434/v1/",
		Model:        "local-model",
		APIKey:       "secret-key",
		Instructions: instructions,
	}); err != nil {
		t.Fatal(err)
	}
	if !manager.Available() {
		t.Fatal("saved provider is unavailable")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("settings permissions = %o, want 600", info.Mode().Perm())
	}

	reloaded, err := NewManager(path, ProviderSettings{})
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.Current()
	if got.BaseURL != "http://127.0.0.1:11434/v1/" || got.Model != "local-model" || !got.HasAPIKey || got.Instructions != instructions {
		t.Fatalf("reloaded settings = %+v", got)
	}

	if err := reloaded.Update(SettingsUpdate{
		BaseURL:      got.BaseURL,
		Model:        got.Model,
		Instructions: got.Instructions,
	}); err != nil {
		t.Fatal(err)
	}
	if !reloaded.Current().HasAPIKey {
		t.Fatal("blank API key replaced the saved key")
	}
	if err := reloaded.Update(SettingsUpdate{
		BaseURL:      got.BaseURL,
		Model:        got.Model,
		ClearAPIKey:  true,
		Instructions: got.Instructions,
	}); err != nil {
		t.Fatal(err)
	}
	if reloaded.Current().HasAPIKey {
		t.Fatal("clear API key did not remove the saved key")
	}
}

func TestManagerRemoveAPIKeyKeepsProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsFilename)
	manager, err := NewManager(path, ProviderSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Update(SettingsUpdate{
		BaseURL:             "http://127.0.0.1:11434/v1",
		Model:               "qwen3:4b",
		APIKey:              "secret",
		TranslationProvider: TranslationProviderNone,
		Instructions:        DefaultInstructions(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveAPIKey(); err != nil {
		t.Fatal(err)
	}
	current := manager.Current()
	if current.HasAPIKey {
		t.Fatal("API key was not removed")
	}
	if current.BaseURL != "http://127.0.0.1:11434/v1" || current.Model != "qwen3:4b" {
		t.Fatalf("provider was changed: %#v", current)
	}
}

func TestManagerRemoveMicrosoftSettingsDisablesMicrosoftTranslation(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsFilename)
	manager, err := NewManager(path, ProviderSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Update(SettingsUpdate{
		TranslationProvider: TranslationProviderMicrosoft,
		MicrosoftEndpoint:   "https://api.cognitive.microsofttranslator.com",
		MicrosoftRegion:     "westeurope",
		MicrosoftAPIKey:     "secret",
		Instructions:        DefaultInstructions(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveMicrosoftSettings(); err != nil {
		t.Fatal(err)
	}
	current := manager.Current()
	if current.HasMicrosoftAPIKey || current.MicrosoftEndpoint != "" || current.MicrosoftRegion != "" {
		t.Fatalf("Microsoft settings were not removed: %#v", current)
	}
	if current.TranslationProvider != TranslationProviderNone {
		t.Fatalf("translation provider = %q, want none", current.TranslationProvider)
	}
}

func TestManagerUsesConfiguredTranslationInstruction(t *testing.T) {
	const instruction = "Keep the translation literal and concise."
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Messages) != 2 || !strings.Contains(request.Messages[0].Content, instruction) {
			t.Fatalf("messages = %#v", request.Messages)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": `{"translation":"Japanese."}`}}},
		})
	}))
	defer server.Close()

	manager, err := NewManager(filepath.Join(t.TempDir(), SettingsFilename), ProviderSettings{})
	if err != nil {
		t.Fatal(err)
	}
	instructions := DefaultInstructions()
	instructions.Translation = instruction
	if err := manager.Update(SettingsUpdate{
		BaseURL:             server.URL,
		Model:               "test-model",
		TranslationProvider: TranslationProviderOpenAI,
		Instructions:        instructions,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Translate(context.Background(), "日本語"); err != nil {
		t.Fatal(err)
	}
}

func TestManagerUpgradesPreviousDefaultInstructions(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsFilename)
	contents, err := json.Marshal(storedSettings{Instructions: previousDefaultInstructions()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	manager, err := NewManager(path, ProviderSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.Current().Instructions; got != DefaultInstructions() {
		t.Fatalf("instructions = %+v, want current defaults", got)
	}
}

func TestManagerUpgradesPreviousTranslationDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsFilename)
	instructions := DefaultInstructions()
	instructions.Translation = "Write one natural English translation of the Japanese sentence. Preserve its meaning and tone. Do not include labels, notes, alternatives, romanization, or quotation marks."
	contents, err := json.Marshal(storedSettings{Instructions: instructions})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	manager, err := NewManager(path, ProviderSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.Current().Instructions.Translation; got != DefaultInstructions().Translation {
		t.Fatalf("translation instruction = %q, want current default", got)
	}
}

func TestEnvironmentProviderOverridesSavedProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsFilename)
	manager, err := NewManager(path, ProviderSettings{
		BaseURL: "https://models.example/v1",
		Model:   "server-model",
		APIKey:  "server-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	instructions := DefaultInstructions()
	instructions.Sentence = "Use one short sentence."
	if err := manager.Update(SettingsUpdate{
		BaseURL:      "https://ignored.example/v1",
		Model:        "ignored-model",
		APIKey:       "ignored-key",
		Instructions: instructions,
	}); err != nil {
		t.Fatal(err)
	}
	got := manager.Current()
	if !got.EnvironmentManaged || got.BaseURL != "https://models.example/v1" || got.Model != "server-model" || !got.HasAPIKey {
		t.Fatalf("effective environment settings = %+v", got)
	}
	if got.Instructions != instructions {
		t.Fatalf("instructions = %+v, want %+v", got.Instructions, instructions)
	}
}

func TestManagerRejectsIncompleteOrUnsafeProvider(t *testing.T) {
	manager, err := NewManager(filepath.Join(t.TempDir(), SettingsFilename), ProviderSettings{})
	if err != nil {
		t.Fatal(err)
	}
	for _, update := range []SettingsUpdate{
		{BaseURL: "https://models.example/v1"},
		{Model: "model"},
		{BaseURL: "http://models.example/v1", Model: "model"},
		{TranslationProvider: TranslationProviderMicrosoft},
		{TranslationProvider: TranslationProviderOpenAI},
	} {
		update.Instructions = DefaultInstructions()
		if err := manager.Update(update); err == nil {
			t.Fatalf("Update() accepted %+v", update)
		}
	}
}

func TestManagerSavesMicrosoftTranslatorSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsFilename)
	manager, err := NewManager(path, ProviderSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Update(SettingsUpdate{
		TranslationProvider: TranslationProviderMicrosoft,
		MicrosoftEndpoint:   "https://api.cognitive.microsofttranslator.com/",
		MicrosoftRegion:     "westeurope",
		MicrosoftAPIKey:     "translator-key",
		Instructions:        DefaultInstructions(),
	}); err != nil {
		t.Fatal(err)
	}
	if manager.Available() {
		t.Fatal("Microsoft Translator enabled example generation")
	}
	if !manager.TranslationAvailable() {
		t.Fatal("Microsoft Translator is unavailable")
	}
	reloaded, err := NewManager(path, ProviderSettings{})
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.Current()
	if got.TranslationProvider != TranslationProviderMicrosoft || got.MicrosoftRegion != "westeurope" || !got.HasMicrosoftAPIKey {
		t.Fatalf("reloaded settings = %+v", got)
	}
}
