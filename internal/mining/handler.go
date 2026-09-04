package mining

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/examplegen"
	"github.com/tomasmik/goi/internal/media"
	"github.com/tomasmik/goi/internal/pronunciation"
	"github.com/tomasmik/goi/internal/vocabulary"
	internalweb "github.com/tomasmik/goi/internal/web"
)

const (
	captureBodyLimit            = 128 << 10
	CaptureMediaBodyLimit int64 = 256 << 20
)

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

type pronunciationProvider interface {
	Search(context.Context, string, string) ([]pronunciation.Recording, error)
	Download(context.Context, int64, string, string) (media.Upload, error)
}

type transitionOptions struct {
	redirectToList      bool
	requireConfirmation bool
}

type ListPage struct {
	Title          string
	Status         Status
	Items          []ListCapture
	Page           int
	PageCount      int
	PreviousURL    string
	NextURL        string
	CSRFToken      string
	Search         string
	Source         string
	PendingCount   int
	AcceptedCount  int
	DiscardedCount int
	Notice         string
	Error          string
	PendingURL     string
	AcceptedURL    string
	DiscardedURL   string
}

type ListCapture struct {
	Capture
	Age              string
	PositionLabel    string
	ClearMatch       bool
	SuggestedReading string
	MeaningPreview   string
	MatchCount       int
	MatchState       EnrichmentState
}

