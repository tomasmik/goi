package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	internalweb "github.com/tomasmik/goi/internal/web"
)

const (
	backupFormBodyLimit             = 64 << 10
	RestoreUploadRequestLimit int64 = MaxBundleBytes + (10 << 20)
	DriveClientUploadLimit    int64 = maxGoogleClientFileBytes + (64 << 10)
	defaultDriveListTimeout         = 2 * time.Second
)

type Handler struct {
	store            *Store
	service          *Service
	drive            DriveManager
	dataDir          string
	backupDir        string
	renderer         *internalweb.Renderer
	driveListTimeout time.Duration
}

type Page struct {
	Title                   string
	CSRFToken               string
	Settings                Settings
	State                   StatePage
	LocalFiles              []FilePage
	DriveFiles              []FilePage
	DriveConfigured         bool
	DriveConnected          bool
	DriveCallbackURL        string
	DriveEnvironmentManaged bool
	BackupBusy              bool
	Restore                 RestoreStatus
	Notice                  string
	Error                   string
	LocalDirectory          string
}

type StatePage struct {
	Status      string
	Trigger     string
	LastAttempt string
	LastSuccess string
	Error       string
}

type FilePage struct {
	ID                  string
	Name                string
	Size                string
	Created             string
	Origin              string
	CurrentInstallation bool
}

func NewHandler(store *Store, service *Service, drive DriveManager, dataDir string, renderer *internalweb.Renderer) *Handler {
	return &Handler{
		store:            store,
		service:          service,
		drive:            drive,
		dataDir:          dataDir,
		backupDir:        service.backupDirectory(),
		renderer:         renderer,
		driveListTimeout: defaultDriveListTimeout,
	}
}

func (h *Handler) Routes(router chi.Router) {
	router.Get("/settings/backups", h.get)
	router.Get("/settings/backups/state", h.state)
	router.Post("/settings/backups", h.save)
	router.Post("/settings/backups/run", h.run)
	router.Get("/settings/backups/local/{name}", h.downloadLocal)
	router.Post("/settings/backups/restore/upload", h.restoreUpload)
	router.Post("/settings/backups/restore/local/{name}", h.restoreLocal)
	router.Post("/settings/backups/restore/google/{id}", h.restoreGoogle)
	router.Post("/settings/backups/restore/cancel", h.cancelRestore)
	router.Get("/settings/backups/google/connect", h.connectGoogle)
	router.Get("/settings/backups/google/callback", h.googleCallback)
	router.Post("/settings/backups/google/disconnect", h.disconnectGoogle)
	router.Post("/settings/backups/google/client", h.saveGoogleClient)
	router.Post("/settings/backups/google/client/delete", h.removeGoogleClient)
}

