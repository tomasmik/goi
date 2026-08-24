package captureapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	browserextension "github.com/tomasmik/goi/browser-extension"
	internalweb "github.com/tomasmik/goi/internal/web"
)

type SettingsHandler struct {
	tokens          *Store
	renderer        *internalweb.Renderer
	publicOrigin    string
	createdTokensMu sync.Mutex
	createdTokens   map[string]createdTokenDisplay
	loadLocation    func(context.Context) (*time.Location, error)
}

type createdTokenDisplay struct {
	token     CreatedToken
	expiresAt time.Time
}

const createdTokenDisplayLifetime = 15 * time.Minute
const createdTokenCookieName = "goi_extension_token_display"

const defaultExtensionTokenName = "Chrome on this device"

type SettingsPage struct {
	Title                   string
	CSRFToken               string
	Tokens                  []Token
	CreatedToken            *CreatedToken
	TokenName               string
	Error                   string
	TimeZone                string
	ExtensionVersion        string
	MinimumChrome           string
	PublicOrigin            string
	CreatedTokenUnavailable bool
}

func NewSettingsHandler(tokens *Store, renderer *internalweb.Renderer, loadLocation func(context.Context) (*time.Location, error), publicOrigin string) *SettingsHandler {
	if loadLocation == nil {
		loadLocation = func(context.Context) (*time.Location, error) { return time.UTC, nil }
	}
	return &SettingsHandler{
		tokens:        tokens,
		renderer:      renderer,
		publicOrigin:  publicOrigin,
		createdTokens: make(map[string]createdTokenDisplay),
		loadLocation:  loadLocation,
	}
}

func (h *SettingsHandler) Routes(r chi.Router) {
	r.Get("/settings/extension", h.page)
	r.Get("/settings/extension/download", h.download)
	r.Post("/settings/extension/tokens", h.create)
	r.Post("/settings/extension/tokens/{id}/revoke", h.revoke)
}

