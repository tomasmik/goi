package mining

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/dictionary/jmdict"
	"github.com/tomasmik/goi/internal/examplegen"
	"github.com/tomasmik/goi/internal/examples"
	"github.com/tomasmik/goi/internal/lessons"
	"github.com/tomasmik/goi/internal/media"
	"github.com/tomasmik/goi/internal/pronunciation"
	"github.com/tomasmik/goi/internal/vocabulary"
	internalweb "github.com/tomasmik/goi/internal/web"
)

func TestMissingCaptureRendersRecoveryPage(t *testing.T) {
	_, db := openMiningTestDatabase(t)
	router := miningTestRouter(t, NewStore(db), "https://goi.example")
	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/mining/captures/999", nil),
		httptest.NewRequest(http.MethodPost, "/mining/captures/999/delete", strings.NewReader("revision=1&confirmed=1")),
	}
	requests[1].Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, request := range requests {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `href="/mining"`) {
			t.Fatalf("missing capture response = %d, %s", response.Code, response.Body.String())
		}
	}
}

func TestMiningCanFindPreviewAndAttachPronunciationAudio(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture, _, err := store.Create(ctx, CreateInput{
		Expression:   "日本",
		ContextText:  "日本に住んでいます。",
		SourceKind:   SourceVideo,
		CaptureNonce: "00000000000000000000000000000098",
	})
	if err != nil {
		t.Fatal(err)
	}
	recordings := &fakePronunciationProvider{
		results: []pronunciation.Recording{{
			ID: 42, SourceName: "Lingua Libre", SourceURL: "https://commons.wikimedia.org/wiki/File:recording.wav",
			LicenseName: "CC0", LicenseURL: "https://creativecommons.org/publicdomain/zero/1.0/",
		}},
		upload: media.Upload{Kind: media.KindAudio, MimeType: "audio/wav", Content: silentMiningWAV()},
	}
	router := miningTestRouterWithServices(t, store, "https://goi.example", nil, recordings)

	search := httptest.NewRecorder()
	router.ServeHTTP(search, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/mining/captures/%d?find_audio=1", capture.ID), nil))
	if search.Code != http.StatusOK {
		t.Fatalf("search status = %d, body = %s", search.Code, search.Body.String())
	}
	for _, expected := range []string{
		"Open Japanese recordings", "Use this recording", "Lingua Libre", "CC0",
		fmt.Sprintf("/pronunciations/%d", recordings.results[0].ID),
	} {
		if !strings.Contains(search.Body.String(), expected) {
			t.Errorf("search page does not contain %q", expected)
		}
	}

	preview := httptest.NewRecorder()
	router.ServeHTTP(preview, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/mining/captures/%d/pronunciations/42", capture.ID), nil))
	if preview.Code != http.StatusOK || preview.Header().Get("Content-Type") != "audio/wav" {
		t.Fatalf("preview response = %d, %q", preview.Code, preview.Header().Get("Content-Type"))
	}

	form := url.Values{"revision": {strconvInt(capture.Revision)}, "reading": {"にほん"}}
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/mining/captures/%d/pronunciations/42", capture.ID), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("attach response = %d, body = %s", response.Code, response.Body.String())
	}
	stored, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PronunciationAudioID == 0 {
		t.Fatal("pronunciation audio was not attached")
	}
}

func TestMiningInboxPaginatesAndPreservesStatus(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	insertMiningCaptures(t, ctx, db, StatusDiscarded, 0, maximumCapturePageSize+1)
	router := miningTestRouter(t, NewStore(db), "https://goi.example")

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/mining?status=discarded", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first page status = %d, body = %s", first.Code, first.Body.String())
	}
	firstBody := first.Body.String()
	if strings.Count(firstBody, `class="mining-card"`) != maximumCapturePageSize {
		t.Fatalf("first page card count = %d", strings.Count(firstBody, `class="mining-card"`))
	}
	for _, expected := range []string{"Page 1 of 2", "capture-100", `href="/mining?page=2&amp;status=discarded" rel="next"`} {
		if !strings.Contains(firstBody, expected) {
			t.Errorf("first page does not contain %q", expected)
		}
	}
	if strings.Contains(firstBody, "capture-000") {
		t.Fatal("first page contains the capture reserved for page 2")
	}

	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/mining?status=discarded&page=2", nil))
	if second.Code != http.StatusOK {
		t.Fatalf("second page status = %d, body = %s", second.Code, second.Body.String())
	}
	secondBody := second.Body.String()
	if strings.Count(secondBody, `class="mining-card"`) != 1 {
		t.Fatalf("second page card count = %d", strings.Count(secondBody, `class="mining-card"`))
	}
	for _, expected := range []string{"Page 2 of 2", "capture-000", `href="/mining?status=discarded" rel="prev"`} {
		if !strings.Contains(secondBody, expected) {
			t.Errorf("second page does not contain %q", expected)
		}
	}
	if strings.Contains(secondBody, "capture-100") {
		t.Fatal("second page contains a capture from page 1")
	}

	clamped := httptest.NewRecorder()
	router.ServeHTTP(clamped, httptest.NewRequest(http.MethodGet, "/mining?status=discarded&page=999", nil))
	if clamped.Code != http.StatusOK || !strings.Contains(clamped.Body.String(), "Page 2 of 2") || !strings.Contains(clamped.Body.String(), "capture-000") {
		t.Fatalf("clamped page = %d, %s", clamped.Code, clamped.Body.String())
	}
}

func TestInvalidMiningFilterRendersRecoveryPage(t *testing.T) {
	_, db := openMiningTestDatabase(t)
	response := httptest.NewRecorder()
	miningTestRouter(t, NewStore(db), "https://goi.example").ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "/mining?status=unknown", nil),
	)

	body := response.Body.String()
	if response.Code != http.StatusBadRequest || !strings.Contains(body, "Choose pending, accepted, or discarded captures.") {
		t.Fatalf("response = %d, body = %s", response.Code, body)
	}
	for _, expected := range []string{`class="site-header"`, `href="/mining"`, "Back to mining"} {
		if !strings.Contains(body, expected) {
			t.Errorf("rendered error does not contain %q: %s", expected, body)
		}
	}
}

func TestCaptureFormCreatesPendingCapture(t *testing.T) {
	_, db := openMiningTestDatabase(t)
	router := miningTestRouter(t, NewStore(db), "https://goi.example/base")
	form := url.Values{
		"expression":              {"食べる"},
		"context_text":            {"昨日、寿司を食べる。"},
		"source_kind":             {"video"},
		"source_title":            {"Japanese lesson"},
		"source_url":              {"https://video.example/watch?v=1"},
		"source_position_seconds": {"12.345"},
		"capture_nonce":           {"00000000000000000000000000000011"},
	}
	response := serveMiningForm(router, http.MethodPost, "/mining/captures", form)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if location := response.Header().Get("Location"); location != "/mining/captures/1?saved=1" {
		t.Fatalf("location = %q", location)
	}
	capture, err := NewStore(db).Get(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if capture.Status != StatusPending || capture.SourcePositionMS == nil || *capture.SourcePositionMS != 12_345 {
		t.Fatalf("capture = %#v", capture)
	}
}

func TestCaptureFormRejectsUnsafeSourceURL(t *testing.T) {
	_, db := openMiningTestDatabase(t)
	router := miningTestRouter(t, NewStore(db), "https://goi.example")
	form := url.Values{
		"expression":    {"猫"},
		"source_kind":   {"web"},
		"source_url":    {"javascript:alert(1)"},
		"capture_nonce": {"00000000000000000000000000000012"},
	}
	response := serveMiningForm(router, http.MethodPost, "/mining/captures", form)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "source URL must use http or https") {
		t.Fatalf("body does not explain invalid URL: %s", response.Body.String())
	}
}

func TestCaptureUpdatePreservesSubmittedFieldsAfterValidationError(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "猫", "00000000000000000000000000000043")
	form := url.Values{
		"revision":                {strconvInt(capture.Revision)},
		"capture_nonce":           {capture.CaptureNonce},
		"expression":              {""},
		"context_text":            {"new context"},
		"source_kind":             {"web"},
		"source_title":            {"new source"},
		"source_url":              {"https://example.com/new"},
		"source_position_seconds": {""},
	}
	response := serveMiningForm(
		miningTestRouter(t, store, "https://goi.example"),
		http.MethodPost,
		fmt.Sprintf("/mining/captures/%d", capture.ID),
		form,
	)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{`name="expression" value=""`, "new context", "new source", "https://example.com/new"} {
		if !strings.Contains(body, expected) {
			t.Errorf("response does not preserve %q: %s", expected, body)
		}
	}
	if !strings.Contains(body, `id="capture-source-title"`) {
		t.Fatal("source editor was not rendered for its validation error")
	}
}

