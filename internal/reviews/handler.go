package reviews

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	internalweb "github.com/tomasmik/goi/internal/web"
)

type Handler struct {
	store           *Store
	lessonCompleter LessonBatchCompleter
	renderer        *internalweb.Renderer
}

type LessonBatchCompleter interface {
	CompleteReviewedBatch(context.Context, int64) error
}

type LaunchPage struct {
	Title     string
	CSRFToken string
	Error     string
	Due       int
	Active    *State
}

type PracticePage struct {
	Title     string
	CSRFToken string
	Error     string
	Study     StudyCounts
}

type SessionPage struct {
	Title        string
	CSRFToken    string
	State        State
	Notice       string
	Confirmation *AnswerConfirmation
	Revealed     bool
}

type AnswerConfirmation struct {
	PromptID int64
	Answer   string
	Correct  bool
	Retry    bool
}

type LeechesPage struct {
	Title     string
	CSRFToken string
	Items     []LeechItem
}

func NewHandler(store *Store, lessonCompleter LessonBatchCompleter, renderer *internalweb.Renderer) *Handler {
	return &Handler{store: store, lessonCompleter: lessonCompleter, renderer: renderer}
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/reviews", h.launch)
	r.Get("/practice", h.practice)
	r.Get("/leeches", h.leeches)
	r.Post("/reviews/start", h.start)
	r.Get("/reviews/session/{id}", h.session)
	r.Post("/reviews/session/{id}/answer", h.answer)
	r.Post("/reviews/session/{id}/confirm", h.confirmAnswer)
	r.Post("/reviews/session/{id}/accept-failure", h.acceptFailure)
	r.Post("/reviews/session/{id}/reveal", h.revealAnswer)
	r.Post("/reviews/session/{id}/grade", h.gradeAnswer)
	r.Post("/reviews/session/{id}/mark-correct", h.markCorrect)
	r.Post("/reviews/session/{id}/show-answer", h.showAnswer)
	r.Post("/reviews/session/{id}/synonym", h.addSynonym)
	r.Post("/reviews/session/{id}/continue", h.continueReview)
	r.Post("/reviews/session/{id}/pause", h.pause)
	r.Post("/reviews/session/{id}/resume", h.resume)
	r.Post("/reviews/session/{id}/undo", h.undo)
	r.Post("/study/recent-lessons", h.study)
	r.Post("/study/recent-mistakes", h.study)
	r.Post("/study/current", h.study)
	r.Post("/study/leeches", h.study)
	r.Post("/study/selected", h.study)
}

func (h *Handler) leeches(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.Leeches(r.Context())
	if err != nil {
		internalweb.InternalError(w, r, "could not load leeches", err)
		return
	}
	h.renderer.Render(w, "leeches.html", LeechesPage{Title: "Difficult words", CSRFToken: internalweb.CSRFToken(r), Items: items})
}

func (h *Handler) study(w http.ResponseWriter, r *http.Request) {
	source := strings.TrimPrefix(r.URL.Path, "/study/")
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		h.renderPractice(w, r, http.StatusBadRequest, "Could not read the practice selection.")
		return
	}
	selected := make([]int64, 0)
	for _, value := range r.Form["vocabulary_id"] {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			h.renderPractice(w, r, http.StatusBadRequest, "The practice selection contains an invalid word.")
			return
		}
		selected = append(selected, id)
	}
	sessionID, err := h.store.StartExtraSource(r.Context(), source, selected)
	if err != nil {
		if message, ok := internalweb.UserErrorMessage(err); ok {
			h.renderPractice(w, r, http.StatusConflict, message)
		} else {
			internalweb.InternalError(w, r, "could not start extra study", err)
		}
		return
	}
	http.Redirect(w, r, "/reviews/session/"+strconv.FormatInt(sessionID, 10), http.StatusSeeOther)
}

func (h *Handler) pause(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	if err := h.store.Pause(r.Context(), sessionID); err != nil {
		h.writeStoreError(w, r, err, "could not pause review", sessionID)
		return
	}
	http.Redirect(w, r, "/reviews/session/"+strconv.FormatInt(sessionID, 10), http.StatusSeeOther)
}

