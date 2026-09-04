package mining

import (
	"fmt"
	"strings"

	"github.com/tomasmik/goi/internal/dictionary/jmdict"
)

type EnrichmentView struct {
	State         EnrichmentState
	Heading       string
	Message       string
	SourceCreated string
	SourceVersion string
	Candidates    []CandidateView
}

type CandidateView struct {
	ID                   int64
	Revision             int64
	Written              string
	Pronunciation        string
	Meanings             string
	MeaningList          []string
	MeaningSummary       string
	Notes                string
	AllowSparse          bool
	ExampleSentence      string
	ExampleTranslation   string
	ExampleTarget        string
	PartsOfSpeech        string
	GlobalRank           string
	NovelRank            string
	GlobalRankLabel      string
	NovelRankLabel       string
	ExistingVocabularyID int64
	Selected             bool
}

func newEnrichmentView(enrichment Enrichment, capture Capture, form detailForm) EnrichmentView {
	view := EnrichmentView{
		State:         enrichment.State,
		SourceCreated: enrichment.SourceCreated,
		SourceVersion: enrichment.SourceVersion,
	}
	switch enrichment.State {
	case EnrichmentReady:
		view.Heading = "Add to vocabulary"
		view.Message = "Check the reading and meaning."
	case EnrichmentAmbiguous:
		view.Heading = "Which entry fits this sentence?"
		view.Message = "Choose the reading and meaning that match the captured sentence."
	case EnrichmentNoMatch:
		view.Heading = "No match found"
		view.Message = "Edit the word or enter the reading and meaning below."
	case EnrichmentFailed:
		view.Heading = "Dictionary unavailable"
		view.Message = "Enter the reading and meaning below."
	default:
		view.State = EnrichmentFailed
		view.Heading = "Dictionary unavailable"
		view.Message = "Enter the reading and meaning below."
	}
	selectedCandidateID := int64(0)
	for index, candidate := range enrichment.Candidates {
		if capture.SuggestedEntrySequence != nil && candidate.EntrySequence == *capture.SuggestedEntrySequence {
			selectedCandidateID = int64(index + 1)
			break
		}
	}
	if selectedCandidateID == 0 && len(enrichment.Candidates) > 0 {
		selectedCandidateID = 1
	}
	if form.submitted && form.candidateID != 0 {
		selectedCandidateID = form.candidateID
	}
	for index, candidate := range enrichment.Candidates {
		candidateID := int64(index + 1)
		meanings := candidateMeanings(candidate)
		candidateView := CandidateView{
			ID:              candidateID,
			Revision:        capture.Revision,
			Written:         candidate.Written,
			Pronunciation:   candidate.Reading,
			Meanings:        strings.Join(meanings, "\n"),
			MeaningList:     meanings,
			MeaningSummary:  summarizeMeanings(meanings),
			PartsOfSpeech:   strings.Join(candidatePartsOfSpeech(candidate), " · "),
			ExampleSentence: capture.ContextText,
			ExampleTarget:   capture.RawText,
			Selected:        candidateID == selectedCandidateID,
		}
		candidateView.GlobalRank, candidateView.GlobalRankLabel = frequencyBadge("Global", candidate.GlobalRank)
		candidateView.NovelRank, candidateView.NovelRankLabel = frequencyBadge("Novel", candidate.NovelRank)
		if strings.TrimSpace(candidateView.ExampleTarget) == "" {
			candidateView.ExampleTarget = capture.Expression
		}
		if candidateView.Written == "" {
			candidateView.Written = capture.Expression
		}
		if form.submitted && form.candidateID == candidateID {
			candidateView.Revision = form.revision
			candidateView.Pronunciation = form.pronunciation
			candidateView.Meanings = form.meanings
			candidateView.MeaningList = splitMeanings(form.meanings)
			candidateView.MeaningSummary = summarizeMeanings(candidateView.MeaningList)
			candidateView.Notes = form.notes
			candidateView.ExampleSentence = form.exampleSentence
			candidateView.ExampleTranslation = form.exampleTranslation
			candidateView.ExampleTarget = form.exampleTarget
		}
		view.Candidates = append(view.Candidates, candidateView)
	}
	return view
}

func frequencyBadge(corpus string, rank *int) (string, string) {
	if rank == nil {
		return "—", "Jiten " + corpus + ": no rank available"
	}
	return fmt.Sprintf("%03d", *rank), fmt.Sprintf("Jiten %s rank %d; lower means more frequent", corpus, *rank)
}

func summarizeMeanings(meanings []string) string {
	const visibleMeanings = 3
	if len(meanings) <= visibleMeanings {
		return strings.Join(meanings, " · ")
	}
	return strings.Join(meanings[:visibleMeanings], " · ") + " · …"
}

func splitMeanings(value string) []string {
	var meanings []string
	for _, meaning := range strings.Split(value, "\n") {
		meanings = appendUnique(meanings, meaning)
	}
	return meanings
}

func candidateMeanings(candidate jmdict.Candidate) []string {
	var values []string
	for _, sense := range candidate.Senses {
		for _, gloss := range sense.Glosses {
			if gloss.Language == "" || gloss.Language == "eng" {
				values = appendUnique(values, gloss.Text)
			}
		}
	}
	return values
}

func candidatePartsOfSpeech(candidate jmdict.Candidate) []string {
	var values []string
	for _, sense := range candidate.Senses {
		for _, value := range sense.PartsOfSpeech {
			values = appendUnique(values, value)
		}
	}
	return values
}

func appendUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
