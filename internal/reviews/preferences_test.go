package reviews

import (
	"database/sql"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
)

func TestBuildPromptCards(t *testing.T) {
	items := []int64{1, 2, 3}
	together := buildPromptCards(items, nil, cardOrderTogether, rand.New(rand.NewSource(1)))
	want := []string{"1:pronunciation", "1:meaning", "2:pronunciation", "2:meaning", "3:pronunciation", "3:meaning"}
	if got := promptCardLabels(together); !slices.Equal(got, want) {
		t.Fatalf("together cards = %v, want %v", got, want)
	}

	for itemCount := 2; itemCount <= normalSessionLimit; itemCount++ {
		items := make([]int64, itemCount)
		for index := range items {
			items[index] = int64(index + 1)
		}
		for seed := int64(0); seed < 20; seed++ {
			cards := buildPromptCards(items, nil, cardOrderSpaced, rand.New(rand.NewSource(seed)))
			positions := make(map[int64]map[string]int, itemCount)
			for position, card := range cards {
				if positions[card.itemID] == nil {
					positions[card.itemID] = make(map[string]int, 2)
				}
				positions[card.itemID][card.promptType] = position
			}
			for _, itemID := range items {
				distance := positions[itemID]["meaning"] - positions[itemID]["pronunciation"]
				if distance < 2 || distance > 5 {
					t.Fatalf("%d items, seed %d, item %d distance = %d", itemCount, seed, itemID, distance)
				}
			}
		}
	}
}

func TestBuildPromptCardsKeepsSingleWordTogether(t *testing.T) {
	cards := buildPromptCards([]int64{7}, nil, cardOrderSpaced, rand.New(rand.NewSource(1)))
	if got := promptCardLabels(cards); !slices.Equal(got, []string{"7:pronunciation", "7:meaning"}) {
		t.Fatalf("single-word cards = %v", got)
	}
}

func TestBuildPromptCardsOmitsPronunciationForKanaOnlyWords(t *testing.T) {
	items := []int64{1, 2, 3}
	meaningOnly := map[int64]bool{2: true}

	together := buildPromptCards(items, meaningOnly, cardOrderTogether, rand.New(rand.NewSource(1)))
	wantTogether := []string{"1:pronunciation", "1:meaning", "2:meaning", "3:pronunciation", "3:meaning"}
	if got := promptCardLabels(together); !slices.Equal(got, wantTogether) {
		t.Fatalf("together cards = %v, want %v", got, wantTogether)
	}

	spaced := buildPromptCards(items, meaningOnly, cardOrderSpaced, rand.New(rand.NewSource(1)))
	wantSpaced := []string{"1:pronunciation", "2:meaning", "3:pronunciation", "1:meaning", "3:meaning"}
	if got := promptCardLabels(spaced); !slices.Equal(got, wantSpaced) {
		t.Fatalf("spaced cards = %v, want %v", got, wantSpaced)
	}
}

func TestKanaOnlyExpressions(t *testing.T) {
	for _, test := range []struct {
		expression string
		want       bool
	}{
		{expression: "ありがとう", want: true},
		{expression: "コーヒー", want: true},
		{expression: "カフェ・オレ", want: true},
		{expression: " ひらがな ", want: true},
		{expression: "食べる", want: false},
		{expression: "ゲーム機", want: false},
		{expression: "kana", want: false},
		{expression: "ー・", want: false},
		{expression: "", want: false},
	} {
		if got := isKanaOnly(test.expression); got != test.want {
			t.Errorf("isKanaOnly(%q) = %t, want %t", test.expression, got, test.want)
		}
	}
}

