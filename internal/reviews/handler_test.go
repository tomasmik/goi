package reviews

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/database"
	"github.com/tomasmik/goi/internal/examples"
	internalweb "github.com/tomasmik/goi/internal/web"
)

func reviewTestRouter(t *testing.T, store *Store, completers ...LessonBatchCompleter) http.Handler {
	t.Helper()
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	var completer LessonBatchCompleter
	if len(completers) > 0 {
		completer = completers[0]
	}
	router := chi.NewRouter()
	NewHandler(store, completer, renderer).Routes(router)
	return router
}

func TestMissingReviewRendersRecoveryPage(t *testing.T) {
	_, db := openReviewTestDatabase(t)
	router := reviewTestRouter(t, NewStore(db))
	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/reviews/session/999", nil),
		httptest.NewRequest(http.MethodPost, "/reviews/session/999/answer", strings.NewReader("prompt_id=1&answer=cat")),
	}
	requests[1].Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, request := range requests {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `href="/reviews"`) {
			t.Fatalf("missing review response = %d, %s", response.Code, response.Body.String())
		}
	}
}

func TestInvalidReviewAnswerRendersRecoveryPage(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	router := reviewTestRouter(t, store)
	request := httptest.NewRequest(
		http.MethodPost,
		fmt.Sprintf("/reviews/session/%d/answer", sessionID),
		strings.NewReader("prompt_id=invalid&answer=cat"),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusBadRequest || !strings.Contains(body, "submitted review prompt is invalid") {
		t.Fatalf("response = %d, body = %s", response.Code, body)
	}
	for _, expected := range []string{`class="site-header"`, fmt.Sprintf(`href="/reviews/session/%d"`, sessionID), "Back to review"} {
		if !strings.Contains(body, expected) {
			t.Errorf("recovery page does not contain %q: %s", expected, body)
		}
	}
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
	for _, expected := range []string{`id="study-stage"`, `class="review-result-row is-correct"`, `data-review-confirmation`, `data-review-confirm`, `<summary>Word details</summary>`, `name="answer"`, state.Pronunciation, "to eat", fmt.Sprintf(`src="/media/%d"`, pictureID)} {
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
	if _, err := db.Exec("INSERT INTO user_settings (id, review_auto_advance) VALUES (1, 1)"); err != nil {
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
	if body := response.Body.String(); strings.Contains(body, "data-review-confirmation") {
		t.Fatalf("automatic advance rendered a confirmation: %s", body)
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

func TestTypedSessionRejectsSelfGradeEndpoint(t *testing.T) {
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
	form := url.Values{"prompt_id": {fmt.Sprint(state.PromptID)}, "grade": {"good"}}
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/reviews/session/%d/grade", sessionID), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	reviewTestRouter(t, store).ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "typed answers") {
		t.Fatalf("typed self-grade response = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestCorrectionShowsLearningContent(t *testing.T) {
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	renderer.Render(response, "review-session.html", SessionPage{
		Title: "Review",
		State: State{
			ID:            1,
			Status:        "active",
			Expression:    "意識",
			PromptType:    "meaning",
			Feedback:      true,
			Pronunciation: "いしき",
			Meanings:      []string{"consciousness"},
			Notes:         "Seen in context.",
			AudioID:       9,
			PictureID:     10,
			Example: examples.Example{
				Sentence:            "意識を失った。",
				Translation:         "I lost consciousness.",
				SourceTitle:         "Novel chapter 2",
				SourceLink:          "https://example.com/chapter-2",
				SourcePositionLabel: "Chapter 2",
				SentenceAudioIDs:    []int64{11},
				VideoFrameID:        12,
			},
		},
	})

	body := response.Body.String()
	for _, expected := range []string{"consciousness", "いしき", "Seen in context.", "意識を失った。", "I lost consciousness.", "Novel chapter 2", `src="/media/9"`, `src="/media/10"`, `src="/media/11"`, `src="/media/12"`} {
		if !strings.Contains(body, expected) {
			t.Errorf("correction does not contain %q", expected)
		}
	}
	if !strings.Contains(body, `<button class="button" type="submit" autofocus>Continue`) {
		t.Fatalf("correction does not focus its explicit continue action: %s", body)
	}
}

func TestReviewAudioOnlyAutoplaysForReadingCards(t *testing.T) {
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	render := func(promptType string, enabled bool, confirmation *AnswerConfirmation) string {
		response := httptest.NewRecorder()
		renderer.Render(response, "review-session.html", SessionPage{
			Title: "Review",
			State: State{
				ID: 1, Status: "active", Expression: "猫", PromptType: promptType,
				Pronunciation: "ねこ", Meanings: []string{"cat"}, AudioID: 9, AudioEnabled: enabled,
			},
			Confirmation: confirmation,
		})
		return response.Body.String()
	}

	disabled := render("pronunciation", false, &AnswerConfirmation{Correct: true})
	if !strings.Contains(disabled, `<audio controls`) || !strings.Contains(disabled, `src="/media/9"`) {
		t.Fatalf("disabled autoplay hid audio controls: %s", disabled)
	}
	if strings.Contains(disabled, "data-feedback-audio") {
		t.Fatalf("disabled autoplay marked audio for playback: %s", disabled)
	}
	enabled := render("pronunciation", true, &AnswerConfirmation{Correct: true})
	if !strings.Contains(enabled, "data-feedback-audio") {
		t.Fatalf("enabled autoplay did not mark revealed audio: %s", enabled)
	}
	if !strings.Contains(enabled, `data-feedback-audio-src="/media/9"`) {
		t.Fatalf("enabled autoplay did not make audio available before submission: %s", enabled)
	}

	prompt := render("pronunciation", true, nil)
	if !strings.Contains(prompt, "data-prime-feedback-audio") || strings.Contains(prompt, "data-feedback-audio preload") {
		t.Fatalf("review prompt did not prime audio without playing it early: %s", prompt)
	}
	incorrect := render("pronunciation", true, &AnswerConfirmation{})
	if strings.Contains(incorrect, "data-feedback-audio preload") {
		t.Fatalf("incorrect answer triggered correct-answer audio: %s", incorrect)
	}

	meaning := render("meaning", true, &AnswerConfirmation{Correct: true})
	if !strings.Contains(meaning, `<audio controls`) || !strings.Contains(meaning, `src="/media/9"`) {
		t.Fatalf("meaning correction hid manual audio controls: %s", meaning)
	}
	if strings.Contains(meaning, "data-feedback-audio") {
		t.Fatalf("meaning answer enabled pronunciation autoplay: %s", meaning)
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
		"Your answer",
		"Incorrect",
		"Correct answer",
		`<details class="review-answer-details"`,
		"Add “consume” as meaning",
		"Mark this answer correct",
		"Retry prompt",
		"Continue",
		`data-review-confirmation`,
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
	for _, absent := range []string{"Not quite", "Show answer", "Attempt 2", `data-review-answer`} {
		if strings.Contains(body, absent) {
			t.Fatalf("incorrect confirmation contains %q: %s", absent, body)
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
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/dashboard" {
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

func TestFinalRejectedAnswerStillShowsWhatWasTyped(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	if _, err := db.Exec("INSERT INTO user_settings (id, retry_count) VALUES (1, 1)"); err != nil {
		t.Fatal(err)
	}
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

	form := url.Values{
		"prompt_id": {fmt.Sprint(state.PromptID)},
		"answer":    {"ちがう"},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		fmt.Sprintf("/reviews/session/%d/answer", sessionID),
		strings.NewReader(form.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Goi-Fragment", "review")
	response := httptest.NewRecorder()
	reviewTestRouter(t, store).ServeHTTP(response, request)

	body := response.Body.String()
	for _, expected := range []string{"Your answer", "ちがう", "Incorrect", "Correct answer", `<details class="review-answer-details" open>`, `data-review-confirmation`, fmt.Sprintf(`action="/reviews/session/%d/continue"`, sessionID)} {
		if !strings.Contains(body, expected) {
			t.Fatalf("final incorrect confirmation does not contain %q: %s", expected, body)
		}
	}
	for _, absent := range []string{"Try again", "Attempt 2", "Not quite"} {
		if strings.Contains(body, absent) {
			t.Fatalf("final incorrect confirmation contains %q: %s", absent, body)
		}
	}
}

func TestShowAnswerRendersTheNormalCorrectionPanel(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state := moveToPromptType(t, ctx, store, sessionID, "meaning")
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "consume"); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"prompt_id": {fmt.Sprint(state.PromptID)}}
	request := httptest.NewRequest(
		http.MethodPost,
		fmt.Sprintf("/reviews/session/%d/show-answer", sessionID),
		strings.NewReader(form.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Goi-Fragment", "review")
	response := httptest.NewRecorder()
	reviewTestRouter(t, store).ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("show answer status = %d, body = %s", response.Code, body)
	}
	for _, expected := range []string{"Correct answer", "たべる", "to eat", "Mark this answer correct", "Continue"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("show answer response does not contain %q: %s", expected, body)
		}
	}
}

func TestShowAnswerRejectsSelfGradedSessions(t *testing.T) {
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
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{"prompt_id": {fmt.Sprint(state.PromptID)}}
	request := httptest.NewRequest(
		http.MethodPost,
		fmt.Sprintf("/reviews/session/%d/show-answer", sessionID),
		strings.NewReader(form.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	reviewTestRouter(t, store).ServeHTTP(response, request)

	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "self grading") {
		t.Fatalf("show answer response = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestReviewProgressSeparatesRemainingWordsFromQueuedRetries(t *testing.T) {
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	renderer.Render(response, "review-session.html", SessionPage{
		Title: "Reviews",
		State: State{
			ID: 1, Status: "active", Kind: "normal", WordTotal: 10, WordCompleted: 4,
			WordsRemaining: 6, RetriesQueued: 2, PromptID: 3, PromptType: "meaning",
			Expression: "意識", MaxAttempts: 2,
		},
	})
	body := response.Body.String()
	for _, expected := range []string{"6 remaining", "2 retries queued", "Save and leave"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("review progress does not contain %q: %s", expected, body)
		}
	}
	if strings.Contains(body, `action="/reviews/session/1/pause"`) {
		t.Fatalf("active review still has a duplicate pause action: %s", body)
	}
}

func TestAddSynonymRejectsAnAnswerFromAStaleReviewTab(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state := moveToPromptType(t, ctx, store, sessionID, "meaning")
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "consume"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "devour"); err != nil {
		t.Fatal(err)
	}

	router := reviewTestRouter(t, store)
	form := url.Values{
		"prompt_id":       {fmt.Sprint(state.PromptID)},
		"rejected_answer": {"consume"},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		fmt.Sprintf("/reviews/session/%d/synonym", sessionID),
		strings.NewReader(form.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), "rejected answer has changed") {
		t.Fatalf("stale synonym response = %d, body = %s", response.Code, response.Body.String())
	}
	var meaningCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM meanings WHERE vocabulary_id = ?", state.VocabularyID).Scan(&meaningCount); err != nil {
		t.Fatal(err)
	}
	if meaningCount != 1 {
		t.Fatalf("meanings after stale submission = %d, want 1", meaningCount)
	}
}

func TestAddSynonymRejectsAnInvalidPrompt(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	router := reviewTestRouter(t, store)
	request := httptest.NewRequest(
		http.MethodPost,
		fmt.Sprintf("/reviews/session/%d/synonym", sessionID),
		strings.NewReader("prompt_id=invalid"),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "submitted review prompt is invalid") {
		t.Fatalf("invalid synonym prompt = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestAddSynonymRejectsInvalidSubmittedAnswers(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state := moveToPromptType(t, ctx, store, sessionID, "meaning")
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "consume"); err != nil {
		t.Fatal(err)
	}
	router := reviewTestRouter(t, store)

	for _, test := range []struct {
		name   string
		answer string
	}{
		{name: "missing"},
		{name: "oversized", answer: strings.Repeat("a", maxRejectedMeaningRunes+1)},
		{name: "altered whitespace", answer: " consume "},
	} {
		t.Run(test.name, func(t *testing.T) {
			form := url.Values{
				"prompt_id":       {fmt.Sprint(state.PromptID)},
				"rejected_answer": {test.answer},
			}
			request := httptest.NewRequest(
				http.MethodPost,
				fmt.Sprintf("/reviews/session/%d/synonym", sessionID),
				strings.NewReader(form.Encode()),
			)
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest ||
				!strings.Contains(response.Body.String(), "submitted rejected answer is invalid") {
				t.Fatalf("invalid synonym response = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}

	var meaningCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM meanings WHERE vocabulary_id = ?", state.VocabularyID).Scan(&meaningCount); err != nil {
		t.Fatal(err)
	}
	if meaningCount != 1 {
		t.Fatalf("meanings after invalid submissions = %d, want 1", meaningCount)
	}
}

func TestStaleRejectedSynonymFailsSafely(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabulary(t, db)
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	state := moveToPromptType(t, ctx, store, sessionID, "meaning")
	if _, err := store.Answer(ctx, sessionID, state.PromptID, "consume"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE meanings SET text = 'to dine', normalized_text = 'to dine'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE vocabulary SET content_revision = content_revision + 1"); err != nil {
		t.Fatal(err)
	}

	router := reviewTestRouter(t, store)
	target := fmt.Sprintf("/reviews/session/%d", sessionID)
	form := url.Values{
		"prompt_id":       {fmt.Sprint(state.PromptID)},
		"rejected_answer": {"consume"},
		"synonym":         {"consume"},
	}
	request := httptest.NewRequest(http.MethodPost, target+"/synonym", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "word changed after the answer was rejected") {
		t.Fatalf("stale synonym submission = %d, body = %s", response.Code, response.Body.String())
	}
	var meaning string
	var revision int64
	if err := db.QueryRow(`
		SELECT m.text, v.content_revision
		FROM meanings m
		JOIN vocabulary v ON v.id = m.vocabulary_id`).Scan(&meaning, &revision); err != nil {
		t.Fatal(err)
	}
	if meaning != "to dine" || revision != 2 {
		t.Fatalf("stale synonym changed vocabulary to %q at revision %d", meaning, revision)
	}
}

func TestReviewPromptHidesExampleTranslationAndSource(t *testing.T) {
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	renderer.Render(response, "review-session.html", SessionPage{
		Title: "Review",
		State: State{
			ID:           1,
			Status:       "active",
			Total:        1,
			Remaining:    1,
			VocabularyID: 1,
			Expression:   "意識",
			PromptID:     1,
			PromptType:   "meaning",
			Example: examples.Example{
				Sentence:            "彼は意識を失った。",
				Translation:         "He lost consciousness.",
				SourceTitle:         "Japanese drama",
				SourceLink:          "https://example.com/drama",
				SourcePositionLabel: "4:12",
				BeforeTarget:        "彼は",
				MatchedTarget:       "意識",
				AfterTarget:         "を失った。",
				HasTarget:           true,
			},
		},
	})

	body := response.Body.String()
	for _, expected := range []string{"彼は", "意識", "を失った。"} {
		if !strings.Contains(body, expected) {
			t.Errorf("review prompt does not contain %q", expected)
		}
	}
	for _, hidden := range []string{"He lost consciousness.", "Japanese drama", "https://example.com/drama", "4:12"} {
		if strings.Contains(body, hidden) {
			t.Errorf("review prompt exposes %q before the answer", hidden)
		}
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
			response := httptest.NewRecorder()
			reviewTestRouter(t, store).ServeHTTP(response, request)

			if response.Code != http.StatusSeeOther || response.Header().Get("Location") != test.destination {
				t.Fatalf("final answer response = %d, location %q", response.Code, response.Header().Get("Location"))
			}
		})
	}
}

func TestLessonCompletionFailureCanBeRetried(t *testing.T) {
	completer := &retryLessonBatchCompleter{err: errors.New("temporary failure")}
	handler := &Handler{lessonCompleter: completer}
	state := State{Status: "completed", LessonSessionID: 42}

	response := httptest.NewRecorder()
	if !handler.finishCompletedReview(response, httptest.NewRequest(http.MethodGet, "/", nil), state) {
		t.Fatal("completed review was not handled")
	}
	if response.Code != http.StatusInternalServerError || completer.calls != 1 {
		t.Fatalf("failed completion response = %d, calls %d", response.Code, completer.calls)
	}

	completer.err = nil
	response = httptest.NewRecorder()
	handler.finishCompletedReview(response, httptest.NewRequest(http.MethodGet, "/", nil), state)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/lessons" {
		t.Fatalf("retried completion response = %d, location %q", response.Code, response.Header().Get("Location"))
	}
	if completer.calls != 2 {
		t.Fatalf("completion calls after retry = %d", completer.calls)
	}
}

type retryLessonBatchCompleter struct {
	calls int
	err   error
}

func (c *retryLessonBatchCompleter) CompleteReviewedBatch(context.Context, int64) error {
	c.calls++
	return c.err
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
	lessonCompleter := &lessonBatchCompleter{db: db}
	router := reviewTestRouter(t, store, lessonCompleter)

	form := url.Values{
		"prompt_id": {fmt.Sprint(last.PromptID)},
		"answer":    {answerForState(last)},
	}
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/reviews/session/%d/confirm", reviewID), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Goi-Fragment", "review")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/lessons" {
		t.Fatalf("final answer response = %d, location %q", response.Code, response.Header().Get("Location"))
	}
	if lessonCompleter.calls != 1 || lessonCompleter.sessionID != lessonID {
		t.Fatalf("lesson completer calls = %d for session %d", lessonCompleter.calls, lessonCompleter.sessionID)
	}
	var lessonStatus string
	if err := db.QueryRow("SELECT status FROM lesson_sessions WHERE id = ?", lessonID).Scan(&lessonStatus); err != nil {
		t.Fatal(err)
	}
	if lessonStatus != "completed" {
		t.Fatalf("lesson status = %q, want completed", lessonStatus)
	}

	request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/reviews/session/%d", reviewID), nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/lessons" {
		t.Fatalf("completed review response = %d, location %q", response.Code, response.Header().Get("Location"))
	}
}

type lessonBatchCompleter struct {
	db        *sql.DB
	calls     int
	sessionID int64
}

func (c *lessonBatchCompleter) CompleteReviewedBatch(ctx context.Context, sessionID int64) error {
	c.calls++
	c.sessionID = sessionID
	_, err := c.db.ExecContext(ctx, "UPDATE lesson_sessions SET status = 'completed' WHERE id = ?", sessionID)
	return err
}

func TestStartHidesStoreFailures(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "closed-review-handler.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	router := reviewTestRouter(t, NewStore(db))

	request := httptest.NewRequest(http.MethodPost, "/reviews/start", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if body := response.Body.String(); !strings.Contains(body, "could not start reviews") || strings.Contains(body, "closed") {
		t.Fatalf("response exposed store failure: %s", body)
	}
}

func TestStartWithoutDueReviewsReturnsConflict(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "empty-review-handler.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	router := reviewTestRouter(t, NewStore(db))
	request := httptest.NewRequest(http.MethodPost, "/reviews/start", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusConflict, response.Body.String())
	}
}

func TestReviewsAndExtraPracticeAreSeparatePages(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "empty-review-page.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	router := reviewTestRouter(t, NewStore(db))
	request := httptest.NewRequest(http.MethodGet, "/reviews", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "No reviews due") || !strings.Contains(body, `href="/practice"`) {
		t.Fatalf("response = %d, body = %s", response.Code, body)
	}
	if strings.Contains(body, "Learning words") || strings.Contains(body, "Recent words") || strings.Contains(body, `class="study-grid"`) {
		t.Fatalf("reviews page contains extra-practice choices: %s", body)
	}
	now := time.Now().UTC().Unix()
	if _, err := db.Exec(`
		INSERT INTO vocabulary (expression, normalized_expression, status, lesson_completed_at, created_at, updated_at)
		VALUES ('猫', '猫', 'active', ?, ?, ?)`, now, now, now); err != nil {
		t.Fatal(err)
	}

	request = httptest.NewRequest(http.MethodGet, "/practice", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	body = response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "Extra practice") ||
		!strings.Contains(body, "Recently learned") || !strings.Contains(body, `action="/study/recent-lessons"`) {
		t.Fatalf("practice response = %d, body = %s", response.Code, body)
	}
	for _, unwanted := range []string{"Newer words", "View and practice leeches"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("practice page contains obsolete choice %q: %s", unwanted, body)
		}
	}
}

func TestContinueAfterCorrectionReturnsTheNextReviewFragment(t *testing.T) {
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
	for {
		outcome, err := store.Answer(ctx, sessionID, state.PromptID, "definitely wrong")
		if err != nil {
			t.Fatal(err)
		}
		if outcome.FinalFailure {
			break
		}
	}

	router := reviewTestRouter(t, store)
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/reviews/session/%d/continue", sessionID), nil)
	request.Header.Set("X-Goi-Fragment", "review")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `id="study-stage"`) {
		t.Fatalf("response = %d, body = %s", response.Code, body)
	}
	if strings.Contains(body, "<!doctype html>") {
		t.Fatalf("continue returned a full page instead of a review fragment: %s", body)
	}
}
