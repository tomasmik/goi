package mining

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

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

// Identity excludes meanings and frequency: dictionary updates may change defaults,
// but must never change which written/reading pair a submitted form selects.
func candidateID(candidate jmdict.Candidate) string {
	identity := fmt.Sprintf("%d:%q:%q", candidate.EntrySequence, candidate.Written, candidate.Reading)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
}

func candidateAcceptanceFor(enrichment Enrichment, id string) (candidateAcceptance, error) {
	for _, candidate := range enrichment.Candidates {
		if candidateID(candidate) == id {
			return candidateAcceptance{
				written:  candidate.Written,
				reading:  candidate.Reading,
				meanings: candidateMeanings(candidate),
			}, nil
		}
	}
	return candidateAcceptance{}, ErrStaleEnrichment
}
