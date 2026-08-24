package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"
	"golang.org/x/time/rate"

	internalweb "github.com/tomasmik/goi/internal/web"
)

const (
	authenticatedKey = "authenticated"
	credentialKey    = "credential_version"
	credentialPrefix = "v2:"
	loginBurst       = 10
	maxLoginClients  = 10_000
	loginClientTTL   = 10 * time.Minute
)

type Store struct {
	usernameHash  [sha256.Size]byte
	passwordHash  [sha256.Size]byte
	credentialKey [sha256.Size]byte
}

type Handler struct {
	store         *Store
	sessions      *scs.SessionManager
	renderer      *internalweb.Renderer
	enabled       bool
	loginLimitsMu sync.Mutex
	loginLimits   map[string]*loginLimit
}

type loginLimit struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type Page struct {
	Title     string
	CSRFToken string
	Error     string
	ReturnTo  string
	Username  string
}

var errInvalidCredentials = errors.New("invalid credentials")

func NewStore(username, password string) *Store {
	username = strings.TrimSpace(username)
	return &Store{
		usernameHash:  sha256.Sum256([]byte(username)),
		passwordHash:  sha256.Sum256([]byte(password)),
		credentialKey: sha256.Sum256([]byte(username + "\x00" + password)),
	}
}

func NewSessionManager(db *sql.DB, secure bool) *scs.SessionManager {
	manager := scs.New()
	manager.Store = newSessionStore(db)
	manager.Lifetime = 30 * 24 * time.Hour
	manager.Cookie.Name = "goi_session"
	manager.Cookie.HttpOnly = true
	manager.Cookie.Path = "/"
	manager.Cookie.SameSite = http.SameSiteLaxMode
	manager.Cookie.Secure = secure
	return manager
}

func NewHandler(store *Store, sessions *scs.SessionManager, renderer *internalweb.Renderer, enabled bool) *Handler {
	return &Handler{
		store:       store,
		sessions:    sessions,
		renderer:    renderer,
		enabled:     enabled,
		loginLimits: make(map[string]*loginLimit),
	}
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/login", h.loginForm)
	r.Post("/login", h.login)
	r.Post("/logout", h.logout)
}

func (h *Handler) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.enabled || publicPath(r.URL.Path) || h.authenticated(r.Context()) {
			next.ServeHTTP(w, r)
			return
		}
		loginURL := "/login"
		returnTo := ""
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			returnTo = safeReturnTo(r.URL.RequestURI())
		}
		if returnTo != "" {
			loginURL += "?return_to=" + url.QueryEscape(returnTo)
		}
		http.Redirect(w, r, loginURL, http.StatusSeeOther)
	})
}