func (h *Handler) resume(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	if err := h.store.Resume(r.Context(), sessionID); err != nil {
		h.writeStoreError(w, r, err, "could not resume review", sessionID)
		return
	}
	http.Redirect(w, r, "/reviews/session/"+strconv.FormatInt(sessionID, 10), http.StatusSeeOther)
}

func (h *Handler) undo(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	if err := h.store.Undo(r.Context(), sessionID); err != nil {
		h.writeStoreError(w, r, err, "could not undo review", sessionID)
		return
	}
	http.Redirect(w, r, "/reviews/session/"+strconv.FormatInt(sessionID, 10), http.StatusSeeOther)
}

func (h *Handler) launch(w http.ResponseWriter, r *http.Request) {
	h.renderLaunch(w, r, http.StatusOK, "")
}

func (h *Handler) practice(w http.ResponseWriter, r *http.Request) {
	h.renderPractice(w, r, http.StatusOK, "")
}

func (h *Handler) renderLaunch(w http.ResponseWriter, r *http.Request, status int, message string) {
	due, err := h.store.DueCount(r.Context())
	if err != nil {
		internalweb.InternalError(w, r, "could not load due reviews", err)
		return
	}
	var active *State
	if sessionID, found, err := h.store.activeStandaloneSession(r.Context(), "normal"); err != nil {
		internalweb.InternalError(w, r, "could not load active reviews", err)
		return
	} else if found {
		state, err := h.store.State(r.Context(), sessionID)
		if err != nil {
			internalweb.InternalError(w, r, "could not load active reviews", err)
			return
		}
		active = &state
	}
	h.renderer.RenderStatus(w, status, "reviews.html", LaunchPage{
		Title:     "Reviews",
		CSRFToken: internalweb.CSRFToken(r),
		Error:     message,
		Due:       due,
		Active:    active,
	})
}

func (h *Handler) renderPractice(w http.ResponseWriter, r *http.Request, status int, message string) {
	study, err := h.store.StudyCounts(r.Context())
	if err != nil {
		internalweb.InternalError(w, r, "could not load practice lists", err)
		return
	}
	h.renderer.RenderStatus(w, status, "practice.html", PracticePage{
		Title:     "Extra practice",
		CSRFToken: internalweb.CSRFToken(r),
		Error:     message,
		Study:     study,
	})
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	sessionID, err := h.store.StartNormal(r.Context(), normalSessionLimit)
	if err != nil {
		if message, ok := internalweb.UserErrorMessage(err); ok {
			h.renderLaunch(w, r, http.StatusConflict, message)
		} else {
			internalweb.InternalError(w, r, "could not start reviews", err)
		}
		return
	}
	http.Redirect(w, r, "/reviews/session/"+strconv.FormatInt(sessionID, 10), http.StatusSeeOther)
}

func (h *Handler) session(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	state, err := h.store.State(r.Context(), sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	}
	if err != nil {
		internalweb.InternalError(w, r, "could not load review", err)
		return
	}
	if state.Status == "abandoned" {
		http.Redirect(w, r, "/reviews", http.StatusSeeOther)
		return
	}
	if h.finishCompletedReview(w, r, state) {
		return
	}
	page := SessionPage{Title: "Review", CSRFToken: internalweb.CSRFToken(r), State: state}
	if r.URL.Query().Get("synonym") == "added" {
		page.Notice = "Meaning added and answer marked correct."
	}
	if r.URL.Query().Get("corrected") == "1" {
		page.Notice = "Answer marked correct."
	}
	if r.Header.Get("X-Goi-Fragment") == "review" {
		h.renderer.Render(w, "review-session-body", page)
		return
	}
	h.renderer.Render(w, "review-session.html", page)
}