type CandidatePreview struct {
	State   EnrichmentState
	Reading string
	Meaning string
	Count   int
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

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	status := Status(r.URL.Query().Get("status"))
	if status == "" {
		status = StatusPending
	}
	if status != StatusPending && status != StatusAccepted && status != StatusDiscarded {
		h.renderer.RenderStatus(w, http.StatusBadRequest, "not-found.html", internalweb.NotFoundPage{
			Title:       "Invalid inbox filter",
			Heading:     "Invalid inbox filter",
			Message:     "Choose pending, accepted, or discarded captures.",
			ReturnURL:   "/mining",
			ReturnLabel: "Back to mining",
		})
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	source := strings.TrimSpace(r.URL.Query().Get("source"))
	totalCount, err := h.store.ListCountFiltered(r.Context(), status, search, source)
	if err != nil {
		internalweb.InternalError(w, r, "could not load mining inbox", err)
		return
	}
	page, pageCount := miningPage(r.URL.Query().Get("page"), totalCount)
	captures, err := h.store.ListPageFiltered(r.Context(), status, search, source, maximumCapturePageSize, (page-1)*maximumCapturePageSize)
	if err != nil {
		internalweb.InternalError(w, r, "could not load mining inbox", err)
		return
	}
	candidatePreviews := map[int64]CandidatePreview{}
	if status == StatusPending {
		candidatePreviews = h.candidatePreviews(r.Context(), captures)
	}
	items := make([]ListCapture, 0, len(captures))
	for _, capture := range captures {
		preview := candidatePreviews[capture.ID]
		items = append(items, ListCapture{
			Capture:          capture,
			Age:              formatAge(capture.CreatedAt, time.Now()),
			PositionLabel:    formatTimestamp(capture.SourcePositionMS),
			ClearMatch:       preview.State == EnrichmentReady && preview.Count == 1,
			SuggestedReading: preview.Reading,
			MeaningPreview:   preview.Meaning,
			MatchCount:       preview.Count,
			MatchState:       preview.State,
		})
	}
	var previousURL, nextURL string
	if page > 1 {
		previousURL = miningFilteredListURL(status, page-1, search, source)
	}
	if page < pageCount {
		nextURL = miningFilteredListURL(status, page+1, search, source)
	}
	counts := make(map[Status]int, 3)
	for _, captureStatus := range []Status{StatusPending, StatusAccepted, StatusDiscarded} {
		counts[captureStatus], err = h.store.ListCount(r.Context(), captureStatus)
		if err != nil {
			internalweb.InternalError(w, r, "could not load mining counts", err)
			return
		}
	}
	h.renderer.Render(w, "mining.html", ListPage{
		Title:          "Mining inbox",
		Status:         status,
		Items:          items,
		Page:           page,
		PageCount:      pageCount,
		PreviousURL:    previousURL,
		NextURL:        nextURL,
		CSRFToken:      internalweb.CSRFToken(r),
		Search:         search,
		Source:         source,
		PendingCount:   counts[StatusPending],
		AcceptedCount:  counts[StatusAccepted],
		DiscardedCount: counts[StatusDiscarded],
		Notice:         bulkMiningNotice(r),
		PendingURL:     miningFilteredListURL(StatusPending, 1, search, source),
		AcceptedURL:    miningFilteredListURL(StatusAccepted, 1, search, source),
		DiscardedURL:   miningFilteredListURL(StatusDiscarded, 1, search, source),
	})
}

func (h *Handler) candidatePreviews(ctx context.Context, captures []Capture) map[int64]CandidatePreview {
	previews := make(map[int64]CandidatePreview, len(captures))
	for _, capture := range captures {
		enrichment := lookupEnrichment(ctx, h.dictionary, capture)
		preview := CandidatePreview{State: enrichment.State, Count: len(enrichment.Candidates)}
		if len(enrichment.Candidates) > 0 {
			candidate := enrichment.Candidates[0]
			for _, choice := range enrichment.Candidates {
				if capture.SuggestedEntrySequence != nil && choice.EntrySequence == *capture.SuggestedEntrySequence {
					candidate = choice
					break
				}
			}
			preview.Reading = candidate.Reading
			meanings := candidateMeanings(candidate)
			if len(meanings) > 0 {
				preview.Meaning = meanings[0]
			}
		}
		previews[capture.ID] = preview
	}
	return previews
}

func (h *Handler) bulkAccept(ctx context.Context, ids []int64) (BulkAcceptResult, error) {
	ids, err := validatedCaptureIDs(ids)
	if err != nil {
		return BulkAcceptResult{}, err
	}
	candidates := make(map[int64]resolvedCandidate, len(ids))
	for _, id := range ids {
		capture, err := h.store.Get(ctx, id)
		if err != nil {
			return BulkAcceptResult{}, err
		}
		if capture.Status != StatusPending {
			return BulkAcceptResult{}, validationError("some selected captures changed; reload and try again")
		}
		enrichment := lookupEnrichment(ctx, h.dictionary, capture)
		if enrichment.State != EnrichmentReady || len(enrichment.Candidates) != 1 {
			continue
		}
		candidate, err := candidateAcceptanceAt(enrichment, 1)
		if err != nil {
			continue
		}
		candidates[id] = resolvedCandidate{revision: capture.Revision, candidate: candidate}
	}
	return h.store.BulkAcceptCandidates(ctx, ids, candidates)
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

func (h *Handler) bulk(w http.ResponseWriter, r *http.Request) {
	if err := parseSmallForm(w, r); err != nil {
		h.renderer.RenderStatus(w, http.StatusBadRequest, "not-found.html", internalweb.NotFoundPage{Title: "Bulk action failed", Heading: "Bulk action failed", Message: publicError(err), ReturnURL: "/mining", ReturnLabel: "Back to mining"})
		return
	}
	ids := make([]int64, 0, len(r.Form["capture_id"]))
	for _, value := range r.Form["capture_id"] {
		id, ok := positiveInt64(value)
		if !ok {
			h.renderer.RenderStatus(w, http.StatusBadRequest, "not-found.html", internalweb.NotFoundPage{Title: "Bulk action failed", Heading: "Bulk action failed", Message: "The capture selection is invalid.", ReturnURL: "/mining", ReturnLabel: "Back to mining"})
			return
		}
		ids = append(ids, id)
	}
	action := r.FormValue("action")
	if action == "accept_ready" {
		result, err := h.bulkAccept(r.Context(), ids)
		if err != nil {
			h.renderer.RenderStatus(w, http.StatusConflict, "not-found.html", internalweb.NotFoundPage{Title: "Bulk action failed", Heading: "Bulk action failed", Message: publicError(err), ReturnURL: "/mining", ReturnLabel: "Back to mining"})
			return
		}
		query := url.Values{}
		query.Set("bulk_added", strconv.Itoa(result.Added))
		query.Set("bulk_attached", strconv.Itoa(result.Attached))
		query.Set("bulk_review", strconv.Itoa(result.NeedsReview))
		http.Redirect(w, r, "/mining?"+query.Encode(), http.StatusSeeOther)
		return
	}
	var from, to Status
	switch action {
	case "discard":
		from, to = StatusPending, StatusDiscarded
	case "restore":
		from, to = StatusDiscarded, StatusPending
	default:
		h.renderer.RenderStatus(w, http.StatusBadRequest, "not-found.html", internalweb.NotFoundPage{Title: "Bulk action failed", Heading: "Bulk action failed", Message: "That bulk action is not available.", ReturnURL: "/mining", ReturnLabel: "Back to mining"})
		return
	}
	if err := h.store.BulkTransition(r.Context(), ids, from, to); err != nil {
		h.renderer.RenderStatus(w, http.StatusConflict, "not-found.html", internalweb.NotFoundPage{Title: "Bulk action failed", Heading: "Bulk action failed", Message: publicError(err), ReturnURL: "/mining", ReturnLabel: "Back to mining"})
		return
	}
	http.Redirect(w, r, miningListURL(to, 1), http.StatusSeeOther)
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
	if err == nil && form.candidateID == 0 {
		if form.pronunciation == "" {
			err = handlerValidationError("pronunciation is required")
		} else if strings.TrimSpace(form.meanings) == "" {
			err = handlerValidationError("at least one meaning is required")
		}
	}
	var candidate candidateAcceptance
	if err == nil && form.candidateID > 0 {
		candidate, err = h.resolveCandidate(r.Context(), id, form.revision, form.candidateID)
	}
	if err == nil {
		err = h.saveCardMedia(r, id, form.revision)
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
		if form.candidateID > 0 {
			_, err = h.store.AcceptCandidate(r.Context(), id, form.revision, candidate, input)
		} else {
			_, err = h.store.Accept(r.Context(), id, form.revision, input)
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

func (h *Handler) addMedia(w http.ResponseWriter, r *http.Request) {
	id, ok := captureID(r)
	if !ok {
		h.notFound(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, CaptureMediaBodyLimit)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		h.renderDetail(w, r, id, detailForm{}, "The media upload is too large or invalid.", "", http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	revision, ok := positiveInt64(r.FormValue("revision"))
	if !ok {
		h.renderDetail(w, r, id, detailForm{}, "The media form is out of date. Reload and try again.", "", http.StatusBadRequest)
		return
	}
	audio, frame, pronunciationAudio, err := readCaptureMedia(r.MultipartForm)
	if err == nil {
		err = h.store.AddMedia(r.Context(), id, revision, audio, frame, pronunciationAudio)
	}
	if err != nil {
		h.renderDetailError(w, r, id, detailForm{}, err)
		return
	}
	http.Redirect(w, r, capturePath(id)+"?media=1", http.StatusSeeOther)
}

func (h *Handler) previewPronunciation(w http.ResponseWriter, r *http.Request) {
	id, ok := captureID(r)
	if !ok {
		h.notFound(w)
		return
	}
	recordingID, ok := positiveInt64(chi.URLParam(r, "recordingID"))
	if !ok || h.recordings == nil {
		h.notFound(w)
		return
	}
	capture, err := h.store.Get(r.Context(), id)
	if err != nil {
		h.notFound(w)
		return
	}
	upload, err := h.recordings.Download(r.Context(), recordingID, pronunciationExpression(capture.Expression, r.URL.Query().Get("expression")), r.URL.Query().Get("reading"))
	if err != nil {
		internalweb.LogError(r, "could not preview pronunciation audio", err)
		http.Error(w, "Pronunciation audio is unavailable.", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", upload.MimeType)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(upload.Content)
}

func (h *Handler) findPronunciation(w http.ResponseWriter, r *http.Request) {
	id, ok := captureID(r)
	if !ok || h.recordings == nil {
		h.notFound(w)
		return
	}
	form, err := readAcceptForm(w, r)
	if err != nil {
		h.renderDetail(w, r, id, form, publicError(err), "", http.StatusBadRequest)
		return
	}
	capture, err := h.store.Get(r.Context(), id)
	if err != nil {
		h.renderDetailError(w, r, id, form, err)
		return
	}
	expression := pronunciationExpression(capture.Expression, r.FormValue("expression"))
	reading := strings.TrimSpace(r.FormValue("reading"))
	recordings, err := h.recordings.Search(r.Context(), expression, reading)
	if err != nil {
		internalweb.LogError(r, "could not search pronunciation audio", err)
		// Optional audio failures must leave the editable page available through proxies.
		h.renderDetail(w, r, id, form, "The open pronunciation library is temporarily unavailable. Try again later or use the external search.", "", http.StatusOK)
		return
	}
	if len(recordings) != 1 {
		page, err := h.detailPage(r, id, form)
		if err != nil {
			h.renderDetailError(w, r, id, form, err)
			return
		}
		page.PronunciationSearched = true
		page.PronunciationResults = recordings
		page.PronunciationExpression = expression
		page.PronunciationReading = reading
		h.renderer.Render(w, "mining-detail.html", page)
		return
	}
	upload, err := h.recordings.Download(r.Context(), recordings[0].ID, expression, reading)
	if err != nil {
		internalweb.LogError(r, "could not download pronunciation audio", err)
		h.renderDetail(w, r, id, form, "Could not download that recording. Try again later or upload an audio file.", "", http.StatusOK)
		return
	}
	if err := h.store.SetPronunciationAudio(r.Context(), id, form.revision, upload); err != nil {
		h.renderDetailError(w, r, id, form, err)
		return
	}
	h.renderDetail(w, r, id, form, "", "Pronunciation audio added to the card.", http.StatusOK)
}

func (h *Handler) usePronunciation(w http.ResponseWriter, r *http.Request) {
	id, ok := captureID(r)
	if !ok {
		h.notFound(w)
		return
	}
	form, err := readAcceptForm(w, r)
	if err != nil {
		h.renderDetail(w, r, id, form, publicError(err), "", http.StatusBadRequest)
		return
	}
	recordingID, ok := positiveInt64(chi.URLParam(r, "recordingID"))
	if !ok || h.recordings == nil {
		h.renderDetail(w, r, id, form, "The pronunciation choice is invalid. Search again.", "", http.StatusBadRequest)
		return
	}
	capture, err := h.store.Get(r.Context(), id)
	if err != nil {
		h.renderDetailError(w, r, id, form, err)
		return
	}
	upload, err := h.recordings.Download(r.Context(), recordingID, pronunciationExpression(capture.Expression, r.FormValue("expression")), r.FormValue("reading"))
	if err != nil {
		internalweb.LogError(r, "could not download pronunciation audio", err)
		h.renderDetail(w, r, id, form, "Could not download that recording. Try again later or upload an audio file.", "", http.StatusOK)
		return
	}
	if err := h.store.SetPronunciationAudio(r.Context(), id, form.revision, upload); err != nil {
		h.renderDetailError(w, r, id, form, err)
		return
	}
	h.renderDetail(w, r, id, form, "", "Pronunciation audio added to the card.", http.StatusOK)
}

func readCaptureMedia(form *multipart.Form) ([]media.Upload, *media.Upload, *media.Upload, error) {
	audio := make([]media.Upload, 0, len(form.File["sentence_audio"]))
	for _, header := range form.File["sentence_audio"] {
		upload, err := readMiningUpload(header, media.KindAudio)
		if err != nil {
			return nil, nil, nil, err
		}
		audio = append(audio, upload)
	}
	frames := form.File["video_frame"]
	if len(frames) > 1 {
		return nil, nil, nil, handlerValidationError("choose one image")
	}
	var frame *media.Upload
	if len(frames) == 1 {
		upload, err := readMiningUpload(frames[0], media.KindImage)
		if err != nil {
			return nil, nil, nil, err
		}
		frame = &upload
	}
	pronunciations := form.File["pronunciation_audio"]
	if len(pronunciations) > 1 {
		return nil, nil, nil, handlerValidationError("choose one pronunciation audio file")
	}
	var pronunciationAudio *media.Upload
	if len(pronunciations) == 1 {
		upload, err := readMiningUpload(pronunciations[0], media.KindAudio)
		if err != nil {
			return nil, nil, nil, err
		}
		pronunciationAudio = &upload
	}
	return audio, frame, pronunciationAudio, nil
}

func (h *Handler) saveCardMedia(r *http.Request, captureID, revision int64) error {
	if r.MultipartForm == nil {
		return nil
	}
	audio, frame, pronunciationAudio, err := readCaptureMedia(r.MultipartForm)
	if err != nil {
		return err
	}
	removeIDs := make([]int64, 0, len(r.Form["remove_media_id"]))
	for _, value := range r.Form["remove_media_id"] {
		mediaID, ok := positiveInt64(value)
		if !ok {
			return handlerValidationError("invalid media removal")
		}
		removeIDs = append(removeIDs, mediaID)
	}
	for _, mediaID := range removeIDs {
		if err := h.store.RemoveMedia(r.Context(), captureID, revision, mediaID); err != nil {
			return err
		}
	}
	if len(audio) == 0 && frame == nil && pronunciationAudio == nil {
		return nil
	}
	return h.store.AddMedia(r.Context(), captureID, revision, audio, frame, pronunciationAudio)
}

func readMiningUpload(header *multipart.FileHeader, kind media.Kind) (media.Upload, error) {
	file, err := header.Open()
	if err != nil {
		return media.Upload{}, fmt.Errorf("open %s upload: %w", kind, err)
	}
	defer file.Close()
	return media.ReadUpload(file, header, kind)
}

func (h *Handler) removeMedia(w http.ResponseWriter, r *http.Request) {
	id, ok := captureID(r)
	if !ok {
		h.notFound(w)
		return
	}
	mediaID, ok := positiveInt64(chi.URLParam(r, "mediaID"))
	if !ok {
		h.notFound(w)
		return
	}
	if err := parseSmallForm(w, r); err != nil {
		h.renderDetailError(w, r, id, detailForm{}, err)
		return
	}
	revision, ok := positiveInt64(r.FormValue("revision"))
	if !ok {
		h.renderDetail(w, r, id, detailForm{}, "The media form is out of date. Reload and try again.", "", http.StatusBadRequest)
		return
	}
	if err := h.store.RemoveMedia(r.Context(), id, revision, mediaID); err != nil {
		h.renderDetailError(w, r, id, detailForm{}, err)
		return
	}
	http.Redirect(w, r, capturePath(id)+"?media=1", http.StatusSeeOther)
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
	if err := h.saveCardMedia(r, id, form.revision); err != nil {
		h.renderDetailError(w, r, id, form, err)
		return
	}
	example := minedExampleDetails{
		Sentence:      form.exampleSentence,
		Translation:   form.exampleTranslation,
		TargetSurface: form.exampleTarget,
	}
	if err := h.store.attachWithExample(r.Context(), id, form.revision, vocabularyID, example); err != nil {
		h.renderDetailError(w, r, id, detailForm{}, err)
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
	if form.candidateID == 0 {
		h.renderDetail(w, r, id, detailForm{}, "invalid dictionary attachment", "", http.StatusBadRequest)
		return
	}
	candidate, err := h.resolveCandidate(r.Context(), id, form.revision, form.candidateID)
	if err != nil {
		h.renderDetailError(w, r, id, form, err)
		return
	}
	if err := h.saveCardMedia(r, id, form.revision); err != nil {
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
	if _, err := h.store.attachCandidateWithExample(r.Context(), id, form.revision, candidate, input, example); err != nil {
		h.renderDetailError(w, r, id, detailForm{}, err)
		return
	}
	http.Redirect(w, r, capturePath(id), http.StatusSeeOther)
}

func (h *Handler) resolveCandidate(ctx context.Context, captureID, revision, candidateID int64) (candidateAcceptance, error) {
	capture, err := h.store.Get(ctx, captureID)
	if err != nil {
		return candidateAcceptance{}, err
	}
	if capture.Status != StatusPending || capture.Revision != revision {
		return candidateAcceptance{}, ErrRevisionConflict
	}
	return candidateAcceptanceAt(lookupEnrichment(ctx, h.dictionary, capture), candidateID)
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
