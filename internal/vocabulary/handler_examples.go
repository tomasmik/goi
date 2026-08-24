package vocabulary

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tomasmik/goi/internal/examplegen"
	"github.com/tomasmik/goi/internal/examples"
	internalweb "github.com/tomasmik/goi/internal/web"
)

func (h *Handler) createExample(w http.ResponseWriter, r *http.Request) {
	id, ok := vocabularyID(r)
	if !ok {
		h.notFound(w)
		return
	}
	if err := parseExampleForm(w, r); err != nil {
		h.renderExampleRequestError(w, r, id, 0, err)
		return
	}
	item, ok := h.loadExampleItem(w, r, id)
	if !ok {
		return
	}
	targetSurface := strings.TrimSpace(r.FormValue("target_surface"))
	if targetSurface == "" {
		targetSurface = item.Expression
	}
	_, err := h.examples.Create(r.Context(), id, examples.Input{
		Origin:        examples.OriginManual,
		Sentence:      r.FormValue("sentence"),
		Translation:   r.FormValue("translation"),
		TargetSurface: targetSurface,
	})
	if err != nil {
		h.renderExampleError(w, r, item, 0, err, "could not add example")
		return
	}
	h.redirectToExamples(w, r, id, "added")
}

func (h *Handler) updateExample(w http.ResponseWriter, r *http.Request) {
	vocabularyID, ok := vocabularyID(r)
	if !ok {
		h.notFound(w)
		return
	}
	exampleID, ok := exampleID(r)
	if !ok {
		h.notFound(w)
		return
	}
	if err := parseExampleForm(w, r); err != nil {
		h.renderExampleRequestError(w, r, vocabularyID, exampleID, err)
		return
	}
	item, ok := h.loadExampleItem(w, r, vocabularyID)
	if !ok {
		return
	}
	existing, ok := findExample(item.Examples, exampleID)
	if !ok {
		h.notFound(w)
		return
	}
	targetSurface := strings.TrimSpace(r.FormValue("target_surface"))
	if targetSurface == "" {
		targetSurface = item.Expression
	}
	_, err := h.examples.Update(r.Context(), vocabularyID, exampleID, examples.Input{
		MiningCaptureID:  existing.MiningCaptureID,
		Origin:           existing.Origin,
		Sentence:         r.FormValue("sentence"),
		Translation:      r.FormValue("translation"),
		TargetSurface:    targetSurface,
		SourceTitle:      existing.SourceTitle,
		SourceURL:        existing.SourceURL,
		SourcePositionMS: existing.SourcePositionMS,
		Provider:         existing.Provider,
		Model:            existing.Model,
	})
	if err != nil {
		h.renderExampleError(w, r, item, exampleID, err, "could not update example")
		return
	}
	h.redirectToExamples(w, r, vocabularyID, "updated")
}

func (h *Handler) deleteExample(w http.ResponseWriter, r *http.Request) {
	vocabularyID, ok := vocabularyID(r)
	if !ok {
		h.notFound(w)
		return
	}
	exampleID, ok := exampleID(r)
	if !ok {
		h.notFound(w)
		return
	}
	if err := parseExampleForm(w, r); err != nil {
		h.renderExampleRequestError(w, r, vocabularyID, exampleID, err)
		return
	}
	if r.FormValue("confirmed") != "1" {
		item, ok := h.loadExampleItem(w, r, vocabularyID)
		if !ok {
			return
		}
		h.renderExampleStatus(w, r, item, http.StatusBadRequest, "", "Confirm example removal before continuing.")
		return
	}
	if err := h.examples.Delete(r.Context(), vocabularyID, exampleID); errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	} else if err != nil {
		internalweb.InternalError(w, r, "could not delete example", err)
		return
	}
	h.redirectToExamples(w, r, vocabularyID, "deleted")
}

