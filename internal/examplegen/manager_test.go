package examplegen

import (
	"encoding/json"
	"os"
	"path/filepath"
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