func TestCaptureUpdateFallsBackToStoredFieldsWhenFormCannotBeParsed(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "猫", "00000000000000000000000000000044")
	request := httptest.NewRequest(
		http.MethodPost,
		fmt.Sprintf("/mining/captures/%d", capture.ID),
		strings.NewReader(strings.Repeat("x", captureBodyLimit+1)),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	miningTestRouter(t, store, "https://goi.example").ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "form is too large") || !strings.Contains(body, `name="expression" value="猫"`) {
		t.Fatalf("response did not restore the stored capture: %s", body)
	}
}

func TestStaleCaptureEditKeepsItsRevisionUntilConflictReload(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "猫", "00000000000000000000000000000045")
	latest, err := store.Update(ctx, capture.ID, capture.Revision, UpdateInput{
		Expression:  "犬",
		ContextText: "latest context",
		SourceKind:  SourceManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	router := miningTestRouter(t, store, "https://goi.example")
	target := fmt.Sprintf("/mining/captures/%d", capture.ID)
	staleForm := url.Values{
		"revision":                {strconvInt(capture.Revision)},
		"capture_nonce":           {capture.CaptureNonce},
		"expression":              {""},
		"context_text":            {"stale context"},
		"source_kind":             {"manual"},
		"source_title":            {""},
		"source_url":              {""},
		"source_position_seconds": {""},
	}
	response := serveMiningForm(router, http.MethodPost, target, staleForm)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("validation response = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	editForm := miningFormBlock(t, body, fmt.Sprintf(`<form class="inline-form" method="post" action="%s"`, target))
	requireMiningFormRevision(t, editForm, capture.Revision)
	if !strings.Contains(editForm, "stale context") {
		t.Fatalf("stale edit values were not preserved: %s", editForm)
	}
	manualForm := miningFormBlock(t, body, fmt.Sprintf(`<form class="mining-card-editor" method="post" action="%s/accept"`, target))
	requireMiningFormRevision(t, manualForm, latest.Revision)

	staleForm.Set("expression", "狐")
	response = serveMiningForm(router, http.MethodPost, target, staleForm)
	if response.Code != http.StatusConflict {
		t.Fatalf("conflict response = %d, body = %s", response.Code, response.Body.String())
	}
	body = response.Body.String()
	if !strings.Contains(body, "changed in another tab") {
		t.Fatalf("conflict response is unclear: %s", body)
	}
	editForm = miningFormBlock(t, body, fmt.Sprintf(`<form class="inline-form" method="post" action="%s"`, target))
	requireMiningFormRevision(t, editForm, latest.Revision)
	if !strings.Contains(editForm, `name="expression" value="犬"`) || !strings.Contains(editForm, "latest context") || strings.Contains(editForm, "stale context") {
		t.Fatalf("conflict did not reload the latest edit values: %s", editForm)
	}

	retry := url.Values{
		"revision":                {strconvInt(latest.Revision)},
		"capture_nonce":           {capture.CaptureNonce},
		"expression":              {latest.Expression},
		"context_text":            {latest.ContextText},
		"source_kind":             {string(latest.SourceKind)},
		"source_title":            {latest.SourceTitle},
		"source_url":              {latest.SourceURL},
		"source_position_seconds": {""},
	}
	response = serveMiningForm(router, http.MethodPost, target, retry)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("safe retry response = %d, body = %s", response.Code, response.Body.String())
	}
	stored, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Expression != latest.Expression || stored.ContextText != latest.ContextText || stored.Revision != latest.Revision {
		t.Fatalf("safe retry changed the winning edit: %#v", stored)
	}
}

func TestStaleManualAcceptanceKeepsItsRevisionUntilConflictReload(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "猫", "00000000000000000000000000000046")
	latest, err := store.Update(ctx, capture.ID, capture.Revision, UpdateInput{Expression: "犬", SourceKind: SourceManual})
	if err != nil {
		t.Fatal(err)
	}
	router := miningTestRouter(t, store, "https://goi.example")
	target := fmt.Sprintf("/mining/captures/%d/accept", capture.ID)
	staleForm := url.Values{
		"revision":      {strconvInt(capture.Revision)},
		"pronunciation": {"ねこ"},
		"meanings":      {""},
	}
	response := serveMiningForm(router, http.MethodPost, target, staleForm)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("validation response = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	manualForm := miningFormBlock(t, body, fmt.Sprintf(`<form class="mining-card-editor" method="post" action="%s"`, target))
	requireMiningFormRevision(t, manualForm, capture.Revision)
	if !strings.Contains(manualForm, `value="ねこ"`) {
		t.Fatalf("submitted manual values were not preserved: %s", manualForm)
	}
	editForm := miningFormBlock(t, body, fmt.Sprintf(`<form class="inline-form" method="post" action="/mining/captures/%d"`, capture.ID))
	requireMiningFormRevision(t, editForm, latest.Revision)

	staleForm.Set("meanings", "cat")
	response = serveMiningForm(router, http.MethodPost, target, staleForm)
	if response.Code != http.StatusConflict {
		t.Fatalf("conflict response = %d, body = %s", response.Code, response.Body.String())
	}
	body = response.Body.String()
	manualForm = miningFormBlock(t, body, fmt.Sprintf(`<form class="mining-card-editor" method="post" action="%s"`, target))
	requireMiningFormRevision(t, manualForm, latest.Revision)
	if strings.Contains(manualForm, `value="ねこ"`) || strings.Contains(manualForm, ">cat</textarea>") {
		t.Fatalf("conflict retained stale manual values: %s", manualForm)
	}
	var vocabularyCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary").Scan(&vocabularyCount); err != nil {
		t.Fatal(err)
	}
	if vocabularyCount != 0 {
		t.Fatalf("stale acceptance created %d vocabulary items", vocabularyCount)
	}

	retry := url.Values{
		"revision":      {strconvInt(latest.Revision)},
		"pronunciation": {"いぬ"},
		"meanings":      {"dog"},
	}
	response = serveMiningForm(router, http.MethodPost, target, retry)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("safe retry response = %d, body = %s", response.Code, response.Body.String())
	}
	var expression, meaning string
	if err := db.QueryRow(`
		SELECT v.expression, m.text
		FROM vocabulary v
		JOIN meanings m ON m.vocabulary_id = v.id AND m.position = 0`).Scan(&expression, &meaning); err != nil {
		t.Fatal(err)
	}
	if expression != "犬" || meaning != "dog" {
		t.Fatalf("safe retry created %q with meaning %q", expression, meaning)
	}
}

func TestBulkAddProcessesClearMatchesAndLeavesChoices(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	clear := createMiningCapture(t, ctx, store, "猫", "000000000000000000000000000000a1")
	needsChoice := createMiningCapture(t, ctx, store, "開く", "000000000000000000000000000000a2")
	completeMiningEnrichment(t, ctx, store, readyMiningMatch("猫", "ねこ", "cat"))

	router := miningTestRouter(t, store, "https://goi.example")
	response := serveMiningForm(router, http.MethodPost, "/mining/bulk", url.Values{
		"action":     {"accept_ready"},
		"capture_id": {strconvInt(clear.ID), strconvInt(needsChoice.ID)},
	})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("bulk response = %d, body = %s", response.Code, response.Body.String())
	}
	location := response.Header().Get("Location")
	if !strings.Contains(location, "bulk_added=1") || !strings.Contains(location, "bulk_review=1") {
		t.Fatalf("bulk redirect = %q", location)
	}

	request := httptest.NewRequest(http.MethodGet, location, nil)
	page := httptest.NewRecorder()
	router.ServeHTTP(page, request)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "1 added · 1 still need a choice") {
		t.Fatalf("bulk result page = %d, body = %s", page.Code, page.Body.String())
	}
	storedClear, err := store.Get(ctx, clear.ID)
	if err != nil || storedClear.Status != StatusAccepted {
		t.Fatalf("clear capture = %#v, %v", storedClear, err)
	}
	storedChoice, err := store.Get(ctx, needsChoice.ID)
	if err != nil || storedChoice.Status != StatusPending {
		t.Fatalf("choice capture = %#v, %v", storedChoice, err)
	}
}

