package examplegen

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	internalweb "github.com/tomasmik/goi/internal/web"
)

type SettingsPage struct {
	Title     string
	CSRFToken string
	Values    SettingsView
	Saved     bool
	Tested    bool
	Error     string
}

type SettingsHandler struct {
	manager  *Manager
	renderer *internalweb.Renderer
}

func NewSettingsHandler(manager *Manager, renderer *internalweb.Renderer) *SettingsHandler {
	return &SettingsHandler{manager: manager, renderer: renderer}
}

func (h *SettingsHandler) Routes(router chi.Router) {
	router.Get("/settings/examples", h.get)
	router.Post("/settings/examples", h.post)
	router.Post("/settings/examples/test", h.test)
	router.Post("/settings/examples/disable", h.disable)
	router.Post("/settings/examples/api-key/remove", h.removeAPIKey)
	router.Post("/settings/examples/microsoft/remove", h.removeMicrosoftSettings)
}

func (h *SettingsHandler) removeAPIKey(w http.ResponseWriter, r *http.Request) {
	if !h.confirmedCredentialRemoval(w, r) {
		return
	}
	if err := h.manager.RemoveAPIKey(); err != nil {
		if message, ok := internalweb.UserErrorMessage(err); ok {
			h.render(w, r, http.StatusUnprocessableEntity, h.manager.Current(), message, false, false)
			return
		}
		internalweb.InternalError(w, r, "could not remove example provider API key", err)
		return
	}
	h.redirectSaved(w, r)
}

func (h *SettingsHandler) removeMicrosoftSettings(w http.ResponseWriter, r *http.Request) {
	if !h.confirmedCredentialRemoval(w, r) {
		return
	}
	if err := h.manager.RemoveMicrosoftSettings(); err != nil {
		internalweb.InternalError(w, r, "could not remove Microsoft Translator settings", err)
		return
	}
	h.redirectSaved(w, r)
}

func (h *SettingsHandler) confirmedCredentialRemoval(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil || r.FormValue("confirmed") != "1" {
		h.render(w, r, http.StatusBadRequest, h.manager.Current(), "Confirm credential removal before continuing.", false, false)
		return false
	}
	return true
}

func (h *SettingsHandler) redirectSaved(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/settings/examples?saved=1", http.StatusSeeOther)
}

func (h *SettingsHandler) disable(w http.ResponseWriter, r *http.Request) {
	if err := h.manager.Disable(); err != nil {
		if message, ok := internalweb.UserErrorMessage(err); ok {
			h.render(w, r, http.StatusUnprocessableEntity, h.manager.Current(), message, false, false)
			return
		}
		internalweb.InternalError(w, r, "could not disable example generation", err)
		return
	}
	h.redirectSaved(w, r)
}

func (h *SettingsHandler) test(w http.ResponseWriter, r *http.Request) {
	update, values, ok := h.settingsUpdate(w, r)
	if !ok {
		return
	}
	if err := h.manager.Test(r.Context(), update); err != nil {
		h.render(w, r, http.StatusUnprocessableEntity, values, "Could not connect to the provider: "+err.Error(), false, false)
		return
	}
	if err := h.manager.Update(update); err != nil {
		internalweb.InternalError(w, r, "could not save tested example generation settings", err)
		return
	}
	http.Redirect(w, r, "/settings/examples?tested=1", http.StatusSeeOther)
}

func (h *SettingsHandler) get(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, h.manager.Current(), "", r.URL.Query().Get("saved") == "1", r.URL.Query().Get("tested") == "1")
}

func (h *SettingsHandler) post(w http.ResponseWriter, r *http.Request) {
	update, values, ok := h.settingsUpdate(w, r)
	if !ok {
		return
	}
	if err := h.manager.Update(update); err != nil {
		if message, ok := internalweb.UserErrorMessage(err); ok {
			h.render(w, r, http.StatusUnprocessableEntity, values, message, false, false)
			return
		}
		internalweb.InternalError(w, r, "could not save example generation settings", err)
		return
	}
	h.redirectSaved(w, r)
}

func (h *SettingsHandler) settingsUpdate(w http.ResponseWriter, r *http.Request) (SettingsUpdate, SettingsView, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		h.render(w, r, http.StatusBadRequest, h.manager.Current(), "The settings form is too large or invalid.", false, false)
		return SettingsUpdate{}, SettingsView{}, false
	}
	current := h.manager.Current()
	update := SettingsUpdate{
		BaseURL:                r.FormValue("base_url"),
		Model:                  r.FormValue("model"),
		APIKey:                 r.FormValue("api_key"),
		ClearAPIKey:            r.FormValue("clear_api_key") == "on",
		TranslationProvider:    r.FormValue("translation_provider"),
		MicrosoftEndpoint:      r.FormValue("microsoft_endpoint"),
		MicrosoftRegion:        r.FormValue("microsoft_region"),
		MicrosoftAPIKey:        r.FormValue("microsoft_api_key"),
		ClearMicrosoftSettings: r.FormValue("clear_microsoft_settings") == "on",
		Instructions: Instructions{
			Sentence:      r.FormValue("sentence_instruction"),
			Translation:   r.FormValue("translation_instruction"),
			TargetSurface: r.FormValue("target_instruction"),
		},
	}
	if current.EnvironmentManaged {
		update.BaseURL = current.BaseURL
		update.Model = current.Model
	}
	values := SettingsView{
		BaseURL:             update.BaseURL,
		Model:               update.Model,
		HasAPIKey:           current.HasAPIKey,
		EnvironmentManaged:  current.EnvironmentManaged,
		Instructions:        update.Instructions,
		TranslationProvider: normalizeTranslationProvider(update.TranslationProvider),
		MicrosoftEndpoint:   update.MicrosoftEndpoint,
		MicrosoftRegion:     update.MicrosoftRegion,
		HasMicrosoftAPIKey:  current.HasMicrosoftAPIKey,
	}
	return update, values, true
}

func (h *SettingsHandler) render(w http.ResponseWriter, r *http.Request, status int, values SettingsView, message string, saved, tested bool) {
	h.renderer.RenderStatus(w, status, "example-generation-settings.html", SettingsPage{
		Title:     "Translation and examples",
		CSRFToken: internalweb.CSRFToken(r),
		Values:    values,
		Saved:     saved,
		Tested:    tested,
		Error:     message,
	})
}
