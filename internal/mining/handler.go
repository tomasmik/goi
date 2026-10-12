package mining

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/examplegen"
	"github.com/tomasmik/goi/internal/pronunciation"
	"github.com/tomasmik/goi/internal/vocabulary"
	internalweb "github.com/tomasmik/goi/internal/web"
)

const captureBodyLimit = 128 << 10

//go:embed bookmarklet.js
var bookmarkletScript string

type handlerValidationError string

func (err handlerValidationError) Error() string {
	return string(err)
}

type Handler struct {
	store        *Store
	dictionary   DictionaryLookup
	generator    examplegen.Generator
	translator   examplegen.Translator
	recordings   pronunciationProvider
	renderer     *internalweb.Renderer
	publicOrigin string
}

type transitionOptions struct {
	redirectToList      bool
	requireConfirmation bool
}

type CapturePage struct {
	Title                 string
	CSRFToken             string
	Error                 string
	Expression            string
	ContextText           string
	SourceKind            string
	SourceTitle           string
	SourceURL             string
	SourcePositionSeconds string
	CaptureNonce          string
	Bookmarklet           template.URL
	BookmarkletOrigin     string
	Pronunciation         string
	Meanings              string
	Notes                 string
}

func NewHandler(store *Store, renderer *internalweb.Renderer, baseURL string, generator examplegen.Generator, dictionary DictionaryLookup) *Handler {
	return newHandler(store, renderer, baseURL, generator, pronunciation.NewLibrary(nil), dictionary)
}

func newHandler(store *Store, renderer *internalweb.Renderer, baseURL string, generator examplegen.Generator, recordings pronunciationProvider, dictionary DictionaryLookup) *Handler {
	translator, _ := generator.(examplegen.Translator)
	return &Handler{store: store, dictionary: dictionary, generator: generator, translator: translator, recordings: recordings, renderer: renderer, publicOrigin: validPublicOrigin(baseURL)}
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/mining", h.list)
	r.Get("/mining/capture", h.captureForm)
	r.Post("/mining/captures", h.create)
	r.Get("/mining/captures/{id}", h.detail)
	r.Post("/mining/captures/{id}", h.update)
	r.Post("/mining/captures/{id}/accept", h.accept)
	r.Post("/mining/captures/{id}/generate", h.generate)
	r.Post("/mining/captures/{id}/translate", h.translate)
	r.Post("/mining/captures/{id}/media", h.addMedia)
	r.Post("/mining/captures/{id}/media/{mediaID}/delete", h.removeMedia)
	r.Post("/mining/captures/{id}/pronunciations/search", h.findPronunciation)
	r.Get("/mining/captures/{id}/pronunciations/{recordingID}", h.previewPronunciation)
	r.Post("/mining/captures/{id}/pronunciations/{recordingID}", h.usePronunciation)
	r.Post("/mining/captures/{id}/attach", h.attach)
	r.Post("/mining/captures/{id}/attach-candidate", h.attachCandidate)
	r.Post("/mining/captures/{id}/discard", h.discard)
	r.Post("/mining/captures/{id}/restore", h.restore)
	r.Post("/mining/captures/{id}/delete", h.delete)
	r.Post("/mining/bulk", h.bulk)
}

