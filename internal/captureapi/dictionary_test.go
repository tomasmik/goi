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

	"github.com/tomasmik/goi/internal/dictionary/jmdict"
)

type fakeDictionaryLookup struct {
	query string
	match jmdict.Match
	err   error
}

func (lookup *fakeDictionaryLookup) Lookup(_ context.Context, expression, reading string) (jmdict.Match, error) {
	lookup.query = expression
	if reading != "" {
		panic("unexpected reading")
	}
	return lookup.match, lookup.err
}

func TestDictionaryLookupReturnsUsefulEnglishDetails(t *testing.T) {
	lookup := &fakeDictionaryLookup{match: jmdict.Match{
		State: jmdict.MatchReady,
		Candidates: []jmdict.Candidate{{
			EntrySequence: 123456,
			Written:       "食べる",
			Reading:       "たべる",
			GlobalRank:    new(192),
			NovelRank:     new(210),
			Senses: []jmdict.Sense{{
				PartsOfSpeech: []string{"Ichidan verb"},
				Glosses: []jmdict.Gloss{
					{Text: "to eat", Language: "eng"},
					{Text: "essen", Language: "ger"},
				}}, {
				PartsOfSpeech: []string{"transitive verb"},
				Glosses: []jmdict.Gloss{
					{Text: "to eat", Language: "eng"},
					{Text: "to live on", Language: "eng"},
				},
			}},
		}},
	}}
	router, token := dictionaryTestRouter(t, lookup)
	request := httptest.NewRequest(http.MethodGet, "/api/extension/v1/dictionary?expression=%E9%A3%9F%E3%81%B9%E3%82%8B", nil)
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if lookup.query != "食べる" {
		t.Fatalf("query = %q", lookup.query)
	}
	want := `{"query":"食べる","state":"ready","candidates":[{"entry_sequence":123456,"written":"食べる","reading":"たべる","global_rank":192,"novel_rank":210,"meanings":["to eat","to live on"],"senses":[{"parts_of_speech":["Ichidan verb"],"meanings":["to eat"]},{"parts_of_speech":["transitive verb"],"meanings":["to eat","to live on"]}]}]}` + "\n"
	if response.Body.String() != want {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestDictionaryLookupReportsUnavailableDictionary(t *testing.T) {
	router, token := dictionaryTestRouter(t, &fakeDictionaryLookup{err: jmdict.ErrUnavailable})
	request := httptest.NewRequest(http.MethodGet, "/api/extension/v1/dictionary?expression=%E7%8C%AB", nil)
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "dictionary_unavailable") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestDictionaryMissingRanksAreNull(t *testing.T) {
	response := dictionaryResult("猫", jmdict.Match{Candidates: []jmdict.Candidate{{Written: "猫", Reading: "ねこ", Senses: []jmdict.Sense{{Glosses: []jmdict.Gloss{{Text: "cat"}}}}}}})
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"global_rank":null,"novel_rank":null`) || strings.Contains(string(data), "commonness") {
		t.Fatalf("missing rank response = %s", data)
	}
}

func TestDictionaryLookupRejectsBadInput(t *testing.T) {
	lookup := &fakeDictionaryLookup{err: errors.New("should not be called")}
	router, token := dictionaryTestRouter(t, lookup)
	request := httptest.NewRequest(http.MethodGet, "/api/extension/v1/dictionary?expression=", nil)
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func dictionaryTestRouter(t *testing.T, lookup DictionaryLookup) (http.Handler, string) {
	t.Helper()
	ctx, db := openCaptureAPITestDatabase(t)
	store := NewStore(db)
	token, err := store.Create(ctx, "Test browser")
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, &fakeCaptureCreator{}, nil, lookup, nil, nil, false).Routes(router)
	return router, token.Plaintext
}
