package settings

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/dictionary/jmdict"
	internalweb "github.com/tomasmik/goi/internal/web"
)

type DictionaryManager interface {
	Status() jmdict.ManagerStatus
	Refresh(context.Context) error
}

type Handler struct {
	store       *Store
	renderer    *internalweb.Renderer
	dictionary  DictionaryManager
	authEnabled bool
}

type Page struct {
	Title                   string
	CSRFToken               string
	Values                  Values
	Dictionary              DictionaryPage
	Error                   string
	Saved                   bool
	DictionaryRefreshResult string
	AuthEnabled             bool
}

type DictionaryPage struct {
	Available      bool
	Version        string
	SourceCreated  string
	LastCheck      string
	LastSuccess    string
	Error          string
	RefreshRunning bool
}

func NewHandler(store *Store, renderer *internalweb.Renderer, dictionary DictionaryManager, authEnabled bool) *Handler {
	return &Handler{store: store, renderer: renderer, dictionary: dictionary, authEnabled: authEnabled}
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/settings", h.get)
	r.Post("/settings", h.post)
	r.Post("/settings/jmdict/refresh", h.refreshDictionary)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	values, err := h.store.Get(r.Context())
	if err != nil {
		internalweb.InternalError(w, r, "could not load settings", err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "goi_theme", Value: values.Theme, Path: "/", MaxAge: 31536000, SameSite: http.SameSiteLaxMode})
	h.render(w, r, http.StatusOK, values, "", r.URL.Query().Get("saved") == "1")
}

func (h *Handler) post(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		h.renderer.RenderStatus(w, http.StatusBadRequest, "not-found.html", internalweb.NotFoundPage{
			Title:       "Could not save settings",
			Heading:     "Could not save settings",
			Message:     "The settings form is too large or invalid.",
			ReturnURL:   "/settings",
			ReturnLabel: "Back to settings",
		})
		return
	}
	values, err := valuesFromRequest(r)
	if err != nil {
		h.render(w, r, http.StatusUnprocessableEntity, values, err.Error(), false)
		return
	}
	if err := h.store.Update(r.Context(), values); err != nil {
		if message, ok := internalweb.UserErrorMessage(err); ok {
			h.render(w, r, http.StatusUnprocessableEntity, values, message, false)
		} else {
			internalweb.InternalError(w, r, "could not update settings", err)
		}
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "goi_theme", Value: values.Theme, Path: "/", MaxAge: 31536000, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

func (h *Handler) refreshDictionary(w http.ResponseWriter, r *http.Request) {
	result := "updated"
	if err := h.dictionary.Refresh(r.Context()); err != nil && !errors.Is(err, jmdict.ErrNotModified) {
		result = "failed"
	}
	http.Redirect(w, r, "/settings?jmdict_refresh="+result, http.StatusSeeOther)
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, values Values, message string, saved bool) {
	h.renderer.RenderStatus(w, status, "settings.html", Page{
		Title:                   "Settings",
		CSRFToken:               internalweb.CSRFToken(r),
		Values:                  values,
		Dictionary:              newDictionaryPage(h.dictionary.Status()),
		Error:                   message,
		Saved:                   saved,
		DictionaryRefreshResult: r.URL.Query().Get("jmdict_refresh"),
		AuthEnabled:             h.authEnabled,
	})
}

func newDictionaryPage(status jmdict.ManagerStatus) DictionaryPage {
	return DictionaryPage{
		Available:      status.Available,
		Version:        status.Metadata.Version,
		SourceCreated:  status.Metadata.Created,
		LastCheck:      formatDictionaryTime(status.LastCheck),
		LastSuccess:    formatDictionaryTime(status.LastSuccess),
		Error:          strings.ReplaceAll(status.LastErrorCode, "_", " "),
		RefreshRunning: status.RefreshRunning,
	}
}

func formatDictionaryTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format("2006-01-02 15:04 UTC")
}

func valuesFromRequest(r *http.Request) (Values, error) {
	values := Values{
		TimeZone:          r.FormValue("time_zone"),
		Theme:             r.FormValue("theme"),
		ReviewMode:        r.FormValue("review_mode"),
		ReviewOrder:       r.FormValue("review_order"),
		ReviewCardOrder:   r.FormValue("review_card_order"),
		ReviewAutoAdvance: r.FormValue("review_auto_advance") == "on",
		AudioEnabled:      r.FormValue("audio_enabled") == "on",
		SixMonthReview:    r.FormValue("six_month_review") == "on",
	}
	if values.ReviewMode == "" {
		values.ReviewMode = "typed"
	}
	if values.ReviewOrder == "" {
		values.ReviewOrder = "stage_ascending"
	}
	if values.ReviewCardOrder == "" {
		values.ReviewCardOrder = "together"
	}

	var validationErr error
	parseNumber := func(field, message string, destination *int) {
		value, err := strconv.Atoi(r.FormValue(field))
		if err != nil {
			if validationErr == nil {
				validationErr = errors.New(message)
			}
			return
		}
		*destination = value
	}

	parseNumber("lesson_window_hours", "recent lesson window must be a number", &values.LessonWindowHours)
	parseNumber("extra_study_limit", "extra-study list size must be a number", &values.ExtraStudyLimit)
	parseNumber("retry_count", "answer attempts must be a number", &values.RetryCount)
	parseNumber("leech_failure_count", "leech failure count must be a number", &values.LeechFailureCount)
	parseNumber("leech_suspend_count", "leech suspension count must be a number", &values.LeechSuspendCount)
	parseNumber("leech_recovery_streak", "leech recovery streak must be a number", &values.LeechRecoveryStreak)

	return values, validationErr
}
