package lessons

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	internalweb "github.com/tomasmik/goi/internal/web"
)

type ReviewStarter interface {
	StartLesson(context.Context, int64, []int64) (int64, error)
}

type Handler struct {
	store    *Store
	reviews  ReviewStarter
	renderer *internalweb.Renderer
}

const lessonPickerPageSize = 40

type PickerPage struct {
	Title            string
	CSRFToken        string
	Items            []AvailableItem
	TotalCount       int
	Page             int
	PageCount        int
	PreviousURL      string
	NextURL          string
	ActiveSession    Session
	HasActiveSession bool
	Error            string
}

type SessionPage struct {
	Title     string
	CSRFToken string
	Session   Session
}

func NewHandler(store *Store, reviews ReviewStarter, renderer *internalweb.Renderer) *Handler {
	return &Handler{store: store, reviews: reviews, renderer: renderer}
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/lessons", h.list)
	r.Post("/lessons/start", h.start)
	r.Post("/lessons/start-next", h.startNext)
	r.Get("/lessons/session/{id}", h.session)
	r.Post("/lessons/session/{id}/return", h.returnToQueue)
	r.Get("/lessons/session/{id}/word/{position}", h.legacyStudyWord)
	r.Post("/lessons/session/{id}/word/{position}", h.studyWord)
	r.Post("/lessons/session/{id}/review", h.review)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	h.renderPicker(w, r, http.StatusOK, "", requestedLessonPage(r.URL.Query().Get("page")), lessonSelection(r.URL.Query()["selected"]))
}

func lessonSelection(values []string) []int64 {
	if values == nil {
		return nil
	}
	ids := make([]int64, 0, len(values))
	seen := make(map[int64]struct{}, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func (h *Handler) renderPicker(w http.ResponseWriter, r *http.Request, status int, message string, requestedPage int, selectedIDs []int64) {
	var activeSession Session
	activeSessionID, hasActiveSession, err := h.store.ActiveSession(r.Context())
	if err != nil {
		internalweb.InternalError(w, r, "could not load active lesson", err)
		return
	}
	if hasActiveSession {
		activeSession, err = h.store.Current(r.Context(), activeSessionID)
		if err != nil {
			internalweb.InternalError(w, r, "could not load active lesson", err)
			return
		}
	}
	totalCount, err := h.store.AvailableCount(r.Context())
	if err != nil {
		internalweb.InternalError(w, r, "could not load lesson vocabulary", err)
		return
	}
	page, pageCount := lessonPage(requestedPage, totalCount)
	items, err := h.store.AvailablePage(r.Context(), lessonPickerPageSize, (page-1)*lessonPickerPageSize)
	if err != nil {
		internalweb.InternalError(w, r, "could not load lesson vocabulary", err)
		return
	}
	applyPickerSelection(items, selectedIDs)
	var previousURL, nextURL string
	if page > 1 {
		previousURL = lessonListURL(page - 1)
	}
	if page < pageCount {
		nextURL = lessonListURL(page + 1)
	}
	h.renderer.RenderStatus(w, status, "lessons.html", PickerPage{
		Title:            "Lessons",
		CSRFToken:        internalweb.CSRFToken(r),
		Items:            items,
		TotalCount:       totalCount,
		Page:             page,
		PageCount:        pageCount,
		PreviousURL:      previousURL,
		NextURL:          nextURL,
		ActiveSession:    activeSession,
		HasActiveSession: hasActiveSession,
		Error:            message,
	})
}

func requestedLessonPage(value string) int {
	page, err := strconv.Atoi(value)
	if err != nil || page <= 0 {
		return 1
	}
	return page
}

func lessonPage(page, total int) (int, int) {
	pageCount := 1
	if total > 0 {
		pageCount = 1 + (total-1)/lessonPickerPageSize
	}
	if page > pageCount {
		page = pageCount
	}
	return page, pageCount
}

func lessonListURL(page int) string {
	if page <= 1 {
		return "/lessons"
	}
	return "/lessons?page=" + strconv.Itoa(page)
}

func applyPickerSelection(items []AvailableItem, selectedIDs []int64) {
	if selectedIDs == nil {
		return
	}
	selected := make(map[int64]struct{}, len(selectedIDs))
	for _, id := range selectedIDs {
		selected[id] = struct{}{}
	}
	for index := range items {
		_, items[index].Selected = selected[items[index].ID]
	}
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	page := requestedLessonPage(r.URL.Query().Get("page"))
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		h.renderPicker(w, r, http.StatusBadRequest, "could not read selection", page, nil)
		return
	}
	if submittedPage := r.PostForm.Get("page"); submittedPage != "" {
		page = requestedLessonPage(submittedPage)
	}
	values := r.Form["vocabulary_id"]
	ids := make([]int64, 0, len(values))
	invalidSelection := false
	for _, value := range values {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			invalidSelection = true
			continue
		}
		ids = append(ids, id)
	}
	if invalidSelection {
		h.renderPicker(w, r, http.StatusBadRequest, "the selection contains an invalid word", page, ids)
		return
	}
	sessionID, err := h.store.Start(r.Context(), ids)
	if err != nil {
		if message, ok := internalweb.UserErrorMessage(err); ok {
			h.renderPicker(w, r, http.StatusConflict, message, page, ids)
		} else {
			internalweb.InternalError(w, r, "could not start lesson", err)
		}
		return
	}
	http.Redirect(w, r, "/lessons/session/"+strconv.FormatInt(sessionID, 10), http.StatusSeeOther)
}