func TestBulkAddNeverChoosesAnAmbiguousDictionaryMatch(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "あく", "000000000000000000000000000000a3")
	completeMiningEnrichment(t, ctx, store, jmdict.Match{
		State: jmdict.MatchAmbiguous, SourceCreated: "2026-07-26", SourceVersion: "1.10",
		Candidates: []jmdict.Candidate{
			miningCandidate(101, "開く", "あく", "to open"),
			miningCandidate(102, "空く", "あく", "to become empty"),
		},
	})
	result, err := bulkAcceptMining(ctx, store, []int64{capture.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 0 || result.Attached != 0 || result.NeedsReview != 1 {
		t.Fatalf("bulk result = %+v", result)
	}
	stored, err := store.Get(ctx, capture.ID)
	if err != nil || stored.Status != StatusPending {
		t.Fatalf("ambiguous capture = %#v, %v", stored, err)
	}
	var vocabularyCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary").Scan(&vocabularyCount); err != nil {
		t.Fatal(err)
	}
	if vocabularyCount != 0 {
		t.Fatalf("ambiguous bulk acceptance created %d vocabulary rows", vocabularyCount)
	}
}

func TestBulkAddCompletesKnownExistingVocabularyAndQueuesLesson(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	if _, err := vocabulary.NewStore(db).AddKnown(ctx, "猫"); err != nil {
		t.Fatal(err)
	}
	capture := createMiningCapture(t, ctx, store, "猫", "000000000000000000000000000000a4")
	completeMiningEnrichment(t, ctx, store, readyMiningMatch("猫", "ねこ", "cat"))

	result, err := bulkAcceptMining(ctx, store, []int64{capture.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 0 || result.Attached != 1 || result.NeedsReview != 0 {
		t.Fatalf("bulk result = %+v", result)
	}
	var status, pronunciation, meaning string
	if err := db.QueryRow(`
		SELECT v.status, v.pronunciation, m.text
		FROM vocabulary v
		JOIN meanings m ON m.vocabulary_id = v.id AND m.position = 0
		WHERE v.normalized_expression = '猫'`).Scan(&status, &pronunciation, &meaning); err != nil {
		t.Fatal(err)
	}
	if status != "unlearned" || pronunciation != "ねこ" || meaning != "cat" {
		t.Fatalf("completed vocabulary = status %q, reading %q, meaning %q", status, pronunciation, meaning)
	}
	available, err := lessons.NewStore(db).AvailableCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if available != 1 {
		t.Fatalf("available lessons = %d, want 1", available)
	}
}

func TestBulkAddLeavesExistingHomographForReview(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	if _, err := vocabulary.NewStore(db).Create(ctx, vocabulary.CreateInput{
		Expression:    "開く",
		Pronunciation: "ひらく",
		Meanings:      []string{"to open"},
	}); err != nil {
		t.Fatal(err)
	}
	capture := createMiningCapture(t, ctx, store, "開く", "000000000000000000000000000000a5")
	completeMiningEnrichment(t, ctx, store, readyMiningMatch("開く", "あく", "to become open"))

	result, err := bulkAcceptMining(ctx, store, []int64{capture.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 0 || result.Attached != 0 || result.NeedsReview != 1 {
		t.Fatalf("bulk result = %+v", result)
	}
	stored, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusPending || stored.VocabularyID != nil {
		t.Fatalf("homograph capture = %#v", stored)
	}
	var pronunciation string
	if err := db.QueryRow("SELECT pronunciation FROM vocabulary WHERE normalized_expression = '開く'").Scan(&pronunciation); err != nil {
		t.Fatal(err)
	}
	if pronunciation != "ひらく" {
		t.Fatalf("existing homograph reading = %q", pronunciation)
	}
}

func TestMiningInboxEscapesCapturedContent(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	_, _, err := store.Create(ctx, CreateInput{
		Expression:   `<img src=x onerror=alert(1)>`,
		ContextText:  `<script>alert("context")</script>`,
		SourceKind:   SourceWeb,
		CaptureNonce: "00000000000000000000000000000013",
	})
	if err != nil {
		t.Fatal(err)
	}
	router := miningTestRouter(t, store, "https://goi.example")
	request := httptest.NewRequest(http.MethodGet, "/mining", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, `<script>alert`) || strings.Contains(body, `<img src=x`) {
		t.Fatalf("captured markup was rendered as HTML: %s", body)
	}
	if !strings.Contains(body, `&lt;script&gt;`) || !strings.Contains(body, `&lt;img src=x`) {
		t.Fatalf("escaped capture content not found: %s", body)
	}
}

func TestAcceptCaptureCreatesVocabulary(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	position := int64(42_000)
	capture, _, err := store.Create(ctx, CreateInput{
		Expression:       "食べる",
		SourceKind:       SourceWeb,
		SourceTitle:      "Japanese lesson",
		SourceURL:        "https://user:secret@example.com/lesson",
		SourcePositionMS: &position,
		CaptureNonce:     "00000000000000000000000000000014",
	})
	if err != nil {
		t.Fatal(err)
	}
	router := miningTestRouter(t, store, "https://goi.example")
	form := url.Values{
		"revision":      {strconvInt(capture.Revision)},
		"expression":    {"飲む"},
		"source_title":  {"spoofed source"},
		"source_url":    {"https://attacker:secret@example.net/"},
		"pronunciation": {"たべる"},
		"meanings":      {"to eat\nto have a meal"},
	}
	response := serveMiningForm(router, http.MethodPost, fmt.Sprintf("/mining/captures/%d/accept", capture.ID), form)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/vocabulary/1" {
		t.Fatalf("response = %d %q, body = %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	accepted, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != StatusAccepted || accepted.VocabularyID == nil {
		t.Fatalf("accepted capture = %#v", accepted)
	}
	var expression, sourceLabel string
	if err := db.QueryRow("SELECT expression, source_label FROM vocabulary WHERE id = ?", *accepted.VocabularyID).Scan(&expression, &sourceLabel); err != nil {
		t.Fatal(err)
	}
	if expression != "食べる" || sourceLabel != "Japanese lesson" {
		t.Fatalf("vocabulary expression = %q, source label = %q", expression, sourceLabel)
	}
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/mining/captures/%d", capture.ID), nil)
	detail := httptest.NewRecorder()
	router.ServeHTTP(detail, request)
	if detail.Code != http.StatusOK ||
		!strings.Contains(detail.Body.String(), "Permanently delete this capture, any mined example, and captured media?") ||
		!strings.Contains(detail.Body.String(), "Japanese lesson") ||
		!strings.Contains(detail.Body.String(), "0:42") {
		t.Fatalf("accepted detail = %d, body = %s", detail.Code, detail.Body.String())
	}
}

func TestMiningDetailRendersReadyDictionarySuggestion(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "開ける", "00000000000000000000000000000031")
	completeMiningEnrichment(t, ctx, store, readyMiningMatch("開ける", "あける", "to open"))

	router := miningTestRouter(t, store, "https://goi.example")
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/mining/captures/%d", capture.ID), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{"Add to vocabulary", `name="candidate_id"`, `value="あける"`, "to open", "JMdict/EDRDG", "CC BY-SA 4.0"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("body does not contain %q: %s", expected, body)
		}
	}
}

func TestMiningDetailKeepsTranslationAvailableWithoutExampleGeneration(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "開ける", "00000000000000000000000000000131")
	completeMiningEnrichment(t, ctx, store, readyMiningMatch("開ける", "あける", "to open"))
	generator := &fakeExampleGenerator{generationOff: true}

	response := httptest.NewRecorder()
	miningTestRouterWithGenerator(t, store, "https://goi.example", generator).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, fmt.Sprintf("/mining/captures/%d", capture.ID), nil),
	)
	body := response.Body.String()
	if !strings.Contains(body, `data-remote-translation="true"`) {
		t.Fatalf("configured translator was not exposed to the mining form: %s", body)
	}
	if strings.Contains(body, "Generate example") {
		t.Fatal("example generation should remain unavailable")
	}
}

