package reviews

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/database"
	internalweb "github.com/tomasmik/goi/internal/web"
)

func reviewTestRouter(t *testing.T, store *Store) http.Handler {
	t.Helper()
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, renderer).Routes(router)
	return router
}

func TestCorrectAnswerWaitsForExplicitConfirmation(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "review-handler.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	insertDueVocabulary(t, db)
	var vocabularyID int64
	if err := db.QueryRow("SELECT id FROM vocabulary").Scan(&vocabularyID); err != nil {
		t.Fatal(err)
	}
	pictureID := attachReviewTestPicture(t, db, vocabularyID)

	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	router := reviewTestRouter(t, store)

	form := url.Values{
		"prompt_id": {fmt.Sprint(state.PromptID)},
		"answer":    {answerForState(state)},
	}
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/reviews/session/%d/answer", sessionID), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Goi-Fragment", "review")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{`id="study-stage"`, `class="review-result-row is-correct" role="status"`, `data-review-confirmation`, `data-review-confirm`, `<summary aria-keyshortcuts="E">Word details</summary>`, "Your answer", `name="answer"`, state.Pronunciation, "to eat", fmt.Sprintf(`src="/media/%d"`, pictureID)} {
		if !strings.Contains(body, expected) {
			t.Fatalf("confirmation fragment does not contain %q: %s", expected, body)
		}
	}
	for _, absent := range []string{`data-review-retry`, `Try again`, `<kbd>Esc</kbd>`, `<details class="review-answer-details" open>`, `is-correction`, `data-next-url`, `data-result-delay`, `data-review-result="correct"`} {
		if strings.Contains(body, absent) {
			t.Fatalf("confirmation fragment contains automatic navigation %q: %s", absent, body)
		}
	}
	if strings.Contains(body, "<!doctype html>") {
		t.Fatal("confirmation unexpectedly rendered a full page")
	}
	var attemptCount int
	if err := db.QueryRow("SELECT attempt_count FROM review_prompts WHERE id = ?", state.PromptID).Scan(&attemptCount); err != nil {
		t.Fatal(err)
	}
	if attemptCount != 0 {
		t.Fatalf("answer preview recorded %d attempts", attemptCount)
	}
	stillCurrent, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if stillCurrent.PromptID != state.PromptID {
		t.Fatalf("answer preview advanced from prompt %d to %d", state.PromptID, stillCurrent.PromptID)
	}

	confirm := url.Values{
		"prompt_id": {fmt.Sprint(state.PromptID)},
		"answer":    {answerForState(state)},
	}
	request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/reviews/session/%d/confirm", sessionID), strings.NewReader(confirm.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Goi-Fragment", "review")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, body = %s", response.Code, response.Body.String())
	}
	after, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PromptID == state.PromptID {
		t.Fatal("confirmed answer did not advance")
	}
	var promptStatus string
	if err := db.QueryRow("SELECT attempt_count, status FROM review_prompts WHERE id = ?", state.PromptID).Scan(&attemptCount, &promptStatus); err != nil {
		t.Fatal(err)
	}
	if attemptCount != 1 || promptStatus != "passed" {
		t.Fatalf("confirmed prompt = %d attempts, status %q; want 1/passed", attemptCount, promptStatus)
	}
}

func TestCorrectAnswerAdvancesWhenConfigured(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	if _, err := db.Exec("INSERT INTO user_settings (id, review_auto_advance, audio_enabled) VALUES (1, 1, 1)"); err != nil {
		t.Fatal(err)
	}
	insertDueVocabulary(t, db)
	var vocabularyID int64
	if err := db.QueryRow("SELECT id FROM vocabulary").Scan(&vocabularyID); err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(
		"INSERT INTO media (kind, mime_type, sha256, created_at) VALUES ('audio', 'audio/mpeg', ?, 1)",
		strings.Repeat("a", 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	audioID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO vocabulary_media (vocabulary_id, purpose, media_id) VALUES (?, 'pronunciation', ?)",
		vocabularyID, audioID,
	); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if before.PromptType != "pronunciation" || !before.AudioEnabled || before.AudioID != audioID {
		t.Fatalf("reading prompt audio state = %+v", before)
	}

	form := url.Values{
		"prompt_id": {fmt.Sprint(before.PromptID)},
		"answer":    {answerForState(before)},
	}
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/reviews/session/%d/answer", sessionID), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Goi-Fragment", "review")
	response := httptest.NewRecorder()
	reviewTestRouter(t, store).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, "data-review-confirmation") {
		t.Fatalf("automatic advance rendered a confirmation: %s", body)
	}
	if !strings.Contains(body, "data-play-primed-feedback-audio") {
		t.Fatalf("automatic advance did not preserve correct-answer audio: %s", body)
	}
	after, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PromptID == before.PromptID {
		t.Fatal("automatic advance left the answered prompt current")
	}
}

