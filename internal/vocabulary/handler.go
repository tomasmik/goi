package vocabulary

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/examplegen"
	"github.com/tomasmik/goi/internal/examples"
	"github.com/tomasmik/goi/internal/media"
	"github.com/tomasmik/goi/internal/pronunciation"
	internalweb "github.com/tomasmik/goi/internal/web"
)

type Handler struct {
	store      *Store
	examples   *examples.Store
	generator  examplegen.Generator
	recordings pronunciationProvider
	renderer   *internalweb.Renderer
}

type pronunciationProvider interface {
	Search(context.Context, string, string) ([]pronunciation.Recording, error)
	Download(context.Context, int64, string, string) (media.Upload, error)
}

const (
	KnownWordsBodyLimit    int64 = 2 << 20
	ExampleFormBodyLimit   int64 = 64 << 10
	vocabularyListPageSize       = 40
)

type ListPage struct {
	Title               string
	CSRFToken           string
	Search              string
	Filter              ListFilter
	Sort                ListSort
	Filters             []ListFilterLink
	Items               []ListItem
	TotalCount          int
	KnownCount          int
	LearningCount       int
	KnownElsewhereCount int
	KnownWords          string
	Page                int
	PageCount           int
	PreviousURL         string
	NextURL             string
	Notice              string
	Error               string
}

type ListFilterLink struct {
	Label  string
	URL    string
	Active bool
	Count  int
}

type EditPage struct {
	CSRFToken             string
	Title                 string
	Item                  Item
	ID                    int64
	ContentRevision       int64
	AllowSparse           bool
	Expression            string
	Pronunciation         string
	Meanings              string
	Notes                 string
	SourceLabel           string
	AudioID               int64
	PictureID             int64
	RemoveAudio           bool
	RemovePicture         bool
	ExampleForm           ExampleForm
	GeneratorAvailable    bool
	JishoURL              string
	ForvoURL              string
	PronunciationResults  []pronunciation.Recording
	PronunciationSearched bool
	PronunciationError    string
	Notice                string
	Error                 string
}

type DetailPage struct {
	Title               string
	CSRFToken           string
	Item                Item
	ExampleForm         ExampleForm
	GenerationAvailable bool
	Now                 time.Time
	Notice              string
	Error               string
}

type ExampleForm struct {
	ID            int64
	Sentence      string
	Translation   string
	TargetSurface string
}

type formError string

func (err formError) Error() string {
	return string(err)
}

func (err formError) UserMessage() string {
	return string(err)
}

func NewHandler(store *Store, renderer *internalweb.Renderer, generator examplegen.Generator) *Handler {
	return newHandler(store, renderer, generator, pronunciation.NewLibrary(nil))
}

func newHandler(store *Store, renderer *internalweb.Renderer, generator examplegen.Generator, recordings pronunciationProvider) *Handler {
	return &Handler{
		store:      store,
		examples:   store.examples,
		generator:  generator,
		recordings: recordings,
		renderer:   renderer,
	}
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/vocabulary", h.list)
	r.Get("/vocabulary/new", h.redirectToCapture)
	r.Get("/vocabulary/export", h.exportKnown)
	r.Post("/vocabulary/known", h.addKnown)
	r.Post("/vocabulary/known/preview", h.previewKnown)
	r.Get("/vocabulary/{id}/edit", h.edit)
	r.Post("/vocabulary/{id}", h.update)
	r.Post("/vocabulary/{id}/pronunciations/search", h.searchPronunciations)
	r.Get("/vocabulary/{id}/pronunciations/{recordingID}", h.previewPronunciation)
	r.Post("/vocabulary/{id}/pronunciations/{recordingID}", h.usePronunciation)
	r.Post("/vocabulary/{id}/action", h.action)
	r.Post("/vocabulary/{id}/examples", h.createExample)
	r.Post("/vocabulary/{id}/examples/generate", h.generateExample)
	r.Post("/vocabulary/{id}/examples/generate-draft", h.generateExampleDraft)
	r.Post("/vocabulary/{id}/examples/{exampleID}/generate", h.generateExampleFields)
	r.Post("/vocabulary/{id}/examples/{exampleID}", h.updateExample)
	r.Post("/vocabulary/{id}/examples/{exampleID}/delete", h.deleteExample)
	r.Get("/vocabulary/{id}", h.detail)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	h.renderList(w, r, http.StatusOK, "", "")
}

