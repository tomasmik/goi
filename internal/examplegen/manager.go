package examplegen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/tomasmik/goi/internal/securefile"
)

const SettingsFilename = "example-generation.json"

const (
	TranslationProviderNone      = "none"
	TranslationProviderOpenAI    = "openai"
	TranslationProviderMicrosoft = "microsoft"
)

type ProviderSettings struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	APIKey  string `json:"api_key,omitempty"`
}

type MicrosoftTranslatorSettings struct {
	Endpoint string `json:"endpoint"`
	Region   string `json:"region"`
	APIKey   string `json:"api_key,omitempty"`
}

type SettingsView struct {
	BaseURL             string
	Model               string
	HasAPIKey           bool
	EnvironmentManaged  bool
	Instructions        Instructions
	TranslationProvider string
	MicrosoftEndpoint   string
	MicrosoftRegion     string
	HasMicrosoftAPIKey  bool
}

type SettingsUpdate struct {
	BaseURL                string
	Model                  string
	APIKey                 string
	ClearAPIKey            bool
	Instructions           Instructions
	TranslationProvider    string
	MicrosoftEndpoint      string
	MicrosoftRegion        string
	MicrosoftAPIKey        string
	ClearMicrosoftSettings bool
}

type storedSettings struct {
	Provider            ProviderSettings            `json:"provider"`
	TranslationProvider string                      `json:"translation_provider,omitempty"`
	Microsoft           MicrosoftTranslatorSettings `json:"microsoft_translator,omitempty"`
	Instructions        Instructions                `json:"instructions"`
}

type Manager struct {
	mu          sync.RWMutex
	path        string
	environment ProviderSettings
	stored      storedSettings
}

type settingsError string

func (err settingsError) Error() string {
	return string(err)
}

func (err settingsError) UserMessage() string {
	return string(err)
}

func NewManager(path string, environment ProviderSettings) (*Manager, error) {
	environment.BaseURL = strings.TrimSpace(environment.BaseURL)
	environment.Model = strings.TrimSpace(environment.Model)
	if err := validateProvider(environment); err != nil {
		return nil, fmt.Errorf("validate environment example provider: %w", err)
	}
	stored, err := readSettings(path)
	if err != nil {
		return nil, err
	}
	return &Manager{path: path, environment: environment, stored: stored}, nil
}

func (m *Manager) Available() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	provider := m.providerLocked()
	return provider.BaseURL != "" && provider.Model != ""
}

func (m *Manager) Current() SettingsView {
	m.mu.RLock()
	defer m.mu.RUnlock()
	provider := m.providerLocked()
	return SettingsView{
		BaseURL:             provider.BaseURL,
		Model:               provider.Model,
		HasAPIKey:           provider.APIKey != "",
		EnvironmentManaged:  m.environment.BaseURL != "",
		Instructions:        m.stored.Instructions,
		TranslationProvider: m.translationProviderLocked(),
		MicrosoftEndpoint:   m.stored.Microsoft.Endpoint,
		MicrosoftRegion:     m.stored.Microsoft.Region,
		HasMicrosoftAPIKey:  m.stored.Microsoft.APIKey != "",
	}
}

func (m *Manager) Update(value SettingsUpdate) error {
	instructions, err := normalizeInstructions(value.Instructions)
	if err != nil {
		return settingsError(err.Error())
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.stored
	next.Instructions = instructions
	next.TranslationProvider = normalizeTranslationProvider(value.TranslationProvider)
	next.Microsoft = microsoftFromUpdate(value, next.Microsoft)
	if err := validateMicrosoftSettings(next.Microsoft); err != nil {
		return settingsError(err.Error())
	}
	if m.environment.BaseURL == "" {
		provider := providerFromUpdate(value, next.Provider)
		if err := validateProvider(provider); err != nil {
			return settingsError(err.Error())
		}
		next.Provider = provider
	}
	effectiveProvider := next.Provider
	if m.environment.BaseURL != "" {
		effectiveProvider = m.environment
	}
	if err := validateTranslationSelection(next.TranslationProvider, next.Microsoft, effectiveProvider); err != nil {
		return settingsError(err.Error())
	}
	if err := writeSettings(m.path, next); err != nil {
		return fmt.Errorf("save example generation settings: %w", err)
	}
	m.stored = next
	return nil
}

func (m *Manager) Disable() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.environment.BaseURL != "" {
		return settingsError("the example provider is managed by the server environment")
	}
	next := m.stored
	next.Provider = ProviderSettings{}
	if next.TranslationProvider == TranslationProviderOpenAI {
		next.TranslationProvider = TranslationProviderNone
	}
	if err := writeSettings(m.path, next); err != nil {
		return fmt.Errorf("disable example generation: %w", err)
	}
	m.stored = next
	return nil
}

