package examplegen

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMicrosoftTranslatorTranslatesJapaneseToEnglish(t *testing.T) {
	var requestBody []struct {
		Text string `json:"Text"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/translate" || r.URL.Query().Get("api-version") != "3.0" || r.URL.Query().Get("from") != "ja" || r.URL.Query().Get("to") != "en" {
			t.Errorf("request URL = %s", r.URL.String())
		}
		if r.Header.Get("Ocp-Apim-Subscription-Key") != "test-key" || r.Header.Get("Ocp-Apim-Subscription-Region") != "westeurope" {
			t.Errorf("authentication headers = %#v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"translations":[{"text":"It is hot today.","to":"en"}]}]`)
	}))
	defer server.Close()

	translator, err := NewMicrosoftTranslator(MicrosoftTranslatorConfig{
		Endpoint:   server.URL,
		Region:     "westeurope",
		APIKey:     "test-key",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := translator.Translate(context.Background(), " 今日は暑いです。 ")
	if err != nil {
		t.Fatal(err)
	}
	if len(requestBody) != 1 || requestBody[0].Text != "今日は暑いです。" {
		t.Fatalf("request body = %#v", requestBody)
	}
	if result.Text != "It is hot today." || result.Provider != microsoftTranslatorProvider {
		t.Fatalf("translation = %#v", result)
	}
}

func TestMicrosoftTranslatorRejectsIncompleteSettings(t *testing.T) {
	tests := []MicrosoftTranslatorConfig{
		{},
		{Endpoint: "https://api.cognitive.microsofttranslator.com", Region: "westeurope"},
		{Endpoint: "http://translator.example", Region: "westeurope", APIKey: "key"},
		{Endpoint: "https://api.cognitive.microsofttranslator.com", APIKey: "key"},
	}
	for _, config := range tests {
		if _, err := NewMicrosoftTranslator(config); err == nil {
			t.Fatalf("NewMicrosoftTranslator(%#v) succeeded", config)
		}
	}
}

func TestMicrosoftTranslatorDoesNotExposeProviderErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"secret provider detail"}}`)
	}))
	defer server.Close()
	translator, err := NewMicrosoftTranslator(MicrosoftTranslatorConfig{
		Endpoint:   server.URL,
		Region:     "westeurope",
		APIKey:     "test-key",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = translator.Translate(context.Background(), "日本語")
	if err == nil || strings.Contains(err.Error(), "secret provider detail") {
		t.Fatalf("Translate() error = %v", err)
	}
}
