package web

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

func PathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	return id, err == nil && id > 0
}
