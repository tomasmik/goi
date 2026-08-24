package mining

import (
	"context"
	"errors"
	"testing"

	"github.com/tomasmik/goi/internal/dictionary/jmdict"
)

type fakeDictionaryLookup struct {
	matches    map[string]jmdict.Match
	match      jmdict.Match
	err        error
	calls      int
	expression string
	reading    string
}

func (f *fakeDictionaryLookup) Lookup(_ context.Context, expression, reading string) (jmdict.Match, error) {
	f.calls++
	f.expression = expression
	f.reading = reading
	if f.matches != nil {
		return f.matches[expression], f.err
	}
	return f.match, f.err
}

func TestLookupEnrichmentConvertsLiveDictionaryCandidates(t *testing.T) {
	lookup := &fakeDictionaryLookup{match: jmdict.Match{
		State:         jmdict.MatchReady,
		SourceCreated: "2026-07-26",
		SourceVersion: "1.10",
		Candidates: []jmdict.Candidate{{
			EntrySequence: 123,
			Written:       "開ける",
			Reading:       "あける",
			Priority:      1,
			Senses: []jmdict.Sense{{
				Number:        1,
				PartsOfSpeech: []string{"Ichidan verb"},
				Glosses:       []jmdict.Gloss{{Text: "to open", Language: "eng"}},
			}},
		}},
	}}

	result := lookupEnrichment(context.Background(), lookup, Capture{Expression: "開ける"})

	if lookup.expression != "開ける" || lookup.reading != "" {
		t.Fatalf("lookup arguments = %q, %q", lookup.expression, lookup.reading)
	}
	if result.State != EnrichmentReady || len(result.Candidates) != 1 {
		t.Fatalf("enrichment = %#v", result)
	}
	candidate := result.Candidates[0]
	if candidate.EntrySequence != 123 || candidate.Reading != "あける" || candidateMeanings(candidate)[0] != "to open" {
		t.Fatalf("candidate = %#v", candidate)
	}
}

func TestLookupEnrichmentKeepsManualEntryAvailableOnFailure(t *testing.T) {
	result := lookupEnrichment(context.Background(), &fakeDictionaryLookup{err: errors.New("offline")}, Capture{Expression: "猫"})
	if result.State != EnrichmentFailed || len(result.Candidates) != 0 {
		t.Fatalf("enrichment = %#v", result)
	}
}

func TestCandidateAcceptanceUsesCurrentResultPosition(t *testing.T) {
	enrichment := Enrichment{Candidates: []jmdict.Candidate{
		{Written: "開く", Reading: "あく", Senses: []jmdict.Sense{{Glosses: []jmdict.Gloss{{Text: "to open", Language: "eng"}}}}},
		{Written: "空く", Reading: "あく", Senses: []jmdict.Sense{{Glosses: []jmdict.Gloss{{Text: "to become empty", Language: "eng"}}}}},
	}}
	candidate, err := candidateAcceptanceAt(enrichment, 2)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.written != "空く" || candidate.reading != "あく" || len(candidate.meanings) != 1 || candidate.meanings[0] != "to become empty" {
		t.Fatalf("candidate = %#v", candidate)
	}
	if _, err := candidateAcceptanceAt(enrichment, 3); !errors.Is(err, ErrStaleEnrichment) {
		t.Fatalf("stale candidate error = %v", err)
	}
}