func (m *Manager) RemoveAPIKey() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.environment.BaseURL != "" {
		return settingsError("the example provider is managed by the server environment")
	}
	next := m.stored
	next.Provider.APIKey = ""
	if err := writeSettings(m.path, next); err != nil {
		return fmt.Errorf("remove example provider API key: %w", err)
	}
	m.stored = next
	return nil
}

func (m *Manager) RemoveMicrosoftSettings() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.stored
	next.Microsoft = MicrosoftTranslatorSettings{}
	if next.TranslationProvider == TranslationProviderMicrosoft {
		next.TranslationProvider = TranslationProviderNone
	}
	if err := writeSettings(m.path, next); err != nil {
		return fmt.Errorf("remove Microsoft Translator settings: %w", err)
	}
	m.stored = next
	return nil
}

func (m *Manager) Test(ctx context.Context, value SettingsUpdate) error {
	instructions, err := normalizeInstructions(value.Instructions)
	if err != nil {
		return settingsError(err.Error())
	}
	m.mu.RLock()
	provider := m.environment
	microsoft := microsoftFromUpdate(value, m.stored.Microsoft)
	translationProvider := normalizeTranslationProvider(value.TranslationProvider)
	if provider.BaseURL == "" {
		provider = providerFromUpdate(value, m.stored.Provider)
	}
	m.mu.RUnlock()
	if err := validateProvider(provider); err != nil {
		return settingsError(err.Error())
	}
	if err := validateMicrosoftSettings(microsoft); err != nil {
		return settingsError(err.Error())
	}
	if err := validateTranslationSelection(translationProvider, microsoft, provider); err != nil {
		return settingsError(err.Error())
	}
	if translationProvider == TranslationProviderNone && provider.BaseURL == "" {
		return settingsError("configure a provider before testing the connection")
	}
	if translationProvider == TranslationProviderMicrosoft {
		client, err := NewMicrosoftTranslator(MicrosoftTranslatorConfig{
			Endpoint: microsoft.Endpoint,
			Region:   microsoft.Region,
			APIKey:   microsoft.APIKey,
		})
		if err != nil {
			return settingsError(err.Error())
		}
		if _, err := client.Translate(ctx, "日本語"); err != nil {
			return fmt.Errorf("Microsoft Translator test failed: %w", err)
		}
	}
	if provider.BaseURL != "" {
		client, err := New(Config{
			BaseURL:      provider.BaseURL,
			Model:        provider.Model,
			APIKey:       provider.APIKey,
			Instructions: instructions,
		})
		if err != nil {
			return settingsError(err.Error())
		}
		_, err = client.Generate(ctx, Request{
			Expression:    "日本語",
			Pronunciation: "にほんご",
			Meanings:      []string{"Japanese language"},
		})
		if err != nil {
			return fmt.Errorf("example provider test failed: %w", err)
		}
	}
	return nil
}

func microsoftFromUpdate(value SettingsUpdate, current MicrosoftTranslatorSettings) MicrosoftTranslatorSettings {
	settings := MicrosoftTranslatorSettings{
		Endpoint: strings.TrimSpace(value.MicrosoftEndpoint),
		Region:   strings.TrimSpace(value.MicrosoftRegion),
		APIKey:   current.APIKey,
	}
	if value.ClearMicrosoftSettings || settings.Endpoint == "" && settings.Region == "" {
		return MicrosoftTranslatorSettings{}
	}
	if value.MicrosoftAPIKey != "" {
		settings.APIKey = value.MicrosoftAPIKey
	}
	return settings
}