func (h *Handler) captureForm(w http.ResponseWriter, r *http.Request) {
	nonce, err := newCaptureNonce()
	if err != nil {
		internalweb.InternalError(w, r, "could not prepare capture form", err)
		return
	}
	h.renderer.Render(w, "mining-capture.html", CapturePage{
		Title:             "Capture a word",
		CSRFToken:         internalweb.CSRFToken(r),
		SourceKind:        string(SourceManual),
		CaptureNonce:      nonce,
		Bookmarklet:       bookmarklet(h.publicOrigin),
		BookmarkletOrigin: h.publicOrigin,
		Expression:        strings.TrimSpace(r.URL.Query().Get("expression")),
	})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	form, err := readCaptureForm(w, r)
	direct := r.FormValue("action") == "add"
	if err == nil && direct {
		if strings.TrimSpace(form.directPronunciation) == "" {
			err = handlerValidationError("reading is required when adding directly")
		} else if strings.TrimSpace(form.directMeanings) == "" {
			err = handlerValidationError("at least one meaning is required when adding directly")
		}
	}
	if err != nil {
		h.renderCaptureError(w, r, form, err)
		return
	}
	capture, _, err := h.store.Create(r.Context(), CreateInput{
		RawText:          form.expression,
		Expression:       form.expression,
		ContextText:      form.contextText,
		SourceKind:       form.sourceKind,
		SourceTitle:      form.sourceTitle,
		SourceURL:        form.sourceURL,
		SourcePositionMS: form.sourcePositionMS,
		CaptureNonce:     form.captureNonce,
	})
	if err != nil {
		h.renderCaptureError(w, r, form, err)
		return
	}
	if direct {
		vocabularyID, acceptErr := h.store.Accept(r.Context(), capture.ID, capture.Revision, vocabulary.CreateInput{
			Pronunciation: form.directPronunciation,
			Meanings:      strings.Split(form.directMeanings, "\n"),
			Notes:         form.directNotes,
		})
		if acceptErr != nil {
			h.renderDetailError(w, r, capture.ID, detailForm{
				submitted: true, revision: capture.Revision,
				pronunciation: form.directPronunciation,
				meanings:      form.directMeanings,
				notes:         form.directNotes,
			}, acceptErr)
			return
		}
		http.Redirect(w, r, "/vocabulary/"+strconv.FormatInt(vocabularyID, 10), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/mining/captures/"+strconv.FormatInt(capture.ID, 10)+"?saved=1", http.StatusSeeOther)
}

func (h *Handler) detail(w http.ResponseWriter, r *http.Request) {
	id, ok := captureID(r)
	if !ok {
		h.notFound(w)
		return
	}
	notice := ""
	if r.URL.Query().Get("saved") == "1" {
		notice = "Saved to inbox."
	} else if r.URL.Query().Get("media") == "1" {
		notice = "Media saved."
	} else if r.URL.Query().Get("pronunciation_added") == "1" {
		notice = "Pronunciation audio added to the card."
	}
	h.renderDetail(w, r, id, detailForm{}, "", notice, http.StatusOK)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := captureID(r)
	if !ok {
		h.notFound(w)
		return
	}
	form, err := readDetailForm(w, r)
	if err == nil {
		_, err = h.store.Update(r.Context(), id, form.revision, UpdateInput{
			Expression:       form.expression,
			ContextText:      form.contextText,
			SourceKind:       form.sourceKind,
			SourceTitle:      form.sourceTitle,
			SourceURL:        form.sourceURL,
			SourcePositionMS: form.sourcePositionMS,
		})
	}
	if err != nil {
		h.renderDetailError(w, r, id, form, err)
		return
	}
	http.Redirect(w, r, capturePath(id), http.StatusSeeOther)
}

func (h *Handler) accept(w http.ResponseWriter, r *http.Request) {
	id, ok := captureID(r)
	if !ok {
		h.notFound(w)
		return
	}
	form, err := readAcceptForm(w, r)
	defer removeMultipartFiles(r)
	if err == nil && form.candidateID == "" {
		if form.pronunciation == "" {
			err = handlerValidationError("pronunciation is required")
		} else if strings.TrimSpace(form.meanings) == "" {
			err = handlerValidationError("at least one meaning is required")
		}
	}
	var candidate candidateAcceptance
	if err == nil && form.candidateID != "" {
		candidate, err = h.resolveCandidate(r.Context(), id, form.revision, form.candidateID, form.writeAs)
	}
	var changes cardMediaChanges
	if err == nil {
		changes, err = readCardMedia(r)
	}
	if err == nil {
		input := vocabulary.CreateInput{
			Pronunciation:      form.pronunciation,
			Meanings:           strings.Split(form.meanings, "\n"),
			Notes:              form.notes,
			ExampleSentence:    form.exampleSentence,
			ExampleTranslation: form.exampleTranslation,
			ExampleTarget:      form.exampleTarget,
		}
		if form.candidateID != "" {
			_, err = h.store.acceptCandidateWithMedia(r.Context(), id, form.revision, candidate, input, changes)
		} else {
			_, err = h.store.acceptWithMedia(r.Context(), id, form.revision, input, changes)
		}
	}
	if err != nil {
		h.renderDetailError(w, r, id, form, err)
		return
	}
	http.Redirect(w, r, capturePath(id), http.StatusSeeOther)
}

func (h *Handler) generate(w http.ResponseWriter, r *http.Request) {
	id, ok := captureID(r)
	if !ok {
		h.notFound(w)
		return
	}
	form, err := readAcceptForm(w, r)
	defer removeMultipartFiles(r)
	if err == nil && form.pronunciation == "" {
		err = handlerValidationError("reading is required before generating an example")
	}
	if err == nil && strings.TrimSpace(form.meanings) == "" {
		err = handlerValidationError("at least one meaning is required before generating an example")
	}
	if err != nil {
		h.renderDetailError(w, r, id, form, err)
		return
	}
	if h.generator == nil || !h.generator.Available() {
		h.renderDetail(w, r, id, form, "Example generation is not configured. Set it up in Settings.", "", http.StatusUnprocessableEntity)
		return
	}
	capture, err := h.store.Get(r.Context(), id)
	if err != nil {
		h.renderDetailError(w, r, id, form, err)
		return
	}
	generated, err := h.generator.Generate(r.Context(), examplegen.Request{
		Expression:    capture.Expression,
		Pronunciation: form.pronunciation,
		Meanings:      strings.Split(form.meanings, "\n"),
		Sentence:      form.exampleSentence,
	})
	if err != nil {
		internalweb.LogError(r, "could not generate mining example", err)
		h.renderDetail(w, r, id, form, "Could not generate the example. Check the configured provider and try again.", "", http.StatusBadGateway)
		return
	}
	form.exampleSentence = generated.Sentence
	form.exampleTranslation = generated.Translation
	form.exampleTarget = generated.TargetSurface
	h.renderDetail(w, r, id, form, "", "Example fields generated. Check them before saving.", http.StatusOK)
}

func (h *Handler) translate(w http.ResponseWriter, r *http.Request) {
	id, ok := captureID(r)
	if !ok {
		h.notFound(w)
		return
	}
	form, err := readAcceptForm(w, r)
	defer removeMultipartFiles(r)
	if err == nil && strings.TrimSpace(form.exampleSentence) == "" {
		err = handlerValidationError("enter a Japanese sentence before translating it")
	}
	if err != nil {
		h.renderDetailError(w, r, id, form, err)
		return
	}
	if h.translator == nil || !h.translator.TranslationAvailable() {
		h.renderDetail(w, r, id, form, "Translation is not configured. Set it up in Settings.", "", http.StatusUnprocessableEntity)
		return
	}
	translated, err := h.translator.Translate(r.Context(), form.exampleSentence)
	if err != nil {
		internalweb.LogError(r, "could not translate mining sentence", err)
		h.renderDetail(w, r, id, form, "Could not translate the sentence. Check the configured provider and try again.", "", http.StatusBadGateway)
		return
	}
	form.exampleTranslation = translated.Text
	h.renderDetail(w, r, id, form, "", "Translation added. Check it before saving.", http.StatusOK)
}

func (h *Handler) attach(w http.ResponseWriter, r *http.Request) {
	id, ok := captureID(r)
	if !ok {
		h.notFound(w)
		return
	}
	form, err := readAcceptForm(w, r)
	defer removeMultipartFiles(r)
	if err != nil {
		h.renderDetail(w, r, id, form, publicError(err), "", http.StatusBadRequest)
		return
	}
	vocabularyID, vocabularyOK := positiveInt64(r.FormValue("vocabulary_id"))
	if !vocabularyOK {
		h.renderDetail(w, r, id, detailForm{}, "invalid attach request", "", http.StatusBadRequest)
		return
	}
	changes, err := readCardMedia(r)
	if err != nil {
		h.renderDetailError(w, r, id, form, err)
		return
	}
	example := minedExampleDetails{
		Sentence:      form.exampleSentence,
		Translation:   form.exampleTranslation,
		TargetSurface: form.exampleTarget,
	}
	if err := h.store.attachWithExample(r.Context(), id, form.revision, vocabularyID, example, changes); err != nil {
		h.renderDetailError(w, r, id, form, err)
		return
	}
	http.Redirect(w, r, capturePath(id), http.StatusSeeOther)
}

func (h *Handler) attachCandidate(w http.ResponseWriter, r *http.Request) {
	id, ok := captureID(r)
	if !ok {
		h.notFound(w)
		return
	}
	form, err := readAcceptForm(w, r)
	defer removeMultipartFiles(r)
	if err != nil {
		h.renderDetail(w, r, id, detailForm{}, publicError(err), "", http.StatusBadRequest)
		return
	}
	if form.candidateID == "" {
		h.renderDetail(w, r, id, detailForm{}, "invalid dictionary attachment", "", http.StatusBadRequest)
		return
	}
	candidate, err := h.resolveCandidate(r.Context(), id, form.revision, form.candidateID, form.writeAs)
	if err != nil {
		h.renderDetailError(w, r, id, form, err)
		return
	}
	changes, err := readCardMedia(r)
	if err != nil {
		h.renderDetailError(w, r, id, form, err)
		return
	}
	example := minedExampleDetails{
		Sentence:      form.exampleSentence,
		Translation:   form.exampleTranslation,
		TargetSurface: form.exampleTarget,
	}
	input := vocabulary.CreateInput{
		Pronunciation: form.pronunciation,
		Meanings:      strings.Split(form.meanings, "\n"),
	}
	if _, err := h.store.attachCandidateWithExample(r.Context(), id, form.revision, candidate, input, example, changes); err != nil {
		h.renderDetailError(w, r, id, form, err)
		return
	}
	http.Redirect(w, r, capturePath(id), http.StatusSeeOther)
}

func (h *Handler) resolveCandidate(ctx context.Context, captureID, revision int64, candidateID, writeAs string) (candidateAcceptance, error) {
	capture, err := h.store.Get(ctx, captureID)
	if err != nil {
		return candidateAcceptance{}, err
	}
	if capture.Status != StatusPending || capture.Revision != revision {
		return candidateAcceptance{}, ErrRevisionConflict
	}
	return candidateAcceptanceFor(lookupEnrichment(ctx, h.dictionary, capture), candidateID, writeAs)
}

func (h *Handler) discard(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, h.store.Discard, transitionOptions{})
}

func (h *Handler) restore(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, h.store.Restore, transitionOptions{})
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, h.store.Delete, transitionOptions{
		redirectToList:      true,
		requireConfirmation: true,
	})
}