func TestMiningDetailShowsCardFieldsAndAllCapturedMedia(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture, _, err := store.Create(ctx, CreateInput{
		RawText:      "育てる",
		Expression:   "育てる",
		ContextText:  "ぶどうを育てています。",
		SourceKind:   SourceVideo,
		CaptureNonce: "00000000000000000000000000000034",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddMedia(ctx, capture.ID, capture.Revision, []media.Upload{
		{Kind: media.KindAudio, MimeType: "audio/webm", Content: []byte("first")},
		{Kind: media.KindAudio, MimeType: "audio/webm", Content: []byte("second")},
	}, &media.Upload{Kind: media.KindImage, MimeType: "image/png", Content: []byte("frame")}, nil); err != nil {
		t.Fatal(err)
	}
	completeMiningEnrichment(t, ctx, store, readyMiningMatch("育てる", "そだてる", "to raise"))

	response := httptest.NewRecorder()
	miningTestRouter(t, store, "https://goi.example").ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, fmt.Sprintf("/mining/captures/%d", capture.ID), nil),
	)
	body := response.Body.String()
	for _, expected := range []string{
		"Sentence context",
		"ぶどうを育てています。",
		"Picture and audio",
		"2 included",
		"Remove track 1",
		"Remove track 2",
		`name="sentence_audio"`,
		`name="video_frame"`,
		`name="pronunciation_audio"`,
		"Recommended",
		"Find word audio",
		"Open in Jisho",
		"Search Forvo",
		"Reading",
		"Meanings",
		"Japanese sentence",
		"English translation",
		"Word in sentence",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("mining review does not contain %q", expected)
		}
	}
	if strings.Count(body, `<audio controls`) != 2 {
		t.Errorf("audio players = %d, want 2", strings.Count(body, `<audio controls`))
	}
	if strings.Contains(body, "Save media changes") {
		t.Error("media should not have a separate save action")
	}
	if strings.Index(body, "Picture and audio") > strings.Index(body, ">Save card</button>") {
		t.Error("media should be part of the card before its save action")
	}
	if strings.Index(body, "Open in Jisho") > strings.Index(body, "Picture and audio") {
		t.Error("word lookup should appear with the word, before card media")
	}
	if !strings.Contains(body, `class="mining-card-editor" method="post"`) || !strings.Contains(body, `enctype="multipart/form-data"`) {
		t.Error("card editor should submit its fields and media together")
	}
}

func TestMiningMediaUploadAcceptsMultipleAudioTracks(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture, _, err := store.Create(ctx, CreateInput{
		Expression:   "育てる",
		ContextText:  "ぶどうを育てています。",
		SourceKind:   SourceVideo,
		CaptureNonce: "00000000000000000000000000000036",
	})
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("revision", strconvInt(capture.Revision)); err != nil {
		t.Fatal(err)
	}
	for index, filename := range []string{"first.wav", "second.wav"} {
		part, err := writer.CreateFormFile("sentence_audio", filename)
		if err != nil {
			t.Fatal(err)
		}
		content := silentMiningWAV()
		content[44] = byte(index)
		if _, err := part.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/mining/captures/%d/media", capture.ID), &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	miningTestRouter(t, store, "https://goi.example").ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	stored, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.SentenceAudioIDs) != 2 {
		t.Fatalf("audio IDs = %v, want two stored tracks", stored.SentenceAudioIDs)
	}
}

func TestAcceptSavesCardFieldsAndMediaTogether(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture, _, err := store.Create(ctx, CreateInput{
		Expression:   "育てる",
		ContextText:  "ぶどうを育てています。",
		SourceKind:   SourceVideo,
		CaptureNonce: "00000000000000000000000000000136",
	})
	if err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := []struct {
		name  string
		value string
	}{
		{"revision", strconvInt(capture.Revision)},
		{"pronunciation", "そだてる"},
		{"meanings", "to raise"},
		{"example_sentence", "ぶどうを育てています。"},
		{"example_translation", "I am growing grapes."},
		{"example_target", "育てて"},
	}
	for _, field := range fields {
		if err := writer.WriteField(field.name, field.value); err != nil {
			t.Fatal(err)
		}
	}
	uploads := []struct {
		field    string
		filename string
	}{
		{"sentence_audio", "sentence.wav"},
		{"pronunciation_audio", "word.wav"},
	}
	for _, upload := range uploads {
		part, err := writer.CreateFormFile(upload.field, upload.filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(silentMiningWAV()); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/mining/captures/%d/accept", capture.ID), &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	miningTestRouter(t, store, "https://goi.example").ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}

	accepted, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.VocabularyID == nil {
		t.Fatal("capture was not linked to the saved card")
	}
	item, err := vocabulary.NewStore(db).Get(ctx, *accepted.VocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Media) != 1 || item.Media[0].Kind != "audio" {
		t.Fatalf("card media = %+v, want pronunciation audio", item.Media)
	}
	example, err := examples.NewStore(db).Preferred(ctx, *accepted.VocabularyID)
	if err != nil {
		t.Fatal(err)
	}
	if example.SentenceAudioID == 0 {
		t.Fatal("sentence audio was not saved with the example")
	}
}

func TestGenerateFillsExampleFieldsWithoutSavingTheCard(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture, _, err := store.Create(ctx, CreateInput{
		RawText:      "育てる",
		Expression:   "育てる",
		ContextText:  "ぶどうを育てています。",
		SourceKind:   SourceVideo,
		CaptureNonce: "00000000000000000000000000000035",
	})
	if err != nil {
		t.Fatal(err)
	}
	completeMiningEnrichment(t, ctx, store, readyMiningMatch("育てる", "そだてる", "to raise"))
	generator := &fakeExampleGenerator{result: examplegen.Example{
		Sentence:      "ぶどうを育てています。",
		Translation:   "I am growing grapes.",
		TargetSurface: "育てています",
	}}
	form := url.Values{
		"revision":         {strconvInt(capture.Revision)},
		"candidate_id":     {"1"},
		"pronunciation":    {"そだてる"},
		"meanings":         {"to raise\nto grow"},
		"example_sentence": {capture.ContextText},
		"example_target":   {capture.RawText},
	}
	response := serveMiningForm(
		miningTestRouterWithGenerator(t, store, "https://goi.example", generator),
		http.MethodPost,
		fmt.Sprintf("/mining/captures/%d/generate", capture.ID),
		form,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{"I am growing grapes.", `value="育てています"`, "Example fields generated. Check them before saving."} {
		if !strings.Contains(body, expected) {
			t.Errorf("generated review does not contain %q", expected)
		}
	}
	if generator.request.Sentence != capture.ContextText || generator.request.Expression != capture.Expression {
		t.Fatalf("generation request = %+v", generator.request)
	}
	var vocabularyCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary").Scan(&vocabularyCount); err != nil {
		t.Fatal(err)
	}
	if vocabularyCount != 0 {
		t.Fatalf("generation saved %d vocabulary entries", vocabularyCount)
	}
}

func TestTranslateFillsOnlyTheTranslationWithoutSavingTheCard(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture, _, err := store.Create(ctx, CreateInput{
		Expression:   "育てる",
		ContextText:  "ぶどうを育てています。",
		SourceKind:   SourceVideo,
		CaptureNonce: "00000000000000000000000000000036",
	})
	if err != nil {
		t.Fatal(err)
	}
	generator := &fakeExampleGenerator{translation: examplegen.Translation{Text: "I am growing grapes."}}
	form := url.Values{
		"revision":         {strconvInt(capture.Revision)},
		"pronunciation":    {"そだてる"},
		"meanings":         {"to raise"},
		"example_sentence": {capture.ContextText},
		"example_target":   {"育てています"},
	}
	response := serveMiningForm(
		miningTestRouterWithGenerator(t, store, "https://goi.example", generator),
		http.MethodPost,
		fmt.Sprintf("/mining/captures/%d/translate", capture.ID),
		form,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{"I am growing grapes.", "Translation added. Check it before saving.", "育てています"} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Errorf("translated review does not contain %q", expected)
		}
	}
	if generator.translationText != capture.ContextText {
		t.Fatalf("translation input = %q", generator.translationText)
	}
	var vocabularyCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary").Scan(&vocabularyCount); err != nil {
		t.Fatal(err)
	}
	if vocabularyCount != 0 {
		t.Fatalf("translation saved %d vocabulary entries", vocabularyCount)
	}
}

