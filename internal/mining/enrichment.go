package mining

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/tomasmik/goi/internal/dictionary/jmdict"
	"github.com/tomasmik/goi/internal/kana"
)

const (
	writeAsWritten = "written"
	writeAsKana    = "kana"
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

// candidateExpression is the form a card is saved under: the user's choice when there
// is one, otherwise kana for words JMdict marks as usually written in kana.
func candidateExpression(candidate jmdict.Candidate, writeAs string) string {
	if !offersKana(candidate) {
		return candidate.Written
	}
	switch writeAs {
	case writeAsKana:
		return candidate.Reading
	case writeAsWritten:
		return candidate.Written
	}
	if usuallyKana(candidate) {
		return candidate.Reading
	}
	return candidate.Written
}

func offersKana(candidate jmdict.Candidate) bool {
	return candidate.Written != "" && kana.ToHiragana(candidate.Written) != kana.ToHiragana(candidate.Reading)
}

func usuallyKana(candidate jmdict.Candidate) bool {
	return len(candidate.Senses) > 0 && candidate.Senses[0].UsuallyKana
}

func candidateAcceptanceFor(enrichment Enrichment, id, writeAs string) (candidateAcceptance, error) {
	for _, candidate := range enrichment.Candidates {
		if candidateID(candidate) == id {
			return candidateAcceptance{
				written:  candidateExpression(candidate, writeAs),
				reading:  candidate.Reading,
				meanings: candidateMeanings(candidate),
			}, nil
		}
	}
	return candidateAcceptance{}, ErrStaleEnrichment
}