func (h *Handler) startNext(w http.ResponseWriter, r *http.Request) {
	if sessionID, found, err := h.store.ActiveSession(r.Context()); err != nil {
		internalweb.InternalError(w, r, "could not load active lesson", err)
		return
	} else if found {
		http.Redirect(w, r, "/lessons/session/"+strconv.FormatInt(sessionID, 10), http.StatusSeeOther)
		return
	}
	ids, err := h.store.NextBatch(r.Context())
	if err != nil {
		if message, ok := internalweb.UserErrorMessage(err); ok {
			h.renderPicker(w, r, http.StatusConflict, message, requestedLessonPage(r.URL.Query().Get("page")), nil)
		} else {
			internalweb.InternalError(w, r, "could not select lesson vocabulary", err)
		}
		return
	}
	sessionID, err := h.store.Start(r.Context(), ids)
	if err != nil {
		if message, ok := internalweb.UserErrorMessage(err); ok {
			h.renderPicker(w, r, http.StatusConflict, message, requestedLessonPage(r.URL.Query().Get("page")), nil)
		} else {
			internalweb.InternalError(w, r, "could not start lesson", err)
		}
		return
	}
	http.Redirect(w, r, "/lessons/session/"+strconv.FormatInt(sessionID, 10), http.StatusSeeOther)
}