func (h *Handler) transition(w http.ResponseWriter, r *http.Request, action func(context.Context, int64, int64) error, options transitionOptions) {
	id, ok := captureID(r)
	if !ok {
		h.notFound(w)
		return
	}
	if err := parseSmallForm(w, r); err != nil {
		h.renderDetail(w, r, id, detailForm{}, publicError(err), "", http.StatusBadRequest)
		return
	}
	if options.requireConfirmation && r.FormValue("confirmed") != "1" {
		h.renderDetail(w, r, id, detailForm{}, "Confirm permanent capture deletion before continuing.", "", http.StatusBadRequest)
		return
	}
	revision, ok := positiveInt64(r.FormValue("revision"))
	if !ok {
		h.renderDetail(w, r, id, detailForm{}, "The capture action form is out of date. Reload and try again.", "", http.StatusBadRequest)
		return
	}
	if err := action(r.Context(), id, revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			h.notFound(w)
			return
		}
		h.renderDetailError(w, r, id, detailForm{}, err)
		return
	}
	if options.redirectToList {
		http.Redirect(w, r, "/mining", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, capturePath(id), http.StatusSeeOther)
}

func (h *Handler) renderCaptureError(w http.ResponseWriter, r *http.Request, form captureForm, err error) {
	status := responseStatus(err)
	if status == http.StatusInternalServerError {
		internalweb.LogError(r, "could not create mining capture", err)
	}
	if errors.Is(err, ErrNonceConflict) || errors.Is(err, ErrCaptureDeleted) {
		form.captureNonce = ""
	}
	if form.captureNonce == "" {
		nonce, nonceErr := newCaptureNonce()
		if nonceErr != nil {
			internalweb.InternalError(w, r, "could not prepare capture form", nonceErr)
			return
		}
		form.captureNonce = nonce
	}
	h.renderer.RenderStatus(w, status, "mining-capture.html", CapturePage{
		Title:                 "Capture a word",
		CSRFToken:             internalweb.CSRFToken(r),
		Error:                 publicError(err),
		Expression:            form.expression,
		ContextText:           form.contextText,
		SourceKind:            string(form.sourceKind),
		SourceTitle:           form.sourceTitle,
		SourceURL:             form.sourceURL,
		SourcePositionSeconds: form.sourcePositionSeconds,
		CaptureNonce:          form.captureNonce,
		Bookmarklet:           bookmarklet(h.publicOrigin),
		BookmarkletOrigin:     h.publicOrigin,
		Pronunciation:         form.directPronunciation,
		Meanings:              form.directMeanings,
		Notes:                 form.directNotes,
	})
}

func (h *Handler) renderDetailError(w http.ResponseWriter, r *http.Request, id int64, form detailForm, err error) {
	status := responseStatus(err)
	if status == http.StatusInternalServerError {
		internalweb.LogError(r, "could not complete mining request", err)
	}
	if errors.Is(err, ErrRevisionConflict) {
		form = detailForm{}
	}
	h.renderDetail(w, r, id, form, publicError(err), "", status)
}

func (h *Handler) notFound(w http.ResponseWriter) {
	h.renderer.NotFound(w, internalweb.NotFoundPage{
		Title:       "Capture not found",
		Heading:     "Capture not found",
		Message:     "This capture may have been deleted, or the link may be out of date.",
		ReturnURL:   "/mining",
		ReturnLabel: "Back to mining",
	})
}
