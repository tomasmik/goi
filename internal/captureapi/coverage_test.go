package captureapi

import (
	"context"
	"encoding/json"
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
