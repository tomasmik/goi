package examplegen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTranslateSendsOnlyTextAndParsesStrictJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var body chatRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 2 || !strings.Contains(body.Messages[0].Content, "exactly one string field") {
			t.Fatalf("messages = %#v", body.Messages)
		}
		if !strings.Contains(body.Messages[0].Content, "Use plain, direct English.") {
			t.Fatalf("translation instructions = %q", body.Messages[0].Content)
		}
		var input struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(body.Messages[1].Content), &input); err != nil {
			t.Fatal(err)
		}
		if input.Text != "昨日、本を読みました。" {
			t.Fatalf("text = %q", input.Text)
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"translation":"I read a book yesterday."}`,
			}}},
		})
	}))
	defer server.Close()

	instructions := DefaultInstructions()
	instructions.Translation = "Use plain, direct English."
	client, err := New(Config{BaseURL: server.URL, Model: "local-model", Instructions: instructions})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Translate(context.Background(), " 昨日、本を読みました。 ")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "I read a book yesterday." || result.Model != "local-model" {
		t.Fatalf("translation = %#v", result)
	}
}

func TestGenerateSendsVocabularyAndParsesFencedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}

		var body struct {
			Model          string `json:"model"`
			ResponseFormat struct {
				Type string `json:"type"`
			} `json:"response_format"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "test-model" || body.ResponseFormat.Type != "json_object" || len(body.Messages) != 2 {
			t.Errorf("request body = %#v", body)
		}
		for _, instruction := range []string{"Keep it brief.", "Use plain English.", "Return the inflected spelling."} {
			if !strings.Contains(body.Messages[0].Content, instruction) {
				t.Errorf("system instructions do not contain %q: %s", instruction, body.Messages[0].Content)
			}
		}
		var input map[string]any
		if err := json.Unmarshal([]byte(body.Messages[1].Content), &input); err != nil {
			t.Fatal(err)
		}
		if len(input) != 3 || input["expression"] != "食べる" || input["pronunciation"] != "たべる" {
			t.Errorf("generation input = %#v", input)
		}
		meanings, ok := input["meanings"].([]any)
		if !ok || len(meanings) != 1 || meanings[0] != "to eat" {
			t.Errorf("meanings = %#v", input["meanings"])
		}

		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"content": "```json\n{\"sentence\":\"朝ご飯を食べました。\",\"translation\":\"I ate breakfast.\",\"target_surface\":\"食べました\"}\n```",
				},
			}},
		})
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL: server.URL + "/v1/",
		Model:   "test-model",
		APIKey:  "test-key",
		Instructions: Instructions{
			Sentence:      "Keep it brief.",
			Translation:   "Use plain English.",
			TargetSurface: "Return the inflected spelling.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	example, err := client.Generate(context.Background(), Request{
		Expression:    " 食べる ",
		Pronunciation: " たべる ",
		Meanings:      []string{"", " to eat "},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantProvider := strings.TrimPrefix(server.URL, "http://")
	if example.Sentence != "朝ご飯を食べました。" || example.Translation != "I ate breakfast." || example.TargetSurface != "食べました" {
		t.Fatalf("example = %#v", example)
	}
	if example.Provider != wantProvider || example.Model != "test-model" {
		t.Fatalf("provenance = provider %q, model %q", example.Provider, example.Model)
	}
}

func TestGeneratePreservesSuppliedSentence(t *testing.T) {
	const sentence = "昨日、家で本を読みました。"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var body chatRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		var input generationInput
		if err := json.Unmarshal([]byte(body.Messages[1].Content), &input); err != nil {
			t.Fatal(err)
		}
		if input.Sentence != sentence {
			t.Errorf("sentence sent to provider = %q", input.Sentence)
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": `{"sentence":"昨日、家で本を読みました。","translation":"I read a book at home yesterday.","target_surface":"読みました"}`}}},
		})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Model: "local-model"})
	if err != nil {
		t.Fatal(err)
	}
	example, err := client.Generate(context.Background(), Request{
		Expression: "読む", Meanings: []string{"to read"}, Sentence: sentence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if example.Sentence != sentence || example.TargetSurface != "読みました" {
		t.Fatalf("example = %#v", example)
	}
}

func TestGenerateAllowsMissingAPIKeyAndEmbeddedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "" {
			t.Errorf("authorization = %q", got)
		}
		_, _ = response.Write([]byte(`{"choices":[{"message":{"content":"Here is the example: {\"sentence\":\"日本語を勉強します。\",\"translation\":\"I study Japanese.\",\"target_surface\":\"勉強します\"}"}}]}`))
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Model: "local-model"})
	if err != nil {
		t.Fatal(err)
	}
	example, err := client.Generate(context.Background(), Request{Expression: "勉強する", Meanings: []string{"to study"}})
	if err != nil {
		t.Fatal(err)
	}
	if example.TargetSurface != "勉強します" {
		t.Fatalf("example = %#v", example)
	}
}

func TestGenerateRejectsInvalidExamples(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "empty", content: ""},
		{name: "not JSON", content: "an example"},
		{name: "missing translation", content: `{"sentence":"本を読みます。","target_surface":"読みます"}`},
		{name: "target absent", content: `{"sentence":"本を読みます。","translation":"I read a book.","target_surface":"読む"}`},
		{name: "extra field", content: `{"sentence":"本を読みます。","translation":"I read a book.","target_surface":"読みます","note":"extra"}`},
		{name: "second object", content: `{"sentence":"本を読みます。","translation":"I read a book.","target_surface":"読みます"} {"note":"extra"}`},
		{name: "extra sentence text", content: `{"sentence":"本を読みます。\nNote: short example","translation":"I read a book.","target_surface":"読みます"}`},
		{name: "extra translation text", content: `{"sentence":"本を読みます。","translation":"I read a book.\nLiteral: book read","target_surface":"読みます"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				_ = json.NewEncoder(response).Encode(map[string]any{
					"choices": []any{map[string]any{"message": map[string]any{"content": test.content}}},
				})
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, Model: "local-model"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Generate(context.Background(), Request{Expression: "読む", Meanings: []string{"to read"}}); err == nil {
				t.Fatal("Generate() accepted an invalid example")
			}
		})
	}
}

func TestNewRequiresHTTPSForRemoteProviders(t *testing.T) {
	for _, config := range []Config{
		{BaseURL: "http://models.example/v1", Model: "model"},
		{BaseURL: "https://models.example/v1?mode=fast", Model: "model"},
		{BaseURL: "https://models.example/v1", Model: ""},
		{BaseURL: "https://models.example/v1", Model: "model", Timeout: -time.Second},
	} {
		if _, err := New(config); err == nil {
			t.Fatalf("New() accepted %#v", config)
		}
	}
}