func (h *Handler) session(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	session, err := h.store.Current(r.Context(), sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	}
	if err != nil {
		internalweb.InternalError(w, r, "could not load lesson", err)
		return
	}
	if session.Status == "abandoned" {
		http.Redirect(w, r, "/lessons", http.StatusSeeOther)
		return
	}
	if session.Status == "completed" {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	if session.Status == "active" && session.Phase == "review" && session.ReviewSessionID > 0 {
		http.Redirect(w, r, "/reviews/session/"+strconv.FormatInt(session.ReviewSessionID, 10), http.StatusSeeOther)
		return
	}
	page := SessionPage{Title: "Lesson", CSRFToken: internalweb.CSRFToken(r), Session: session}
	if r.Header.Get("X-Goi-Fragment") == "lesson" {
		h.renderer.Render(w, "lesson-session-body", page)
		return
	}
	h.renderer.Render(w, "lesson-session.html", page)
}

func (h *Handler) returnToQueue(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	if err := h.store.ReturnToQueue(r.Context(), sessionID); err != nil {
		if message, ok := internalweb.UserErrorMessage(err); ok {
			h.renderRecovery(
				w,
				http.StatusConflict,
				"Lesson changed",
				message,
				lessonSessionURL(sessionID),
				"Back to lesson",
			)
			return
		}
		internalweb.InternalError(w, r, "could not return lesson words to the queue", err)
		return
	}
	http.Redirect(w, r, "/lessons", http.StatusSeeOther)
}

func (h *Handler) review(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	session, err := h.store.Current(r.Context(), sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	}
	if err != nil {
		internalweb.InternalError(w, r, "could not load lesson", err)
		return
	}
	if session.Status == "completed" {
		http.Redirect(w, r, "/lessons/session/"+strconv.FormatInt(sessionID, 10), http.StatusSeeOther)
		return
	}
	if session.Phase != "study" {
		returnURL := lessonSessionURL(sessionID)
		returnLabel := "Back to lesson"
		message := "This lesson batch is no longer ready to start a new review."
		if session.ReviewSessionID > 0 {
			returnURL = "/reviews/session/" + strconv.FormatInt(session.ReviewSessionID, 10)
			returnLabel = "Continue review"
			message = "A review has already started for this lesson batch."
		}
		h.renderRecovery(w, http.StatusConflict, "Review already started", message, returnURL, returnLabel)
		return
	}
	if err := h.store.MarkCurrentViewed(r.Context(), sessionID); err != nil {
		h.writeStoreError(w, r, err, "could not finish lesson word", sessionID)
		return
	}
	session, err = h.store.Current(r.Context(), sessionID)
	if err != nil {
		internalweb.InternalError(w, r, "could not reload lesson", err)
		return
	}
	if len(session.Items) == 0 || !session.StudyReady {
		h.renderRecovery(
			w,
			http.StatusConflict,
			"Review is not ready",
			"Open every word in this lesson batch before starting its review.",
			lessonSessionURL(sessionID),
			"Back to lesson",
		)
		return
	}
	ids := make([]int64, 0, len(session.Items))
	for _, item := range session.Items {
		ids = append(ids, item.ID)
	}
	reviewID, err := h.reviews.StartLesson(r.Context(), sessionID, ids)
	if err != nil {
		h.writeStoreError(w, r, err, "could not start lesson review", sessionID)
		return
	}
	http.Redirect(w, r, "/reviews/session/"+strconv.FormatInt(reviewID, 10), http.StatusSeeOther)
}

func (h *Handler) studyWord(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	position, err := strconv.Atoi(chi.URLParam(r, "position"))
	if err != nil || position < 0 {
		h.notFound(w)
		return
	}
	if err := h.store.SelectStudyItem(r.Context(), sessionID, position); err != nil {
		h.writeStoreError(w, r, err, "could not select lesson word", sessionID)
		return
	}
	if r.Header.Get("X-Goi-Fragment") == "lesson" {
		session, err := h.store.Current(r.Context(), sessionID)
		if err != nil {
			internalweb.InternalError(w, r, "could not load lesson", err)
			return
		}
		h.renderer.Render(w, "lesson-session-body", SessionPage{Title: "Lesson", CSRFToken: internalweb.CSRFToken(r), Session: session})
		return
	}
	http.Redirect(w, r, "/lessons/session/"+strconv.FormatInt(sessionID, 10), http.StatusSeeOther)
}

func (h *Handler) legacyStudyWord(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	http.Redirect(w, r, "/lessons/session/"+strconv.FormatInt(sessionID, 10), http.StatusSeeOther)
}

func (h *Handler) notFound(w http.ResponseWriter) {
	h.renderer.NotFound(w, internalweb.NotFoundPage{
		Title:       "Lesson not found",
		Heading:     "Lesson not found",
		Message:     "This lesson may have ended, or the link may be out of date.",
		ReturnURL:   "/lessons",
		ReturnLabel: "Back to lessons",
	})
}

func parseID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	return id, err == nil && id > 0
}

func (h *Handler) writeStoreError(w http.ResponseWriter, r *http.Request, err error, fallback string, sessionID int64) {
	if message, ok := internalweb.UserErrorMessage(err); ok {
		h.renderRecovery(
			w,
			http.StatusConflict,
			"Lesson changed",
			message,
			lessonSessionURL(sessionID),
			"Back to lesson",
		)
		return
	}
	internalweb.InternalError(w, r, fallback, err)
}

func (h *Handler) renderRecovery(w http.ResponseWriter, status int, heading, message, returnURL, returnLabel string) {
	h.renderer.RenderStatus(w, status, "not-found.html", internalweb.NotFoundPage{
		Title:       heading,
		Heading:     heading,
		Message:     message,
		ReturnURL:   returnURL,
		ReturnLabel: returnLabel,
	})
}

func lessonSessionURL(sessionID int64) string {
	return "/lessons/session/" + strconv.FormatInt(sessionID, 10)
}
