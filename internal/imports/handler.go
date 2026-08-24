package imports

import (
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/media"
	internalweb "github.com/tomasmik/goi/internal/web"
)

type Handler struct {
	store    *Store
	renderer *internalweb.Renderer
}

type Page struct {
	Title               string
	CSRFToken           string
	RunID               int64
	Preview             Preview
	PreviewNotes        []PreviewNote
	PreviewNotesOmitted int
	SuggestedMapping    Mapping
	Result              *ApplyResult
	TextResult          *TextImportResult
	TextPreview         *TextImportPreview
	TextData            string
	TextKnown           bool
	TextAllowDuplicates bool
	Error               string
}

type PreviewNote struct {
	ID     int64
	Fields []string
}

const (
	previewNoteLimit           = 25
	previewFieldCharacterLimit = 240
)

func NewHandler(store *Store, renderer *internalweb.Renderer) *Handler {
	return &Handler{store: store, renderer: renderer}
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/imports", h.page)
	r.Post("/imports/anki/upload", h.upload)
	r.Get("/imports/anki/{id}/mapping", h.mapping)
	r.Post("/imports/anki/{id}/apply", h.apply)
	r.Get("/imports/anki/{id}/result", h.result)
	r.Get("/imports/anki/{id}/errors.csv", h.errorReport)
	r.Post("/imports/text", h.importText)
}

func (h *Handler) page(w http.ResponseWriter, r *http.Request) {
	h.renderer.Render(w, "imports.html", Page{Title: "Import vocabulary", CSRFToken: internalweb.CSRFToken(r)})
}

func (h *Handler) importText(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, "the pasted data is too large or invalid", http.StatusUnprocessableEntity)
		return
	}
	value := r.FormValue("data")
	options := TextImportOptions{
		KnownElsewhere: r.FormValue("import_as") == "known",
		AllowDuplicate: r.FormValue("duplicates") == "create",
	}
	if r.FormValue("action") != "import" {
		preview, err := h.store.PreviewText(r.Context(), value, options)
		if err != nil {
			message, status, ok := userErrorResponse(err)
			if !ok {
				internalweb.InternalError(w, r, "could not preview text import", err)
				return
			}
			h.renderer.RenderStatus(w, status, "imports.html", Page{Title: "Import vocabulary", CSRFToken: internalweb.CSRFToken(r), TextData: value, TextKnown: options.KnownElsewhere, TextAllowDuplicates: options.AllowDuplicate, Error: message})
			return
		}
		h.renderer.Render(w, "imports.html", Page{Title: "Preview text import", CSRFToken: internalweb.CSRFToken(r), TextData: value, TextKnown: options.KnownElsewhere, TextAllowDuplicates: options.AllowDuplicate, TextPreview: &preview})
		return
	}
	result, err := h.store.ImportText(r.Context(), value, options)
	if err != nil {
		message, status, ok := userErrorResponse(err)
		if !ok {
			internalweb.InternalError(w, r, "could not import text", err)
			return
		}
		h.renderer.RenderStatus(w, status, "imports.html", Page{Title: "Import vocabulary", CSRFToken: internalweb.CSRFToken(r), TextData: value, Error: message})
		return
	}
	h.renderer.Render(w, "imports.html", Page{Title: "Import vocabulary", CSRFToken: internalweb.CSRFToken(r), TextResult: &result, TextData: value, TextKnown: options.KnownElsewhere, TextAllowDuplicates: options.AllowDuplicate})
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	requestLimit := MaxArchiveBytes + (10 << 20)
	r.Body = http.MaxBytesReader(w, r.Body, requestLimit)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		h.renderUploadError(w, r, "the Anki upload is too large or invalid", http.StatusUnprocessableEntity)
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("package")
	if errors.Is(err, http.ErrMissingFile) {
		h.renderError(w, r, "select an Anki package", http.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		h.renderUploadError(w, r, "could not read the Anki package", http.StatusUnprocessableEntity)
		return
	}
	defer file.Close()
	runID, _, err := h.store.Upload(r.Context(), file, header.Filename)
	if err != nil {
		h.renderUploadStoreError(w, r, err, "could not import the Anki package")
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/imports/anki/%d/mapping", runID), http.StatusSeeOther)
}

func (h *Handler) mapping(w http.ResponseWriter, r *http.Request) {
	runID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	h.renderMapping(w, r, runID, Mapping{}, "", http.StatusOK)
}

func (h *Handler) renderMapping(w http.ResponseWriter, r *http.Request, runID int64, mapping Mapping, message string, status int) {
	preview, err := h.store.Preview(r.Context(), runID)
	if err != nil {
		if errors.Is(err, errRunUnavailable) {
			h.redirectToResult(w, r, runID)
			return
		}
		h.writeStoreError(w, r, err, "could not load the Anki import")
		return
	}
	if message == "" {
		mapping = suggestedMapping(preview.Fields)
	}
	previewNotes, omitted := limitedPreviewNotes(preview.Notes)
	h.renderer.RenderStatus(w, status, "imports.html", Page{
		Title:               "Map Anki fields",
		CSRFToken:           internalweb.CSRFToken(r),
		RunID:               runID,
		Preview:             preview,
		PreviewNotes:        previewNotes,
		PreviewNotesOmitted: omitted,
		SuggestedMapping:    mapping,
		Error:               message,
	})
}

func (h *Handler) apply(w http.ResponseWriter, r *http.Request) {
	runID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		h.renderer.RenderStatus(w, http.StatusBadRequest, "not-found.html", internalweb.NotFoundPage{
			Title:       "Could not apply field mapping",
			Heading:     "Could not apply field mapping",
			Message:     "The field mapping form is too large or invalid.",
			ReturnURL:   fmt.Sprintf("/imports/anki/%d/mapping", runID),
			ReturnLabel: "Back to field mapping",
		})
		return
	}
	mapping, err := mappingFromRequest(r)
	if err != nil {
		message, status, ok := userErrorResponse(err)
		if !ok {
			internalweb.InternalError(w, r, "could not read field mapping", err)
			return
		}
		h.renderMapping(w, r, runID, mapping, message, status)
		return
	}
	_, err = h.store.Apply(r.Context(), runID, mapping)
	if err != nil {
		if errors.Is(err, errRunUnavailable) {
			h.redirectToResult(w, r, runID)
			return
		}
		if message, status, ok := userErrorResponse(err); ok && errors.Is(err, errInvalidMapping) {
			h.renderMapping(w, r, runID, mapping, message, status)
			return
		}
		h.writeStoreError(w, r, err, "could not apply the Anki import")
		return
	}
	h.redirectToResult(w, r, runID)
}