func (h *Handler) exportKnown(w http.ResponseWriter, r *http.Request) {
	expressions, err := h.store.KnownExpressions(r.Context())
	if err != nil {
		internalweb.InternalError(w, r, "could not export known vocabulary", err)
		return
	}

	var contentType, filename string
	var body []byte
	switch r.URL.Query().Get("format") {
	case "json":
		body, err = json.Marshal(expressions)
		contentType = "application/json; charset=utf-8"
		filename = "goi-known-words.json"
	case "comma":
		body = []byte(strings.Join(expressions, ", "))
		contentType = "text/plain; charset=utf-8"
		filename = "goi-known-words.txt"
	default:
		http.Error(w, "Choose JSON or comma-separated format.", http.StatusBadRequest)
		return
	}
	if err != nil {
		internalweb.InternalError(w, r, "could not encode known vocabulary", err)
		return
	}
	body = append(body, '\n')
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (h *Handler) addKnown(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, KnownWordsBodyLimit)
	if err := r.ParseForm(); err != nil {
		h.renderList(w, r, http.StatusBadRequest, "", "The list is too large or invalid.")
		return
	}
	words := r.FormValue("known_words")
	result, err := h.store.AddKnown(r.Context(), words)
	if err != nil {
		if message, ok := internalweb.UserErrorMessage(err); ok {
			h.renderList(w, r, http.StatusUnprocessableEntity, words, message)
			return
		}
		internalweb.InternalError(w, r, "could not add known vocabulary", err)
		return
	}
	location := fmt.Sprintf(
		"/vocabulary?known_added=%d&known_existing=%d&known_reserved=%d",
		result.Added(), result.AlreadyKnown, result.SkippedActiveLesson,
	)
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (h *Handler) previewKnown(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, KnownWordsBodyLimit)
	if err := r.ParseForm(); err != nil {
		h.writeKnownPreviewError(w, http.StatusBadRequest, "The list is too large or invalid.")
		return
	}
	result, err := h.store.PreviewKnown(r.Context(), r.FormValue("known_words"))
	if err != nil {
		if message, ok := internalweb.UserErrorMessage(err); ok {
			h.writeKnownPreviewError(w, http.StatusUnprocessableEntity, message)
			return
		}
		internalweb.InternalError(w, r, "could not preview known vocabulary", err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(struct {
		Created             int `json:"created"`
		MarkedKnown         int `json:"markedKnown"`
		AlreadyKnown        int `json:"alreadyKnown"`
		SkippedActiveLesson int `json:"skippedActiveLesson"`
	}{result.Created, result.MarkedKnown, result.AlreadyKnown, result.SkippedActiveLesson})
}

func (h *Handler) writeKnownPreviewError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{message})
}

func (h *Handler) renderList(w http.ResponseWriter, r *http.Request, status int, knownWords, message string) {
	search := r.URL.Query().Get("q")
	filter := normalizeListFilter(r.URL.Query().Get("status"))
	sort := normalizeListSort(r.URL.Query().Get("sort"))
	totalCount, err := h.store.ListCountFiltered(r.Context(), search, filter)
	if err != nil {
		internalweb.InternalError(w, r, "could not load vocabulary", err)
		return
	}
	page, pageCount := vocabularyPage(r.URL.Query().Get("page"), totalCount)
	items, err := h.store.ListPageSorted(r.Context(), search, filter, sort, vocabularyListPageSize, (page-1)*vocabularyListPageSize)
	if err != nil {
		internalweb.InternalError(w, r, "could not load vocabulary", err)
		return
	}
	knownCount, err := h.store.KnownCount(r.Context())
	if err != nil {
		internalweb.InternalError(w, r, "could not load known vocabulary count", err)
		return
	}
	learningCount, err := h.store.StatusCount(r.Context(), ListFilterLearning)
	if err != nil {
		internalweb.InternalError(w, r, "could not load learning vocabulary count", err)
		return
	}
	knownElsewhereCount, err := h.store.StatusCount(r.Context(), ListFilterKnown)
	if err != nil {
		internalweb.InternalError(w, r, "could not load known-elsewhere count", err)
		return
	}
	var previousURL, nextURL string
	if page > 1 {
		previousURL = vocabularyListURLSorted(search, filter, sort, page-1)
	}
	if page < pageCount {
		nextURL = vocabularyListURLSorted(search, filter, sort, page+1)
	}
	h.renderer.RenderStatus(w, status, "vocabulary.html", ListPage{
		Title:               "Vocabulary",
		CSRFToken:           internalweb.CSRFToken(r),
		Search:              search,
		Filter:              filter,
		Sort:                sort,
		Filters:             h.vocabularyFilterLinks(r.Context(), search, filter, sort),
		Items:               items,
		TotalCount:          totalCount,
		KnownCount:          knownCount,
		LearningCount:       learningCount,
		KnownElsewhereCount: knownElsewhereCount,
		KnownWords:          knownWords,
		Page:                page,
		PageCount:           pageCount,
		PreviousURL:         previousURL,
		NextURL:             nextURL,
		Notice:              knownVocabularyNotice(r),
		Error:               message,
	})
}