func TestKanaOnlyVocabularyCreatesOnlyMeaningPrompts(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	insertDueVocabularyWithExpression(t, db, "ありがとう")
	insertDueVocabularyWithExpression(t, db, "コーヒー")
	insertDueVocabularyWithExpression(t, db, "食べる")

	sessionID, err := NewStore(db).StartNormal(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`
		SELECT v.expression, rp.prompt_type
		FROM review_prompts rp
		JOIN review_session_items rsi ON rsi.id = rp.session_item_id
		JOIN vocabulary v ON v.id = rsi.vocabulary_id
		WHERE rsi.session_id = ?
		ORDER BY v.expression, rp.position`, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	prompts := make(map[string][]string)
	for rows.Next() {
		var expression, promptType string
		if err := rows.Scan(&expression, &promptType); err != nil {
			t.Fatal(err)
		}
		prompts[expression] = append(prompts[expression], promptType)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got := prompts["ありがとう"]; !slices.Equal(got, []string{"meaning"}) {
		t.Fatalf("hiragana prompts = %v, want [meaning]", got)
	}
	if got := prompts["コーヒー"]; !slices.Equal(got, []string{"meaning"}) {
		t.Fatalf("katakana prompts = %v, want [meaning]", got)
	}
	if got := prompts["食べる"]; !slices.Equal(got, []string{"pronunciation", "meaning"}) {
		t.Fatalf("kanji prompts = %v, want [pronunciation meaning]", got)
	}
}

func TestNormalReviewOrderUsesSRSStage(t *testing.T) {
	for _, test := range []struct {
		name   string
		order  string
		stages []int
		want   []int
	}{
		{name: "ascending", order: "stage_ascending", stages: []int{5, 1, 3}, want: []int{1, 3, 5}},
		{name: "descending", order: "stage_descending", stages: []int{5, 1, 3}, want: []int{5, 3, 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, db := openReviewTestDatabase(t)
			if _, err := db.Exec("INSERT INTO user_settings (id, review_order) VALUES (1, ?)", test.order); err != nil {
				t.Fatal(err)
			}
			for index, stage := range test.stages {
				expression := fmt.Sprintf("word-%d", index)
				insertDueVocabularyWithExpression(t, db, expression)
				if _, err := db.Exec(`
					UPDATE srs_states SET stage = ?
					WHERE vocabulary_id = (SELECT id FROM vocabulary WHERE expression = ?)`, stage, expression); err != nil {
					t.Fatal(err)
				}
			}

			sessionID, err := NewStore(db).StartNormal(ctx, len(test.stages))
			if err != nil {
				t.Fatal(err)
			}
			got := reviewItemStages(t, db, sessionID)
			if !slices.Equal(got, test.want) {
				t.Fatalf("review stages = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRandomReviewOrderSelectsEveryDueWordOnce(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	if _, err := db.Exec("INSERT INTO user_settings (id, review_order) VALUES (1, 'random')"); err != nil {
		t.Fatal(err)
	}
	for index := range 8 {
		insertDueVocabularyWithExpression(t, db, fmt.Sprintf("word-%d", index))
	}
	sessionID, err := NewStore(db).StartNormal(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	var total, distinct int
	if err := db.QueryRow(`
		SELECT COUNT(*), COUNT(DISTINCT vocabulary_id)
		FROM review_session_items WHERE session_id = ?`, sessionID).Scan(&total, &distinct); err != nil {
		t.Fatal(err)
	}
	if total != 8 || distinct != 8 {
		t.Fatalf("random review items = %d total/%d distinct, want 8/8", total, distinct)
	}
}

func TestSpacedSessionUsesGlobalPromptQueue(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	if _, err := db.Exec("INSERT INTO user_settings (id, review_card_order) VALUES (1, 'spaced')"); err != nil {
		t.Fatal(err)
	}
	for index := range 5 {
		insertDueVocabularyWithExpression(t, db, fmt.Sprintf("word-%d", index))
	}
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	assertStoredPromptSpacing(t, db, sessionID)

	first, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if first.PromptType != "pronunciation" {
		t.Fatalf("first prompt = %q, want pronunciation", first.PromptType)
	}
	if _, err := store.Answer(ctx, sessionID, first.PromptID, answerForState(first)); err != nil {
		t.Fatal(err)
	}
	second, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if second.VocabularyID == first.VocabularyID || second.PromptType != "pronunciation" {
		t.Fatalf("second card = word %d/%s after word %d, want another reading", second.VocabularyID, second.PromptType, first.VocabularyID)
	}
	var resultCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM review_results").Scan(&resultCount); err != nil {
		t.Fatal(err)
	}
	if resultCount != 0 {
		t.Fatalf("review results after one card = %d, want 0", resultCount)
	}
}

func TestSessionKeepsModeAndCardOrderFromStart(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	if _, err := db.Exec(`
		INSERT INTO user_settings (id, review_mode, review_card_order)
		VALUES (1, 'self_grade', 'spaced')`); err != nil {
		t.Fatal(err)
	}
	for index := range 3 {
		insertDueVocabularyWithExpression(t, db, fmt.Sprintf("word-%d", index))
	}
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		UPDATE user_settings
		SET review_mode = 'typed', review_card_order = 'together'
		WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.AnswerMode != reviewModeSelfGrade {
		t.Fatalf("active session mode = %q, want self_grade", state.AnswerMode)
	}
	var cardOrder string
	if err := db.QueryRow("SELECT card_order FROM review_sessions WHERE id = ?", sessionID).Scan(&cardOrder); err != nil {
		t.Fatal(err)
	}
	if cardOrder != cardOrderSpaced {
		t.Fatalf("active session card order = %q, want spaced", cardOrder)
	}
	assertStoredPromptSpacing(t, db, sessionID)
}

func TestUndoPreservesCompletedCardsInSpacedQueue(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	if _, err := db.Exec("INSERT INTO user_settings (id, review_card_order) VALUES (1, 'spaced')"); err != nil {
		t.Fatal(err)
	}
	insertDueVocabularyWithExpression(t, db, "first")
	insertDueVocabularyWithExpression(t, db, "second")
	store := NewStore(db)
	sessionID, err := store.StartNormal(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}

	firstReading, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Answer(ctx, sessionID, firstReading.PromptID, answerForState(firstReading)); err != nil {
		t.Fatal(err)
	}
	secondReading, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Answer(ctx, sessionID, secondReading.PromptID, answerForState(secondReading)); err != nil {
		t.Fatal(err)
	}
	firstMeaning, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if firstMeaning.VocabularyID != firstReading.VocabularyID || firstMeaning.PromptType != "meaning" {
		t.Fatalf("third card = word %d/%s, want first word meaning", firstMeaning.VocabularyID, firstMeaning.PromptType)
	}
	if _, err := store.Answer(ctx, sessionID, firstMeaning.PromptID, answerForState(firstMeaning)); err != nil {
		t.Fatal(err)
	}
	current, err := store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if current.VocabularyID != secondReading.VocabularyID || current.PromptType != "meaning" {
		t.Fatalf("fourth card = word %d/%s, want second word meaning", current.VocabularyID, current.PromptType)
	}
	if !current.UndoAvailable {
		t.Fatal("completed spaced item does not offer undo")
	}

	if err := store.Undo(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	for {
		current, err = store.State(ctx, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if current.VocabularyID != firstReading.VocabularyID {
			break
		}
		if _, err := store.Answer(ctx, sessionID, current.PromptID, answerForState(current)); err != nil {
			t.Fatal(err)
		}
	}
	if current.VocabularyID != secondReading.VocabularyID || current.PromptType != "meaning" {
		t.Fatalf("card after undo replay = word %d/%s, want pending second meaning", current.VocabularyID, current.PromptType)
	}
	var readingStatus string
	if err := db.QueryRow("SELECT status FROM review_prompts WHERE id = ?", secondReading.PromptID).Scan(&readingStatus); err != nil {
		t.Fatal(err)
	}
	if readingStatus != "passed" {
		t.Fatalf("previously passed reading became %q after undo", readingStatus)
	}
}

func TestSelfGradeGoodAndAgain(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	if _, err := db.Exec("INSERT INTO user_settings (id, review_mode, retry_count) VALUES (1, 'self_grade', 3)"); err != nil {
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
	if state.AnswerMode != reviewModeSelfGrade {
		t.Fatalf("answer mode = %q", state.AnswerMode)
	}
	if _, err := store.Answer(ctx, sessionID, state.PromptID, answerForState(state)); err == nil || !strings.Contains(err.Error(), "self grading") {
		t.Fatalf("typed answer in self-grade mode error = %v", err)
	}
	if _, err := store.Grade(ctx, sessionID, state.PromptID, true); err != nil {
		t.Fatal(err)
	}
	state, err = store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := store.Grade(ctx, sessionID, state.PromptID, false)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.FinalFailure {
		t.Fatal("Again did not create a final failure")
	}
	state, err = store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Feedback || state.Attempts != state.MaxAttempts {
		t.Fatalf("self-grade failure = feedback %t, attempts %d/%d", state.Feedback, state.Attempts, state.MaxAttempts)
	}
}

func TestSelfGradeExtraStudyDoesNotChangeSRS(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	if _, err := db.Exec("INSERT INTO user_settings (id, review_mode) VALUES (1, 'self_grade')"); err != nil {
		t.Fatal(err)
	}
	insertDueVocabulary(t, db)
	var stageBefore, dueBefore int64
	if err := db.QueryRow("SELECT stage, due_at FROM srs_states LIMIT 1").Scan(&stageBefore, &dueBefore); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	sessionID, err := store.StartExtraSource(ctx, "current", nil)
	if err != nil {
		t.Fatal(err)
	}
	for {
		state, err := store.State(ctx, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if state.Status == "completed" {
			break
		}
		if _, err := store.Grade(ctx, sessionID, state.PromptID, true); err != nil {
			t.Fatal(err)
		}
	}

	var stageAfter, dueAfter int64
	if err := db.QueryRow("SELECT stage, due_at FROM srs_states LIMIT 1").Scan(&stageAfter, &dueAfter); err != nil {
		t.Fatal(err)
	}
	if stageAfter != stageBefore || dueAfter != dueBefore {
		t.Fatalf("self-grade extra study changed SRS from %d/%d to %d/%d", stageBefore, dueBefore, stageAfter, dueAfter)
	}
}

func TestMarkCorrectReversesFinalFailureAndLeechEffects(t *testing.T) {
	ctx, db := openReviewTestDatabase(t)
	if _, err := db.Exec(`
		INSERT INTO user_settings (id, retry_count, leech_failure_threshold)
		VALUES (1, 1, 1)`); err != nil {
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
	failedPromptID := state.PromptID
	if _, err := store.Answer(ctx, sessionID, failedPromptID, "wrong"); err != nil {
		t.Fatal(err)
	}
	var activeLeech int
	if err := db.QueryRow("SELECT active FROM leech_states").Scan(&activeLeech); err != nil {
		t.Fatal(err)
	}
	if activeLeech != 1 {
		t.Fatalf("active leech after failure = %d, want 1", activeLeech)
	}
	if err := store.MarkCorrect(ctx, sessionID, failedPromptID); err != nil {
		t.Fatal(err)
	}
	state, err = store.State(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Feedback || state.PromptID == failedPromptID {
		t.Fatalf("review did not advance after correction: %+v", state)
	}
	var leechFailures int
	if err := db.QueryRow("SELECT active, failures_toward_leech FROM leech_states").Scan(&activeLeech, &leechFailures); err != nil {
		t.Fatal(err)
	}
	if activeLeech != 0 {
		t.Fatalf("active leech after correction = %d, want 0", activeLeech)
	}
	if leechFailures != 0 {
		t.Fatalf("leech failures after correction = %d, want 0", leechFailures)
	}
	var resultCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM review_results").Scan(&resultCount); err != nil {
		t.Fatal(err)
	}
	if resultCount != 0 {
		t.Fatalf("review results before second prompt = %d, want 0", resultCount)
	}
	if _, err := store.Answer(ctx, sessionID, state.PromptID, answerForState(state)); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := db.QueryRow("SELECT outcome FROM review_results").Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "success" {
		t.Fatalf("corrected review outcome = %q, want success", outcome)
	}
}

func promptCardLabels(cards []promptCard) []string {
	labels := make([]string, len(cards))
	for index, card := range cards {
		labels[index] = fmt.Sprintf("%d:%s", card.itemID, card.promptType)
	}
	return labels
}

func reviewItemStages(t *testing.T, db *sql.DB, sessionID int64) []int {
	t.Helper()
	rows, err := db.Query(`
		SELECT ss.stage
		FROM review_session_items rsi
		JOIN srs_states ss ON ss.vocabulary_id = rsi.vocabulary_id
		WHERE rsi.session_id = ?
		ORDER BY rsi.position`, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var stages []int
	for rows.Next() {
		var stage int
		if err := rows.Scan(&stage); err != nil {
			t.Fatal(err)
		}
		stages = append(stages, stage)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return stages
}

func assertStoredPromptSpacing(t *testing.T, db *sql.DB, sessionID int64) {
	t.Helper()
	rows, err := db.Query(`
		SELECT rsi.id, rp.prompt_type, rp.queue_position
		FROM review_prompts rp
		JOIN review_session_items rsi ON rsi.id = rp.session_item_id
		WHERE rsi.session_id = ?
		ORDER BY rp.queue_position`, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type positions struct{ reading, meaning int }
	byItem := make(map[int64]positions)
	seenQueue := make(map[int]struct{})
	for rows.Next() {
		var itemID int64
		var promptType string
		var queuePosition int
		if err := rows.Scan(&itemID, &promptType, &queuePosition); err != nil {
			t.Fatal(err)
		}
		if _, duplicate := seenQueue[queuePosition]; duplicate {
			t.Fatalf("duplicate queue position %d", queuePosition)
		}
		seenQueue[queuePosition] = struct{}{}
		item := byItem[itemID]
		if promptType == "pronunciation" {
			item.reading = queuePosition
		} else {
			item.meaning = queuePosition
		}
		byItem[itemID] = item
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for itemID, item := range byItem {
		distance := item.meaning - item.reading
		if distance < 2 || distance > 5 {
			t.Errorf("item %d prompt distance = %d", itemID, distance)
		}
	}
}
