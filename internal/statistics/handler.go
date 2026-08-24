package statistics

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	internalweb "github.com/tomasmik/goi/internal/web"
)

type Handler struct {
	store    *Store
	renderer *internalweb.Renderer
}

type Page struct {
	Title     string
	CSRFToken string
	Summary   Summary
	Mistakes  []Mistake
}

func NewHandler(store *Store, renderer *internalweb.Renderer) *Handler {
	return &Handler{store: store, renderer: renderer}
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/statistics", h.page)
	r.Post("/statistics/mistakes/{id}/visibility", h.hideMistake)
}

func (h *Handler) page(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	summary, err := h.store.Summary(r.Context(), now)
	if err != nil {
		internalweb.InternalError(w, r, "could not load statistics", err)
		return
	}
	mistakes, err := h.store.RecentMistakes(r.Context(), now)
	if err != nil {
		internalweb.InternalError(w, r, "could not load recent mistakes", err)
		return
	}
	h.renderer.Render(w, "statistics.html", Page{Title: "Progress", CSRFToken: internalweb.CSRFToken(r), Summary: summary, Mistakes: mistakes})
}

func (h *Handler) hideMistake(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		h.notFound(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		h.renderer.RenderStatus(w, http.StatusBadRequest, "not-found.html", internalweb.NotFoundPage{
			Title:       "Could not hide mistake",
			Heading:     "Could not hide mistake",
			Message:     "The mistake action form is too large or invalid.",
			ReturnURL:   "/statistics",
			ReturnLabel: "Back to progress",
		})
		return
	}
	if err := h.store.HideMistake(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	} else if err != nil {
		internalweb.InternalError(w, r, "could not hide mistake", err)
		return
	}
	http.Redirect(w, r, "/statistics", http.StatusSeeOther)
}

func (h *Handler) notFound(w http.ResponseWriter) {
	h.renderer.NotFound(w, internalweb.NotFoundPage{
		Title:       "Mistake not found",
		Heading:     "Mistake not found",
		Message:     "This mistake may no longer be available.",
		ReturnURL:   "/statistics",
		ReturnLabel: "Back to progress",
	})
}