func (h *SettingsHandler) download(w http.ResponseWriter, r *http.Request) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	err := fs.WalkDir(browserextension.Assets, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		contents, err := fs.ReadFile(browserextension.Assets, path)
		if err != nil {
			return err
		}
		file, err := writer.Create("goi-browser-extension/" + path)
		if err != nil {
			return err
		}
		_, err = file.Write(contents)
		return err
	})
	if err == nil {
		err = writer.Close()
	}
	if err != nil {
		internalweb.InternalError(w, r, "could not build extension download", err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="goi-browser-extension.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(archive.Bytes())
}

func (h *SettingsHandler) page(w http.ResponseWriter, r *http.Request) {
	tokens, ok := h.listTokens(w, r)
	if !ok {
		return
	}
	displayID := r.URL.Query().Get("created")
	if displayID == "" {
		if cookie, err := r.Cookie(createdTokenCookieName); err == nil {
			displayID = cookie.Value
		}
	}
	created := h.createdToken(displayID)
	if displayID != "" && created == nil {
		h.clearCreatedTokenCookie(w, r)
	}
	h.renderPage(w, r, tokens, created, displayID != "" && created == nil && len(tokens) > 0, defaultExtensionTokenName, "", http.StatusOK)
}

func (h *SettingsHandler) create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		h.render(w, r, nil, defaultExtensionTokenName, "Could not read the extension name.", http.StatusUnprocessableEntity)
		return
	}
	name := r.FormValue("name")
	displayID, err := newCreatedTokenDisplayID()
	if err != nil {
		internalweb.InternalError(w, r, "could not prepare extension token display", err)
		return
	}
	created, err := h.tokens.Create(r.Context(), name)
	if errors.Is(err, ErrInvalidTokenName) {
		h.render(w, r, nil, name, "Extension name must be between 1 and 100 characters.", http.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		internalweb.InternalError(w, r, "could not create extension token", err)
		return
	}
	h.rememberCreatedToken(displayID, created)
	http.SetCookie(w, &http.Cookie{
		Name:     createdTokenCookieName,
		Value:    displayID,
		Path:     "/settings/extension",
		MaxAge:   int(createdTokenDisplayLifetime.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	http.Redirect(w, r, "/settings/extension?created="+displayID, http.StatusSeeOther)
}

func (h *SettingsHandler) clearCreatedTokenCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     createdTokenCookieName,
		Path:     "/settings/extension",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
}

func newCreatedTokenDisplayID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func (h *SettingsHandler) rememberCreatedToken(displayID string, token CreatedToken) {
	expiresAt := time.Now().Add(createdTokenDisplayLifetime)
	h.createdTokensMu.Lock()
	h.createdTokens[displayID] = createdTokenDisplay{token: token, expiresAt: expiresAt}
	h.createdTokensMu.Unlock()
	time.AfterFunc(createdTokenDisplayLifetime, func() {
		h.createdTokensMu.Lock()
		if display, ok := h.createdTokens[displayID]; ok && !display.expiresAt.After(time.Now()) {
			delete(h.createdTokens, displayID)
		}
		h.createdTokensMu.Unlock()
	})
}

func (h *SettingsHandler) createdToken(displayID string) *CreatedToken {
	decoded, err := hex.DecodeString(displayID)
	if err != nil || len(decoded) != 16 {
		return nil
	}
	h.createdTokensMu.Lock()
	display, ok := h.createdTokens[displayID]
	h.createdTokensMu.Unlock()
	if !ok || !display.expiresAt.After(time.Now()) {
		return nil
	}
	return &display.token
}

func (h *SettingsHandler) forgetCreatedToken(tokenID int64) string {
	h.createdTokensMu.Lock()
	defer h.createdTokensMu.Unlock()
	for displayID, display := range h.createdTokens {
		if display.token.ID == tokenID {
			delete(h.createdTokens, displayID)
			return displayID
		}
	}
	return ""
}

func (h *SettingsHandler) revoke(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		h.renderer.NotFound(w, internalweb.NotFoundPage{
			Title:       "Extension token not found",
			Heading:     "Extension token not found",
			Message:     "This extension token link is not valid.",
			ReturnURL:   "/settings/extension",
			ReturnLabel: "Back to extension settings",
		})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		h.renderTokenActionError(w, "The token action form is too large or invalid.")
		return
	}
	if r.FormValue("confirmed") != "1" {
		h.renderTokenActionError(w, "Confirm removing this token before continuing.")
		return
	}
	if err := h.tokens.Revoke(r.Context(), id); err != nil && !errors.Is(err, ErrTokenNotFound) {
		internalweb.InternalError(w, r, "could not revoke extension token", err)
		return
	}
	removedDisplayID := h.forgetCreatedToken(id)
	if cookie, err := r.Cookie(createdTokenCookieName); err == nil && cookie.Value == removedDisplayID {
		h.clearCreatedTokenCookie(w, r)
	}
	http.Redirect(w, r, "/settings/extension", http.StatusSeeOther)
}

func (h *SettingsHandler) renderTokenActionError(w http.ResponseWriter, message string) {
	h.renderer.RenderStatus(w, http.StatusBadRequest, "not-found.html", internalweb.NotFoundPage{
		Title:       "Could not remove token",
		Heading:     "Could not remove token",
		Message:     message,
		ReturnURL:   "/settings/extension",
		ReturnLabel: "Back to extension settings",
	})
}

func (h *SettingsHandler) render(w http.ResponseWriter, r *http.Request, created *CreatedToken, tokenName, message string, status int) {
	tokens, ok := h.listTokens(w, r)
	if !ok {
		return
	}
	h.renderPage(w, r, tokens, created, false, tokenName, message, status)
}

func (h *SettingsHandler) listTokens(w http.ResponseWriter, r *http.Request) ([]Token, bool) {
	tokens, err := h.tokens.List(r.Context())
	if err != nil {
		internalweb.InternalError(w, r, "could not load extension tokens", err)
		return nil, false
	}
	return tokens, true
}

func (h *SettingsHandler) renderPage(w http.ResponseWriter, r *http.Request, tokens []Token, created *CreatedToken, createdTokenUnavailable bool, tokenName, message string, status int) {
	location, err := h.loadLocation(r.Context())
	if err != nil {
		internalweb.InternalError(w, r, "could not load extension settings timezone", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	version, minimumChrome := extensionRelease()
	h.renderer.RenderStatus(w, status, "extension-settings.html", SettingsPage{
		Title:                   "Browser extension",
		CSRFToken:               internalweb.CSRFToken(r),
		Tokens:                  tokensExcept(tokens, created),
		CreatedToken:            created,
		TokenName:               tokenName,
		Error:                   message,
		TimeZone:                location.String(),
		ExtensionVersion:        version,
		MinimumChrome:           minimumChrome,
		PublicOrigin:            h.publicOrigin,
		CreatedTokenUnavailable: createdTokenUnavailable,
	})
}

func tokensExcept(tokens []Token, created *CreatedToken) []Token {
	if created == nil {
		return tokens
	}
	other := make([]Token, 0, len(tokens))
	for _, token := range tokens {
		if token.ID != created.ID {
			other = append(other, token)
		}
	}
	return other
}

func extensionRelease() (string, string) {
	contents, err := fs.ReadFile(browserextension.Assets, "manifest.json")
	if err != nil {
		return "unknown", "unknown"
	}
	var manifest struct {
		Version              string `json:"version"`
		MinimumChromeVersion string `json:"minimum_chrome_version"`
	}
	if json.Unmarshal(contents, &manifest) != nil {
		return "unknown", "unknown"
	}
	return manifest.Version, manifest.MinimumChromeVersion
}
