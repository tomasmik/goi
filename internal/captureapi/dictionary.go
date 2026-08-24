package captureapi

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/tomasmik/goi/internal/dictionary/jmdict"
	internalweb "github.com/tomasmik/goi/internal/web"
)

const (
	dictionaryQueryRuneLimit = 200
	dictionaryCandidateLimit = 5
	dictionaryMeaningLimit   = 8
)

type dictionaryCandidate struct {
	EntrySequence   int64             `json:"entry_sequence"`
	Written         string            `json:"written"`
	Reading         string            `json:"reading"`
	Commonness      int               `json:"commonness"`
	CommonnessScore int               `json:"commonness_score"`
	Meanings        []string          `json:"meanings"`
	Senses          []dictionarySense `json:"senses"`
}

type dictionarySense struct {
	PartsOfSpeech []string `json:"parts_of_speech"`
	Meanings      []string `json:"meanings"`
}

type dictionaryResponse struct {
	Query      string                `json:"query"`
	State      jmdict.MatchState     `json:"state"`
	Candidates []dictionaryCandidate `json:"candidates"`
}

func (h *Handler) lookupDictionary(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("expression"))
	if query == "" || !utf8.ValidString(query) || utf8.RuneCountInString(query) > dictionaryQueryRuneLimit {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_expression", "expression must contain 1 to 200 characters")
		return
	}
	if h.dictionary == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "dictionary_unavailable", "dictionary is unavailable")
		return
	}
	match, err := h.dictionary.Lookup(r.Context(), query, "")
	if err != nil {
		if errors.Is(err, jmdict.ErrUnavailable) {
			writeAPIError(w, http.StatusServiceUnavailable, "dictionary_unavailable", "dictionary is unavailable")
			return
		}
		internalweb.LogError(r, "could not look up extension dictionary entry", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "could not look up word")
		return
	}
	writeJSON(w, http.StatusOK, dictionaryResult(query, match))
}

func dictionaryResult(query string, match jmdict.Match) dictionaryResponse {
	result := dictionaryResponse{Query: query, State: match.State, Candidates: []dictionaryCandidate{}}
	for _, candidate := range match.Candidates {
		if len(result.Candidates) >= dictionaryCandidateLimit {
			break
		}
		meanings, senses := dictionarySenses(candidate.Senses)
		if len(meanings) == 0 {
			continue
		}
		commonness := jmdict.CommonnessScore(candidate.Priority)
		result.Candidates = append(result.Candidates, dictionaryCandidate{
			EntrySequence:   candidate.EntrySequence,
			Written:         candidate.Written,
			Reading:         candidate.Reading,
			Commonness:      (commonness + 9) / 10,
			CommonnessScore: commonness,
			Meanings:        meanings,
			Senses:          senses,
		})
	}
	if len(result.Candidates) == 0 {
		result.State = jmdict.MatchNone
	} else if len(result.Candidates) == 1 {
		result.State = jmdict.MatchReady
	} else {
		result.State = jmdict.MatchAmbiguous
	}
	return result
}

func dictionarySenses(senses []jmdict.Sense) ([]string, []dictionarySense) {
	seen := make(map[string]struct{})
	meanings := make([]string, 0, dictionaryMeaningLimit)
	details := make([]dictionarySense, 0, len(senses))
	detailCount := 0
	for _, sense := range senses {
		senseMeanings := make([]string, 0, len(sense.Glosses))
		senseSeen := make(map[string]struct{})
		for _, gloss := range sense.Glosses {
			if gloss.Language != "" && gloss.Language != "eng" {
				continue
			}
			meaning := strings.TrimSpace(gloss.Text)
			if meaning == "" {
				continue
			}
			if _, exists := senseSeen[meaning]; exists {
				continue
			}
			senseSeen[meaning] = struct{}{}
			senseMeanings = append(senseMeanings, meaning)
			detailCount++
			if _, exists := seen[meaning]; !exists {
				seen[meaning] = struct{}{}
				meanings = append(meanings, meaning)
			}
			if detailCount == dictionaryMeaningLimit {
				break
			}
		}
		if len(senseMeanings) > 0 {
			details = append(details, dictionarySense{
				PartsOfSpeech: append([]string{}, sense.PartsOfSpeech...),
				Meanings:      senseMeanings,
			})
		}
		if detailCount == dictionaryMeaningLimit {
			break
		}
	}
	return meanings, details
}
