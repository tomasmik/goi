package mining

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/tomasmik/goi/internal/pronunciation"
	"github.com/tomasmik/goi/internal/vocabulary"
	internalweb "github.com/tomasmik/goi/internal/web"
)

type DetailPage struct {
	Title                   string
	CSRFToken               string
	Capture                 Capture
	ExistingVocabulary      *vocabulary.Item
	EditRevision            int64
	ManualAcceptRevision    int64
	Error                   string
	Notice                  string
	Expression              string
	ContextText             string
	SourceKind              string
	SourceTitle             string
	SourceURL               string
	SourcePositionSeconds   string
	SourcePositionLabel     string
	Pronunciation           string
	Meanings                string
	Notes                   string
	AllowSparse             bool
	ExampleSentence         string
	ExampleTranslation      string
	ExampleTarget           string
	Enrichment              *EnrichmentView
	HasCandidates           bool
	GenerationAvailable     bool
	TranslationAvailable    bool
	JishoURL                string
	ForvoURL                string
	PronunciationExpression string
	PronunciationReading    string
	PronunciationResults    []pronunciation.Recording
	PronunciationSearched   bool
	PronunciationError      string
	NextPendingID           int64
}

func (h *Handler) renderDetail(w http.ResponseWriter, r *http.Request, id int64, form detailForm, message, notice string, status int) {
	capture, existingVocabulary, err := h.loadDetailVocabulary(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	}
	if err != nil {
		internalweb.InternalError(w, r, "could not load capture", err)
		return
	}
	form, editRevision, manualAcceptRevision := detailFormDefaults(capture, existingVocabulary, form)
	enrichment, err := h.detailEnrichment(r.Context(), capture, form)
	if err != nil {
		internalweb.InternalError(w, r, "could not load dictionary suggestions", err)
		return
	}
	nextPendingID, err := h.nextPendingID(r.Context(), capture.ID)
	if err != nil {
		internalweb.InternalError(w, r, "could not load next mining capture", err)
		return
	}
	audioExpression, audioReading := pronunciationLookup(capture.Expression, form.pronunciation, enrichment)
	recordings, pronunciationSearched, pronunciationError := h.searchPronunciations(r, capture, audioExpression, audioReading)

	h.renderer.RenderStatus(w, status, "mining-detail.html", DetailPage{
		Title:                   capture.Expression,
		CSRFToken:               internalweb.CSRFToken(r),
		Capture:                 capture,
		ExistingVocabulary:      existingVocabulary,
		EditRevision:            editRevision,
		ManualAcceptRevision:    manualAcceptRevision,
		Error:                   message,
		Notice:                  notice,
		Expression:              form.expression,
		ContextText:             form.contextText,
		SourceKind:              string(form.sourceKind),
		SourceTitle:             form.sourceTitle,
		SourceURL:               form.sourceURL,
		SourcePositionSeconds:   form.sourcePositionSeconds,
		SourcePositionLabel:     formatTimestamp(capture.SourcePositionMS),
		Pronunciation:           form.pronunciation,
		Meanings:                form.meanings,
		Notes:                   form.notes,
		ExampleSentence:         form.exampleSentence,
		ExampleTranslation:      form.exampleTranslation,
		ExampleTarget:           form.exampleTarget,
		Enrichment:              enrichment,
		HasCandidates:           enrichment != nil && len(enrichment.Candidates) > 0,
		GenerationAvailable:     h.generator != nil && h.generator.Available(),
		TranslationAvailable:    h.translator != nil && h.translator.TranslationAvailable(),
		JishoURL:                "https://jisho.org/search/" + url.PathEscape(capture.Expression),
		ForvoURL:                "https://forvo.com/search/" + url.PathEscape(capture.Expression) + "/ja/",
		PronunciationExpression: audioExpression,
		PronunciationReading:    audioReading,
		PronunciationResults:    recordings,
		PronunciationSearched:   pronunciationSearched,
		PronunciationError:      pronunciationError,
		NextPendingID:           nextPendingID,
	})
}

