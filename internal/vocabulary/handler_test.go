package vocabulary

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/examplegen"
	"github.com/tomasmik/goi/internal/examples"
	internalweb "github.com/tomasmik/goi/internal/web"
)

type fakeExampleGenerator struct {
	result       examplegen.Example
	err          error
	beforeReturn func()
	requests     []examplegen.Request
}

func (generator *fakeExampleGenerator) Available() bool {
	return true
}

func vocabularyTestRouter(t *testing.T, store *Store) http.Handler {
	t.Helper()
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, renderer, nil).Routes(router)
	return router
}

func vocabularyTestRouterWithGenerator(t *testing.T, store *Store, generator examplegen.Generator) http.Handler {
	t.Helper()
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewHandler(store, renderer, generator).Routes(router)
	return router
}

func TestVocabularyListURLPreservesSearchFilterAndPage(t *testing.T) {
	got := vocabularyListURLSorted("日本語", ListFilterLearning, ListSortUpdated, 2)
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/vocabulary" || parsed.Query().Get("q") != "日本語" || parsed.Query().Get("status") != "learning" || parsed.Query().Get("page") != "2" {
		t.Fatalf("vocabulary list URL = %q", got)
	}
}

func TestVocabularyListURLPreservesSort(t *testing.T) {
	got := vocabularyListURLSorted("", ListFilterKnown, ListSortExpression, 1)
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("status") != "known" || parsed.Query().Get("sort") != "expression" {
		t.Fatalf("vocabulary list URL = %q", got)
	}
}

func (generator *fakeExampleGenerator) Generate(_ context.Context, request examplegen.Request) (examplegen.Example, error) {
	generator.requests = append(generator.requests, request)
	if generator.beforeReturn != nil {
		generator.beforeReturn()
	}
	return generator.result, generator.err
}

func TestExampleRoutesCreateUpdateAndDelete(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{
		Expression:    "食べる",
		Pronunciation: "たべる",
		Meanings:      []string{"to eat"},
		SourceLabel:   "My notebook",
	})
	if err != nil {
		t.Fatal(err)
	}
	router := vocabularyTestRouter(t, store)

	form := url.Values{
		"sentence":       {"毎朝パンを食べます。"},
		"translation":    {"I eat bread every morning."},
		"target_surface": {"食べます"},
	}
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/examples", id), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
	}

	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Examples) != 1 {
		t.Fatalf("examples after create = %+v", item.Examples)
	}
	exampleID := item.Examples[0].ID
	if item.Examples[0].SourceTitle != "" || !item.Examples[0].HasTarget {
		t.Fatalf("created example = %+v", item.Examples[0])
	}

	form.Set("sentence", "昨日寿司を食べました。")
	form.Set("translation", "I ate sushi yesterday.")
	form.Set("target_surface", "食べました")
	request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/examples/%d", id, exampleID), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d, body = %s", response.Code, response.Body.String())
	}
	item, err = store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := item.Examples[0]; got.Sentence != "昨日寿司を食べました。" || got.Translation != "I ate sushi yesterday." || got.TargetSurface != "食べました" {
		t.Fatalf("updated example = %+v", got)
	}

	request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/examples/%d/delete", id, exampleID), strings.NewReader(""))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed delete status = %d, body = %s", response.Code, response.Body.String())
	}
	item, err = store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Examples) != 1 {
		t.Fatalf("unconfirmed delete removed examples: %+v", item.Examples)
	}

	request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/examples/%d/delete", id, exampleID), strings.NewReader("confirmed=1"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d, body = %s", response.Code, response.Body.String())
	}
	item, err = store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Examples) != 0 {
		t.Fatalf("examples after delete = %+v", item.Examples)
	}
}