func (h *Handler) notFound(w http.ResponseWriter) {
	h.renderer.NotFound(w, internalweb.NotFoundPage{
		Title:       "Review not found",
		Heading:     "Review not found",
		Message:     "This review session may have ended, or the link may be out of date.",
		ReturnURL:   "/reviews",
		ReturnLabel: "Back to reviews",
	})
}

func (h *Handler) answer(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	promptID, answer, ok := h.parseAnswer(w, r, sessionID)
	if !ok {
		return
	}
	before, err := h.store.State(r.Context(), sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	}
	if err != nil {
		internalweb.InternalError(w, r, "could not load review", err)
		return
	}
	if before.AnswerMode != reviewModeTyped {
		h.writeStoreError(w, r, stateError("this review uses self grading"), "could not check review answer", sessionID)
		return
	}
	match, err := h.store.CheckAnswer(r.Context(), sessionID, promptID, answer)
	if err != nil {
		h.writeStoreError(w, r, err, "could not check review answer", sessionID)
		return
	}
	if match != Incorrect {
		if before.AutoAdvance {
			if _, err := h.store.ConfirmAnswer(r.Context(), sessionID, promptID, answer); err != nil {
				h.writeStoreError(w, r, err, "could not confirm review answer", sessionID)
				return
			}
			h.respondAfterReviewAction(w, r, sessionID, "")
			return
		}
		page := SessionPage{
			Title:     "Review",
			CSRFToken: internalweb.CSRFToken(r),
			State:     before,
			Confirmation: &AnswerConfirmation{
				PromptID: promptID,
				Answer:   answer,
				Correct:  true,
				Retry:    before.ActiveRetry,
			},
		}
		if r.Header.Get("X-Goi-Fragment") == "review" {
			h.renderer.Render(w, "review-session-body", page)
			return
		}
		h.renderer.Render(w, "review-session.html", page)
		return
	}

	if _, err := h.store.Answer(r.Context(), sessionID, promptID, answer); err != nil {
		h.writeStoreError(w, r, err, "could not record review answer", sessionID)
		return
	}
	state, err := h.store.State(r.Context(), sessionID)
	if err != nil {
		internalweb.InternalError(w, r, "could not load review feedback", err)
		return
	}
	page := SessionPage{Title: "Review", CSRFToken: internalweb.CSRFToken(r), State: state}
	page.Confirmation = &AnswerConfirmation{PromptID: promptID, Answer: answer}
	if r.Header.Get("X-Goi-Fragment") == "review" {
		h.renderer.Render(w, "review-session-body", page)
		return
	}
	h.renderer.Render(w, "review-session.html", page)
}

func (h *Handler) confirmAnswer(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	promptID, answer, ok := h.parseAnswer(w, r, sessionID)
	if !ok {
		return
	}
	if _, err := h.store.ConfirmAnswer(r.Context(), sessionID, promptID, answer); err != nil {
		h.writeStoreError(w, r, err, "could not confirm review answer", sessionID)
		return
	}
	h.respondAfterReviewAction(w, r, sessionID, "")
}

func (h *Handler) acceptFailure(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	promptID, ok := h.parsePromptID(w, r, sessionID)
	if !ok {
		return
	}
	if _, err := h.store.GiveUp(r.Context(), sessionID, promptID); err != nil {
		h.writeStoreError(w, r, err, "could not record review answer", sessionID)
		return
	}
	if err := h.store.Continue(r.Context(), sessionID); err != nil {
		h.writeStoreError(w, r, err, "could not continue review", sessionID)
		return
	}
	h.respondAfterReviewAction(w, r, sessionID, "")
}

func (h *Handler) revealAnswer(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	promptID, ok := h.parsePromptID(w, r, sessionID)
	if !ok {
		return
	}
	state, err := h.store.State(r.Context(), sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		h.notFound(w)
		return
	}
	if err != nil {
		internalweb.InternalError(w, r, "could not load review", err)
		return
	}
	if state.AnswerMode != reviewModeSelfGrade || state.PromptID != promptID || state.Feedback || state.Status != "active" {
		h.writeStoreError(w, r, stateError("review card is no longer available"), "could not reveal review answer", sessionID)
		return
	}
	page := SessionPage{
		Title: "Review", CSRFToken: internalweb.CSRFToken(r), State: state, Revealed: true,
	}
	if r.Header.Get("X-Goi-Fragment") == "review" {
		h.renderer.Render(w, "review-session-body", page)
		return
	}
	h.renderer.Render(w, "review-session.html", page)
}