func (h *Handler) generateExample(w http.ResponseWriter, r *http.Request) {
	id, ok := vocabularyID(r)
	if !ok {
		h.notFound(w)
		return
	}
	if err := parseExampleForm(w, r); err != nil {
		h.renderExampleRequestError(w, r, id, 0, err)
		return
	}
	item, ok := h.loadExampleItem(w, r, id)
	if !ok {
		return
	}
	if h.generator == nil || !h.generator.Available() {
		h.renderExampleStatus(w, r, item, http.StatusUnprocessableEntity, "", "Example generation is not configured. Set it up in Settings.")
		return
	}
	if item.Pronunciation == "" || len(item.Meanings) == 0 {
		h.renderExampleStatus(w, r, item, http.StatusUnprocessableEntity, "", "Add a reading and meaning before generating an example.")
		return
	}
	if len(item.Examples) > 0 {
		h.renderExampleStatus(w, r, item, http.StatusConflict, "", "An example already exists for this word.")
		return
	}
	generated, err := h.generator.Generate(r.Context(), examplegen.Request{
		Expression:    item.Expression,
		Pronunciation: item.Pronunciation,
		Meanings:      item.Meanings,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "generate vocabulary example", "vocabulary_id", id, "error", err)
		h.renderExampleStatus(w, r, item, http.StatusBadGateway, "", "Could not generate an example. Check the configured provider and try again.")
		return
	}
	if _, err := h.examples.CreateGeneratedIfEmpty(r.Context(), id, item.ContentRevision, examples.Input{
		Sentence:      generated.Sentence,
		Translation:   generated.Translation,
		TargetSurface: generated.TargetSurface,
		Provider:      generated.Provider,
		Model:         generated.Model,
	}); errors.Is(err, examples.ErrAlreadyExists) {
		item, ok := h.loadExampleItem(w, r, id)
		if !ok {
			return
		}
		h.renderExampleStatus(w, r, item, http.StatusConflict, "", "An example was added while generation was running.")
		return
	} else if errors.Is(err, examples.ErrVocabularyChanged) {
		item, ok := h.loadExampleItem(w, r, id)
		if !ok {
			return
		}
		h.renderExampleStatus(w, r, item, http.StatusConflict, "", "The word changed while generation was running. Review it and try again.")
		return
	} else if errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	} else if err != nil {
		internalweb.InternalError(w, r, "could not save generated example", err)
		return
	}
	h.redirectToExamples(w, r, id, "generated")
}

func (h *Handler) generateExampleFields(w http.ResponseWriter, r *http.Request) {
	vocabularyID, ok := vocabularyID(r)
	if !ok {
		h.notFound(w)
		return
	}
	exampleID, ok := exampleID(r)
	if !ok {
		h.notFound(w)
		return
	}
	if err := parseExampleForm(w, r); err != nil {
		h.renderExampleRequestError(w, r, vocabularyID, exampleID, err)
		return
	}
	item, ok := h.loadExampleItem(w, r, vocabularyID)
	if !ok {
		return
	}
	if _, ok := findExample(item.Examples, exampleID); !ok {
		h.notFound(w)
		return
	}
	h.renderGeneratedExampleDraft(w, r, item, exampleID)
}

func (h *Handler) generateExampleDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := vocabularyID(r)
	if !ok {
		h.notFound(w)
		return
	}
	if err := parseExampleForm(w, r); err != nil {
		h.renderExampleRequestError(w, r, id, 0, err)
		return
	}
	item, ok := h.loadExampleItem(w, r, id)
	if !ok {
		return
	}
	h.renderGeneratedExampleDraft(w, r, item, 0)
}

func (h *Handler) renderGeneratedExampleDraft(w http.ResponseWriter, r *http.Request, item Item, exampleID int64) {
	if h.generator == nil || !h.generator.Available() || item.Pronunciation == "" || len(item.Meanings) == 0 {
		h.renderExampleStatus(w, r, item, http.StatusUnprocessableEntity, "", "Add a reading and meaning, then configure example generation.")
		return
	}
	generated, err := h.generator.Generate(r.Context(), examplegen.Request{
		Expression:    item.Expression,
		Pronunciation: item.Pronunciation,
		Meanings:      item.Meanings,
		Sentence:      r.FormValue("sentence"),
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "generate vocabulary example draft", "vocabulary_id", item.ID, "example_id", exampleID, "error", err)
		h.renderExampleStatus(w, r, item, http.StatusBadGateway, "", "Could not generate the example fields. Check the configured provider and try again.")
		return
	}
	page := h.editPage(r, item, "Example fields generated. Check them before saving.", "")
	page.ExampleForm = ExampleForm{
		ID:            exampleID,
		Sentence:      generated.Sentence,
		Translation:   generated.Translation,
		TargetSurface: generated.TargetSurface,
	}
	h.renderer.Render(w, "vocabulary-new.html", page)
}

func (h *Handler) loadExampleItem(w http.ResponseWriter, r *http.Request, id int64) (Item, bool) {
	item, err := h.store.Get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return Item{}, false
	}
	if err != nil {
		internalweb.InternalError(w, r, "could not load vocabulary", err)
		return Item{}, false
	}
	return item, true
}