func (h *Handler) loadDetailVocabulary(ctx context.Context, id int64) (Capture, *vocabulary.Item, error) {
	capture, err := h.store.Get(ctx, id)
	if err != nil || capture.ExistingVocabularyID == nil {
		return capture, nil, err
	}
	vocabularyID := *capture.ExistingVocabularyID
	item, err := h.store.vocabulary.Get(ctx, vocabularyID)
	if err != nil {
		return Capture{}, nil, fmt.Errorf("load existing vocabulary: %w", err)
	}
	return capture, &item, nil
}

func detailFormDefaults(capture Capture, existing *vocabulary.Item, form detailForm) (detailForm, int64, int64) {
	editRevision := capture.Revision
	if form.captureFieldsSubmitted {
		editRevision = form.revision
	} else {
		form.expression = capture.Expression
		form.contextText = capture.ContextText
		form.sourceKind = capture.SourceKind
		form.sourceTitle = capture.SourceTitle
		form.sourceURL = capture.SourceURL
		form.sourcePositionSeconds = formatTimestamp(capture.SourcePositionMS)
	}
	manualAcceptRevision := capture.Revision
	if form.submitted {
		manualAcceptRevision = form.revision
		return form, editRevision, manualAcceptRevision
	}
	form.exampleSentence = capture.ContextText
	form.exampleTarget = strings.TrimSpace(capture.RawText)
	if form.exampleTarget == "" {
		form.exampleTarget = capture.Expression
	}
	if existing != nil {
		form.pronunciation = existing.Pronunciation
		form.meanings = strings.Join(existing.Meanings, "\n")
	}
	return form, editRevision, manualAcceptRevision
}

func (h *Handler) detailEnrichment(ctx context.Context, capture Capture, form detailForm) (*EnrichmentView, error) {
	if capture.Status != StatusPending {
		return nil, nil
	}
	enrichment := lookupEnrichment(ctx, h.dictionary, capture)
	view := newEnrichmentView(enrichment, capture, form)
	for index := range view.Candidates {
		if err := h.addCandidateVocabularyState(ctx, &view.Candidates[index]); err != nil {
			return nil, err
		}
	}
	return &view, nil
}

func (h *Handler) addCandidateVocabularyState(ctx context.Context, candidate *CandidateView) error {
	vocabularyID, err := h.store.existingVocabularyID(ctx, candidate.Written)
	if err != nil {
		return err
	}
	candidate.ExistingVocabularyID = vocabularyID
	return nil
}

func (h *Handler) nextPendingID(ctx context.Context, captureID int64) (int64, error) {
	id, err := h.store.NextPendingID(ctx, captureID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func pronunciationLookup(expression, reading string, enrichment *EnrichmentView) (string, string) {
	expression = strings.TrimSpace(expression)
	reading = strings.TrimSpace(reading)
	if enrichment == nil {
		return expression, reading
	}
	for _, candidate := range enrichment.Candidates {
		if !candidate.Selected {
			continue
		}
		if candidate.Written != "" {
			expression = candidate.Written
		}
		if candidate.Pronunciation != "" {
			reading = candidate.Pronunciation
		}
		break
	}
	return expression, reading
}

func pronunciationExpression(captureExpression, requestedExpression string) string {
	if expression := strings.TrimSpace(requestedExpression); expression != "" {
		return expression
	}
	return strings.TrimSpace(captureExpression)
}

func (h *Handler) searchPronunciations(r *http.Request, capture Capture, expression, reading string) ([]pronunciation.Recording, bool, string) {
	searched := capture.Status == StatusPending && r.URL.Query().Get("find_audio") == "1"
	if !searched || h.recordings == nil {
		return nil, searched, ""
	}
	recordings, err := h.recordings.Search(r.Context(), expression, reading)
	if err == nil {
		return recordings, true, ""
	}
	internalweb.LogError(r, "could not search pronunciation audio", err)
	return nil, true, "The open pronunciation library could not be reached. Try again or use the external search."
}