func (h *Handler) loginForm(w http.ResponseWriter, r *http.Request) {
	if !h.enabled {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	h.renderLogin(w, r, "", safeReturnTo(r.URL.Query().Get("return_to")), "")
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.enabled {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		h.renderLogin(w, r, "Could not read the sign-in form.", "", "")
		return
	}
	returnTo := safeReturnTo(r.FormValue("return_to"))
	username := r.FormValue("username")
	if !h.allowLogin(r) {
		w.Header().Set("Retry-After", "60")
		h.renderer.RenderStatus(w, http.StatusTooManyRequests, "login.html", Page{
			Title:     "Sign in",
			CSRFToken: internalweb.CSRFToken(r),
			Error:     "Too many attempts. Try again shortly.",
			ReturnTo:  returnTo,
			Username:  username,
		})
		return
	}
	if err := h.store.Authenticate(username, r.FormValue("password")); err != nil {
		h.renderLogin(w, r, "Incorrect credentials.", returnTo, username)
		return
	}
	if err := h.sessions.RenewToken(r.Context()); err != nil {
		internalweb.InternalError(w, r, "could not start session", err)
		return
	}
	sessionToken := h.sessions.Token(r.Context())
	if sessionToken == "" {
		internalweb.InternalError(w, r, "could not start session", errors.New("session token is empty"))
		return
	}
	h.sessions.Put(r.Context(), authenticatedKey, true)
	h.sessions.Put(r.Context(), credentialKey, h.store.sessionCredentialMarker(sessionToken))
	if returnTo == "" {
		returnTo = "/dashboard"
	}
	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}

func (h *Handler) renderLogin(w http.ResponseWriter, r *http.Request, message, returnTo, username string) {
	h.renderer.Render(w, "login.html", Page{
		Title:     "Sign in",
		CSRFToken: internalweb.CSRFToken(r),
		Error:     message,
		ReturnTo:  returnTo,
		Username:  username,
	})
}

func (h *Handler) authenticated(ctx context.Context) bool {
	if !h.sessions.GetBool(ctx, authenticatedKey) {
		return false
	}
	sessionToken := h.sessions.Token(ctx)
	storedMarker := h.sessions.GetString(ctx, credentialKey)
	if sessionToken == "" || storedMarker == "" {
		return false
	}
	expectedMarker := h.store.sessionCredentialMarker(sessionToken)
	return subtle.ConstantTimeCompare([]byte(storedMarker), []byte(expectedMarker)) == 1
}

func (h *Handler) allowLogin(r *http.Request) bool {
	address := r.RemoteAddr
	if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	}
	if address == "" {
		address = "unknown"
	}
	now := time.Now()
	h.loginLimitsMu.Lock()
	defer h.loginLimitsMu.Unlock()
	if limit := h.loginLimits[address]; limit != nil {
		limit.lastSeen = now
		return limit.limiter.Allow()
	}
	if len(h.loginLimits) >= maxLoginClients {
		oldestKey := ""
		var oldest time.Time
		for key, limit := range h.loginLimits {
			if now.Sub(limit.lastSeen) > loginClientTTL {
				delete(h.loginLimits, key)
				continue
			}
			if oldestKey == "" || limit.lastSeen.Before(oldest) {
				oldestKey = key
				oldest = limit.lastSeen
			}
		}
		if len(h.loginLimits) >= maxLoginClients && oldestKey != "" {
			delete(h.loginLimits, oldestKey)
		}
	}
	limit := &loginLimit{limiter: rate.NewLimiter(rate.Every(time.Minute/loginBurst), loginBurst)}
	h.loginLimits[address] = limit
	limit.lastSeen = now
	return limit.limiter.Allow()
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.sessions.Destroy(r.Context()); err != nil {
		internalweb.InternalError(w, r, "could not end session", err)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Store) Authenticate(username, password string) error {
	usernameHash := sha256.Sum256([]byte(strings.TrimSpace(username)))
	passwordHash := sha256.Sum256([]byte(password))
	usernameMatch := subtle.ConstantTimeCompare(usernameHash[:], s.usernameHash[:])
	passwordMatch := subtle.ConstantTimeCompare(passwordHash[:], s.passwordHash[:])
	if usernameMatch != 1 || passwordMatch != 1 {
		return errInvalidCredentials
	}
	return nil
}

func (s *Store) sessionCredentialMarker(sessionToken string) string {
	mac := hmac.New(sha256.New, s.credentialKey[:])
	_, _ = mac.Write([]byte("goi-session-credential-v1\x00"))
	_, _ = mac.Write([]byte(sessionToken))
	return credentialPrefix + hex.EncodeToString(mac.Sum(nil))
}

func publicPath(path string) bool {
	return path == "/healthz" || path == "/readyz" || path == "/login" || strings.HasPrefix(path, "/static/")
}

func safeReturnTo(value string) string {
	if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\\\r\n") {
		return ""
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil || parsed.Fragment != "" {
		return ""
	}
	if strings.ContainsAny(parsed.Path, "\\\r\n") ||
		!strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") || parsed.Path == "/login" {
		return ""
	}
	return parsed.RequestURI()
}
