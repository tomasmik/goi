package media

import (
	"bytes"
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	internalweb "github.com/tomasmik/goi/internal/web"
)

type Handler struct {
	db *sql.DB
}

func NewHandler(db *sql.DB) *Handler {
	return &Handler{db: db}
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/media/{id}", h.serve)
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	content, err := Load(r.Context(), h.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		internalweb.InternalError(w, r, "could not load media", err)
		return
	}
	w.Header().Set("Content-Type", content.MimeType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "media-"+strconv.FormatInt(id, 10), content.CreatedAt, bytes.NewReader(content.Bytes))
}
