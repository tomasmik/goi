package mining

import (
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/media"
	"github.com/tomasmik/goi/internal/pronunciation"
	internalweb "github.com/tomasmik/goi/internal/web"
)

const CaptureMediaBodyLimit int64 = 256 << 20

type pronunciationProvider interface {
	Search(context.Context, string, string) ([]pronunciation.Recording, error)
	Download(context.Context, int64, string, string) (media.Upload, error)
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

func readCardMedia(r *http.Request) (cardMediaChanges, error) {
	if r.MultipartForm == nil {
		return cardMediaChanges{}, nil
	}
	audio, frame, pronunciationAudio, err := readCaptureMedia(r.MultipartForm)
	if err != nil {
		return cardMediaChanges{}, err
	}
	changes := cardMediaChanges{audio: audio, frame: frame, pronunciation: pronunciationAudio}
	for _, value := range r.Form["remove_media_id"] {
		mediaID, ok := positiveInt64(value)
		if !ok {
			return cardMediaChanges{}, handlerValidationError("invalid media removal")
		}
		changes.removeIDs = append(changes.removeIDs, mediaID)
	}
	return changes, nil
}

func readMiningUpload(header *multipart.FileHeader, kind media.Kind) (media.Upload, error) {
	file, err := header.Open()
	if err != nil {
		return media.Upload{}, fmt.Errorf("open %s upload: %w", kind, err)
	}
	defer file.Close()
	return media.ReadUpload(file, header, kind)
}
