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

	form := url.Values{
		"revision":      {strconvInt(capture.Revision)},
		"reading":       {"にほん"},
		"pronunciation": {"にほん"},
		"meanings":      {"Japan"},
		"notes":         {"keep this note"},
	}
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/mining/captures/%d/pronunciations/42", capture.ID), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("attach response = %d, body = %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{"Pronunciation audio added to the card.", `value="にほん"`, ">Japan</textarea>", ">keep this note</textarea>"} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("attach response did not preserve %q: %s", expected, response.Body.String())
		}
	}
	stored, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PronunciationAudioID == 0 {
		t.Fatal("pronunciation audio was not attached")
	}
}

func TestMiningAutomaticallyUsesSinglePronunciationResult(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "日本", "00000000000000000000000000000097")
	recordings := &fakePronunciationProvider{
		results: []pronunciation.Recording{{ID: 42, Label: "にほん"}},
		upload:  media.Upload{Kind: media.KindAudio, MimeType: "audio/wav", Content: silentMiningWAV()},
	}
	router := miningTestRouterWithServices(t, store, "https://goi.example", nil, recordings)
	form := url.Values{
		"revision":      {strconvInt(capture.Revision)},
		"reading":       {"にほん"},
		"pronunciation": {"にほん"},
		"meanings":      {"Japan"},
		"notes":         {"keep this note"},
	}
	response := serveMiningForm(router, http.MethodPost, fmt.Sprintf("/mining/captures/%d/pronunciations/search", capture.ID), form)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d, body = %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{"Pronunciation audio added to the card.", `value="にほん"`, ">Japan</textarea>", ">keep this note</textarea>"} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("response did not preserve %q: %s", expected, response.Body.String())
		}
	}
	stored, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PronunciationAudioID == 0 {
		t.Fatal("single pronunciation result was not attached")
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
		State: jmdict.MatchAmbiguous,
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
	next := createMiningCapture(t, ctx, store, "飲む", "00000000000000000000000000000013")
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
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != capturePath(capture.ID) {
		t.Fatalf("response = %d %q, body = %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	accepted, err := store.Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != StatusAccepted || accepted.VocabularyID == nil {
		t.Fatalf("accepted capture = %#v", accepted)
	}
	completion := httptest.NewRecorder()
	router.ServeHTTP(completion, httptest.NewRequest(http.MethodGet, capturePath(capture.ID), nil))
	for _, expected := range []string{
		"Linked vocabulary",
		fmt.Sprintf(`href="/vocabulary/%d"`, *accepted.VocabularyID),
		fmt.Sprintf(`href="/mining/captures/%d"`, next.ID),
		"Next capture",
	} {
		if !strings.Contains(completion.Body.String(), expected) {
			t.Fatalf("completion page does not contain %q: %s", expected, completion.Body.String())
		}
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

func TestAttachmentFailureRollsBackCardMedia(t *testing.T) {
	for _, action := range []string{"attach", "attach-candidate"} {
		t.Run(action, func(t *testing.T) {
			ctx, db := openMiningTestDatabase(t)
			store := NewStore(db)
			vocabularyStore := vocabulary.NewStore(db)
			if action == "attach" {
				if _, err := vocabularyStore.Create(ctx, vocabulary.CreateInput{Expression: "猫", Pronunciation: "ねこ", Meanings: []string{"cat"}}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := vocabularyStore.AddKnown(ctx, "猫"); err != nil {
				t.Fatal(err)
			}
			before, err := vocabularyStore.Get(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			capture, _, err := store.Create(ctx, CreateInput{
				Expression: "猫", ContextText: "猫がいます。", SourceKind: SourceManual,
				CaptureNonce: "00000000000000000000000000000139",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.AddMedia(ctx, capture.ID, capture.Revision, []media.Upload{{Kind: media.KindAudio, MimeType: "audio/webm", Content: []byte("original audio")}}, nil, nil); err != nil {
				t.Fatal(err)
			}
			capture, err = store.Get(ctx, capture.ID)
			if err != nil {
				t.Fatal(err)
			}
			completeMiningEnrichment(t, ctx, store, readyMiningMatch("猫", "ねこ", "cat"))
			if _, err := db.Exec(`CREATE TRIGGER reject_mined_example BEFORE INSERT ON vocabulary_examples BEGIN SELECT RAISE(FAIL, 'example write failed'); END`); err != nil {
				t.Fatal(err)
			}
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			fields := map[string]string{
				"revision": strconvInt(capture.Revision), "vocabulary_id": "1",
				"remove_media_id": strconvInt(capture.SentenceAudioID),
			}
			if action == "attach-candidate" {
				fields["candidate_id"] = candidateID(miningCandidate(123, "猫", "ねこ", ""))
			}
			for name, value := range fields {
				if err := writer.WriteField(name, value); err != nil {
					t.Fatal(err)
				}
			}
			part, err := writer.CreateFormFile("pronunciation_audio", "word.wav")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write(silentMiningWAV()); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			router := miningTestRouter(t, store, "https://goi.example")
			submit := func() *httptest.ResponseRecorder {
				request := httptest.NewRequest(http.MethodPost, capturePath(capture.ID)+"/"+action, bytes.NewReader(body.Bytes()))
				request.Header.Set("Content-Type", writer.FormDataContentType())
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				return response
			}
			if response := submit(); response.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			stored, err := store.Get(ctx, capture.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Status != StatusPending || stored.Revision != capture.Revision || stored.SentenceAudioID != capture.SentenceAudioID || stored.PronunciationAudioID != 0 {
				t.Fatalf("failed attachment changed capture/media: %+v", stored)
			}
			after, err := vocabularyStore.Get(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			if before.Status != after.Status || before.Pronunciation != after.Pronunciation || strings.Join(before.Meanings, ",") != strings.Join(after.Meanings, ",") {
				t.Fatalf("failed attachment changed vocabulary: before = %+v, after = %+v", before, after)
			}
			for table, want := range map[string]int{"vocabulary": 1, "vocabulary_examples": 0, "vocabulary_media": 0, "media": 1, "media_content": 1} {
				var count int
				if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != want {
					t.Errorf("%s rows = %d, want %d", table, count, want)
				}
			}
			if _, err := db.Exec("DROP TRIGGER reject_mined_example"); err != nil {
				t.Fatal(err)
			}
			if response := submit(); response.Code != http.StatusSeeOther {
				t.Fatalf("retry status = %d, body = %s", response.Code, response.Body.String())
			}
			stored, err = store.Get(ctx, capture.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Status != StatusAccepted || stored.SentenceAudioID != 0 || stored.PronunciationAudioID == 0 {
				t.Fatalf("successful retry did not apply media: %+v", stored)
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

func TestCandidateSelectionSurvivesDictionaryReordering(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "あく", "00000000000000000000000000000138")
	match := jmdict.Match{State: jmdict.MatchAmbiguous, Candidates: []jmdict.Candidate{
		miningCandidate(101, "開く", "あく", "to open"),
		miningCandidate(101, "空く", "あく", "to become empty"),
	}}
	completeMiningEnrichment(t, ctx, store, match)
	router := miningTestRouter(t, store, "https://goi.example")
	page := httptest.NewRecorder()
	router.ServeHTTP(page, httptest.NewRequest(http.MethodGet, capturePath(capture.ID), nil))
	if page.Code != http.StatusOK {
		t.Fatalf("detail status = %d", page.Code)
	}
	selectedID := renderedCandidateID(t, page.Body.String(), 0)
	match.Candidates[0], match.Candidates[1] = match.Candidates[1], match.Candidates[0]
	miningTestDictionary(store).matches[capture.Expression] = match
	response := serveMiningForm(router, http.MethodPost, capturePath(capture.ID)+"/accept", url.Values{
		"revision": {strconvInt(capture.Revision)}, "candidate_id": {selectedID},
		"pronunciation": {"あく"}, "meanings": {"my opening meaning"},
	})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var expression, meaning string
	if err := db.QueryRow(`SELECT v.expression, m.text FROM vocabulary v JOIN meanings m ON m.vocabulary_id = v.id`).Scan(&expression, &meaning); err != nil {
		t.Fatal(err)
	}
	if expression != "開く" || meaning != "my opening meaning" {
		t.Fatalf("saved selection = %q / %q", expression, meaning)
	}
}

func TestRemovedCandidatePreservesEditsWithoutApplyingThemToAnotherReading(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "開く", "00000000000000000000000000000140")
	match := jmdict.Match{State: jmdict.MatchAmbiguous, Candidates: []jmdict.Candidate{
		miningCandidate(101, "開く", "あく", "to be open"),
		miningCandidate(101, "開く", "ひらく", "to open"),
	}}
	completeMiningEnrichment(t, ctx, store, match)
	router := miningTestRouter(t, store, "https://goi.example")
	page := httptest.NewRecorder()
	router.ServeHTTP(page, httptest.NewRequest(http.MethodGet, capturePath(capture.ID), nil))
	selectedID := renderedCandidateID(t, page.Body.String(), 0)
	match.Candidates = match.Candidates[1:]
	miningTestDictionary(store).matches[capture.Expression] = match
	response := serveMiningForm(router, http.MethodPost, capturePath(capture.ID)+"/accept", url.Values{
		"revision": {strconvInt(capture.Revision)}, "candidate_id": {selectedID},
		"pronunciation": {"あく"}, "meanings": {"my opening meaning"}, "notes": {"my note"},
		"example_sentence": {"窓が開く。"}, "example_translation": {"The window opens."}, "example_target": {"開く"},
	})
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, value := range []string{"Your unsaved edits", `value="あく"`, "my opening meaning", "my note", "窓が開く。", "The window opens."} {
		if !strings.Contains(body, value) {
			t.Errorf("lost submitted field %q", value)
		}
	}
	currentForm := miningFormBlock(t, body, `<form class="mining-card-editor"`)
	if !strings.Contains(currentForm, `value="ひらく"`) || strings.Contains(currentForm, "my opening meaning") {
		t.Fatalf("old edits were assigned to another candidate: %s", currentForm)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale candidate created %d vocabulary rows", count)
	}
}

func TestAcceptDictionaryCandidateSavesChosenWrittenForm(t *testing.T) {
	for _, test := range []struct {
		writeAs string
		want    string
	}{
		{"", "ちょうど"},
		{writeAsKana, "ちょうど"},
		{writeAsWritten, "丁度"},
	} {
		ctx, db := openMiningTestDatabase(t)
		store := NewStore(db)
		capture := createMiningCapture(t, ctx, store, "ちょうど", "00000000000000000000000000000034")
		match := readyMiningMatch("丁度", "ちょうど", "exactly")
		match.Candidates[0].Senses[0].UsuallyKana = true
		completeMiningEnrichment(t, ctx, store, match)
		router := miningTestRouter(t, store, "https://goi.example")

		detail := serveMiningForm(router, http.MethodGet, capturePath(capture.ID), nil).Body.String()
		if !strings.Contains(detail, `name="write_as" value="kana" checked`) || !strings.Contains(detail, "Usually written in kana.") {
			t.Fatalf("usually-kana word does not default to kana: %s", detail)
		}

		form := url.Values{
			"revision":     {strconvInt(capture.Revision)},
			"candidate_id": {candidateID(match.Candidates[0])},
			"write_as":     {test.writeAs},
		}
		response := serveMiningForm(router, http.MethodPost, fmt.Sprintf("/mining/captures/%d/accept", capture.ID), form)
		if response.Code != http.StatusSeeOther {
			t.Fatalf("write_as %q response = %d, body = %s", test.writeAs, response.Code, response.Body.String())
		}
		var expression, pronunciation string
		if err := db.QueryRow("SELECT expression, pronunciation FROM vocabulary").Scan(&expression, &pronunciation); err != nil {
			t.Fatal(err)
		}
		if expression != test.want || pronunciation != "ちょうど" {
			t.Fatalf("write_as %q saved %q (%q), want %q", test.writeAs, expression, pronunciation, test.want)
		}
	}
}

func TestAcceptKanaAmbiguityUsesSelectedWrittenForm(t *testing.T) {
	ctx, db := openMiningTestDatabase(t)
	store := NewStore(db)
	capture := createMiningCapture(t, ctx, store, "あく", "00000000000000000000000000000038")
	match := jmdict.Match{
		State: jmdict.MatchAmbiguous,
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
		"candidate_id": {candidateID(match.Candidates[1])},
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
		State: jmdict.MatchAmbiguous,
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
		"candidate_id":  {candidateID(match.Candidates[1])},
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
	results     []pronunciation.Recording
	upload      media.Upload
	searches    int
	searchErr   error
	downloadErr error
}

func (provider *fakePronunciationProvider) Search(context.Context, string, string) ([]pronunciation.Recording, error) {
	provider.searches++
	return provider.results, provider.searchErr
}

func (provider *fakePronunciationProvider) Download(context.Context, int64, string, string) (media.Upload, error) {
	return provider.upload, provider.downloadErr
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
		State:      jmdict.MatchReady,
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

func renderedCandidateID(t *testing.T, body string, index int) string {
	t.Helper()
	fields := strings.Split(body, `name="candidate_id" value="`)
	if index < 0 || index+1 >= len(fields) {
		t.Fatalf("candidate %d not rendered", index)
	}
	id, _, ok := strings.Cut(fields[index+1], `"`)
	if !ok || id == "" {
		t.Fatal("candidate identity is missing")
	}
	return id
}
