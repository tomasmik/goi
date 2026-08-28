package wanikani

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	internalweb "github.com/tomasmik/goi/internal/web"
)

const settingsFormLimit = 8 << 10

type Handler struct {
	service  *Service
	renderer *internalweb.Renderer
}

type SettingsPage struct {
	Title           string
	CSRFToken       string
	Connected       bool
	Syncing         bool
	Username        string
	UserLevel       int
	SubjectCount    int
	LastAttempt     string
	LastSuccess     string
	ShowLastAttempt bool
	LastError       string
	Error           string
	Notice          string
}

func NewHandler(service *Service, renderer *internalweb.Renderer) *Handler {
	return &Handler{service: service, renderer: renderer}
}

func (h *Handler) Routes(router chi.Router) {
	router.Get("/settings/wanikani", h.get)
	router.Post("/settings/wanikani/connect", h.connect)
	router.Post("/settings/wanikani/sync", h.sync)
	router.Post("/settings/wanikani/disconnect", h.disconnect)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	h.renderStatus(w, r, http.StatusOK, "")
}

func (h *Handler) connect(w http.ResponseWriter, r *http.Request) {
	if !h.parseForm(w, r) {
		return
	}
	_, changed, err := h.service.Connect(r.Context(), r.PostForm.Get("token"))
	if err != nil {
		switch {
		case errors.Is(err, ErrAuthentication):
			h.renderStatus(w, r, http.StatusUnprocessableEntity, "WaniKani did not accept that token. Create a read-only personal access token and try again.")
		case errors.Is(err, ErrSyncInProgress):
			h.renderStatus(w, r, http.StatusConflict, "Wait for the current synchronization to finish before replacing the token.")
		case errors.Is(err, ErrInvalidToken):
			h.renderStatus(w, r, http.StatusUnprocessableEntity, "Enter a valid WaniKani personal access token.")
		default:
			internalweb.LogError(r, "could not connect WaniKani", err)
			h.renderStatus(w, r, http.StatusBadGateway, "Could not connect WaniKani. Try again.")
		}
		return
	}
	result := "connected"
	if changed {
		result = "account_changed"
	}
	h.redirect(w, r, result)
}

func (h *Handler) sync(w http.ResponseWriter, r *http.Request) {
	if !h.parseForm(w, r) {
		return
	}
	status, err := h.service.Status(r.Context())
	if err != nil {
		internalweb.InternalError(w, r, "could not load WaniKani status", err)
		return
	}
	if !status.Connected {
		h.renderStatus(w, r, http.StatusConflict, "Connect WaniKani before requesting a synchronization.")
		return
	}
	h.service.RequestSync()
	h.redirect(w, r, "sync_requested")
}

func (h *Handler) disconnect(w http.ResponseWriter, r *http.Request) {
	if !h.parseForm(w, r) {
		return
	}
	if r.PostForm.Get("confirmed") != "1" {
		h.renderStatus(w, r, http.StatusUnprocessableEntity, "Confirm that you want to disconnect WaniKani. Imported vocabulary will remain known in Goi.")
		return
	}
	if err := h.service.Disconnect(r.Context()); err != nil {
		if errors.Is(err, ErrSyncInProgress) {
			h.renderStatus(w, r, http.StatusConflict, "Wait for the current synchronization to finish before disconnecting WaniKani.")
			return
		}
		internalweb.InternalError(w, r, "could not disconnect WaniKani", err)
		return
	}
	h.redirect(w, r, "disconnected")
}

func (h *Handler) parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, settingsFormLimit)
	if err := r.ParseForm(); err != nil {
		h.renderStatus(w, r, http.StatusBadRequest, "The WaniKani form is too large or invalid.")
		return false
	}
	return true
}

func (h *Handler) renderStatus(w http.ResponseWriter, r *http.Request, responseStatus int, message string) {
	status, err := h.service.Status(r.Context())
	if err != nil {
		internalweb.InternalError(w, r, "could not load WaniKani status", err)
		return
	}
	page := SettingsPage{
		Title:        "WaniKani",
		CSRFToken:    internalweb.CSRFToken(r),
		Connected:    status.Connected,
		Syncing:      status.Syncing,
		Username:     status.Username,
		UserLevel:    status.UserLevel,
		SubjectCount: status.SubjectCount,
		LastAttempt:  formatSettingsTime(status.LastAttemptAt),
		LastSuccess:  formatSettingsTime(status.LastSuccessAt),
		LastError:    status.LastError,
		Error:        message,
	}
	page.ShowLastAttempt = !status.LastAttemptAt.IsZero() && !status.LastAttemptAt.Equal(status.LastSuccessAt)
	switch r.URL.Query().Get("result") {
	case "connected":
		page.Notice = "WaniKani connected. The first synchronization has been queued."
	case "account_changed":
		page.Notice = "A different WaniKani account was connected. Its learned vocabulary will be added without removing earlier imports."
	case "sync_requested":
		page.Notice = "Synchronization requested."
	case "disconnected":
		page.Notice = "WaniKani disconnected. Imported vocabulary remains known in Goi."
	}
	h.renderer.RenderStatus(w, responseStatus, "wanikani-settings.html", page)
}

func (h *Handler) redirect(w http.ResponseWriter, r *http.Request, result string) {
	http.Redirect(w, r, "/settings/wanikani?result="+result, http.StatusSeeOther)
}

func formatSettingsTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format("2006-01-02 15:04 UTC")
}
