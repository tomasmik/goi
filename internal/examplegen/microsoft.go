package examplegen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const microsoftTranslatorProvider = "microsoft-translator"

type MicrosoftTranslatorConfig struct {
	Endpoint   string
	Region     string
	APIKey     string
	HTTPClient *http.Client
	Timeout    time.Duration
}

type MicrosoftTranslator struct {
	endpoint   string
	region     string
	apiKey     string
	httpClient *http.Client
	timeout    time.Duration
}

func NewMicrosoftTranslator(config MicrosoftTranslatorConfig) (*MicrosoftTranslator, error) {
	endpoint, err := microsoftTranslateEndpoint(config.Endpoint)
	if err != nil {
		return nil, err
	}
	region := strings.TrimSpace(config.Region)
	if region == "" {
		return nil, errors.New("Microsoft Translator region is required")
	}
	if err := validateSingleLine(region, maxProvenanceRunes, "Microsoft Translator region"); err != nil {
		return nil, err
	}
	if config.APIKey == "" {
		return nil, errors.New("Microsoft Translator API key is required")
	}
	if len(config.APIKey) > maxAPIKeyBytes {
		return nil, errors.New("Microsoft Translator API key is too long")
	}
	if strings.ContainsAny(config.APIKey, "\r\n") {
		return nil, errors.New("Microsoft Translator API key must be a single line")
	}
	if config.Timeout < 0 {
		return nil, errors.New("timeout must not be negative")
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	baseHTTPClient := config.HTTPClient
	if baseHTTPClient == nil {
		baseHTTPClient = http.DefaultClient
	}
	httpClient := *baseHTTPClient
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &MicrosoftTranslator{
		endpoint:   endpoint,
		region:     region,
		apiKey:     config.APIKey,
		httpClient: &httpClient,
		timeout:    timeout,
	}, nil
}

func (translator *MicrosoftTranslator) Translate(ctx context.Context, text string) (Translation, error) {
	if ctx == nil {
		return Translation{}, errors.New("context is required")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Translation{}, errors.New("text is required")
	}
	if utf8.RuneCountInString(text) > maxTranslationInputRunes {
		return Translation{}, errors.New("text is too long")
	}
	if err := validateText(text, "text"); err != nil {
		return Translation{}, err
	}
	body, err := json.Marshal([]struct {
		Text string `json:"Text"`
	}{{Text: text}})
	if err != nil {
		return Translation{}, fmt.Errorf("encode Microsoft Translator request: %w", err)
	}
	requestContext, cancel := context.WithTimeout(ctx, translator.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, translator.endpoint, bytes.NewReader(body))
	if err != nil {
		return Translation{}, fmt.Errorf("create Microsoft Translator request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json; charset=UTF-8")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Ocp-Apim-Subscription-Key", translator.apiKey)
	request.Header.Set("Ocp-Apim-Subscription-Region", translator.region)
	response, err := translator.httpClient.Do(request)
	if err != nil {
		return Translation{}, fmt.Errorf("send Microsoft Translator request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Translation{}, fmt.Errorf("Microsoft Translator returned HTTP %d", response.StatusCode)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return Translation{}, fmt.Errorf("read Microsoft Translator response: %w", err)
	}
	if len(contents) > maxResponseBytes {
		return Translation{}, errors.New("Microsoft Translator response is too large")
	}
	if !utf8.Valid(contents) {
		return Translation{}, errors.New("Microsoft Translator response is not valid UTF-8")
	}
	var result []struct {
		Translations []struct {
			Text string `json:"text"`
		} `json:"translations"`
	}
	if err := json.Unmarshal(contents, &result); err != nil {
		return Translation{}, fmt.Errorf("decode Microsoft Translator response: %w", err)
	}
	if len(result) == 0 || len(result[0].Translations) == 0 {
		return Translation{}, errors.New("Microsoft Translator returned no translation")
	}
	translated := strings.TrimSpace(result[0].Translations[0].Text)
	if translated == "" {
		return Translation{}, errors.New("Microsoft Translator returned an empty translation")
	}
	if utf8.RuneCountInString(translated) > maxTranslationInputRunes {
		return Translation{}, errors.New("translation is too long")
	}
	if err := validateText(translated, "translation"); err != nil {
		return Translation{}, err
	}
	return Translation{Text: translated, Provider: microsoftTranslatorProvider}, nil
}

func microsoftTranslateEndpoint(value string) (string, error) {
	baseURL, parsed, err := normalizeBaseURL(value)
	if err != nil {
		return "", fmt.Errorf("validate Microsoft Translator endpoint: %w", err)
	}
	parsed, err = url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse Microsoft Translator endpoint: %w", err)
	}
	if !strings.HasSuffix(parsed.Path, "/translate") {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/translate"
	}
	query := parsed.Query()
	query.Set("api-version", "3.0")
	query.Set("from", "ja")
	query.Set("to", "en")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