func providerFromUpdate(value SettingsUpdate, current ProviderSettings) ProviderSettings {
	provider := ProviderSettings{
		BaseURL: strings.TrimSpace(value.BaseURL),
		Model:   strings.TrimSpace(value.Model),
		APIKey:  current.APIKey,
	}
	if provider.BaseURL == "" && provider.Model == "" {
		provider.APIKey = ""
	} else if value.ClearAPIKey {
		provider.APIKey = ""
	} else if value.APIKey != "" {
		provider.APIKey = value.APIKey
	}
	return provider
}

func (m *Manager) Generate(ctx context.Context, request Request) (Example, error) {
	m.mu.RLock()
	provider := m.providerLocked()
	instructions := m.stored.Instructions
	m.mu.RUnlock()
	if provider.BaseURL == "" {
		return Example{}, errors.New("example generation is not configured")
	}
	client, err := New(Config{
		BaseURL:      provider.BaseURL,
		Model:        provider.Model,
		APIKey:       provider.APIKey,
		Instructions: instructions,
	})
	if err != nil {
		return Example{}, fmt.Errorf("configure example generator: %w", err)
	}
	return client.Generate(ctx, request)
}

func (m *Manager) Translate(ctx context.Context, text string) (Translation, error) {
	m.mu.RLock()
	provider := m.providerLocked()
	microsoft := m.stored.Microsoft
	instructions := m.stored.Instructions
	translationProvider := m.translationProviderLocked()
	m.mu.RUnlock()
	if translationProvider == TranslationProviderMicrosoft {
		client, err := NewMicrosoftTranslator(MicrosoftTranslatorConfig{
			Endpoint: microsoft.Endpoint,
			Region:   microsoft.Region,
			APIKey:   microsoft.APIKey,
		})
		if err != nil {
			return Translation{}, fmt.Errorf("configure Microsoft Translator: %w", err)
		}
		return client.Translate(ctx, text)
	}
	if translationProvider != TranslationProviderOpenAI || provider.BaseURL == "" {
		return Translation{}, errors.New("translation is not configured")
	}
	client, err := New(Config{
		BaseURL:      provider.BaseURL,
		Model:        provider.Model,
		APIKey:       provider.APIKey,
		Instructions: instructions,
	})
	if err != nil {
		return Translation{}, fmt.Errorf("configure translator: %w", err)
	}
	return client.Translate(ctx, text)
}

func (m *Manager) TranslationAvailable() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch m.translationProviderLocked() {
	case TranslationProviderMicrosoft:
		return microsoftConfigured(m.stored.Microsoft)
	case TranslationProviderOpenAI:
		provider := m.providerLocked()
		return provider.BaseURL != "" && provider.Model != ""
	default:
		return false
	}
}

func (m *Manager) translationProviderLocked() string {
	provider := normalizeTranslationProvider(m.stored.TranslationProvider)
	if provider == TranslationProviderNone && m.stored.TranslationProvider == "" {
		if configured := m.providerLocked(); configured.BaseURL != "" {
			return TranslationProviderOpenAI
		}
	}
	return provider
}

func normalizeTranslationProvider(value string) string {
	switch strings.TrimSpace(value) {
	case TranslationProviderOpenAI:
		return TranslationProviderOpenAI
	case TranslationProviderMicrosoft:
		return TranslationProviderMicrosoft
	default:
		return TranslationProviderNone
	}
}

func microsoftConfigured(settings MicrosoftTranslatorSettings) bool {
	return settings.Endpoint != "" && settings.Region != "" && settings.APIKey != ""
}

