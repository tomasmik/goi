package captureapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/coverage"
)

type fakeCoverageAnalyzer struct {
	blocks []coverage.Block
	result coverage.Result
	err    error
}

func (analyzer *fakeCoverageAnalyzer) Analyze(_ context.Context, blocks []coverage.Block) (coverage.Result, error) {
	analyzer.blocks = blocks
	return analyzer.result, analyzer.err
}

func TestCoverageAnalyzesAuthenticatedTextBlocks(t *testing.T) {
	ctx, db := openCaptureAPITestDatabase(t)
	tokens := NewStore(db)
	created, err := tokens.Create(ctx, "Coverage browser")
	if err != nil {
		t.Fatal(err)
	}
	analyzer := &fakeCoverageAnalyzer{result: coverage.Result{
		Summary: coverage.Summary{KnownOccurrences: 1, TotalOccurrences: 2, UnknownUnique: 1},
		Blocks: []coverage.BlockResult{{
			ID: 7,
			Tokens: []coverage.Token{{
				Surface: "食べました", Expression: "食べる", Reading: "たべました",
				StartUTF16: 3, EndUTF16: 8, Status: coverage.StatusKnown,
			}},
		}},
	}}
	router := chi.NewRouter()
	NewHandler(tokens, &fakeCaptureCreator{}, analyzer, nil, nil, nil, false).Routes(router)

	request := httptest.NewRequest(http.MethodPost, "/api/extension/v1/coverage", strings.NewReader(`{
		"blocks":[{"id":7,"text":"寿司を食べました。"}]
	}`))
	request.RemoteAddr = "127.0.0.1:4321"
	request.Header.Set("Authorization", "Bearer "+created.Plaintext)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if len(analyzer.blocks) != 1 || analyzer.blocks[0].ID != 7 || analyzer.blocks[0].Text != "寿司を食べました。" {
		t.Fatalf("analyzer blocks = %#v", analyzer.blocks)
	}
	var result coverage.Result
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Summary.TotalOccurrences != 2 || len(result.Blocks) != 1 ||
		result.Blocks[0].Tokens[0].Expression != "食べる" || result.Blocks[0].Tokens[0].Reading != "たべました" {
		t.Fatalf("coverage response = %#v", result)
	}
}

func TestCoverageBodyLimitAccommodatesTheDocumentedTextLimit(t *testing.T) {
	ctx, db := openCaptureAPITestDatabase(t)
	tokens := NewStore(db)
	created, err := tokens.Create(ctx, "Large coverage browser")
	if err != nil {
		t.Fatal(err)
	}
	analyzer := &fakeCoverageAnalyzer{}
	router := chi.NewRouter()
	NewHandler(tokens, &fakeCaptureCreator{}, analyzer, nil, nil, nil, false).Routes(router)

	blocks := make([]coverage.Block, 10)
	for index := range blocks {
		blocks[index] = coverage.Block{ID: index + 1, Text: strings.Repeat("語", coverageBlockRuneLimit)}
	}
	body, err := json.Marshal(coverageRequest{Blocks: blocks})
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(body)) <= 512<<10 || int64(len(body)) >= CoverageBodyLimit {
		t.Fatalf("coverage request size = %d, limit = %d", len(body), CoverageBodyLimit)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/extension/v1/coverage", strings.NewReader(string(body)))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer "+created.Plaintext)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(analyzer.blocks) != len(blocks) {
		t.Fatalf("large coverage response = %d, blocks = %d, body = %s", response.Code, len(analyzer.blocks), response.Body.String())
	}
}

func TestCoverageValidatesAuthenticationAndInput(t *testing.T) {
	ctx, db := openCaptureAPITestDatabase(t)
	tokens := NewStore(db)
	created, err := tokens.Create(ctx, "Coverage browser")
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(tokens, &fakeCaptureCreator{}, &fakeCoverageAnalyzer{}, nil, nil, nil, false).Routes(router)

	tests := []struct {
		name        string
		body        string
		contentType string
		token       string
		want        int
	}{
		{name: "missing token", body: `{"blocks":[{"id":1,"text":"読む"}]}`, contentType: "application/json", want: http.StatusUnauthorized},
		{name: "wrong content type", body: `{}`, contentType: "text/plain", token: created.Plaintext, want: http.StatusUnsupportedMediaType},
		{name: "unknown field", body: `{"blocks":[{"id":1,"text":"読む"}],"url":"https://example.com"}`, contentType: "application/json", token: created.Plaintext, want: http.StatusBadRequest},
		{name: "duplicate IDs", body: `{"blocks":[{"id":1,"text":"読む"},{"id":1,"text":"見る"}]}`, contentType: "application/json", token: created.Plaintext, want: http.StatusUnprocessableEntity},
		{name: "invalid ID", body: `{"blocks":[{"id":0,"text":"読む"}]}`, contentType: "application/json", token: created.Plaintext, want: http.StatusUnprocessableEntity},
		{name: "no blocks", body: `{"blocks":[]}`, contentType: "application/json", token: created.Plaintext, want: http.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/extension/v1/coverage", strings.NewReader(test.body))
			request.TLS = &tls.ConnectionState{}
			request.Header.Set("Content-Type", test.contentType)
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d, body = %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestCoverageHidesAnalyzerFailures(t *testing.T) {
	ctx, db := openCaptureAPITestDatabase(t)
	tokens := NewStore(db)
	created, err := tokens.Create(ctx, "Coverage browser")
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(tokens, &fakeCaptureCreator{}, &fakeCoverageAnalyzer{err: errors.New("private database failure")}, nil, nil, nil, false).Routes(router)
	request := httptest.NewRequest(http.MethodPost, "/api/extension/v1/coverage", strings.NewReader(`{"blocks":[{"id":1,"text":"読む"}]}`))
	request.RemoteAddr = "127.0.0.1:4321"
	request.Header.Set("Authorization", "Bearer "+created.Plaintext)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "private database failure") {
		t.Fatalf("response = %d, body = %s", response.Code, response.Body.String())
	}
}