func TestMiningDetailRendersAmbiguousChoices(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "あく", "00000000000000000000000000000032")
	match := jmdict.Match{
		State: jmdict.MatchAmbiguous, SourceCreated: "2026-07-26", SourceVersion: "1.10",
		Candidates: []jmdict.Candidate{
			miningCandidate(101, "開く", "あく", "to open"),
			miningCandidate(102, "空く", "あく", "to become empty"),
		},
	}
	match.Candidates[0].Priority = 0
	match.Candidates[1].Priority = 68*1001 + 68
	if _, err := db.Exec(`UPDATE mining_captures SET suggested_entry_sequence = ? WHERE id = ?`, 102, capture.ID); err != nil {
		t.Fatal(err)
	}
	completeMiningEnrichment(t, ctx, store, match)
	capture, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	previews := (&Handler{store: store, dictionary: miningTestDictionary(store)}).candidatePreviews(ctx, []Capture{capture})
	preview := previews[capture.ID]
	if preview.Reading != "あく" || preview.Meaning != "to become empty" || preview.Count != 2 {
		t.Fatalf("candidate preview = %#v", preview)
	}

	router := miningTestRouter(t, store, "https://goi.example")
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/mining/captures/%d", capture.ID), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	body := response.Body.String()
	if !strings.Contains(body, "Which entry fits this sentence?") || strings.Count(body, `name="candidate_id"`) != 2 || !strings.Contains(body, "to become empty") {
		t.Fatalf("ambiguous detail body = %s", body)
	}
	if strings.Count(body, `name="meanings" rows="5"`) != 2 || !strings.Contains(body, `>to become empty</textarea>`) {
		t.Fatalf("dictionary meanings are not visible and editable: %s", body)
	}
	if strings.Count(body, `data-mining-candidate-choice=`) != 2 ||
		strings.Count(body, `data-mining-candidate-editor`) != 2 ||
		strings.Count(body, `aria-pressed="true"`) != 1 ||
		strings.Count(body, `data-mining-candidate-editor data-mining-dirty-form hidden`) != 1 {
		t.Fatalf("ambiguous choices are not presented as one compact picker and editor: %s", body)
	}
	if !strings.Contains(body, "to open") || !strings.Contains(body, "to become empty") {
		t.Fatalf("candidate summaries do not expose their meanings: %s", body)
	}
	if strings.Contains(body, "Chosen while mining") || strings.Contains(body, "Entry chosen") {
		t.Fatalf("internal selection state is exposed to the user: %s", body)
	}
	if strings.Count(body, `title="Estimated from JMdict priority data"`) < 2 ||
		!strings.Contains(body, "Commonness 75/100") || !strings.Contains(body, "Commonness 6/100") ||
		!strings.Contains(body, ">75/100</span>") || !strings.Contains(body, ">6/100</span>") {
		t.Fatalf("frequency cues are missing: %s", body)
	}
}

func TestCaptureActionErrorsRenderDetailPage(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "猫", "00000000000000000000000000000042")
	router := miningTestRouter(t, store, "https://goi.example")

	tests := []struct {
		name    string
		path    string
		form    url.Values
		message string
	}{
		{
			name:    "invalid revision",
			path:    fmt.Sprintf("/mining/captures/%d/discard", capture.ID),
			form:    url.Values{"revision": {"invalid"}},
			message: "The capture action form is out of date.",
		},
		{
			name:    "missing deletion confirmation",
			path:    fmt.Sprintf("/mining/captures/%d/delete", capture.ID),
			form:    url.Values{"revision": {strconvInt(capture.Revision)}},
			message: "Confirm permanent capture deletion before continuing.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := serveMiningForm(router, http.MethodPost, test.path, test.form)
			body := response.Body.String()
			if response.Code != http.StatusBadRequest || !strings.Contains(body, test.message) {
				t.Fatalf("response = %d, body = %s", response.Code, body)
			}
			for _, expected := range []string{
				`class="site-header"`,
				`class="alert alert-error" role="alert"`,
				`<h1 class="jp-word" lang="ja">猫</h1>`,
				fmt.Sprintf(`name="revision" value="%d"`, capture.Revision),
			} {
				if !strings.Contains(body, expected) {
					t.Errorf("rendered error does not contain %q: %s", expected, body)
				}
			}
		})
	}
}

func TestPermanentCaptureDeletionRequiresServerConfirmation(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "猫", "00000000000000000000000000000043")
	router := miningTestRouter(t, store, "https://goi.example")
	path := fmt.Sprintf("/mining/captures/%d/delete", capture.ID)
	form := url.Values{"revision": {strconvInt(capture.Revision)}}

	response := serveMiningForm(router, http.MethodPost, path, form)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed response = %d, body = %s", response.Code, response.Body.String())
	}
	if _, err := store.Get(ctx, capture.ID); err != nil {
		t.Fatalf("unconfirmed deletion removed capture: %v", err)
	}

	form.Set("confirmed", "1")
	response = serveMiningForm(router, http.MethodPost, path, form)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/mining" {
		t.Fatalf("confirmed response = %d %q, body = %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if _, err := store.Get(ctx, capture.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted capture error = %v, want sql.ErrNoRows", err)
	}
}

func TestEnrichmentViewLabelsEveryState(t *testing.T) {
	tests := []struct {
		state   EnrichmentState
		heading string
	}{
		{EnrichmentReady, "Add to vocabulary"},
		{EnrichmentAmbiguous, "Which entry fits this sentence?"},
		{EnrichmentNoMatch, "No match found"},
		{EnrichmentFailed, "Dictionary unavailable"},
	}
	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			view := newEnrichmentView(Enrichment{State: test.state}, Capture{}, detailForm{})
			if view.Heading != test.heading || view.Message == "" {
				t.Fatalf("view = %#v", view)
			}
			if test.state == EnrichmentFailed && strings.Contains(strings.ToLower(view.Message), "unavailable") {
				t.Fatalf("failed lookup message assumes dictionary availability: %q", view.Message)
			}
		})
	}
}

func TestAcceptDictionaryCandidateUsesCopiedDefaults(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "開ける", "00000000000000000000000000000033")
	completeMiningEnrichment(t, ctx, store, readyMiningMatch("開ける", "あける", "to open"))

	router := miningTestRouter(t, store, "https://goi.example")
	form := url.Values{
		"revision":     {strconvInt(capture.Revision)},
		"candidate_id": {"1"},
	}
	response := serveMiningForm(router, http.MethodPost, fmt.Sprintf("/mining/captures/%d/accept", capture.ID), form)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/vocabulary/1" {
		t.Fatalf("response = %d %q, body = %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	var pronunciation, meaning string
	if err := db.QueryRow(`
		SELECT v.pronunciation, m.text
		FROM vocabulary v
		JOIN meanings m ON m.vocabulary_id = v.id AND m.position = 0
		WHERE v.id = 1`).Scan(&pronunciation, &meaning); err != nil {
		t.Fatal(err)
	}
	if pronunciation != "あける" || meaning != "to open" {
		t.Fatalf("accepted defaults = %q, %q", pronunciation, meaning)
	}
}

func TestAcceptDictionaryCandidatePreservesEdits(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "開ける", "00000000000000000000000000000034")
	completeMiningEnrichment(t, ctx, store, readyMiningMatch("開ける", "あける", "to open"))
	form := url.Values{
		"revision":      {strconvInt(capture.Revision)},
		"candidate_id":  {"1"},
		"pronunciation": {"ひらける"},
		"meanings":      {"custom meaning"},
		"notes":         {"my note"},
	}
	response := serveMiningForm(miningTestRouter(t, store, "https://goi.example"), http.MethodPost, fmt.Sprintf("/mining/captures/%d/accept", capture.ID), form)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var pronunciation, meaning, notes string
	if err := db.QueryRow(`
		SELECT v.pronunciation, m.text, v.notes
		FROM vocabulary v
		JOIN meanings m ON m.vocabulary_id = v.id AND m.position = 0
		WHERE v.id = 1`).Scan(&pronunciation, &meaning, &notes); err != nil {
		t.Fatal(err)
	}
	if pronunciation != "ひらける" || meaning != "custom meaning" || notes != "my note" {
		t.Fatalf("accepted edits = %q, %q, %q", pronunciation, meaning, notes)
	}
}