func validateTranslationSelection(selection string, microsoft MicrosoftTranslatorSettings, provider ProviderSettings) error {
	switch selection {
	case TranslationProviderMicrosoft:
		if !microsoftConfigured(microsoft) {
			return errors.New("complete the Microsoft Translator endpoint, region, and API key")
		}
	case TranslationProviderOpenAI:
		if provider.BaseURL == "" || provider.Model == "" {
			return errors.New("configure an OpenAI-compatible provider for translation")
		}
	}
	return nil
}

func validateMicrosoftSettings(settings MicrosoftTranslatorSettings) error {
	if settings.Endpoint == "" && settings.Region == "" && settings.APIKey == "" {
		return nil
	}
	_, err := NewMicrosoftTranslator(MicrosoftTranslatorConfig{
		Endpoint: settings.Endpoint,
		Region:   settings.Region,
		APIKey:   settings.APIKey,
	})
	return err
}

func (m *Manager) providerLocked() ProviderSettings {
	if m.environment.BaseURL != "" {
		return m.environment
	}
	return m.stored.Provider
}

func validateProvider(provider ProviderSettings) error {
	if provider.BaseURL == "" && provider.Model == "" {
		if provider.APIKey != "" {
			return errors.New("clear the API key or configure a provider")
		}
		return nil
	}
	if provider.BaseURL == "" {
		return errors.New("base URL is required when a model is configured")
	}
	if provider.Model == "" {
		return errors.New("model is required when a base URL is configured")
	}
	_, err := New(Config{BaseURL: provider.BaseURL, Model: provider.Model, APIKey: provider.APIKey})
	return err
}

func readSettings(path string) (storedSettings, error) {
	defaults := storedSettings{Instructions: DefaultInstructions()}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaults, nil
	}
	if err != nil {
		return storedSettings{}, fmt.Errorf("inspect example generation settings: %w", err)
	}
	if !info.Mode().IsRegular() {
		return storedSettings{}, errors.New("example generation settings are not a regular file")
	}
	if info.Size() > 64<<10 {
		return storedSettings{}, errors.New("example generation settings are too large")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return storedSettings{}, fmt.Errorf("secure example generation settings: %w", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return storedSettings{}, fmt.Errorf("read example generation settings: %w", err)
	}
	var stored storedSettings
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return storedSettings{}, fmt.Errorf("parse example generation settings: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return storedSettings{}, fmt.Errorf("parse example generation settings: %w", err)
	}
	stored.Provider.BaseURL = strings.TrimSpace(stored.Provider.BaseURL)
	stored.Provider.Model = strings.TrimSpace(stored.Provider.Model)
	stored.Microsoft.Endpoint = strings.TrimSpace(stored.Microsoft.Endpoint)
	stored.Microsoft.Region = strings.TrimSpace(stored.Microsoft.Region)
	if stored.TranslationProvider == "" && stored.Provider.BaseURL != "" {
		stored.TranslationProvider = TranslationProviderOpenAI
	} else {
		stored.TranslationProvider = normalizeTranslationProvider(stored.TranslationProvider)
	}
	if err := validateProvider(stored.Provider); err != nil {
		return storedSettings{}, fmt.Errorf("validate saved example provider: %w", err)
	}
	if err := validateMicrosoftSettings(stored.Microsoft); err != nil {
		return storedSettings{}, fmt.Errorf("validate saved Microsoft Translator settings: %w", err)
	}
	// Replace only the unedited previous default; user-written prompts are preserved.
	if stored.Instructions == previousDefaultInstructions() {
		stored.Instructions = DefaultInstructions()
	}
	previousTranslation := "Write one natural English translation of the Japanese sentence. Preserve its meaning and tone. Do not include labels, notes, alternatives, romanization, or quotation marks."
	if stored.Instructions.Translation == previousTranslation {
		stored.Instructions.Translation = DefaultInstructions().Translation
	}
	stored.Instructions, err = normalizeInstructions(stored.Instructions)
	if err != nil {
		return storedSettings{}, fmt.Errorf("validate saved example instructions: %w", err)
	}
	return stored, nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("settings contain more than one JSON value")
}

func writeSettings(path string, value storedSettings) error {
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	return securefile.Write(path, contents)
}