func TestGenerateExampleStoresProviderResult(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{
		Expression:    "食べる",
		Pronunciation: "たべる",
		Meanings:      []string{"to eat", "to consume"},
	})
	if err != nil {
		t.Fatal(err)
	}
	generator := &fakeExampleGenerator{result: examplegen.Example{
		Sentence:      "昨日寿司を食べた。",
		Translation:   "I ate sushi yesterday.",
		TargetSurface: "食べた",
		Provider:      "local.test",
		Model:         "small-japanese-model",
	}}
	router := vocabularyTestRouterWithGenerator(t, store, generator)

	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/examples/generate", id), strings.NewReader(""))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if len(generator.requests) != 1 {
		t.Fatalf("generator requests = %+v", generator.requests)
	}
	generatedRequest := generator.requests[0]
	if generatedRequest.Expression != "食べる" || generatedRequest.Pronunciation != "たべる" || strings.Join(generatedRequest.Meanings, ",") != "to eat,to consume" {
		t.Fatalf("generator request = %+v", generatedRequest)
	}
	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Examples) != 1 || item.Examples[0].Origin != examples.OriginGenerated || item.Examples[0].Model != "small-japanese-model" {
		t.Fatalf("generated examples = %+v", item.Examples)
	}

	request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/vocabulary/%d", id), nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	body := response.Body.String()
	if !strings.Contains(body, "昨日寿司を") || !strings.Contains(body, "I ate sushi yesterday.") || !strings.Contains(body, "<mark>食べた</mark>") {
		t.Fatalf("detail does not render generated example: %s", body)
	}
	if strings.Contains(body, ">Generate example<") {
		t.Fatal("detail still offers generation after an example exists")
	}
	for _, label := range []string{
		`aria-label="Edit example 1"`,
		`aria-label="Save example 1"`,
		`aria-label="Remove example 1"`,
	} {
		if !strings.Contains(body, label) {
			t.Fatalf("detail does not identify repeated example controls with %q: %s", label, body)
		}
	}
}

func TestGenerateExampleDoesNotRaceWithManualContext(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{
		Expression:    "読む",
		Pronunciation: "よむ",
		Meanings:      []string{"to read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var manualID int64
	var manualErr error
	generator := &fakeExampleGenerator{
		result: examplegen.Example{
			Sentence:      "新聞を読みます。",
			Translation:   "I read a newspaper.",
			TargetSurface: "読みます",
			Provider:      "local.test",
			Model:         "small-japanese-model",
		},
		beforeReturn: func() {
			var manual examples.Example
			manual, manualErr = examples.NewStore(db).Create(ctx, id, examples.Input{
				Sentence:      "毎日本を読む。",
				Translation:   "I read a book every day.",
				TargetSurface: "読む",
			})
			manualID = manual.ID
		},
	}
	router := vocabularyTestRouterWithGenerator(t, store, generator)

	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/examples/generate", id), strings.NewReader(""))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if manualErr != nil {
		t.Fatal(manualErr)
	}
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "added while generation was running") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Examples) != 1 || item.Examples[0].ID != manualID || item.Examples[0].Origin != examples.OriginManual {
		t.Fatalf("examples = %+v, want manual example %d", item.Examples, manualID)
	}
}

func TestGenerateExampleDoesNotAttachAfterVocabularyEdit(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{
		Expression:    "読む",
		Pronunciation: "よむ",
		Meanings:      []string{"to read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	original, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var editErr error
	generator := &fakeExampleGenerator{
		result: examplegen.Example{
			Sentence:      "新聞を読みます。",
			Translation:   "I read a newspaper.",
			TargetSurface: "読みます",
			Provider:      "local.test",
			Model:         "small-japanese-model",
		},
		beforeReturn: func() {
			editErr = store.Update(ctx, id, original.ContentRevision, CreateInput{
				Expression:    "読み込む",
				Pronunciation: "よみこむ",
				Meanings:      []string{"to load"},
			})
		},
	}
	router := vocabularyTestRouterWithGenerator(t, store, generator)

	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/examples/generate", id), strings.NewReader(""))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if editErr != nil {
		t.Fatal(editErr)
	}
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "word changed while generation was running") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if item.Expression != "読み込む" || item.Pronunciation != "よみこむ" || len(item.Meanings) != 1 || item.Meanings[0] != "to load" || len(item.Examples) != 0 {
		t.Fatalf("vocabulary after generation race = %+v", item)
	}
}