func (h *Handler) gradeAnswer(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	promptID, ok := h.parsePromptID(w, r, sessionID)
	if !ok {
		return
	}
	grade := r.FormValue("grade")
	if grade != "again" && grade != "good" {
		h.renderRecovery(
			w, http.StatusBadRequest, "Grade could not be submitted",
			"Choose Again or Good and try again.", reviewSessionURL(sessionID), "Back to review",
		)
		return
	}
	if _, err := h.store.Grade(r.Context(), sessionID, promptID, grade == "good"); err != nil {
		h.writeStoreError(w, r, err, "could not grade review answer", sessionID)
		return
	}
	h.respondAfterReviewAction(w, r, sessionID, "")
}

func (h *Handler) markCorrect(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	promptID, ok := h.parsePromptID(w, r, sessionID)
	if !ok {
		return
	}
	if err := h.store.MarkCorrect(r.Context(), sessionID, promptID); err != nil {
		h.writeStoreError(w, r, err, "could not correct review answer", sessionID)
		return
	}
	if r.Header.Get("X-Goi-Fragment") == "review" {
		h.respondAfterReviewAction(w, r, sessionID, "Answer marked correct.")
		return
	}
	http.Redirect(w, r, reviewSessionURL(sessionID)+"?corrected=1", http.StatusSeeOther)
}

func (h *Handler) showAnswer(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	promptID, ok := h.parsePromptID(w, r, sessionID)
	if !ok {
		return
	}
	if _, err := h.store.GiveUp(r.Context(), sessionID, promptID); err != nil {
		h.writeStoreError(w, r, err, "could not reveal review answer", sessionID)
		return
	}
	h.respondAfterReviewAction(w, r, sessionID, "")
}

func (h *Handler) addSynonym(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		h.renderRecovery(
			w,
			http.StatusBadRequest,
			"Synonym could not be added",
			"The submitted synonym could not be read. Return to the review and try again.",
			reviewSessionURL(sessionID),
			"Back to review",
		)
		return
	}
	promptID, err := strconv.ParseInt(r.FormValue("prompt_id"), 10, 64)
	if err != nil || promptID <= 0 {
		h.renderRecovery(
			w,
			http.StatusBadRequest,
			"Synonym could not be added",
			"The submitted review prompt is invalid. Return to the review and try again.",
			reviewSessionURL(sessionID),
			"Back to review",
		)
		return
	}
	rejectedAnswer := r.FormValue("rejected_answer")
	if rejectedAnswer == "" || cleanRejectedMeaning(rejectedAnswer) != rejectedAnswer {
		h.renderRecovery(
			w,
			http.StatusBadRequest,
			"Synonym could not be added",
			"The submitted rejected answer is invalid. Return to the review and try again.",
			reviewSessionURL(sessionID),
			"Back to review",
		)
		return
	}
	synonym := r.FormValue("synonym")
	if synonym == "" {
		synonym = rejectedAnswer
	}
	if synonym == "" || cleanRejectedMeaning(synonym) != synonym {
		h.renderRecovery(
			w,
			http.StatusBadRequest,
			"Synonym could not be added",
			"Enter a short meaning and try again.",
			reviewSessionURL(sessionID),
			"Back to review",
		)
		return
	}
	if _, err := h.store.AddEditedSynonym(r.Context(), sessionID, promptID, rejectedAnswer, synonym); err != nil {
		h.writeStoreError(w, r, err, "could not add review synonym", sessionID)
		return
	}
	if r.Header.Get("X-Goi-Fragment") == "review" {
		h.respondAfterReviewAction(w, r, sessionID, "Meaning added and answer marked correct.")
		return
	}
	http.Redirect(w, r, reviewSessionURL(sessionID)+"?synonym=added", http.StatusSeeOther)
}