func (h *Handler) redirectToCapture(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/mining/capture", http.StatusSeeOther)
}

func (h *Handler) edit(w http.ResponseWriter, r *http.Request) {
	id, ok := vocabularyID(r)
	if !ok {
		h.notFound(w)
		return
	}
	item, err := h.store.Get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	}
	if err != nil {
		internalweb.InternalError(w, r, "could not load vocabulary", err)
		return
	}
	h.renderer.Render(w, "vocabulary-new.html", h.editPage(r, item, "", ""))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := vocabularyID(r)
	if !ok {
		h.notFound(w)
		return
	}
	input, err := inputFromRequest(w, r)
	if err != nil {
		h.renderEditFormError(w, r, err, id, 0, "could not read vocabulary form")
		return
	}
	revision, err := parseContentRevision(r.FormValue("content_revision"))
	if err != nil {
		h.renderEditFormError(w, r, err, id, 0, "could not read vocabulary form")
		return
	}
	if err := h.store.Update(r.Context(), id, revision, input); errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	} else if err != nil {
		h.renderEditFormError(w, r, err, id, revision, "could not update vocabulary")
		return
	}
	if returnToEdit(r) {
		http.Redirect(w, r, fmt.Sprintf("/vocabulary/%d/edit?saved=1", id), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/vocabulary/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (h *Handler) previewPronunciation(w http.ResponseWriter, r *http.Request) {
	id, ok := vocabularyID(r)
	recordingID, recordingOK := pronunciationRecordingID(r)
	if !ok || !recordingOK || h.recordings == nil {
		h.notFound(w)
		return
	}
	item, err := h.store.Get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	}
	if err != nil {
		internalweb.InternalError(w, r, "could not load vocabulary", err)
		return
	}
	upload, err := h.recordings.Download(r.Context(), recordingID, item.Expression, item.Pronunciation)
	if err != nil {
		internalweb.LogError(r, "could not preview pronunciation audio", err)
		http.Error(w, "Pronunciation audio is unavailable.", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", upload.MimeType)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(upload.Content)
}

func (h *Handler) searchPronunciations(w http.ResponseWriter, r *http.Request) {
	id, ok := vocabularyID(r)
	if !ok || h.recordings == nil {
		h.notFound(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, ExampleFormBodyLimit)
	if err := r.ParseForm(); err != nil {
		h.renderPronunciationError(w, r, id, http.StatusBadRequest, "The pronunciation form is too large or invalid.")
		return
	}
	item, err := h.store.Get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	}
	if err != nil {
		internalweb.InternalError(w, r, "could not load vocabulary", err)
		return
	}
	expression := strings.TrimSpace(r.FormValue("expression"))
	if expression == "" {
		expression = item.Expression
	}
	reading := strings.TrimSpace(r.FormValue("pronunciation"))
	recordings, searchErr := h.recordings.Search(r.Context(), expression, reading)
	if searchErr == nil && len(recordings) == 1 {
		revision, err := parseContentRevision(r.FormValue("content_revision"))
		if err != nil {
			h.renderPronunciationError(w, r, id, http.StatusConflict, err.Error())
			return
		}
		err = h.setPronunciationAudio(r.Context(), id, recordings[0].ID, revision, expression, reading)
		if errors.Is(err, ErrRevisionConflict) {
			h.renderPronunciationError(w, r, id, http.StatusConflict, "This card changed in another tab. Reload it and find the recording again.")
			return
		}
		if err != nil {
			internalweb.LogError(r, "could not attach pronunciation audio", err)
			h.renderPronunciationError(w, r, id, http.StatusOK, "Could not attach that recording. Try again later or upload an audio file.")
			return
		}
		item, err = h.store.Get(r.Context(), id)
		if err != nil {
			internalweb.InternalError(w, r, "could not reload vocabulary", err)
			return
		}
		h.renderer.Render(w, "vocabulary-new.html", h.editPageFromForm(r, item, "Pronunciation audio added.", ""))
		return
	}
	page := h.editPageFromForm(r, item, "", "")
	if revision, err := parseContentRevision(r.FormValue("content_revision")); err == nil {
		page.ContentRevision = revision
	}
	page.PronunciationSearched = true
	page.PronunciationResults = recordings
	if searchErr != nil {
		internalweb.LogError(r, "could not search pronunciation audio", searchErr)
		page.PronunciationError = "The open pronunciation library could not be reached. Try again or use the external search."
	}
	h.renderer.Render(w, "vocabulary-new.html", page)
}

func (h *Handler) usePronunciation(w http.ResponseWriter, r *http.Request) {
	id, ok := vocabularyID(r)
	recordingID, recordingOK := pronunciationRecordingID(r)
	if !ok || !recordingOK || h.recordings == nil {
		h.notFound(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, ExampleFormBodyLimit)
	if err := r.ParseForm(); err != nil {
		h.renderPronunciationError(w, r, id, http.StatusBadRequest, "The pronunciation form is too large or invalid.")
		return
	}
	revision, err := parseContentRevision(r.FormValue("content_revision"))
	if err != nil {
		h.renderPronunciationError(w, r, id, http.StatusConflict, err.Error())
		return
	}
	item, err := h.store.Get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	}
	if err != nil {
		internalweb.InternalError(w, r, "could not load vocabulary", err)
		return
	}
	expression := strings.TrimSpace(r.FormValue("expression"))
	if expression == "" {
		expression = item.Expression
	}
	reading := strings.TrimSpace(r.FormValue("pronunciation"))
	err = h.setPronunciationAudio(r.Context(), id, recordingID, revision, expression, reading)
	if errors.Is(err, ErrRevisionConflict) {
		h.renderPronunciationError(w, r, id, http.StatusConflict, "This card changed in another tab. Reload it and choose the recording again.")
		return
	}
	if err != nil {
		internalweb.LogError(r, "could not attach pronunciation audio", err)
		h.renderPronunciationError(w, r, id, http.StatusOK, "Could not attach that recording. Try again later or upload an audio file.")
		return
	}
	item, err = h.store.Get(r.Context(), id)
	if err != nil {
		internalweb.InternalError(w, r, "could not reload vocabulary", err)
		return
	}
	h.renderer.Render(w, "vocabulary-new.html", h.editPageFromForm(r, item, "Pronunciation audio added.", ""))
}

func (h *Handler) setPronunciationAudio(ctx context.Context, id, recordingID, revision int64, expression, reading string) error {
	upload, err := h.recordings.Download(ctx, recordingID, expression, reading)
	if err != nil {
		return err
	}
	return h.store.SetPronunciationAudio(ctx, id, revision, upload)
}

func (h *Handler) renderPronunciationError(w http.ResponseWriter, r *http.Request, id int64, status int, message string) {
	item, err := h.store.Get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	}
	if err != nil {
		internalweb.InternalError(w, r, "could not reload vocabulary", err)
		return
	}
	h.renderer.RenderStatus(w, status, "vocabulary-new.html", h.editPageFromForm(r, item, "", message))
}

func (h *Handler) action(w http.ResponseWriter, r *http.Request) {
	id, ok := vocabularyID(r)
	if !ok {
		h.notFound(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		h.renderVocabularyDetailError(w, r, id, http.StatusBadRequest, "The word action form is too large or invalid.")
		return
	}
	action := Action(r.FormValue("action"))
	if !validAction(action) {
		h.renderVocabularyDetailError(w, r, id, http.StatusBadRequest, "That word action is not available.")
		return
	}
	if (action == ActionReset || action == ActionDelete || action == ActionMarkKnown) &&
		r.FormValue("confirmed") != "1" {
		h.renderVocabularyDetailError(w, r, id, http.StatusBadRequest, "Confirm this destructive word action before continuing.")
		return
	}
	if err := h.store.ApplyAction(r.Context(), id, action); errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	} else if err != nil {
		if message, ok := internalweb.UserErrorMessage(err); ok {
			h.renderVocabularyDetailError(w, r, id, http.StatusConflict, message)
		} else {
			internalweb.InternalError(w, r, "could not update vocabulary", err)
		}
		return
	}
	if action == ActionDelete {
		http.Redirect(w, r, "/vocabulary", http.StatusSeeOther)
		return
	}
	if r.FormValue("return_to") == "/leeches" && (action == ActionHideLeech || action == ActionRestoreLeech) {
		http.Redirect(w, r, "/leeches", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/vocabulary/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (h *Handler) detail(w http.ResponseWriter, r *http.Request) {
	id, ok := vocabularyID(r)
	if !ok {
		h.notFound(w)
		return
	}
	item, err := h.store.Get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	}
	if err != nil {
		internalweb.InternalError(w, r, "could not load vocabulary", err)
		return
	}
	h.renderer.Render(w, "vocabulary-detail.html", h.detailPage(r, item, "", ""))
}

func (h *Handler) notFound(w http.ResponseWriter) {
	h.renderer.NotFound(w, internalweb.NotFoundPage{
		Title:       "Word not found",
		Heading:     "Word not found",
		Message:     "This word may have been removed, or the link may be out of date.",
		ReturnURL:   "/vocabulary",
		ReturnLabel: "Back to vocabulary",
	})
}

func (h *Handler) renderEditForm(w http.ResponseWriter, r *http.Request, status int, message string, item Item, revision int64) {
	page := h.editPageFromForm(r, item, "", message)
	page.ContentRevision = revision
	h.renderer.RenderStatus(w, status, "vocabulary-new.html", page)
}

func (h *Handler) editPageFromForm(r *http.Request, item Item, notice, message string) EditPage {
	page := h.editPage(r, item, notice, message)
	page.Expression = r.FormValue("expression")
	page.Pronunciation = r.FormValue("pronunciation")
	page.Meanings = r.FormValue("meanings")
	page.Notes = r.FormValue("notes")
	page.SourceLabel = r.FormValue("source_label")
	page.RemoveAudio = r.FormValue("remove_audio") == "on"
	page.RemovePicture = r.FormValue("remove_picture") == "on"
	return page
}

func (h *Handler) editPage(r *http.Request, item Item, notice, message string) EditPage {
	audioID, pictureID := vocabularyMediaIDs(item.Media)
	if notice == "" {
		if r.URL.Query().Get("saved") == "1" {
			notice = "Card saved."
		} else if r.URL.Query().Get("pronunciation_added") == "1" {
			notice = "Pronunciation audio added."
		} else {
			notice = exampleNotice(r.URL.Query().Get("example"))
		}
	}
	return EditPage{
		CSRFToken:          internalweb.CSRFToken(r),
		Title:              "Edit card",
		Item:               item,
		ID:                 item.ID,
		ContentRevision:    item.ContentRevision,
		AllowSparse:        item.KnownElsewhere,
		Expression:         item.Expression,
		Pronunciation:      item.Pronunciation,
		Meanings:           strings.Join(item.Meanings, "\n"),
		Notes:              item.Notes,
		SourceLabel:        item.SourceLabel,
		AudioID:            audioID,
		PictureID:          pictureID,
		GeneratorAvailable: exampleGenerationAvailable(h.generator) && item.Pronunciation != "" && len(item.Meanings) > 0,
		JishoURL:           "https://jisho.org/search/" + url.PathEscape(item.Expression),
		ForvoURL:           "https://forvo.com/search/" + url.PathEscape(item.Expression) + "/ja/",
		Notice:             notice,
		Error:              message,
	}
}

func (h *Handler) renderEditFormError(w http.ResponseWriter, r *http.Request, err error, id, revision int64, fallback string) {
	message, ok := internalweb.UserErrorMessage(err)
	if !ok {
		internalweb.InternalError(w, r, fallback, err)
		return
	}
	message = messageWithReplacementFileWarning(message, r)
	item, loadErr := h.store.Get(r.Context(), id)
	if errors.Is(loadErr, sql.ErrNoRows) {
		h.notFound(w)
		return
	}
	if loadErr != nil {
		internalweb.InternalError(w, r, "could not reload vocabulary form", loadErr)
		return
	}
	if revision <= 0 || errors.Is(err, ErrRevisionConflict) {
		revision = item.ContentRevision
	}
	h.renderEditForm(
		w,
		r,
		vocabularyFormErrorStatus(err),
		message,
		item,
		revision,
	)
}

func messageWithReplacementFileWarning(message string, r *http.Request) string {
	warning := replacementFileWarning(r)
	if warning == "" {
		return message
	}
	return message + " " + warning
}

func replacementFileWarning(r *http.Request) string {
	if r.MultipartForm == nil {
		return ""
	}
	audioSubmitted := multipartFileSubmitted(r.MultipartForm.File["audio"])
	pictureSubmitted := multipartFileSubmitted(r.MultipartForm.File["picture"])
	switch {
	case audioSubmitted && pictureSubmitted:
		return "The audio and image files were not retained. Select them again before saving."
	case audioSubmitted:
		return "The audio file was not retained. Select it again before saving."
	case pictureSubmitted:
		return "The image file was not retained. Select it again before saving."
	default:
		return ""
	}
}

func multipartFileSubmitted(headers []*multipart.FileHeader) bool {
	for _, header := range headers {
		if header != nil && header.Filename != "" {
			return true
		}
	}
	return false
}

func vocabularyMediaIDs(items []MediaItem) (audioID, pictureID int64) {
	for _, item := range items {
		switch item.Kind {
		case "audio":
			audioID = item.ID
		case "image":
			pictureID = item.ID
		}
	}
	return audioID, pictureID
}

func vocabularyFormErrorStatus(err error) int {
	var malformed formError
	if errors.As(err, &malformed) {
		return http.StatusBadRequest
	}
	if errors.Is(err, ErrDuplicate) {
		return http.StatusConflict
	}
	if errors.Is(err, ErrRevisionConflict) {
		return http.StatusConflict
	}
	return http.StatusUnprocessableEntity
}

func parseContentRevision(value string) (int64, error) {
	revision, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || revision <= 0 {
		return 0, formError("the edit form is out of date; reload the page and try again")
	}
	return revision, nil
}

func inputFromRequest(w http.ResponseWriter, r *http.Request) (CreateInput, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 25<<20)
	if err := r.ParseMultipartForm(25 << 20); err != nil {
		return CreateInput{}, formError("the form is too large or invalid")
	}
	defer r.MultipartForm.RemoveAll()
	input := CreateInput{
		Expression:    r.FormValue("expression"),
		Pronunciation: r.FormValue("pronunciation"),
		Meanings:      strings.Split(r.FormValue("meanings"), "\n"),
		Notes:         r.FormValue("notes"),
		SourceLabel:   r.FormValue("source_label"),
		RemoveAudio:   r.FormValue("remove_audio") == "on",
		RemovePicture: r.FormValue("remove_picture") == "on",
	}
	for _, field := range []struct {
		name   string
		kind   media.Kind
		target **media.Upload
	}{
		{name: "audio", kind: media.KindAudio, target: &input.Audio},
		{name: "picture", kind: media.KindImage, target: &input.Picture},
	} {
		file, header, err := r.FormFile(field.name)
		if errors.Is(err, http.ErrMissingFile) {
			continue
		}
		if err != nil {
			return CreateInput{}, formError("could not read uploaded media")
		}
		upload, readErr := media.ReadUpload(file, header, field.kind)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			if readErr != nil {
				return CreateInput{}, readErr
			}
			return CreateInput{}, fmt.Errorf("close uploaded media: %w", closeErr)
		}
		*field.target = &upload
	}
	return input, nil
}

func vocabularyID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	return id, err == nil && id > 0
}

func exampleID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "exampleID"), 10, 64)
	return id, err == nil && id > 0
}

func pronunciationRecordingID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "recordingID"), 10, 64)
	return id, err == nil && id > 0
}

func validAction(action Action) bool {
	switch action {
	case ActionSuspend, ActionArchive, ActionReset, ActionDelete, ActionHideLeech, ActionRestoreLeech, ActionLearn, ActionMarkKnown:
		return true
	default:
		return false
	}
}