func TestStaleCandidateAcceptanceReloadsCurrentCandidateBeforeRetry(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "猫", "00000000000000000000000000000047")
	completeMiningEnrichment(t, ctx, store, readyMiningMatch("猫", "ねこ", "cat"))
	staleCandidateID := int64(1)

	latest, err := store.Update(ctx, capture.ID, capture.Revision, UpdateInput{Expression: "犬", SourceKind: SourceManual})
	if err != nil {
		t.Fatal(err)
	}
	completeMiningEnrichment(t, ctx, store, readyMiningMatch("犬", "いぬ", "dog"))
	currentCandidateID := int64(1)

	router := miningTestRouter(t, store, "https://goi.example")
	target := fmt.Sprintf("/mining/captures/%d/accept", capture.ID)
	staleForm := url.Values{
		"revision":      {strconvInt(capture.Revision)},
		"candidate_id":  {strconvInt(staleCandidateID)},
		"pronunciation": {"ねこ"},
		"meanings":      {"cat"},
	}
	response := serveMiningForm(router, http.MethodPost, target, staleForm)
	if response.Code != http.StatusConflict {
		t.Fatalf("conflict response = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	candidateForm := miningFormBlock(t, body, fmt.Sprintf(`<form class="mining-card-editor" method="post" action="%s"`, target))
	requireMiningFormRevision(t, candidateForm, latest.Revision)
	if !strings.Contains(candidateForm, fmt.Sprintf(`name="candidate_id" value="%d"`, currentCandidateID)) ||
		!strings.Contains(candidateForm, `value="いぬ"`) ||
		!strings.Contains(candidateForm, ">dog</textarea>") ||
		strings.Contains(candidateForm, `value="ねこ"`) {
		t.Fatalf("conflict did not reload the current candidate: %s", candidateForm)
	}
	var vocabularyCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary").Scan(&vocabularyCount); err != nil {
		t.Fatal(err)
	}
	if vocabularyCount != 0 {
		t.Fatalf("stale candidate created %d vocabulary items", vocabularyCount)
	}

	retry := url.Values{
		"revision":     {strconvInt(latest.Revision)},
		"candidate_id": {strconvInt(currentCandidateID)},
	}
	response = serveMiningForm(router, http.MethodPost, target, retry)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("safe retry response = %d, body = %s", response.Code, response.Body.String())
	}
	var expression, pronunciation, meaning string
	if err := db.QueryRow(`
		SELECT v.expression, v.pronunciation, m.text
		FROM vocabulary v
		JOIN meanings m ON m.vocabulary_id = v.id AND m.position = 0`).Scan(&expression, &pronunciation, &meaning); err != nil {
		t.Fatal(err)
	}
	if expression != "犬" || pronunciation != "いぬ" || meaning != "dog" {
		t.Fatalf("safe retry created %q, %q, %q", expression, pronunciation, meaning)
	}
}

func TestAcceptKanaAmbiguityUsesSelectedWrittenForm(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "あく", "00000000000000000000000000000038")
	match := jmdict.Match{
		State: jmdict.MatchAmbiguous, SourceCreated: "2026-07-26", SourceVersion: "1.10",
		Candidates: []jmdict.Candidate{
			miningCandidate(101, "開く", "あく", "to open"),
			miningCandidate(102, "空く", "あく", "to become empty"),
		},
	}
	match.Candidates[0].MatchType = "reading"
	match.Candidates[1].MatchType = "reading"
	completeMiningEnrichment(t, ctx, store, match)
	form := url.Values{
		"revision":     {strconvInt(capture.Revision)},
		"candidate_id": {"2"},
	}
	response := serveMiningForm(miningTestRouter(t, store, "https://goi.example"), http.MethodPost, fmt.Sprintf("/mining/captures/%d/accept", capture.ID), form)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var expression, meaning string
	if err := db.QueryRow(`
		SELECT v.expression, m.text
		FROM vocabulary v
		JOIN meanings m ON m.vocabulary_id = v.id AND m.position = 0
		WHERE v.id = 1`).Scan(&expression, &meaning); err != nil {
		t.Fatal(err)
	}
	if expression != "空く" || meaning != "to become empty" {
		t.Fatalf("selected vocabulary = %q, %q", expression, meaning)
	}
}

func TestAttachKanaCandidateToExactExistingVocabulary(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	if _, err := vocabulary.NewStore(db).AddKnown(ctx, "空く"); err != nil {
		t.Fatal(err)
	}
	var existingID int64
	if err := db.QueryRow("SELECT id FROM vocabulary WHERE normalized_expression = '空く'").Scan(&existingID); err != nil {
		t.Fatal(err)
	}
	capture := createMiningCapture(t, ctx, store, "あく", "00000000000000000000000000000039")
	match := jmdict.Match{
		State: jmdict.MatchAmbiguous, SourceCreated: "2026-07-26", SourceVersion: "1.10",
		Candidates: []jmdict.Candidate{
			miningCandidate(101, "開く", "あく", "to open"),
			miningCandidate(102, "空く", "あく", "to become empty"),
		},
	}
	match.Candidates[0].MatchType = "reading"
	match.Candidates[1].MatchType = "reading"
	completeMiningEnrichment(t, ctx, store, match)
	router := miningTestRouter(t, store, "https://goi.example")
	detailRequest := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/mining/captures/%d", capture.ID), nil)
	detailResponse := httptest.NewRecorder()
	router.ServeHTTP(detailResponse, detailRequest)
	body := detailResponse.Body.String()
	if !strings.Contains(body, "Restart from first lesson") {
		t.Fatalf("candidate-aware attach is missing: %s", detailResponse.Body.String())
	}
	if strings.Contains(body, "Create separate card") || strings.Contains(body, "allow_duplicate") {
		t.Fatalf("candidate-aware attach still offers a duplicate card: %s", body)
	}
	form := url.Values{
		"revision":      {strconvInt(capture.Revision)},
		"candidate_id":  {"2"},
		"pronunciation": {"あく"},
		"meanings":      {"to become empty"},
	}
	response := serveMiningForm(router, http.MethodPost, fmt.Sprintf("/mining/captures/%d/attach-candidate", capture.ID), form)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	attached, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if attached.Status != StatusAccepted || attached.VocabularyID == nil || *attached.VocabularyID != existingID {
		t.Fatalf("attached capture = %#v", attached)
	}
	var vocabularyCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary").Scan(&vocabularyCount); err != nil {
		t.Fatal(err)
	}
	if vocabularyCount != 1 {
		t.Fatalf("vocabulary count = %d, want 1", vocabularyCount)
	}
	var status, pronunciation, meaning string
	if err := db.QueryRow(`
		SELECT v.status, v.pronunciation, m.text
		FROM vocabulary v
		JOIN meanings m ON m.vocabulary_id = v.id AND m.position = 0
		WHERE v.id = ?`, existingID).Scan(&status, &pronunciation, &meaning); err != nil {
		t.Fatal(err)
	}
	if status != "unlearned" || pronunciation != "あく" || meaning != "to become empty" {
		t.Fatalf("completed existing vocabulary = status %q, reading %q, meaning %q", status, pronunciation, meaning)
	}
	available, err := lessons.NewStore(db).AvailableCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if available != 1 {
		t.Fatalf("available lessons = %d, want 1", available)
	}
}

