package dashboard

import (
	"net/http"
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
}

func NewHandler(store *Store, renderer *internalweb.Renderer) *Handler {
	return &Handler{store: store, renderer: renderer}
}

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	summary, err := h.store.Summary(r.Context(), time.Now().UTC())
	if err != nil {
		internalweb.InternalError(w, r, "could not load dashboard", err)
		return
	}
	h.renderer.Render(w, "dashboard.html", Page{Title: "Dashboard", CSRFToken: internalweb.CSRFToken(r), Summary: summary})
}

func (h *Handler) ReviewScheduleRedirect(w http.ResponseWriter, r *http.Request) {
	date := chi.URLParam(r, "date")
	_, err := time.Parse("2006-01-02", date)
	if err != nil {
		h.renderer.NotFound(w, internalweb.NotFoundPage{
			Title:       "Page not found",
			Heading:     "Page not found",
			Message:     "This review schedule link is not valid.",
			ReturnURL:   "/dashboard",
			ReturnLabel: "Back to dashboard",
		})
		return
	}
	http.Redirect(w, r, "/dashboard#upcoming-reviews", http.StatusSeeOther)
}
