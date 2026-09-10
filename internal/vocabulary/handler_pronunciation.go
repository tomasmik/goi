package vocabulary

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/media"
	"github.com/tomasmik/goi/internal/pronunciation"
	internalweb "github.com/tomasmik/goi/internal/web"
)

type pronunciationProvider interface {
	Search(context.Context, string, string) ([]pronunciation.Recording, error)
	Download(context.Context, int64, string, string) (media.Upload, error)
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
		h.renderer.Render(w, "vocabulary-edit.html", h.editPageFromForm(r, item, "Pronunciation audio added.", ""))
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
	h.renderer.Render(w, "vocabulary-edit.html", page)
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
	h.renderer.Render(w, "vocabulary-edit.html", h.editPageFromForm(r, item, "Pronunciation audio added.", ""))
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
	h.renderer.RenderStatus(w, status, "vocabulary-edit.html", h.editPageFromForm(r, item, "", message))
}

func pronunciationRecordingID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "recordingID"), 10, 64)
	return id, err == nil && id > 0
}