func TestMiningRestartsAnExistingVocabularyCard(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	canonicalID, err := vocabulary.NewStore(db).Create(ctx, vocabulary.CreateInput{
		Expression: "猫", Pronunciation: "ねこ", Meanings: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE vocabulary SET status = 'active', lesson_completed_at = 1 WHERE id = ?", canonicalID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO srs_states (vocabulary_id, stage, due_at) VALUES (?, 5, 1)", canonicalID); err != nil {
		t.Fatal(err)
	}
	capture := createMiningCapture(t, ctx, store, "猫", "00000000000000000000000000000058")
	router := miningTestRouter(t, store, "https://goi.example")

	detail := httptest.NewRecorder()
	router.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/mining/captures/%d", capture.ID), nil))
	body := detail.Body.String()
	for _, expected := range []string{
		"Sentence context",
		"This card already exists",
		"Restart from first lesson",
		"Current review progress and leech status are cleared.",
		`name="vocabulary_id" value="1"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("restart choice does not contain %q: %s", expected, body)
		}
	}
	for _, unwanted := range []string{
		"Save to existing card",
		"Create a separate card",
		"Create separate card",
		`name="allow_duplicate"`,
		"Card details",
	} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("restart choice still contains %q: %s", unwanted, body)
		}
	}
	if strings.Count(body, "Restart from first lesson") != 1 {
		t.Fatalf("restart action count = %d, want 1: %s", strings.Count(body, "Restart from first lesson"), body)
	}

	duplicateForm := url.Values{
		"revision":        {strconvInt(capture.Revision)},
		"pronunciation":   {"ねこ"},
		"meanings":        {"feline"},
		"allow_duplicate": {"1"},
	}
	response := serveMiningForm(router, http.MethodPost, fmt.Sprintf("/mining/captures/%d/accept", capture.ID), duplicateForm)
	if response.Code != http.StatusConflict {
		t.Fatalf("forced duplicate response = %d, body = %s", response.Code, response.Body.String())
	}

	restartForm := url.Values{
		"revision":      {strconvInt(capture.Revision)},
		"vocabulary_id": {strconvInt(canonicalID)},
	}
	response = serveMiningForm(router, http.MethodPost, fmt.Sprintf("/mining/captures/%d/attach", capture.ID), restartForm)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("restart response = %d, body = %s", response.Code, response.Body.String())
	}
	accepted, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.VocabularyID == nil || *accepted.VocabularyID != canonicalID {
		t.Fatalf("restarted vocabulary = %v, want %d", accepted.VocabularyID, canonicalID)
	}
	var vocabularyCount, srsCount int
	var status, reading, meaning string
	if err := db.QueryRow(`
		SELECT v.status, v.pronunciation, m.text
		FROM vocabulary v
		JOIN meanings m ON m.vocabulary_id = v.id AND m.position = 0
		WHERE v.id = ?`, canonicalID).Scan(&status, &reading, &meaning); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary").Scan(&vocabularyCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM srs_states WHERE vocabulary_id = ?", canonicalID).Scan(&srsCount); err != nil {
		t.Fatal(err)
	}
	if status != "unlearned" || reading != "ねこ" || meaning != "cat" || vocabularyCount != 1 || srsCount != 0 {
		t.Fatalf("restarted card = status %q, reading %q, meaning %q, vocabulary %d, SRS %d", status, reading, meaning, vocabularyCount, srsCount)
	}
	available, err := lessons.NewStore(db).AvailableCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if available != 1 {
		t.Fatalf("available lessons = %d, want 1", available)
	}
}

func TestMiningRequiresCompletingASparseExistingCardBeforeRestart(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	if _, err := vocabulary.NewStore(db).AddKnown(ctx, "猫"); err != nil {
		t.Fatal(err)
	}
	capture := createMiningCapture(t, ctx, store, "猫", "00000000000000000000000000000059")
	router := miningTestRouter(t, store, "https://goi.example")

	detail := httptest.NewRecorder()
	router.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/mining/captures/%d", capture.ID), nil))
	body := detail.Body.String()
	for _, expected := range []string{"This card already exists", "Complete card", "needs a reading and meaning"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("sparse existing card does not contain %q: %s", expected, body)
		}
	}
	for _, unwanted := range []string{"Restart from first lesson", "Create a separate card", "Enter the reading and meanings"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("sparse existing card contains %q: %s", unwanted, body)
		}
	}
}

func TestMiningPrefillsASparseExistingCardFromTheDictionary(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	if _, err := vocabulary.NewStore(db).AddKnown(ctx, "猫"); err != nil {
		t.Fatal(err)
	}
	capture := createMiningCapture(t, ctx, store, "猫", "00000000000000000000000000000064")
	completeMiningEnrichment(t, ctx, store, readyMiningMatch("猫", "ねこ", "cat"))

	response := httptest.NewRecorder()
	miningTestRouter(t, store, "https://goi.example").ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, fmt.Sprintf("/mining/captures/%d", capture.ID), nil),
	)
	body := response.Body.String()
	for _, expected := range []string{
		"This card already exists",
		"Choose the matching entry",
		`name="pronunciation" value="ねこ"`,
		">cat</textarea>",
		fmt.Sprintf(`action="/mining/captures/%d/accept"`, capture.ID),
		fmt.Sprintf(`formaction="/mining/captures/%d/attach-candidate"`, capture.ID),
		"Restart from first lesson",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("sparse existing card does not contain %q: %s", expected, body)
		}
	}
	if strings.Contains(body, ">Complete card</a>") {
		t.Fatalf("sparse existing card still requires manual completion: %s", body)
	}
}

func TestFormatTimestamp(t *testing.T) {
	tests := []struct {
		milliseconds int64
		want         string
	}{
		{milliseconds: 48_210, want: "0:48"},
		{milliseconds: 754_000, want: "12:34"},
		{milliseconds: 3_723_000, want: "1:02:03"},
	}

	for _, test := range tests {
		if got := formatTimestamp(&test.milliseconds); got != test.want {
			t.Errorf("formatTimestamp(%d) = %q, want %q", test.milliseconds, got, test.want)
		}
	}
}

func TestAcceptDictionaryCandidateRejectsUnknownResultPosition(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "猫", "00000000000000000000000000000035")
	completeMiningEnrichment(t, ctx, store, readyMiningMatch("猫", "ねこ", "cat"))
	form := url.Values{
		"revision":     {strconvInt(capture.Revision)},
		"candidate_id": {"2"},
	}
	response := serveMiningForm(miningTestRouter(t, store, "https://goi.example"), http.MethodPost, fmt.Sprintf("/mining/captures/%d/accept", capture.ID), form)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "dictionary suggestion is stale") {
		t.Fatalf("response = %d, body = %s", response.Code, response.Body.String())
	}
	remaining, err := store.Get(ctx, capture.ID)
	if err != nil || remaining.Status != StatusPending {
		t.Fatalf("first capture = %#v, %v", remaining, err)
	}
}

func TestAcceptDictionaryCandidateRejectsCandidateFromOldRevision(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "猫", "00000000000000000000000000000037")
	completeMiningEnrichment(t, ctx, store, readyMiningMatch("猫", "ねこ", "cat"))
	updated, err := store.Update(ctx, capture.ID, capture.Revision, UpdateInput{Expression: "子猫", SourceKind: SourceManual})
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"revision":     {strconvInt(updated.Revision)},
		"candidate_id": {"1"},
	}
	response := serveMiningForm(miningTestRouter(t, store, "https://goi.example"), http.MethodPost, fmt.Sprintf("/mining/captures/%d/accept", capture.ID), form)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "dictionary suggestion is stale") {
		t.Fatalf("response = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestDeletingVocabularyRemovesItsAcceptedCapture(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "猫", "00000000000000000000000000000015")
	vocabularyID, err := store.Accept(ctx, capture.ID, capture.Revision, vocabulary.CreateInput{
		Pronunciation: "ねこ",
		Meanings:      []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := vocabulary.NewStore(db).ApplyAction(ctx, vocabularyID, vocabulary.ActionDelete); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, capture.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("removed capture error = %v, want sql.ErrNoRows", err)
	}
	var tombstones int
	if err := db.QueryRow("SELECT COUNT(*) FROM mining_capture_tombstones WHERE capture_nonce = ?", capture.CaptureNonce).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstones != 1 {
		t.Fatalf("capture tombstones = %d, want 1", tombstones)
	}

	router := miningTestRouter(t, store, "https://goi.example")
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/mining/captures/%d", capture.ID), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("response = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestCapturePageIncludesBookmarkletOnlyForValidOrigin(t *testing.T) {
	_, db := openMiningTestDatabase(t)
	store := NewStore(db)
	for _, test := range []struct {
		name       string
		baseURL    string
		wantButton bool
	}{
		{name: "valid", baseURL: "https://goi.example/some/path", wantButton: true},
		{name: "invalid scheme", baseURL: "javascript:alert(1)"},
		{name: "missing", baseURL: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := miningTestRouter(t, store, test.baseURL)
			request := httptest.NewRequest(http.MethodGet, "/mining/capture", nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			hasButton := strings.Contains(response.Body.String(), ">Mine to Goi</a>")
			if hasButton != test.wantButton {
				t.Fatalf("bookmarklet button = %t, want %t", hasButton, test.wantButton)
			}
			if test.wantButton && !strings.Contains(response.Body.String(), `href="javascript:`) {
				t.Fatalf("bookmarklet URL was sanitized or omitted: %s", response.Body.String())
			}
			if test.wantButton && !strings.Contains(response.Body.String(), `<code data-bookmarklet-origin>https://goi.example</code>`) {
				t.Fatalf("bookmarklet destination is missing: %s", response.Body.String())
			}
		})
	}
}

func TestBookmarkletEmbedsQuotedOrigin(t *testing.T) {
	if count := strings.Count(bookmarkletScript, "__GOI_ORIGIN__"); count != 1 {
		t.Fatalf("bookmarklet origin placeholders = %d, want 1", count)
	}

	script := string(bookmarklet("https://goi.example/\"quoted"))
	if strings.Contains(script, "__GOI_ORIGIN__") {
		t.Fatal("bookmarklet still contains the origin placeholder")
	}
	if !strings.Contains(script, `const origin = "https://goi.example/\"quoted";`) {
		t.Fatalf("bookmarklet does not contain a quoted origin: %s", script)
	}
}