func TestSelfGradeRevealAndGood(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	if _, err := db.Exec("INSERT INTO user_settings (id, review_mode) VALUES (1, 'self_grade')"); err != nil {
		t.Fatal(err)
	}
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	router := reviewTestRouter(t, store)

	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/reviews/session/%d", sessionID), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if body := response.Body.String(); response.Code != http.StatusOK ||
		!strings.Contains(body, "Show answer") || strings.Contains(body, "data-review-answer") {
		t.Fatalf("self-grade front = %d, body = %s", response.Code, body)
	}

	reveal := url.Values{"prompt_id": {fmt.Sprint(before.PromptID)}}
	request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/reviews/session/%d/reveal", sessionID), strings.NewReader(reveal.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Goi-Fragment", "review")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "data-review-self-grade") ||
		!strings.Contains(body, "Again") || !strings.Contains(body, "Good") ||
		!strings.Contains(body, before.Pronunciation) {
		t.Fatalf("revealed self-grade card = %d, body = %s", response.Code, body)
	}
	var attempts int
	if err := db.QueryRow("SELECT attempt_count FROM review_prompts WHERE id = ?", before.PromptID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("revealing recorded %d attempts", attempts)
	}

	grade := url.Values{"prompt_id": {fmt.Sprint(before.PromptID)}, "grade": {"good"}}
	request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/reviews/session/%d/grade", sessionID), strings.NewReader(grade.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Goi-Fragment", "review")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "data-review-self-grade") {
		t.Fatalf("Good response = %d, body = %s", response.Code, response.Body.String())
	}
	after, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PromptID == before.PromptID {
		t.Fatal("Good did not advance the review")
	}

	request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/reviews/session/%d/grade", sessionID), strings.NewReader(grade.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("repeated Good status = %d, want conflict", response.Code)
	}
}

func TestSelfGradeAgainCanBeMarkedCorrect(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	if _, err := db.Exec("INSERT INTO user_settings (id, review_mode) VALUES (1, 'self_grade')"); err != nil {
		t.Fatal(err)
	}
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	router := reviewTestRouter(t, store)

	grade := url.Values{"prompt_id": {fmt.Sprint(before.PromptID)}, "grade": {"again"}}
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/reviews/session/%d/grade", sessionID), strings.NewReader(grade.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Goi-Fragment", "review")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Mark this answer correct") {
		t.Fatalf("Again response = %d, body = %s", response.Code, response.Body.String())
	}

	correction := url.Values{"prompt_id": {fmt.Sprint(before.PromptID)}}
	request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/reviews/session/%d/mark-correct", sessionID), strings.NewReader(correction.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Goi-Fragment", "review")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Answer marked correct.") {
		t.Fatalf("mark-correct response = %d, body = %s", response.Code, response.Body.String())
	}
	after, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PromptID == before.PromptID || after.Feedback {
		t.Fatalf("mark correct did not advance: %+v", after)
	}
}