func (h *Handler) redirectToResult(w http.ResponseWriter, r *http.Request, runID int64) {
	http.Redirect(w, r, fmt.Sprintf("/imports/anki/%d/result", runID), http.StatusSeeOther)
}

func (h *Handler) result(w http.ResponseWriter, r *http.Request) {
	runID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	result, err := h.store.Result(r.Context(), runID)
	if err != nil {
		h.writeStoreError(w, r, err, "could not load the Anki import result")
		return
	}
	h.renderer.Render(w, "imports.html", Page{
		Title:     "Anki import result",
		CSRFToken: internalweb.CSRFToken(r),
		RunID:     runID,
		Result:    &result,
	})
}

func (h *Handler) errorReport(w http.ResponseWriter, r *http.Request) {
	runID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	report, err := h.store.ErrorReport(r.Context(), runID)
	if err != nil {
		h.writeStoreError(w, r, err, "could not load the import error report")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="goi-import-%d-errors.csv"`, runID))
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"result", "message"})
	for _, row := range report {
		_ = writer.Write([]string{row.Action, row.Message})
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		internalweb.LogError(r, "write import error report", err)
	}
}

func (h *Handler) renderError(w http.ResponseWriter, r *http.Request, message string, status int) {
	h.renderer.RenderStatus(w, status, "imports.html", Page{Title: "Import from Anki", CSRFToken: internalweb.CSRFToken(r), Error: message})
}

func (h *Handler) renderUploadError(w http.ResponseWriter, r *http.Request, message string, status int) {
	h.renderError(w, r, message+". Select the Anki package again before retrying.", status)
}

func (h *Handler) renderUploadStoreError(w http.ResponseWriter, r *http.Request, err error, fallback string) {
	if message, status, ok := userErrorResponse(err); ok {
		h.renderUploadError(w, r, message, status)
		return
	}
	internalweb.LogError(r, fallback, err)
	h.renderUploadError(w, r, fallback, http.StatusInternalServerError)
}

func (h *Handler) writeStoreError(w http.ResponseWriter, r *http.Request, err error, fallback string) {
	if errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	}
	if message, status, ok := userErrorResponse(err); ok {
		h.renderError(w, r, message, status)
		return
	}
	internalweb.InternalError(w, r, fallback, err)
}

func (h *Handler) notFound(w http.ResponseWriter) {
	h.renderer.NotFound(w, internalweb.NotFoundPage{
		Title:       "Import not found",
		Heading:     "Import not found",
		Message:     "This import may have expired, or the link may be out of date.",
		ReturnURL:   "/imports",
		ReturnLabel: "Back to imports",
	})
}

func userErrorResponse(err error) (string, int, bool) {
	message, ok := internalweb.UserErrorMessage(err)
	if !ok {
		return "", 0, false
	}
	switch {
	case errors.Is(err, errRunUnavailable):
		return message, http.StatusConflict, true
	case errors.Is(err, errInvalidPackage), errors.Is(err, errInvalidMapping):
		return message, http.StatusUnprocessableEntity, true
	default:
		return "", 0, false
	}
}

func mappingFromRequest(r *http.Request) (Mapping, error) {
	mapping := Mapping{
		ExpressionField:    -1,
		PronunciationField: -1,
		MeaningField:       -1,
		NotesField:         -1,
		ExampleField:       -1,
		TranslationField:   -1,
		AudioField:         -1,
		PictureField:       -1,
		KnownElsewhere:     r.FormValue("import_as") == "known",
		AllowDuplicate:     r.FormValue("duplicates") == "create",
		ExtendedFields:     true,
	}
	fields := []struct {
		name     string
		target   *int
		optional bool
	}{
		{name: "expression_field", target: &mapping.ExpressionField},
		{name: "pronunciation_field", target: &mapping.PronunciationField},
		{name: "meaning_field", target: &mapping.MeaningField},
		{name: "notes_field", target: &mapping.NotesField, optional: true},
		{name: "example_field", target: &mapping.ExampleField, optional: true},
		{name: "translation_field", target: &mapping.TranslationField, optional: true},
		{name: "audio_field", target: &mapping.AudioField},
		{name: "picture_field", target: &mapping.PictureField},
	}
	for _, field := range fields {
		raw := r.FormValue(field.name)
		if field.optional && raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			return mapping, invalidMapping(fmt.Sprintf("invalid %s", strings.ReplaceAll(field.name, "_", " ")))
		}
		*field.target = value
	}
	return mapping, nil
}

func parseID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	return id, err == nil && id > 0
}

func limitedPreviewNotes(notes []Note) ([]PreviewNote, int) {
	visibleCount := len(notes)
	if visibleCount > previewNoteLimit {
		visibleCount = previewNoteLimit
	}
	visible := make([]PreviewNote, 0, visibleCount)
	for _, note := range notes[:visibleCount] {
		fields := make([]string, len(note.Fields))
		for index, field := range note.Fields {
			fields[index] = truncatePreviewField(previewImportedField(field))
		}
		visible = append(visible, PreviewNote{ID: note.ID, Fields: fields})
	}
	return visible, len(notes) - visibleCount
}

func previewImportedField(value string) string {
	markers := make([]string, 0, 2)
	if name := mediaReference(value, media.KindAudio); name != "" {
		value = removeSoundReference(value)
		markers = append(markers, "[audio: "+name+"]")
	}
	if name := mediaReference(value, media.KindImage); name != "" {
		markers = append(markers, "[image: "+name+"]")
	}

	preview := cleanImportedText(value)
	if preview != "" && len(markers) > 0 {
		preview += "\n"
	}
	return preview + strings.Join(markers, "\n")
}

func removeSoundReference(value string) string {
	start := strings.Index(value, "[sound:")
	if start < 0 {
		return value
	}
	end := strings.IndexByte(value[start:], ']')
	if end < 0 {
		return value
	}
	return value[:start] + value[start+end+1:]
}

func truncatePreviewField(value string) string {
	characterCount := 0
	for byteIndex := range value {
		if characterCount == previewFieldCharacterLimit {
			return value[:byteIndex] + "…"
		}
		characterCount++
	}
	return value
}

func suggestedMapping(fields []string) Mapping {
	mapping := Mapping{
		ExpressionField:    -1,
		PronunciationField: -1,
		MeaningField:       -1,
		NotesField:         -1,
		ExampleField:       -1,
		TranslationField:   -1,
		AudioField:         -1,
		PictureField:       -1,
	}
	for index, field := range fields {
		switch importedFieldRole(strings.Split(field, " / ")[0]) {
		case "expression":
			if mapping.ExpressionField == -1 {
				mapping.ExpressionField = index
			}
		case "pronunciation":
			if mapping.PronunciationField == -1 {
				mapping.PronunciationField = index
			}
		case "meaning":
			if mapping.MeaningField == -1 {
				mapping.MeaningField = index
			}
		case "notes":
			if mapping.NotesField == -1 {
				mapping.NotesField = index
			}
		case "example":
			if mapping.ExampleField == -1 {
				mapping.ExampleField = index
			}
		case "translation":
			if mapping.TranslationField == -1 {
				mapping.TranslationField = index
			}
		case "audio":
			if mapping.AudioField == -1 {
				mapping.AudioField = index
			}
		case "picture":
			if mapping.PictureField == -1 {
				mapping.PictureField = index
			}
		}
	}
	return mapping
}