func TestKnownVocabularyExportDownloadsBothFormats(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	if _, err := store.AddKnown(ctx, "beta alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, CreateInput{
		Expression:    "future",
		Pronunciation: "future",
		Meanings:      []string{"not learned"},
	}); err != nil {
		t.Fatal(err)
	}
	router := vocabularyTestRouter(t, store)

	tests := []struct {
		format      string
		contentType string
		filename    string
		body        string
	}{
		{format: "json", contentType: "application/json; charset=utf-8", filename: "goi-known-words.json", body: "[\"alpha\",\"beta\"]\n"},
		{format: "comma", contentType: "text/plain; charset=utf-8", filename: "goi-known-words.txt", body: "alpha, beta\n"},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodGet, "/vocabulary/export?format="+test.format, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != test.body {
			t.Errorf("%s export = %d, %q", test.format, response.Code, response.Body.String())
		}
		if got := response.Header().Get("Content-Type"); got != test.contentType {
			t.Errorf("%s content type = %q", test.format, got)
		}
		if got := response.Header().Get("Content-Disposition"); !strings.Contains(got, test.filename) {
			t.Errorf("%s content disposition = %q", test.format, got)
		}
		if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Errorf("%s cache control = %q", test.format, got)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/vocabulary/export", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing export format status = %d", response.Code)
	}
}

func TestVocabularyEditRecoversFromStaleRevision(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{
		Expression:    "読む",
		Pronunciation: "よむ",
		Meanings:      []string{"to read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	original, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	router := vocabularyTestRouter(t, store)

	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/vocabulary/%d/edit", id), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), fmt.Sprintf(`name="content_revision" value="%d"`, original.ContentRevision)) {
		t.Fatalf("edit status = %d, body = %s", response.Code, response.Body.String())
	}

	if err := store.Update(ctx, id, original.ContentRevision, CreateInput{
		Expression:    "読み込む",
		Pronunciation: "よみこむ",
		Meanings:      []string{"to load"},
	}); err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"content_revision": {strconv.FormatInt(original.ContentRevision, 10)},
		"expression":       {"古い編集"},
		"pronunciation":    {"ふるいへんしゅう"},
		"meanings":         {"stale edit"},
	}
	request = vocabularyFormRequest(t, fmt.Sprintf("/vocabulary/%d", id), form)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{
		"This word changed after you opened the edit form",
		"Your text and removal choices are still here",
		"古い編集",
		fmt.Sprintf(`name="content_revision" value="%d"`, original.ContentRevision+1),
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("stale edit response does not contain %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "file was not retained") || strings.Contains(body, "files were not retained") {
		t.Fatalf("stale edit without an upload shows a file warning: %s", body)
	}
	current, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if current.Expression != "読み込む" || current.ContentRevision != original.ContentRevision+1 {
		t.Fatalf("stale form overwrote current vocabulary: %+v", current)
	}

	form.Set("content_revision", strconv.FormatInt(current.ContentRevision, 10))
	request = vocabularyFormRequest(t, fmt.Sprintf("/vocabulary/%d", id), form)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("retry status = %d, body = %s", response.Code, response.Body.String())
	}
	retried, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Expression != "古い編集" || retried.Pronunciation != "ふるいへんしゅう" || len(retried.Meanings) != 1 || retried.Meanings[0] != "stale edit" {
		t.Fatalf("retry did not save the preserved edit: %+v", retried)
	}
}

func vocabularyFormRequest(t *testing.T, path string, values url.Values) *http.Request {
	return vocabularyMultipartRequest(t, path, values, "", "", nil)
}

func vocabularyMultipartRequest(t *testing.T, path string, values url.Values, field, filename string, content []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, entries := range values {
		for _, value := range entries {
			if err := writer.WriteField(name, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	if field != "" {
		part, err := writer.CreateFormFile(field, filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}