func (h *Handler) respondAfterReviewAction(w http.ResponseWriter, r *http.Request, sessionID int64, notice string) {
	state, err := h.store.State(r.Context(), sessionID)
	if err != nil {
		internalweb.InternalError(w, r, "could not load review", err)
		return
	}
	if h.finishCompletedReview(w, r, state) {
		return
	}
	if r.Header.Get("X-Goi-Fragment") != "review" {
		http.Redirect(w, r, reviewSessionURL(sessionID), http.StatusSeeOther)
		return
	}
	h.renderer.Render(w, "review-session-body", SessionPage{
		Title: "Review", CSRFToken: internalweb.CSRFToken(r), State: state, Notice: notice,
	})
}

func (h *Handler) finishCompletedReview(w http.ResponseWriter, r *http.Request, state State) bool {
	if state.Status != "completed" {
		return false
	}
	destination := "/dashboard"
	if state.LessonSessionID > 0 {
		if h.lessonCompleter == nil {
			internalweb.InternalError(w, r, "could not finish lesson", errors.New("lesson completion is unavailable"))
			return true
		}
		if err := h.lessonCompleter.CompleteReviewedBatch(r.Context(), state.LessonSessionID); err != nil {
			internalweb.InternalError(w, r, "could not finish lesson", err)
			return true
		}
		destination = "/lessons"
	} else if state.Kind == "extra" {
		destination = "/practice"
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
	return true
}

func (h *Handler) continueReview(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseID(r)
	if !ok {
		h.notFound(w)
		return
	}
	if err := h.store.Continue(r.Context(), sessionID); err != nil {
		h.writeStoreError(w, r, err, "could not continue review", sessionID)
		return
	}
	h.respondAfterReviewAction(w, r, sessionID, "")
}

func parseID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	return id, err == nil && id > 0
}

func (h *Handler) parseAnswer(w http.ResponseWriter, r *http.Request, sessionID int64) (int64, string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		h.renderRecovery(
			w,
			http.StatusBadRequest,
			"Answer could not be submitted",
			"The answer could not be read. Return to the review and try again.",
			reviewSessionURL(sessionID),
			"Back to review",
		)
		return 0, "", false
	}
	promptID, err := strconv.ParseInt(r.FormValue("prompt_id"), 10, 64)
	if err != nil || promptID <= 0 {
		h.renderRecovery(
			w,
			http.StatusBadRequest,
			"Answer could not be submitted",
			"The submitted review prompt is invalid. Return to the review and try again.",
			reviewSessionURL(sessionID),
			"Back to review",
		)
		return 0, "", false
	}
	return promptID, r.FormValue("answer"), true
}

func (h *Handler) parsePromptID(w http.ResponseWriter, r *http.Request, sessionID int64) (int64, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		h.renderRecovery(
			w, http.StatusBadRequest, "Review action could not be submitted",
			"The review action could not be read. Return to the review and try again.",
			reviewSessionURL(sessionID), "Back to review",
		)
		return 0, false
	}
	promptID, err := strconv.ParseInt(r.FormValue("prompt_id"), 10, 64)
	if err != nil || promptID <= 0 {
		h.renderRecovery(
			w, http.StatusBadRequest, "Review action could not be submitted",
			"The submitted review prompt is invalid. Return to the review and try again.",
			reviewSessionURL(sessionID), "Back to review",
		)
		return 0, false
	}
	return promptID, true
}

func (h *Handler) writeStoreError(w http.ResponseWriter, r *http.Request, err error, fallback string, sessionID int64) {
	if message, ok := internalweb.UserErrorMessage(err); ok {
		h.renderRecovery(
			w,
			http.StatusConflict,
			"Review changed",
			message,
			reviewSessionURL(sessionID),
			"Return to review",
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

func reviewSessionURL(sessionID int64) string {
	return "/reviews/session/" + strconv.FormatInt(sessionID, 10)
}
