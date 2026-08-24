package imports

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/database"
	"github.com/tomasmik/goi/internal/vocabulary"
	internalweb "github.com/tomasmik/goi/internal/web"
)

func TestMissingImportRendersRecoveryPage(t *testing.T) {
	_, router, _ := newImportHandlerTest(t)
	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/imports/anki/999/mapping", nil),
		httptest.NewRequest(http.MethodPost, "/imports/anki/999/apply", strings.NewReader("expression_field=0&pronunciation_field=1&meaning_field=2&audio_field=-1&picture_field=-1")),
	}
	requests[1].Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, request := range requests {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `href="/imports"`) {
			t.Fatalf("missing import response = %d, %s", response.Code, response.Body.String())
		}
	}
}

func TestMalformedMappingRendersRecoveryPage(t *testing.T) {
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(nil, renderer).Routes(router)
	request := httptest.NewRequest(
		http.MethodPost,
		"/imports/anki/7/apply",
		strings.NewReader(strings.Repeat("x", (64<<10)+1)),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusBadRequest || !strings.Contains(body, "The field mapping form is too large or invalid.") {
		t.Fatalf("response = %d, body = %s", response.Code, body)
	}
	for _, expected := range []string{`class="site-header"`, `href="/imports/anki/7/mapping"`, "Back to field mapping"} {
		if !strings.Contains(body, expected) {
			t.Errorf("rendered error does not contain %q: %s", expected, body)
		}
	}
}

func TestUploadRedirectsToStableMappingPage(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	db, err := database.Open(ctx, filepath.Join(directory, "goi.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(ctx, db, filepath.Join(directory, "imports"), vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, renderer).Routes(router)

	archivePath := writeTextAnkiPackage(t, directory, []string{"食べる\x1fたべる\x1fto eat"})
	archive, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("package", "deck.apkg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(part, archive); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/imports/anki/upload", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/imports/anki/1/mapping" {
		t.Fatalf("response = %d %q, body = %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
}

func TestUploadShowsOnlyClassifiedPackageErrors(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	db, err := database.Open(ctx, filepath.Join(directory, "goi.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(ctx, db, filepath.Join(directory, "imports"), vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, renderer).Routes(router)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("package", "broken.apkg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("not a ZIP archive")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/imports/anki/upload", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "not a valid Anki package") {
		t.Fatalf("response = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "Select the Anki package again before retrying.") {
		t.Fatalf("upload error did not explain that the file must be selected again: %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), directory) {
		t.Fatalf("response exposed an internal path: %s", response.Body.String())
	}
}

func TestMappingHidesInternalStoreErrors(t *testing.T) {
	previousLogger := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	ctx := context.Background()
	directory := t.TempDir()
	db, err := database.Open(ctx, filepath.Join(directory, "goi.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	store, err := NewStore(ctx, db, filepath.Join(directory, "imports"), vocabulary.NewStore(db))
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, renderer).Routes(router)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/imports/anki/1/mapping", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "could not load the Anki import") {
		t.Fatalf("response = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "database is closed") {
		t.Fatalf("response exposed an internal database error: %s", response.Body.String())
	}
	if !strings.Contains(logs.String(), "could not load the Anki import") || !strings.Contains(logs.String(), "database is closed") {
		t.Fatalf("internal error was not logged: %s", logs.String())
	}
}

func TestMappingRequiresAnExplicitRequiredField(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodPost,
		"/imports/anki/1/apply",
		strings.NewReader("expression_field=&pronunciation_field=1&meaning_field=2&audio_field=-1&picture_field=-1"),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := request.ParseForm(); err != nil {
		t.Fatal(err)
	}

	if _, err := mappingFromRequest(request); !errors.Is(err, errInvalidMapping) {
		t.Fatalf("mapping error = %v, want invalid mapping", err)
	}
}

func TestLimitedPreviewNotesCapsRowsAndFieldLength(t *testing.T) {
	notes := make([]Note, previewNoteLimit+1)
	for index := range notes {
		notes[index] = Note{
			ID:     int64(index + 1),
			Fields: []string{strings.Repeat("語", previewFieldCharacterLimit+1)},
		}
	}

	visible, omitted := limitedPreviewNotes(notes)
	if len(visible) != previewNoteLimit || omitted != 1 {
		t.Fatalf("visible = %d, omitted = %d", len(visible), omitted)
	}
	field := visible[0].Fields[0]
	if len([]rune(field)) != previewFieldCharacterLimit+1 || !strings.HasSuffix(field, "…") {
		t.Fatalf("preview field was not bounded: %q", field)
	}
}

func TestLimitedPreviewNotesLabelsMediaOnlyFields(t *testing.T) {
	notes := []Note{{
		ID: 1,
		Fields: []string{
			`<img src="cover.png">`,
			`[sound:a&amp;b.mp3]`,
			`<b>猫</b><img src="cat.png">[sound:cat.mp3]`,
		},
	}}

	visible, omitted := limitedPreviewNotes(notes)
	if omitted != 0 || len(visible) != 1 {
		t.Fatalf("visible = %d, omitted = %d", len(visible), omitted)
	}
	want := []string{
		"[image: cover.png]",
		"[audio: a&b.mp3]",
		"猫\n[audio: cat.mp3]\n[image: cat.png]",
	}
	for index, field := range visible[0].Fields {
		if field != want[index] {
			t.Errorf("field %d = %q, want %q", index, field, want[index])
		}
	}
}

func TestSuggestedMappingUsesOnlyRecognizedFieldNames(t *testing.T) {
	mapping := suggestedMapping([]string{"Expression", "Reading", "Meaning", "Audio", "Notes"})
	if mapping.ExpressionField != 0 || mapping.PronunciationField != 1 || mapping.MeaningField != 2 || mapping.AudioField != 3 || mapping.NotesField != 4 || mapping.PictureField != -1 {
		t.Fatalf("suggested mapping = %#v", mapping)
	}

	basic := suggestedMapping([]string{"Front", "Back"})
	if basic.ExpressionField != 0 || basic.MeaningField != 1 || basic.PronunciationField != -1 {
		t.Fatalf("basic mapping = %#v", basic)
	}
}

func TestTextImportPreviewsRowsBeforeSaving(t *testing.T) {
	store, router, _ := newImportHandlerTest(t)
	ctx := context.Background()
	if _, err := store.vocabulary.Create(ctx, vocabulary.CreateInput{
		Expression: "既存", Pronunciation: "きそん", Meanings: []string{"existing"},
	}); err != nil {
		t.Fatal(err)
	}
	data := strings.Join([]string{
		"expression\treading\tmeaning",
		"既存\tきそん\texisting",
		"\tから\tblank expression",
		"猫\t\tcat",
		"猫\tねこ\tcat",
		"犬\tいぬ\tdog",
		"犬\tいぬ\tdog",
	}, "\n")
	form := url.Values{"data": {data}, "action": {"preview"}}
	request := httptest.NewRequest(http.MethodPost, "/imports/text", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("preview response = %d, body = %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{
		"2</strong> will be added", "2 duplicates skipped", "2 invalid rows",
		"Missing required fields", "Duplicate — will be skipped", "Import 2 words",
	} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("preview does not contain %q: %s", expected, response.Body.String())
		}
	}
	for _, expression := range []string{"猫", "犬"} {
		count, err := store.vocabulary.ListCount(ctx, expression)
		if err != nil || count != 0 {
			t.Fatalf("preview saved %q: count = %d, err = %v", expression, count, err)
		}
	}
}

func TestTextImportDoesNotOfferAnEmptyImport(t *testing.T) {
	store, router, _ := newImportHandlerTest(t)
	if _, err := store.vocabulary.Create(context.Background(), vocabulary.CreateInput{
		Expression: "猫", Pronunciation: "ねこ", Meanings: []string{"cat"},
	}); err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"data":   {"猫\tねこ\tcat"},
		"action": {"preview"},
	}
	request := httptest.NewRequest(http.MethodPost, "/imports/text", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "1 duplicates skipped") {
		t.Fatalf("preview response = %d, body = %s", response.Code, body)
	}
	if strings.Contains(body, "Import 0 words") {
		t.Fatalf("preview offers a no-op import: %s", body)
	}
}

func TestStructuredTextRejectsDuplicateHeaders(t *testing.T) {
	_, err := parseStructuredTextRows("expression\texpression\n猫\tcat")
	if err == nil || !strings.Contains(err.Error(), "duplicate column") {
		t.Fatalf("duplicate header error = %v", err)
	}
}

func TestStructuredTextKeepsAnEmptyFirstTSVField(t *testing.T) {
	parsed, err := parseStructuredTextRows("expression\treading\tmeaning\n\tから\tblank expression")
	if err != nil {
		t.Fatal(err)
	}
	records := parsed.records
	if len(records) != 1 || records[0]["expression"] != "" || records[0]["reading"] != "から" || records[0]["meaning"] != "blank expression" {
		t.Fatalf("parsed record = %#v", records)
	}
}

func TestStructuredTextAcceptsHeaderlessRows(t *testing.T) {
	parsed, err := parseStructuredTextRows("猫\tねこ\tcat\n犬\tいぬ\tdog")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.firstDataRow != 1 || len(parsed.records) != 2 {
		t.Fatalf("parsed rows = %#v", parsed)
	}
	first := parsed.records[0]
	if first["expression"] != "猫" || first["reading"] != "ねこ" || first["meaning"] != "cat" {
		t.Fatalf("first record = %#v", first)
	}
}

func TestStructuredTextAcceptsSingleColumnKnownWordLists(t *testing.T) {
	parsed, err := parseStructuredTextRows("猫\n犬")
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.records) != 2 || parsed.records[1]["expression"] != "犬" {
		t.Fatalf("records = %#v", parsed.records)
	}
}

func TestApplyRedirectsToStableResultPage(t *testing.T) {
	store, router, directory := newImportHandlerTest(t)
	runID := uploadImportHandlerPackage(t, store, directory)

	request := httptest.NewRequest(
		http.MethodPost,
		"/imports/anki/1/apply",
		strings.NewReader("expression_field=0&pronunciation_field=1&meaning_field=2&audio_field=-1&picture_field=-1"),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	resultPath := "/imports/anki/" + strconv.FormatInt(runID, 10) + "/result"
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != resultPath {
		t.Fatalf("response = %d %q, body = %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}

	for range 2 {
		request = httptest.NewRequest(http.MethodGet, resultPath, nil)
		response = httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "1 added · 0 skipped · 0 failed") {
			t.Fatalf("result response = %d, body = %s", response.Code, response.Body.String())
		}
	}
}

func TestApplyRendersMappingErrorsInContext(t *testing.T) {
	store, router, directory := newImportHandlerTest(t)
	uploadImportHandlerPackage(t, store, directory)

	request := httptest.NewRequest(
		http.MethodPost,
		"/imports/anki/1/apply",
		strings.NewReader("expression_field=0&pronunciation_field=0&meaning_field=2&audio_field=-1&picture_field=-1"),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("response = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "must use different fields") || !strings.Contains(body, "Choose what each field contains") {
		t.Fatalf("mapping error lost its form context: %s", body)
	}
}

func TestAppliedImportRedirectsToStableResult(t *testing.T) {
	store, router, directory := newImportHandlerTest(t)
	runID := uploadImportHandlerPackage(t, store, directory)
	applyPath := "/imports/anki/" + strconv.FormatInt(runID, 10) + "/apply"
	request := httptest.NewRequest(
		http.MethodPost,
		applyPath,
		strings.NewReader("expression_field=0&pronunciation_field=1&meaning_field=2&audio_field=-1&picture_field=-1"),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("apply response = %d, %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	mappingPath := "/imports/anki/" + strconv.FormatInt(runID, 10) + "/mapping"
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, mappingPath, nil))

	resultPath := "/imports/anki/" + strconv.FormatInt(runID, 10) + "/result"
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != resultPath {
		t.Fatalf("applied import response = %d %q, %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
}

func newImportHandlerTest(t *testing.T) (*Store, http.Handler, string) {
	t.Helper()
	ctx := context.Background()
	directory := t.TempDir()
	db, err := database.Open(ctx, filepath.Join(directory, "goi.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(ctx, db, filepath.Join(directory, "imports"), vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, renderer).Routes(router)
	return store, router, directory
}

func uploadImportHandlerPackage(t *testing.T, store *Store, directory string) int64 {
	t.Helper()
	archivePath := writeTextAnkiPackage(t, directory, []string{"食べる\x1fたべる\x1fto eat"})
	archive, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	runID, _, err := store.Upload(context.Background(), archive, "deck.apkg")
	if err != nil {
		t.Fatal(err)
	}
	return runID
}