func (h *Handler) renderVocabularyDetailError(w http.ResponseWriter, r *http.Request, id int64, status int, message string) {
	item, ok := h.loadExampleItem(w, r, id)
	if !ok {
		return
	}
	h.renderDetailStatus(w, r, item, status, "", message)
}

func (h *Handler) renderExampleRequestError(w http.ResponseWriter, r *http.Request, vocabularyID, exampleID int64, err error) {
	item, ok := h.loadExampleItem(w, r, vocabularyID)
	if !ok {
		return
	}
	if exampleID > 0 {
		if _, exists := findExample(item.Examples, exampleID); !exists {
			h.notFound(w)
			return
		}
	}
	h.renderExampleError(w, r, item, exampleID, err, "could not read example form")
}

func (h *Handler) renderExampleError(w http.ResponseWriter, r *http.Request, item Item, exampleID int64, err error, fallback string) {
	if errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	}
	if message, ok := internalweb.UserErrorMessage(err); ok {
		if returnToEdit(r) {
			page := h.editPage(r, item, "", message)
			page.ExampleForm = ExampleForm{
				ID:            exampleID,
				Sentence:      r.FormValue("sentence"),
				Translation:   r.FormValue("translation"),
				TargetSurface: r.FormValue("target_surface"),
			}
			h.renderer.RenderStatus(w, vocabularyFormErrorStatus(err), "vocabulary-new.html", page)
			return
		}
		page := h.detailPage(r, item, "", message)
		page.ExampleForm = ExampleForm{
			ID:            exampleID,
			Sentence:      r.FormValue("sentence"),
			Translation:   r.FormValue("translation"),
			TargetSurface: r.FormValue("target_surface"),
		}
		h.renderer.RenderStatus(w, vocabularyFormErrorStatus(err), "vocabulary-detail.html", page)
		return
	}
	internalweb.InternalError(w, r, fallback, err)
}

func (h *Handler) renderDetailStatus(w http.ResponseWriter, r *http.Request, item Item, status int, notice, message string) {
	h.renderer.RenderStatus(w, status, "vocabulary-detail.html", h.detailPage(r, item, notice, message))
}

func (h *Handler) renderExampleStatus(w http.ResponseWriter, r *http.Request, item Item, status int, notice, message string) {
	if returnToEdit(r) {
		h.renderer.RenderStatus(w, status, "vocabulary-new.html", h.editPage(r, item, notice, message))
		return
	}
	h.renderDetailStatus(w, r, item, status, notice, message)
}

func (h *Handler) detailPage(r *http.Request, item Item, notice, message string) DetailPage {
	if notice == "" {
		notice = exampleNotice(r.URL.Query().Get("example"))
	}
	return DetailPage{
		Title:               item.Expression,
		CSRFToken:           internalweb.CSRFToken(r),
		Item:                item,
		GenerationAvailable: exampleGenerationAvailable(h.generator) && item.Pronunciation != "" && len(item.Meanings) > 0 && len(item.Examples) == 0,
		Now:                 time.Now(),
		Notice:              notice,
		Error:               message,
	}
}

func exampleGenerationAvailable(generator examplegen.Generator) bool {
	return generator != nil && generator.Available()
}

func (h *Handler) redirectToExamples(w http.ResponseWriter, r *http.Request, vocabularyID int64, result string) {
	if returnToEdit(r) {
		location := fmt.Sprintf("/vocabulary/%d/edit?example=%s#examples", vocabularyID, result)
		http.Redirect(w, r, location, http.StatusSeeOther)
		return
	}
	location := fmt.Sprintf("/vocabulary/%d?example=%s#examples", vocabularyID, result)
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func returnToEdit(r *http.Request) bool {
	return r.FormValue("return_to") == "edit"
}

func parseExampleForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, ExampleFormBodyLimit)
	if err := r.ParseForm(); err != nil {
		return formError("the example form is too large or invalid")
	}
	return nil
}

func findExample(values []examples.Example, id int64) (examples.Example, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return examples.Example{}, false
}

func exampleNotice(result string) string {
	switch result {
	case "added":
		return "Example added."
	case "updated":
		return "Example updated."
	case "deleted":
		return "Example removed."
	case "generated":
		return "Example generated. Review it before relying on it."
	default:
		return ""
	}
}
