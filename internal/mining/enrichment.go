package mining

import (
	"context"
	"errors"

	"github.com/tomasmik/goi/internal/dictionary/jmdict"
)

type EnrichmentState string

const (
	EnrichmentReady     EnrichmentState = "ready"
	EnrichmentAmbiguous EnrichmentState = "ambiguous"
	EnrichmentNoMatch   EnrichmentState = "no_match"
	EnrichmentFailed    EnrichmentState = "failed"
)

var ErrStaleEnrichment = errors.New("dictionary suggestion is stale; reload and try again")

type DictionaryLookup interface {
	Lookup(context.Context, string, string) (jmdict.Match, error)
}

type Enrichment struct {
	State         EnrichmentState
	SourceCreated string
	SourceVersion string
	Candidates    []jmdict.Candidate
}

func lookupEnrichment(ctx context.Context, dictionary DictionaryLookup, capture Capture) Enrichment {
	result := Enrichment{}
	if dictionary == nil {
		result.State = EnrichmentFailed
		return result
	}
	match, err := dictionary.Lookup(ctx, capture.Expression, "")
	if err != nil {
		result.State = EnrichmentFailed
		return result
	}
	result.SourceCreated = match.SourceCreated
	result.SourceVersion = match.SourceVersion
	switch match.State {
	case jmdict.MatchReady:
		result.State = EnrichmentReady
	case jmdict.MatchAmbiguous:
		result.State = EnrichmentAmbiguous
	case jmdict.MatchNone:
		result.State = EnrichmentNoMatch
	default:
		result.State = EnrichmentFailed
		return result
	}
	result.Candidates = match.Candidates
	return result
}

func candidateAcceptanceAt(enrichment Enrichment, candidateID int64) (candidateAcceptance, error) {
	if candidateID <= 0 || candidateID > int64(len(enrichment.Candidates)) {
		return candidateAcceptance{}, ErrStaleEnrichment
	}
	candidate := enrichment.Candidates[candidateID-1]
	return candidateAcceptance{
		written:  candidate.Written,
		reading:  candidate.Reading,
		meanings: candidateMeanings(candidate),
	}, nil
}