func TestRejectedMeaningCanBeAddedAsASynonymFromTheReview(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state := moveToPromptType(t, ctx, store, sessionID, "meaning")
	router := reviewTestRouter(t, store)
	answer := url.Values{
		"prompt_id": {fmt.Sprint(state.PromptID)},
		"answer":    {"consume"},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		fmt.Sprintf("/reviews/session/%d/answer", sessionID),
		strings.NewReader(answer.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Goi-Fragment", "review")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("review status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{
		"consume",
		"Not quite",
		"Your answer",
		"Correct answer",
		`<details class="review-answer-details"`,
		`data-review-details`,
		`aria-keyshortcuts="E"`,
		`aria-keyshortcuts="S"`,
		`aria-keyshortcuts="M"`,
		"Add “consume” as meaning",
		"Mark this answer correct",
		"Try again",
		"Continue as incorrect",
		`data-review-confirmation`,
		`class="review-result-row is-incorrect" role="status"`,
		fmt.Sprintf(`action="/reviews/session/%d/synonym"`, sessionID),
		fmt.Sprintf(`action="/reviews/session/%d/accept-failure"`, sessionID),
		fmt.Sprintf(`name="prompt_id" value="%d"`, state.PromptID),
		`name="rejected_answer" value="consume"`,
		`name="synonym" value="consume"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("review does not contain %q: %s", expected, body)
		}
	}
	for _, absent := range []string{"Your answer · Incorrect", "Continue — counts as incorrect", "Show answer", "Attempt 2", `data-review-answer`, `<details class="review-answer-details" open>`} {
		if strings.Contains(body, absent) {
			t.Fatalf("incorrect confirmation contains %q: %s", absent, body)
		}
	}

	fullRequest := httptest.NewRequest(
		http.MethodPost,
		fmt.Sprintf("/reviews/session/%d/answer", sessionID),
		strings.NewReader(answer.Encode()),
	)
	fullRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	fullResponse := httptest.NewRecorder()
	router.ServeHTTP(fullResponse, fullRequest)
	fullBody := fullResponse.Body.String()
	for _, expected := range []string{"<!doctype html>", "Not quite", "Your answer", "consume", "Continue as incorrect", `data-review-confirmation`} {
		if fullResponse.Code != http.StatusOK || !strings.Contains(fullBody, expected) {
			t.Fatalf("full-page incorrect confirmation does not contain %q: status %d, body = %s", expected, fullResponse.Code, fullBody)
		}
	}

	request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/reviews/session/%d", sessionID), nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	body = response.Body.String()
	if !strings.Contains(body, `data-review-answer`) {
		t.Fatalf("retry page has no answer field: %s", body)
	}
	for _, absent := range []string{"Your last answer", "Mark this answer correct", "Show answer", "Attempt 2"} {
		if strings.Contains(body, absent) {
			t.Fatalf("retry page contains stale action %q: %s", absent, body)
		}
	}

	form := url.Values{
		"prompt_id":       {fmt.Sprint(state.PromptID)},
		"rejected_answer": {"consume"},
		"synonym":         {"consume"},
	}
	request = httptest.NewRequest(
		http.MethodPost,
		fmt.Sprintf("/reviews/session/%d/synonym", sessionID),
		strings.NewReader(form.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("add synonym status = %d, body = %s", response.Code, response.Body.String())
	}
	wantLocation := fmt.Sprintf("/reviews/session/%d?synonym=added", sessionID)
	if location := response.Header().Get("Location"); location != wantLocation {
		t.Fatalf("add synonym location = %q, want %q", location, wantLocation)
	}

	request = httptest.NewRequest(http.MethodGet, wantLocation, nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	wantDashboard := fmt.Sprintf("/dashboard?completed_review=%d", sessionID)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != wantDashboard {
		t.Fatalf("completed synonym review = %d, location %q", response.Code, response.Header().Get("Location"))
	}
}

func TestAcceptFailureAdvancesAndQueuesAReinforcementReview(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "wrong"); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"prompt_id": {fmt.Sprint(state.PromptID)}}
	request := httptest.NewRequest(
		http.MethodPost,
		fmt.Sprintf("/reviews/session/%d/accept-failure", sessionID),
		strings.NewReader(form.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Goi-Fragment", "review")
	response := httptest.NewRecorder()
	reviewTestRouter(t, store).ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("accept failure status = %d, body = %s", response.Code, body)
	}
	if !strings.Contains(body, "retry in progress") ||
		strings.Contains(body, fmt.Sprintf(`name="prompt_id" value="%d"`, state.PromptID)) {
		t.Fatalf("accept failure did not advance and queue a retry: %s", body)
	}
	var failures int
	if err := db.QueryRow("SELECT COUNT(*) FROM review_results WHERE outcome = 'failure' AND voided_at IS NULL").Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Fatalf("failure results = %d, want 1", failures)
	}
}

func TestFinalStandaloneReviewAnswerReturnsToItsStartingArea(t *testing.T) {
	for _, test := range []struct {
		name        string
		kind        string
		fragment    bool
		destination string
	}{
		{name: "normal", kind: "normal", fragment: true, destination: "/dashboard"},
		{name: "extra practice", kind: "extra", destination: "/practice"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, db := openReviewTestDatabase(t)
			insertDueVocabulary(t, db)
			store := NewStore(db)

			var sessionID int64
			var err error
			if test.kind == "normal" {
				sessionID, err = store.StartNormal(ctx, 1)
			} else {
				var vocabularyID int64
				if err := db.QueryRow("SELECT id FROM vocabulary LIMIT 1").Scan(&vocabularyID); err != nil {
					t.Fatal(err)
				}
				sessionID, err = store.startSession(ctx, "extra", []int64{vocabularyID}, 3, 0)
			}
			if err != nil {
				t.Fatal(err)
			}

			first, err := store.State(ctx, sessionID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ConfirmAnswer(ctx, sessionID, first.PromptID, answerForState(first)); err != nil {
				t.Fatal(err)
			}
			last, err := store.State(ctx, sessionID)
			if err != nil {
				t.Fatal(err)
			}
			if last.Status != "active" {
				t.Fatalf("review completed before final handler action: %q", last.Status)
			}

			form := url.Values{
				"prompt_id": {fmt.Sprint(last.PromptID)},
				"answer":    {answerForState(last)},
			}
			request := httptest.NewRequest(
				http.MethodPost,
				fmt.Sprintf("/reviews/session/%d/confirm", sessionID),
				strings.NewReader(form.Encode()),
			)
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if test.fragment {
				request.Header.Set("X-Goi-Fragment", "review")
			}
			router := reviewTestRouter(t, store)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			wantDestination := test.destination
			if test.kind == "normal" {
				wantDestination = fmt.Sprintf("/dashboard?completed_review=%d", sessionID)
			}
			if response.Code != http.StatusSeeOther || response.Header().Get("Location") != wantDestination {
				t.Fatalf("final answer response = %d, location %q", response.Code, response.Header().Get("Location"))
			}

			request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/reviews/session/%d", sessionID), nil)
			response = httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusSeeOther || response.Header().Get("Location") != wantDestination {
				t.Fatalf("completed review response = %d, location %q", response.Code, response.Header().Get("Location"))
			}
		})
	}
}

func TestCompletedLessonReviewFinishesLessonAndReturnsToLessons(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "lesson-review-handler.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	vocabularyID := insertUnlearnedReviewVocabulary(t, db, "食べる")
	lessonResult, err := db.Exec(`
		INSERT INTO lesson_sessions (status, phase)
		VALUES ('active', 'study')`)
	if err != nil {
		t.Fatal(err)
	}
	lessonID, err := lessonResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO lesson_session_items (
			session_id, vocabulary_id, position, batch_number, study_viewed_at
		)
		VALUES (?, ?, 0, 0, 1)`, lessonID, vocabularyID); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	reviewID, err := store.StartLesson(ctx, lessonID, []int64{vocabularyID})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.State(ctx, reviewID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmAnswer(ctx, reviewID, first.PromptID, answerForState(first)); err != nil {
		t.Fatal(err)
	}
	last, err := store.State(ctx, reviewID)
	if err != nil {
		t.Fatal(err)
	}
	router := reviewTestRouter(t, store)

	form := url.Values{
		"prompt_id": {fmt.Sprint(last.PromptID)},
		"answer":    {answerForState(last)},
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_handler_schedule BEFORE INSERT ON srs_states BEGIN SELECT RAISE(FAIL, 'schedule write failed'); END`); err != nil {
		t.Fatal(err)
	}
	submit := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/reviews/session/%d/confirm", reviewID), strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("X-Goi-Fragment", "review")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	if response := submit(); response.Code != http.StatusInternalServerError {
		t.Fatalf("failed activation status = %d, body = %s", response.Code, response.Body.String())
	}
	retry, err := store.State(ctx, reviewID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Status != "active" || retry.PromptID != last.PromptID {
		t.Fatalf("failed activation advanced the review: %+v", retry)
	}
	if _, err := db.Exec("DROP TRIGGER reject_handler_schedule"); err != nil {
		t.Fatal(err)
	}
	response := submit()

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/lessons" {
		t.Fatalf("final answer response = %d, location %q", response.Code, response.Header().Get("Location"))
	}
	var lessonStatus string
	if err := db.QueryRow("SELECT status FROM lesson_sessions WHERE id = ?", lessonID).Scan(&lessonStatus); err != nil {
		t.Fatal(err)
	}
	if lessonStatus != "completed" {
		t.Fatalf("lesson status = %q, want completed", lessonStatus)
	}

	if _, err := db.Exec("PRAGMA query_only = ON"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/reviews/session/%d", reviewID), nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/lessons" {
		t.Fatalf("completed review response = %d, location %q", response.Code, response.Header().Get("Location"))
	}
}