func TestBookmarkletAttributesOnlyRelevantVideo(t *testing.T) {
	script := string(bookmarklet("https://goi.example"))
	required := []string{
		`element.closest("[data-goi-caption-text],.caption-window,.ytp-caption-window-container")`,
		`location.hostname === "www.youtube.com"`,
		`location.pathname === "/watch"`,
		`/^\/(?:embed|live|shorts)(?:\/|$)/.test(location.pathname)`,
		`document.querySelector(".html5-video-player video")`,
		`document.querySelector("video")`,
		`const hasVideoContext = Boolean(video) && (Boolean(caption) || isYouTube)`,
		`source_kind: hasVideoContext ? "video" : "web"`,
		`source_position_seconds: hasVideoContext && Number.isFinite(video.currentTime)`,
	}
	for _, fragment := range required {
		if !strings.Contains(script, fragment) {
			t.Errorf("bookmarklet is missing %q", fragment)
		}
	}
	if strings.Contains(script, `source_kind: video ? "video" : "web"`) {
		t.Fatal("bookmarklet attributes every page video to the selection")
	}
}

func TestBookmarkletBoundsSourceURLBeforeTransfer(t *testing.T) {
	script := string(bookmarklet("https://goi.example"))
	if count := strings.Count(script, `if (byteLength(candidate.href) <= 2048)`); count != 3 {
		t.Fatalf("bookmarklet URL length checks = %d, want 3", count)
	}
	fragments := []string{
		`source_url: boundedURL(location.href)`,
		`if (byteLength(candidate.href) <= 2048)`,
		`candidate.hash = ""`,
		`candidate.search = ""`,
		`return byteLength(candidate.origin) <= 2048 ? candidate.origin : ""`,
	}
	previous := -1
	for _, fragment := range fragments {
		index := strings.Index(script, fragment)
		if index < 0 {
			t.Fatalf("bookmarklet is missing %q", fragment)
		}
		if fragment != `source_url: boundedURL(location.href)` && index <= previous {
			t.Fatalf("bookmarklet URL fallback %q is out of order", fragment)
		}
		if fragment != `source_url: boundedURL(location.href)` {
			previous = index
		}
	}
}

func TestCaptureFormEnforcesTightBodyLimit(t *testing.T) {
	_, db := openMiningTestDatabase(t)
	router := miningTestRouter(t, NewStore(db), "https://goi.example")
	request := httptest.NewRequest(http.MethodPost, "/mining/captures", strings.NewReader(strings.Repeat("x", captureBodyLimit+1)))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "form is too large") {
		t.Fatalf("body does not explain size limit: %s", response.Body.String())
	}
}

func miningFormBlock(t *testing.T, body, opening string) string {
	t.Helper()
	start := strings.Index(body, opening)
	if start < 0 {
		t.Fatalf("form %q was not rendered: %s", opening, body)
	}
	end := strings.Index(body[start:], "</form>")
	if end < 0 {
		t.Fatalf("form %q was not closed: %s", opening, body)
	}
	return body[start : start+end+len("</form>")]
}

func requireMiningFormRevision(t *testing.T, form string, revision int64) {
	t.Helper()
	want := fmt.Sprintf(`name="revision" value="%d"`, revision)
	if !strings.Contains(form, want) {
		t.Fatalf("form revision is not %d: %s", revision, form)
	}
}

func miningTestRouter(t *testing.T, store *Store, baseURL string) http.Handler {
	t.Helper()
	return miningTestRouterWithGenerator(t, store, baseURL, nil)
}

func miningTestRouterWithGenerator(t *testing.T, store *Store, baseURL string, generator examplegen.Generator) http.Handler {
	return miningTestRouterWithServices(t, store, baseURL, generator, pronunciation.NewCommons(nil))
}

func miningTestRouterWithServices(t *testing.T, store *Store, baseURL string, generator examplegen.Generator, recordings pronunciationProvider) http.Handler {
	t.Helper()
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	newHandler(store, renderer, baseURL, generator, recordings, miningTestDictionary(store)).Routes(router)
	return router
}

type fakePronunciationProvider struct {
	results []pronunciation.Recording
	upload  media.Upload
	err     error
}

func (provider *fakePronunciationProvider) Search(context.Context, string, string) ([]pronunciation.Recording, error) {
	return provider.results, provider.err
}

func (provider *fakePronunciationProvider) Download(context.Context, int64, string, string) (media.Upload, error) {
	return provider.upload, provider.err
}

type fakeExampleGenerator struct {
	request         examplegen.Request
	result          examplegen.Example
	translationText string
	translation     examplegen.Translation
	err             error
	generationOff   bool
	translationOff  bool
}

func (generator *fakeExampleGenerator) Available() bool {
	return !generator.generationOff
}

func (generator *fakeExampleGenerator) TranslationAvailable() bool {
	return !generator.translationOff
}

func (generator *fakeExampleGenerator) Generate(_ context.Context, request examplegen.Request) (examplegen.Example, error) {
	generator.request = request
	return generator.result, generator.err
}

func (generator *fakeExampleGenerator) Translate(_ context.Context, text string) (examplegen.Translation, error) {
	generator.translationText = text
	return generator.translation, generator.err
}

func serveMiningForm(handler http.Handler, method, target string, form url.Values) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func strconvInt(value int64) string {
	return fmt.Sprintf("%d", value)
}

func silentMiningWAV() []byte {
	content := make([]byte, 46)
	copy(content[0:4], "RIFF")
	binary.LittleEndian.PutUint32(content[4:8], uint32(len(content)-8))
	copy(content[8:12], "WAVE")
	copy(content[12:16], "fmt ")
	binary.LittleEndian.PutUint32(content[16:20], 16)
	binary.LittleEndian.PutUint16(content[20:22], 1)
	binary.LittleEndian.PutUint16(content[22:24], 1)
	binary.LittleEndian.PutUint32(content[24:28], 8_000)
	binary.LittleEndian.PutUint32(content[28:32], 16_000)
	binary.LittleEndian.PutUint16(content[32:34], 2)
	binary.LittleEndian.PutUint16(content[34:36], 16)
	copy(content[36:40], "data")
	binary.LittleEndian.PutUint32(content[40:44], 2)
	return content
}

func completeMiningEnrichment(t *testing.T, ctx context.Context, store *Store, match jmdict.Match) {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, `
		SELECT expression FROM mining_captures
		WHERE status = 'pending'
		ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	lookup := miningTestDictionary(store)
	for rows.Next() {
		var expression string
		if err := rows.Scan(&expression); err != nil {
			t.Fatal(err)
		}
		if _, exists := lookup.matches[expression]; exists {
			continue
		}
		lookup.matches[expression] = match
		return
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("no pending capture needs a dictionary result")
}

var miningTestLookups = map[*Store]*fakeDictionaryLookup{}

func miningTestDictionary(store *Store) *fakeDictionaryLookup {
	lookup := miningTestLookups[store]
	if lookup == nil {
		lookup = &fakeDictionaryLookup{matches: map[string]jmdict.Match{}}
		miningTestLookups[store] = lookup
	}
	return lookup
}

func bulkAcceptMining(ctx context.Context, store *Store, ids []int64) (BulkAcceptResult, error) {
	handler := &Handler{store: store, dictionary: miningTestDictionary(store)}
	return handler.bulkAccept(ctx, ids)
}

func readyMiningMatch(written, reading, meaning string) jmdict.Match {
	return jmdict.Match{
		State: jmdict.MatchReady, SourceCreated: "2026-07-26", SourceVersion: "1.10",
		Candidates: []jmdict.Candidate{miningCandidate(123, written, reading, meaning)},
	}
}

func miningCandidate(sequence int64, written, reading, meaning string) jmdict.Candidate {
	return jmdict.Candidate{
		EntrySequence: sequence, Written: written, Reading: reading, MatchType: "written",
		Priority: 1, SourceOrder: int(sequence),
		Senses: []jmdict.Sense{{
			Number: 1, PartsOfSpeech: []string{"verb"},
			Glosses: []jmdict.Gloss{{Text: meaning, Language: "eng"}},
		}},
	}
}