func (h *Handler) state(w http.ResponseWriter, r *http.Request) {
	settings, err := h.store.Settings(r.Context())
	if err != nil {
		internalweb.InternalError(w, r, "could not load backup settings", err)
		return
	}
	state, err := h.store.State(r.Context())
	if err != nil {
		internalweb.InternalError(w, r, "could not load backup status", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(struct {
		State StatePage `json:"state"`
		Busy  bool      `json:"busy"`
	}{State: newStatePage(state, settings.TimeZone), Busy: h.service.Busy()}); err != nil {
		internalweb.LogError(r, "write backup status", err)
	}
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, "")
}

func (h *Handler) save(w http.ResponseWriter, r *http.Request) {
	if !h.parseForm(w, r) {
		return
	}
	hour, err := parseBackupHour(r.PostForm.Get("hour"))
	if err != nil {
		h.render(w, r, http.StatusUnprocessableEntity, "Choose a whole-hour backup time.")
		return
	}
	retentionDays, err := strconv.Atoi(strings.TrimSpace(r.PostForm.Get("retention_days")))
	if err != nil {
		h.render(w, r, http.StatusUnprocessableEntity, "Choose how many days of backups to keep.")
		return
	}
	values := Settings{
		Enabled:       r.PostForm.Get("enabled") == "on",
		Hour:          hour,
		GoogleDrive:   r.PostForm.Get("google_drive") == "on",
		KeepLocal:     r.PostForm.Get("keep_local") == "on",
		RetentionDays: retentionDays,
	}
	if values.GoogleDrive && (h.drive == nil || !h.drive.Connected()) {
		h.render(w, r, http.StatusUnprocessableEntity, "Connect Google Drive before choosing it as a backup destination.")
		return
	}
	if err := h.store.UpdateSettings(r.Context(), values); err != nil {
		h.render(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	h.service.SignalSettingsChanged()
	h.redirect(w, r, "saved")
}

func parseBackupHour(value string) (int, error) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, ":") {
		return strconv.Atoi(value)
	}
	parts := strings.Split(value, ":")
	if len(parts) != 2 || parts[1] != "00" {
		return 0, errors.New("backup time must use a whole hour")
	}
	return strconv.Atoi(parts[0])
}

func (h *Handler) run(w http.ResponseWriter, r *http.Request) {
	if err := h.service.QueueManual(); err != nil {
		if errors.Is(err, ErrBackupRunning) {
			h.render(w, r, http.StatusConflict, "A backup is already queued or running.")
			return
		}
		internalweb.InternalError(w, r, "could not queue backup", err)
		return
	}
	h.redirect(w, r, "queued")
}

func (h *Handler) downloadLocal(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	path, err := LocalPath(h.backupDir, name)
	if err != nil {
		h.renderer.NotFound(w, internalweb.NotFoundPage{
			Title: "Backup not found", Heading: "Backup not found",
			Message: "This local backup is no longer available.", ReturnURL: "/settings/backups", ReturnLabel: "Back to backups",
		})
		return
	}
	file, err := os.Open(path)
	if err != nil {
		internalweb.InternalError(w, r, "could not open backup", err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		internalweb.InternalError(w, r, "could not inspect backup", err)
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func (h *Handler) restoreUpload(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > RestoreUploadRequestLimit {
		h.render(w, r, http.StatusRequestEntityTooLarge, "The backup upload is too large or invalid.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, RestoreUploadRequestLimit)
	parseErr := r.ParseMultipartForm(8 << 20)
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if parseErr != nil {
		status := http.StatusBadRequest
		var maxBytesError *http.MaxBytesError
		if errors.As(parseErr, &maxBytesError) {
			status = http.StatusRequestEntityTooLarge
		}
		h.render(w, r, status, "The backup upload is too large or invalid.")
		return
	}
	file, header, err := r.FormFile("backup")
	if err != nil {
		h.render(w, r, http.StatusUnprocessableEntity, "Choose a Goi backup file.")
		return
	}
	defer file.Close()
	if !strings.HasSuffix(header.Filename, BundleSuffix) {
		h.render(w, r, http.StatusUnprocessableEntity, "Choose a .goi-backup.zip file created by Goi.")
		return
	}
	if err := h.service.QueueUploadedRestore(r.Context(), file); err != nil {
		h.render(w, r, http.StatusUnprocessableEntity, "Could not queue restore: "+err.Error())
		return
	}
	h.redirect(w, r, "restore_pending")
}

func (h *Handler) restoreGoogle(w http.ResponseWriter, r *http.Request) {
	if !h.parseForm(w, r) {
		return
	}
	if r.PostForm.Get("confirmed") != "1" {
		h.render(w, r, http.StatusUnprocessableEntity, "Confirm the Google Drive restore before continuing.")
		return
	}
	if err := h.service.QueueDriveRestore(r.Context(), chi.URLParam(r, "id")); err != nil {
		h.render(w, r, http.StatusUnprocessableEntity, "Could not queue restore: "+err.Error())
		return
	}
	h.redirect(w, r, "restore_pending")
}

func (h *Handler) restoreLocal(w http.ResponseWriter, r *http.Request) {
	if !h.parseForm(w, r) {
		return
	}
	if r.PostForm.Get("confirmed") != "1" {
		h.render(w, r, http.StatusUnprocessableEntity, "Confirm the local restore before continuing.")
		return
	}
	if err := h.service.QueueLocalRestore(r.Context(), chi.URLParam(r, "name")); err != nil {
		h.render(w, r, http.StatusUnprocessableEntity, "Could not queue restore: "+err.Error())
		return
	}
	h.redirect(w, r, "restore_pending")
}

func (h *Handler) cancelRestore(w http.ResponseWriter, r *http.Request) {
	if err := CancelPendingRestore(h.dataDir); err != nil {
		internalweb.InternalError(w, r, "could not cancel restore", err)
		return
	}
	h.redirect(w, r, "restore_cancelled")
}

func (h *Handler) connectGoogle(w http.ResponseWriter, r *http.Request) {
	if h.drive == nil || !h.drive.Configured() {
		h.render(w, r, http.StatusUnprocessableEntity, "Google Drive has not been configured by the server owner.")
		return
	}
	location, err := h.drive.AuthorizationURL()
	if err != nil {
		internalweb.InternalError(w, r, "could not start Google authorization", err)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (h *Handler) googleCallback(w http.ResponseWriter, r *http.Request) {
	if callbackError := strings.TrimSpace(r.URL.Query().Get("error")); callbackError != "" {
		h.render(w, r, http.StatusUnprocessableEntity, "Google Drive was not connected: "+callbackError)
		return
	}
	if err := h.drive.Connect(r.Context(), r.URL.Query().Get("state"), r.URL.Query().Get("code")); err != nil {
		h.render(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	h.redirect(w, r, "google_connected")
}

func (h *Handler) disconnectGoogle(w http.ResponseWriter, r *http.Request) {
	if !h.parseForm(w, r) {
		return
	}
	if r.PostForm.Get("confirmed") != "1" {
		h.render(w, r, http.StatusUnprocessableEntity, "Confirm that you want to disconnect Google Drive.")
		return
	}
	if err := h.service.DisconnectDrive(r.Context()); err != nil {
		if errors.Is(err, ErrBackupRunning) {
			h.render(w, r, http.StatusConflict, "Wait for the current backup to finish before disconnecting Google Drive.")
			return
		}
		internalweb.InternalError(w, r, "could not disconnect Google Drive", err)
		return
	}
	h.redirect(w, r, "google_disconnected")
}

func (h *Handler) saveGoogleClient(w http.ResponseWriter, r *http.Request) {
	if h.drive == nil {
		h.render(w, r, http.StatusUnprocessableEntity, "Google Drive setup is not available.")
		return
	}
	if r.ContentLength > DriveClientUploadLimit {
		h.render(w, r, http.StatusRequestEntityTooLarge, "The Google OAuth client file is too large or invalid.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, DriveClientUploadLimit)
	parseErr := r.ParseMultipartForm(64 << 10)
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if parseErr != nil {
		status := http.StatusBadRequest
		var maxBytesError *http.MaxBytesError
		if errors.As(parseErr, &maxBytesError) {
			status = http.StatusRequestEntityTooLarge
		}
		h.render(w, r, status, "The Google OAuth client file is too large or invalid.")
		return
	}
	file, header, err := r.FormFile("client")
	if err != nil {
		h.render(w, r, http.StatusUnprocessableEntity, "Choose the OAuth client JSON downloaded from Google.")
		return
	}
	defer file.Close()
	if !strings.HasSuffix(strings.ToLower(header.Filename), ".json") {
		h.render(w, r, http.StatusUnprocessableEntity, "Choose a .json OAuth client file downloaded from Google.")
		return
	}
	if err := h.drive.SaveClient(file); err != nil {
		h.render(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	h.redirect(w, r, "google_client_saved")
}

func (h *Handler) removeGoogleClient(w http.ResponseWriter, r *http.Request) {
	if !h.parseForm(w, r) {
		return
	}
	if h.drive == nil {
		h.render(w, r, http.StatusUnprocessableEntity, "Google Drive setup is not available.")
		return
	}
	if r.PostForm.Get("confirmed") != "1" {
		h.render(w, r, http.StatusUnprocessableEntity, "Confirm that you want to remove the Google OAuth client.")
		return
	}
	if err := h.drive.RemoveClient(); err != nil {
		h.render(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	h.redirect(w, r, "google_client_removed")
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, message string) {
	settings, err := h.store.Settings(r.Context())
	if err != nil {
		internalweb.InternalError(w, r, "could not load backup settings", err)
		return
	}
	state, err := h.store.State(r.Context())
	if err != nil {
		internalweb.InternalError(w, r, "could not load backup status", err)
		return
	}
	localFiles, err := ListLocal(h.backupDir)
	if err != nil {
		internalweb.InternalError(w, r, "could not list local backups", err)
		return
	}
	restoreStatus, err := ReadRestoreStatus(h.dataDir)
	if err != nil {
		internalweb.InternalError(w, r, "could not load restore status", err)
		return
	}
	page := Page{
		Title:           "Backups",
		CSRFToken:       internalweb.CSRFToken(r),
		Settings:        settings,
		State:           newStatePage(state, settings.TimeZone),
		LocalFiles:      localFilePages(localFiles, settings.TimeZone),
		DriveConfigured: h.drive != nil && h.drive.Configured(),
		DriveConnected:  h.drive != nil && h.drive.Connected(),
		BackupBusy:      h.service.Busy(),
		Restore:         restoreStatus,
		Notice:          backupNotice(r.URL.Query().Get("result"), state.Status, h.service.Busy()),
		Error:           message,
		LocalDirectory:  h.backupDir,
	}
	if h.drive != nil {
		page.DriveCallbackURL = h.drive.CallbackURL()
		page.DriveEnvironmentManaged = h.drive.EnvironmentManaged()
	}
	if page.DriveConnected {
		listContext, cancel := context.WithTimeout(r.Context(), h.driveListTimeout)
		remoteFiles, listErr := h.drive.List(listContext)
		cancel()
		if listErr != nil {
			if page.Error == "" {
				page.Error = "Could not list Google Drive backups: " + listErr.Error()
			}
		} else {
			page.DriveFiles = remoteFilePages(remoteFiles, settings.TimeZone)
		}
	}
	h.renderer.RenderStatus(w, status, "backups.html", page)
}

func (h *Handler) parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, backupFormBodyLimit)
	if err := r.ParseForm(); err != nil {
		h.render(w, r, http.StatusBadRequest, "The backup form is too large or invalid.")
		return false
	}
	return true
}

func (h *Handler) redirect(w http.ResponseWriter, r *http.Request, result string) {
	http.Redirect(w, r, "/settings/backups?result="+result, http.StatusSeeOther)
}

func newStatePage(state State, timeZone string) StatePage {
	return StatePage{
		Status:      state.Status,
		Trigger:     state.Trigger,
		LastAttempt: formatTime(state.LastAttemptAt, timeZone),
		LastSuccess: formatTime(state.LastSuccessAt, timeZone),
		Error:       state.ErrorMessage,
	}
}

func localFilePages(files []LocalFile, timeZone string) []FilePage {
	pages := make([]FilePage, 0, len(files))
	for _, file := range files {
		pages = append(pages, FilePage{Name: file.Name, Size: formatBytes(file.Size), Created: formatFileTime(file.CreatedAt, timeZone)})
	}
	return pages
}

func remoteFilePages(files []RemoteBackup, timeZone string) []FilePage {
	pages := make([]FilePage, 0, len(files))
	for _, file := range files {
		origin := "Another Goi server"
		if file.CurrentInstallation {
			origin = "This server"
		}
		pages = append(pages, FilePage{
			ID:                  file.ID,
			Name:                file.Name,
			Size:                formatBytes(file.Size),
			Created:             formatFileTime(file.CreatedAt, timeZone),
			Origin:              origin,
			CurrentInstallation: file.CurrentInstallation,
		})
	}
	return pages
}

func formatFileTime(value time.Time, timeZone string) string {
	if value.IsZero() {
		return "Unknown time"
	}
	location, err := time.LoadLocation(timeZone)
	if err != nil {
		location = time.UTC
	}
	return value.In(location).Format("2006-01-02 15:04:05 MST")
}

func formatTime(value time.Time, timeZone string) string {
	if value.IsZero() {
		return ""
	}
	location, err := time.LoadLocation(timeZone)
	if err != nil {
		location = time.UTC
	}
	return value.In(location).Format("2006-01-02 15:04")
}

func formatBytes(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	if size < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(size)/1024)
	}
	if size < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(size)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(size)/(1024*1024*1024))
}

func backupNotice(result, state string, busy bool) string {
	switch result {
	case "saved":
		return "Backup settings saved."
	case "queued":
		if busy || state == "running" {
			return "Backup queued. This page will update when it finishes."
		}
		if state == "success" {
			return "Backup completed."
		}
		return ""
	case "restore_pending":
		return "Restore verified and queued. Restart Goi before making more changes."
	case "restore_cancelled":
		return "Pending restore cancelled."
	case "google_connected":
		return "Google Drive connected."
	case "google_disconnected":
		return "Google Drive disconnected. Backups will stay local."
	case "google_client_saved":
		return "Google OAuth client saved. You can connect Google Drive now."
	case "google_client_removed":
		return "Google OAuth client removed."
	default:
		return ""
	}
}
