package vocabulary

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	"github.com/tomasmik/goi/internal/media"
	"github.com/tomasmik/goi/internal/pronunciation"
	internalweb "github.com/tomasmik/goi/internal/web"
)

type fakeExampleGenerator struct {
	result       examplegen.Example
	err          error
	beforeReturn func()
	requests     []examplegen.Request
}

type fakePronunciationProvider struct {
	recordings []pronunciation.Recording
	upload     media.Upload
	searches   int
	downloads  int
	expression string
	reading    string
}

func (provider *fakePronunciationProvider) Search(_ context.Context, expression, reading string) ([]pronunciation.Recording, error) {
	provider.searches++
	provider.expression = expression
	provider.reading = reading
	return provider.recordings, nil
}

func (provider *fakePronunciationProvider) Download(_ context.Context, _ int64, expression, reading string) (media.Upload, error) {
	provider.downloads++
	provider.expression = expression
	provider.reading = reading
	return provider.upload, nil
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

func vocabularyTestRouterWithPronunciation(t *testing.T, store *Store, provider pronunciationProvider) http.Handler {
	t.Helper()
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	newHandler(store, renderer, nil, provider).Routes(router)
	return router
}

func TestVocabularyEditFindsAndAttachesPronunciation(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{
		Expression:    "育てる",
		Pronunciation: "そだてる",
		Meanings:      []string{"to raise"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakePronunciationProvider{
		recordings: []pronunciation.Recording{
			{ID: 42, Label: "そだてる", SourceName: "Test recordings"},
			{ID: 43, Label: "そだてる", SourceName: "Test recordings"},
		},
		upload: media.Upload{
			Kind:       media.KindAudio,
			MimeType:   "audio/mpeg",
			Content:    []byte("pronunciation audio"),
			SourceName: "Test recordings",
		},
	}
	router := vocabularyTestRouterWithPronunciation(t, store, provider)

	form := url.Values{
		"content_revision": {strconv.FormatInt(item.ContentRevision, 10)},
		"expression":       {item.Expression},
		"pronunciation":    {item.Pronunciation},
		"meanings":         {strings.Join(item.Meanings, "\n")},
		"notes":            {"unsaved note"},
	}
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/pronunciations/search", id), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("search status = %d, body = %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{"Open Japanese recordings", "そだてる", "Use this recording", "unsaved note", fmt.Sprintf("/vocabulary/%d/pronunciations/42", id)} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("search response does not contain %q: %s", expected, response.Body.String())
		}
	}
	if provider.searches != 1 || provider.expression != item.Expression || provider.reading != item.Pronunciation {
		t.Fatalf("pronunciation searches = %d, want 1", provider.searches)
	}

	request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/vocabulary/%d/pronunciations/42", id), nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "pronunciation audio" || response.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("preview response = %d %q %q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/pronunciations/42", id), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("attach status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "Pronunciation audio added.") || !strings.Contains(response.Body.String(), "unsaved note") {
		t.Fatalf("attach response did not preserve form: %s", response.Body.String())
	}

	updated, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ContentRevision != item.ContentRevision+1 || len(updated.Media) != 1 || updated.Media[0].Kind != string(media.KindAudio) {
		t.Fatalf("updated vocabulary = %+v", updated)
	}
	if updated.Expression != item.Expression || updated.Pronunciation != item.Pronunciation || strings.Join(updated.Meanings, "\n") != strings.Join(item.Meanings, "\n") {
		t.Fatalf("attaching audio changed card text: before=%+v after=%+v", item, updated)
	}
}

func TestVocabularyEditAutomaticallyUsesSinglePronunciationResult(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{
		Expression: "育てる", Pronunciation: "そだてる", Meanings: []string{"to raise"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakePronunciationProvider{
		recordings: []pronunciation.Recording{{ID: 42, Label: "そだてる"}},
		upload:     media.Upload{Kind: media.KindAudio, MimeType: "audio/mpeg", Content: []byte("pronunciation audio")},
	}
	form := url.Values{
		"content_revision": {strconv.FormatInt(item.ContentRevision, 10)},
		"expression":       {item.Expression},
		"pronunciation":    {item.Pronunciation},
		"meanings":         {strings.Join(item.Meanings, "\n")},
		"notes":            {"unsaved note"},
	}
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/pronunciations/search", id), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	vocabularyTestRouterWithPronunciation(t, store, provider).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Pronunciation audio added.") {
		t.Fatalf("response = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "Use this recording") {
		t.Fatal("single pronunciation result still requires a second click")
	}
	updated, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Media) != 1 || provider.downloads != 1 {
		t.Fatalf("updated media = %d, downloads = %d", len(updated.Media), provider.downloads)
	}
}

func TestMissingVocabularyRendersRecoveryPage(t *testing.T) {
	_, db := openTestDatabase(t)
	router := vocabularyTestRouter(t, NewStore(db))
	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/vocabulary/999", nil),
		httptest.NewRequest(http.MethodPost, "/vocabulary/999/action", strings.NewReader("action=delete&confirmed=1")),
	}
	requests[1].Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, request := range requests {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `href="/vocabulary"`) {
			t.Fatalf("missing vocabulary response = %d, %s", response.Code, response.Body.String())
		}
	}
}

func TestVocabularyListURLPreservesSearchFilterAndPage(t *testing.T) {
	got := vocabularyListURL("日本語", ListFilterLearning, 2)
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

func TestNewVocabularyUsesMiningCaptureFlow(t *testing.T) {
	_, db := openTestDatabase(t)
	router := vocabularyTestRouter(t, NewStore(db))
	request := httptest.NewRequest(http.MethodGet, "/vocabulary/new", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/mining/capture" {
		t.Fatalf("response = %d, location = %q", response.Code, response.Header().Get("Location"))
	}
}

func TestKnownVocabularyPreviewExplainsChangesWithoutSaving(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	if _, err := store.Create(ctx, CreateInput{Expression: "未学習", Pronunciation: "みがくしゅう", Meanings: []string{"not learned"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddKnown(ctx, "既知"); err != nil {
		t.Fatal(err)
	}
	router := vocabularyTestRouter(t, store)
	form := url.Values{"known_words": {"未学習 既知 新語"}}
	request := httptest.NewRequest(http.MethodPost, "/vocabulary/known/preview", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("preview status = %d, body = %s", response.Code, response.Body.String())
	}
	var result struct {
		Created      int `json:"created"`
		MarkedKnown  int `json:"markedKnown"`
		AlreadyKnown int `json:"alreadyKnown"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.MarkedKnown != 1 || result.AlreadyKnown != 1 {
		t.Fatalf("preview = %+v", result)
	}
	count, err := store.ListCount(ctx, "新語")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("preview created %d vocabulary rows", count)
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

func TestExampleRouteAcceptsMaximumMultibyteForm(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{
		Expression:    "例",
		Pronunciation: "れい",
		Meanings:      []string{"example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := vocabularyTestRouter(t, store)

	form := url.Values{
		"sentence":       {strings.Repeat("例", 2000)},
		"translation":    {strings.Repeat("訳", 2000)},
		"target_surface": {strings.Repeat("例", 256)},
	}
	encoded := form.Encode()
	if len(encoded) <= 16<<10 || int64(len(encoded)) >= ExampleFormBodyLimit {
		t.Fatalf("encoded example size = %d", len(encoded))
	}
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/examples", id), strings.NewReader(encoded))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Examples) != 1 || item.Examples[0].Sentence != form.Get("sentence") || item.Examples[0].Translation != form.Get("translation") || item.Examples[0].TargetSurface != form.Get("target_surface") {
		t.Fatalf("stored example = %+v", item.Examples)
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

func TestGenerateExampleRejectsSparseKnownVocabulary(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	if _, err := store.AddKnown(ctx, "既知語"); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListPage(ctx, "既知語", maximumListPageSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}

	generator := &fakeExampleGenerator{}
	router := vocabularyTestRouterWithGenerator(t, store, generator)

	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/examples/generate", items[0].ID), strings.NewReader(""))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if len(generator.requests) != 0 {
		t.Fatalf("generator requests = %+v", generator.requests)
	}
	if !strings.Contains(response.Body.String(), "Add a reading and meaning before generating an example") {
		t.Fatalf("body = %s", response.Body.String())
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

func TestGenerateExampleReturnsNotFoundWhenVocabularyIsDeletedDuringRequest(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{
		Expression:    "消える",
		Pronunciation: "きえる",
		Meanings:      []string{"to disappear"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var deleteErr error
	generator := &fakeExampleGenerator{
		result: examplegen.Example{
			Sentence:      "姿が消えます。",
			Translation:   "The figure disappears.",
			TargetSurface: "消えます",
			Provider:      "local.test",
			Model:         "small-japanese-model",
		},
		beforeReturn: func() {
			_, deleteErr = db.ExecContext(ctx, "DELETE FROM vocabulary WHERE id = ?", id)
		},
	}
	router := vocabularyTestRouterWithGenerator(t, store, generator)

	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/examples/generate", id), strings.NewReader(""))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if deleteErr != nil {
		t.Fatal(deleteErr)
	}
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestExampleValidationPreservesSubmittedForms(t *testing.T) {
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
	example, err := examples.NewStore(db).Create(ctx, id, examples.Input{
		Sentence:      "本を読む。",
		Translation:   "I read a book.",
		TargetSurface: "読む",
	})
	if err != nil {
		t.Fatal(err)
	}
	router := vocabularyTestRouter(t, store)
	oversizedTarget := strings.Repeat("長", 257)

	for _, test := range []struct {
		name        string
		path        string
		sentence    string
		translation string
		wantOpen    bool
	}{
		{name: "add", path: fmt.Sprintf("/vocabulary/%d/examples", id), sentence: "新聞を読みます。", translation: "I read the newspaper."},
		{name: "edit", path: fmt.Sprintf("/vocabulary/%d/examples/%d", id, example.ID), sentence: "雑誌を読みます。", translation: "I read a magazine.", wantOpen: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			form := url.Values{
				"sentence":       {test.sentence},
				"translation":    {test.translation},
				"target_surface": {oversizedTarget},
			}
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			body := response.Body.String()
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body = %s", response.Code, body)
			}
			if !strings.Contains(body, test.sentence) || !strings.Contains(body, test.translation) || !strings.Contains(body, oversizedTarget) {
				t.Fatalf("submitted example was not preserved: %s", body)
			}
			if test.wantOpen && !strings.Contains(body, `class="example-edit form-details" open`) {
				t.Fatalf("invalid edit was not reopened: %s", body)
			}
		})
	}
}

func TestGenerateExampleHidesProviderFailure(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{Expression: "見る", Pronunciation: "みる", Meanings: []string{"to see"}})
	if err != nil {
		t.Fatal(err)
	}
	generator := &fakeExampleGenerator{err: errors.New("provider rejected secret-token")}
	router := vocabularyTestRouterWithGenerator(t, store, generator)

	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/examples/generate", id), strings.NewReader(""))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "Could not generate an example") || strings.Contains(body, "secret-token") {
		t.Fatalf("unsafe generation error response: %s", body)
	}
}

func TestGenerateExistingExampleFieldsReturnsToEditorWithoutSaving(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{
		Expression:    "育てる",
		Pronunciation: "そだてる",
		Meanings:      []string{"to raise"},
	})
	if err != nil {
		t.Fatal(err)
	}
	example, err := examples.NewStore(db).Create(ctx, id, examples.Input{
		Sentence:      "庭で花を育てる。",
		TargetSurface: "育てる",
	})
	if err != nil {
		t.Fatal(err)
	}
	generator := &fakeExampleGenerator{result: examplegen.Example{
		Sentence:      "庭で花を育てる。",
		Translation:   "I grow flowers in the garden.",
		TargetSurface: "育てる",
	}}
	router := vocabularyTestRouterWithGenerator(t, store, generator)
	form := url.Values{
		"sentence":  {example.Sentence},
		"return_to": {"edit"},
	}
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/examples/%d/generate", id, example.ID), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, body)
	}
	for _, expected := range []string{"Example fields generated. Check them before saving.", "I grow flowers in the garden.", `value="育てる"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("generated editor does not contain %q: %s", expected, body)
		}
	}
	if len(generator.requests) != 1 || generator.requests[0].Sentence != example.Sentence {
		t.Fatalf("generator requests = %+v", generator.requests)
	}
	stored, err := examples.NewStore(db).List(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Translation != "" {
		t.Fatalf("generation saved before confirmation: %+v", stored)
	}
}

func TestGenerateNewExampleDraftRequiresConfirmation(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{
		Expression:    "育てる",
		Pronunciation: "そだてる",
		Meanings:      []string{"to raise"},
	})
	if err != nil {
		t.Fatal(err)
	}
	generator := &fakeExampleGenerator{result: examplegen.Example{
		Sentence:      "庭で花を育てる。",
		Translation:   "I grow flowers in the garden.",
		TargetSurface: "育てる",
	}}
	router := vocabularyTestRouterWithGenerator(t, store, generator)
	form := url.Values{"return_to": {"edit"}}
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/examples/generate-draft", id), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "I grow flowers in the garden.") || !strings.Contains(body, "Example fields generated. Check them before saving.") {
		t.Fatalf("draft response = %d, body = %s", response.Code, body)
	}
	stored, err := examples.NewStore(db).List(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 0 {
		t.Fatalf("draft generation saved examples: %+v", stored)
	}
}

func TestKnownVocabularyRouteAddsAndRendersSparseWords(t *testing.T) {
	_, db := openTestDatabase(t)
	store := NewStore(db)
	router := vocabularyTestRouter(t, store)

	form := url.Values{"known_words": {"日本語, 読む 見る"}}
	request := httptest.NewRequest(http.MethodPost, "/vocabulary/known", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	location := response.Header().Get("Location")
	if !strings.Contains(location, "known_added=3") {
		t.Fatalf("location = %q, want added count", location)
	}

	request = httptest.NewRequest(http.MethodGet, location, nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", response.Code, body)
	}
	for _, expected := range []string{
		"3 known",
		"3 words added to your known vocabulary.",
		"Import vocabulary",
		"Plain text",
		">JSON</a>",
		`href="/vocabulary/export?format=json"`,
		`href="/vocabulary/export?format=comma"`,
		"Known elsewhere",
		"日本語",
		"読む",
		"見る",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("list does not contain %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "Add known words") {
		t.Fatalf("vocabulary list retains duplicate known-word form: %s", body)
	}

	items, err := store.ListPage(context.Background(), "日本語", maximumListPageSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/vocabulary/%d", items[0].ID), nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	body = response.Body.String()
	if !strings.Contains(body, "No examples.") {
		t.Fatalf("sparse detail has misleading example copy: %s", body)
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

func TestKnownVocabularyRouteAcceptsLargeBatchWithinBodyLimit(t *testing.T) {
	_, db := openTestDatabase(t)
	router := vocabularyTestRouter(t, NewStore(db))

	words := make([]string, 750)
	for index := range words {
		words[index] = strings.Repeat("語", 200) + strconv.Itoa(index)
	}
	form := url.Values{"known_words": {strings.Join(words, ",")}}
	encoded := form.Encode()
	if len(encoded) <= 256<<10 || int64(len(encoded)) >= KnownWordsBodyLimit {
		t.Fatalf("encoded batch size = %d", len(encoded))
	}
	request := httptest.NewRequest(http.MethodPost, "/vocabulary/known", strings.NewReader(encoded))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Header().Get("Location"), "known_added=750") {
		t.Fatalf("location = %q", response.Header().Get("Location"))
	}
}

func TestKnownVocabularyValidationPreservesInput(t *testing.T) {
	_, db := openTestDatabase(t)
	router := vocabularyTestRouter(t, NewStore(db))
	tooLong := strings.Repeat("語", maxExpressionRunes+1)
	form := url.Values{"known_words": {tooLong}}
	request := httptest.NewRequest(http.MethodPost, "/vocabulary/known", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "word must be at most 256 characters") || !strings.Contains(body, tooLong) {
		t.Fatalf("validation response did not preserve input: %s", body)
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

func TestStaleVocabularyEditWarnsWhenPictureMustBeReselected(t *testing.T) {
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
	if err := store.Update(ctx, id, original.ContentRevision, CreateInput{
		Expression:    "読み込む",
		Pronunciation: "よみこむ",
		Meanings:      []string{"to load"},
	}); err != nil {
		t.Fatal(err)
	}
	router := vocabularyTestRouter(t, store)

	values := url.Values{
		"content_revision": {strconv.FormatInt(original.ContentRevision, 10)},
		"expression":       {"古い編集"},
		"pronunciation":    {"ふるいへんしゅう"},
		"meanings":         {"stale edit"},
	}
	request := vocabularyFormRequestWithFile(
		t,
		fmt.Sprintf("/vocabulary/%d", id),
		values,
		"picture",
		"replacement.png",
		testPicture(t),
	)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	responseBody := response.Body.String()
	if response.Code != http.StatusConflict || !strings.Contains(responseBody, "The image file was not retained. Select it again before saving.") {
		t.Fatalf("response = %d, body = %s", response.Code, responseBody)
	}
	if !strings.Contains(responseBody, "古い編集") || !strings.Contains(responseBody, fmt.Sprintf(`name="content_revision" value="%d"`, original.ContentRevision+1)) {
		t.Fatalf("stale response did not preserve text with the current revision: %s", responseBody)
	}
}

func TestEditValidationWarnsWhenPictureMustBeReselected(t *testing.T) {
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
	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	router := vocabularyTestRouter(t, store)
	request := vocabularyFormRequestWithFile(
		t,
		fmt.Sprintf("/vocabulary/%d", id),
		url.Values{
			"content_revision": {strconv.FormatInt(item.ContentRevision, 10)},
			"expression":       {""},
			"pronunciation":    {"よむ"},
			"meanings":         {"to read"},
		},
		"picture",
		"replacement.png",
		testPicture(t),
	)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(body, "expression is required") {
		t.Fatalf("response = %d, body = %s", response.Code, body)
	}
	if !strings.Contains(body, "The image file was not retained. Select it again before saving.") {
		t.Fatalf("edit error does not explain the cleared image input: %s", body)
	}
	if strings.Contains(body, "The audio file was not retained") || !strings.Contains(body, fmt.Sprintf(`name="content_revision" value="%d"`, item.ContentRevision)) {
		t.Fatalf("edit error has the wrong file warning or revision: %s", body)
	}
	stored, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Expression != item.Expression || len(stored.Media) != 0 {
		t.Fatalf("invalid edit changed vocabulary: %+v", stored)
	}
}

func TestVocabularyEditShowsAndRemovesCurrentMedia(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{
		Expression:    "食べる",
		Pronunciation: "たべる",
		Meanings:      []string{"to eat"},
		Audio:         &media.Upload{Kind: media.KindAudio, MimeType: "audio/mpeg", Content: []byte("audio")},
		Picture:       &media.Upload{Kind: media.KindImage, MimeType: "image/png", Content: []byte("picture")},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	router := vocabularyTestRouter(t, store)

	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/vocabulary/%d/edit", id), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `name="remove_audio"`) || !strings.Contains(body, `name="remove_picture"`) {
		t.Fatalf("media edit form = %d, body = %s", response.Code, body)
	}
	for _, asset := range item.Media {
		if !strings.Contains(body, fmt.Sprintf(`src="/media/%d"`, asset.ID)) {
			t.Errorf("edit form does not show media %d: %s", asset.ID, body)
		}
	}

	form := url.Values{
		"content_revision": {strconv.FormatInt(item.ContentRevision, 10)},
		"expression":       {item.Expression},
		"pronunciation":    {item.Pronunciation},
		"meanings":         {strings.Join(item.Meanings, "\n")},
		"remove_picture":   {"on"},
	}
	request = vocabularyFormRequest(t, fmt.Sprintf("/vocabulary/%d", id), form)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("remove image response = %d, body = %s", response.Code, response.Body.String())
	}
	updated, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Media) != 1 || updated.Media[0].Kind != "audio" {
		t.Fatalf("media after image removal = %+v", updated.Media)
	}
}

func TestVocabularyEditActionsReturnToWorkspace(t *testing.T) {
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
	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	router := vocabularyTestRouter(t, store)

	form := url.Values{
		"content_revision": {strconv.FormatInt(item.ContentRevision, 10)},
		"expression":       {"読み込む"},
		"pronunciation":    {"よみこむ"},
		"meanings":         {"to load"},
		"return_to":        {"edit"},
	}
	request := vocabularyFormRequest(t, fmt.Sprintf("/vocabulary/%d", id), form)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != fmt.Sprintf("/vocabulary/%d/edit?saved=1", id) {
		t.Fatalf("update response = %d, location = %q", response.Code, response.Header().Get("Location"))
	}

	exampleForm := url.Values{
		"sentence":       {"本を読み込む。"},
		"translation":    {"Load the book."},
		"target_surface": {"読み込む"},
		"return_to":      {"edit"},
	}
	request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/examples", id), strings.NewReader(exampleForm.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	wantLocation := fmt.Sprintf("/vocabulary/%d/edit?example=added#examples", id)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != wantLocation {
		t.Fatalf("example response = %d, location = %q", response.Code, response.Header().Get("Location"))
	}
}

func TestVocabularyActionErrorsRenderDetailPage(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{
		Expression:    "語彙",
		Pronunciation: "ごい",
		Meanings:      []string{"vocabulary"},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := vocabularyTestRouter(t, store)

	tests := []struct {
		name    string
		form    url.Values
		status  int
		message string
	}{
		{name: "invalid action", form: url.Values{"action": {"unknown"}}, status: http.StatusBadRequest, message: "That word action is not available."},
		{name: "missing confirmation", form: url.Values{"action": {string(ActionDelete)}}, status: http.StatusBadRequest, message: "Confirm this destructive word action"},
		{name: "invalid lifecycle", form: url.Values{"action": {string(ActionSuspend)}}, status: http.StatusConflict, message: "unlearned vocabulary has no reviews to suspend"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/action", id), strings.NewReader(test.form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			body := response.Body.String()
			if response.Code != test.status || !strings.Contains(body, test.message) {
				t.Fatalf("response = %d, body = %s", response.Code, body)
			}
			for _, expected := range []string{`class="site-header"`, `class="alert alert-error" role="alert"`, "語彙"} {
				if !strings.Contains(body, expected) {
					t.Errorf("rendered error does not contain %q: %s", expected, body)
				}
			}
		})
	}
}

func TestMalformedExampleFormRendersVocabularyDetail(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	id, err := store.Create(ctx, CreateInput{
		Expression:    "食べる",
		Pronunciation: "たべる",
		Meanings:      []string{"to eat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := vocabularyTestRouter(t, store)
	request := httptest.NewRequest(
		http.MethodPost,
		fmt.Sprintf("/vocabulary/%d/examples", id),
		strings.NewReader(strings.Repeat("x", int(ExampleFormBodyLimit)+1)),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusBadRequest || !strings.Contains(body, "example form is too large or invalid") {
		t.Fatalf("response = %d, body = %s", response.Code, body)
	}
	for _, expected := range []string{`class="site-header"`, `class="alert alert-error" role="alert"`, "食べる"} {
		if !strings.Contains(body, expected) {
			t.Errorf("rendered error does not contain %q: %s", expected, body)
		}
	}
}

func TestKnownElsewhereEditKeepsStudyFieldsOptional(t *testing.T) {
	ctx, db := openTestDatabase(t)
	store := NewStore(db)
	if _, err := store.AddKnown(ctx, "既知語"); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListPage(ctx, "既知語", maximumListPageSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	item, err := store.Get(ctx, items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	router := vocabularyTestRouter(t, store)
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/vocabulary/%d", item.ID), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "Not scheduled. This word only counts toward coverage.") {
		t.Fatalf("known-elsewhere detail = %d, body = %s", response.Code, body)
	}
	if strings.Contains(body, "<h2>Learning</h2>") || strings.Contains(body, "Memory stage") {
		t.Fatalf("known-elsewhere detail shows review state: %s", body)
	}

	request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/vocabulary/%d/edit", item.ID), nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	body = response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, body)
	}
	for _, name := range []string{"pronunciation", "meanings"} {
		if tag := formControlTag(t, body, name); strings.Contains(tag, "required") {
			t.Fatalf("known-elsewhere %s is required: %s", name, tag)
		}
	}
	for _, expected := range []string{"Add a reading if it is useful for reference", "Add meanings if they are useful for reference"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("known-elsewhere edit does not contain %q: %s", expected, body)
		}
	}

	invalid := url.Values{
		"content_revision": {strconv.FormatInt(item.ContentRevision, 10)},
		"expression":       {""},
		"pronunciation":    {""},
		"meanings":         {""},
	}
	request = vocabularyFormRequest(t, fmt.Sprintf("/vocabulary/%d", item.ID), invalid)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	body = response.Body.String()
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(body, "expression is required") {
		t.Fatalf("invalid status = %d, body = %s", response.Code, body)
	}
	for _, name := range []string{"pronunciation", "meanings"} {
		if tag := formControlTag(t, body, name); strings.Contains(tag, "required") {
			t.Fatalf("validation response made known-elsewhere %s required: %s", name, tag)
		}
	}

	valid := url.Values{
		"content_revision": {strconv.FormatInt(item.ContentRevision, 10)},
		"expression":       {"既知の語"},
		"pronunciation":    {""},
		"meanings":         {""},
		"notes":            {"Reference only"},
	}
	request = vocabularyFormRequest(t, fmt.Sprintf("/vocabulary/%d", item.ID), valid)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("valid status = %d, body = %s", response.Code, response.Body.String())
	}
	updated, err := store.Get(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Expression != "既知の語" || updated.Notes != "Reference only" || updated.Pronunciation != "" || len(updated.Meanings) != 0 {
		t.Fatalf("updated known vocabulary = %+v", updated)
	}
}

func TestUnlearnedDetailHidesUnavailableActions(t *testing.T) {
	_, db := openTestDatabase(t)
	id := insertVocabulary(t, db, "unlearned")
	if _, err := db.Exec(`
		INSERT INTO meanings (vocabulary_id, position, text, normalized_text)
		VALUES (?, 0, 'to eat', 'to eat')`, id); err != nil {
		t.Fatal(err)
	}
	router := vocabularyTestRouter(t, NewStore(db))

	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/vocabulary/%d", id), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, unavailable := range []string{
		`name="action" value="suspend"`,
		`name="action" value="reset"`,
	} {
		if strings.Contains(body, unavailable) {
			t.Errorf("unlearned detail contains unavailable action %q", unavailable)
		}
	}
	if !strings.Contains(body, `name="action" value="archive"`) {
		t.Error("unlearned detail does not contain archive action")
	}
	if !strings.Contains(body, `name="action" value="mark-known"`) || !strings.Contains(body, "I already know this") {
		t.Error("unlearned detail does not offer the known-elsewhere action")
	}
	if !strings.Contains(body, `name="action" value="delete"`) || !strings.Contains(body, `data-confirm=`) {
		t.Error("unlearned detail does not contain confirmed permanent removal")
	}
	if !strings.Contains(body, `id="word-actions-title"`) {
		t.Error("unlearned detail does not show word actions")
	}
	if !strings.Contains(body, `<section class="management-section" aria-labelledby="word-actions-title">`) {
		t.Error("visible word management section is missing")
	}
	if strings.Contains(body, `<details class="management-section`) {
		t.Error("word management is still hidden in a disclosure")
	}
	if strings.Contains(body, `action="/lessons/start"`) || strings.Contains(body, "Start a lesson") {
		t.Fatalf("unlearned detail still starts a one-word lesson: %s", body)
	}
	if !strings.Contains(body, "Not learned yet.") {
		t.Fatalf("unlearned detail does not show its learning state: %s", body)
	}
}

func TestActiveDetailMarksWordAsKnownElsewhere(t *testing.T) {
	ctx, db := openTestDatabase(t)
	id := insertVocabulary(t, db, "active")
	if _, err := db.Exec(`
		INSERT INTO meanings (vocabulary_id, position, text, normalized_text)
		VALUES (?, 0, 'to eat', 'to eat')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO srs_states (vocabulary_id, stage, due_at)
		VALUES (?, 3, 456)`, id); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	router := vocabularyTestRouter(t, store)

	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/vocabulary/%d", id), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "I already know this") {
		t.Fatalf("active detail = %d, %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "Study this word") || strings.Contains(response.Body.String(), `action="/study/selected"`) {
		t.Fatal("active vocabulary detail still offers one-word practice")
	}

	form := url.Values{"action": {string(ActionMarkKnown)}}
	request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/action", id), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed mark known status = %d, body = %s", response.Code, response.Body.String())
	}

	form.Set("confirmed", "1")
	request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/action", id), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("mark known status = %d, body = %s", response.Code, response.Body.String())
	}
	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !item.KnownElsewhere || item.Status != "unlearned" {
		t.Fatalf("marked item = %+v", item)
	}
}

func TestLearnedDetailConfirmsProgressReset(t *testing.T) {
	_, db := openTestDatabase(t)
	id := insertVocabulary(t, db, "active")
	if _, err := db.Exec(`
		INSERT INTO meanings (vocabulary_id, position, text, normalized_text)
		VALUES (?, 0, 'to eat', 'to eat')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO srs_states (vocabulary_id, stage, due_at)
		VALUES (?, 3, 456)`, id); err != nil {
		t.Fatal(err)
	}
	router := vocabularyTestRouter(t, NewStore(db))

	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/vocabulary/%d", id), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `name="action" value="reset"`) {
		t.Fatal("learned detail does not contain reset progress action")
	}
	if !strings.Contains(body, `data-confirm="Reset this word to unlearned and erase its review progress?"`) {
		t.Fatal("learned detail does not confirm progress reset")
	}
}

func TestDeleteRedirectsToVocabularyList(t *testing.T) {
	_, db := openTestDatabase(t)
	id := insertVocabulary(t, db, "unlearned")
	router := vocabularyTestRouter(t, NewStore(db))

	form := url.Values{"action": {string(ActionDelete)}}
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/action", id), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed status = %d, body = %s", response.Code, response.Body.String())
	}
	assertVocabularyState(t, db, id, "unlearned", false, false)

	form.Set("confirmed", "1")
	request = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/action", id), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("confirmed status = %d, body = %s", response.Code, response.Body.String())
	}
	if location := response.Header().Get("Location"); location != "/vocabulary" {
		t.Fatalf("location = %q, want /vocabulary", location)
	}
	assertVocabularyDeleted(t, db, id)
}

func TestForceDeleteAbandonsActiveReviewThroughHandler(t *testing.T) {
	ctx, db := openTestDatabase(t)
	id := insertVocabulary(t, db, "active")
	if _, err := db.Exec(`INSERT INTO srs_states (vocabulary_id, stage, due_at) VALUES (?, 2, 123)`, id); err != nil {
		t.Fatal(err)
	}
	reviewResult, err := db.Exec(`INSERT INTO review_sessions (kind, status) VALUES ('normal', 'active')`)
	if err != nil {
		t.Fatal(err)
	}
	reviewID, err := reviewResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO review_session_items (session_id, vocabulary_id, position, status)
		VALUES (?, ?, 0, 'current')`, reviewID, id); err != nil {
		t.Fatal(err)
	}
	router := vocabularyTestRouter(t, NewStore(db))
	form := url.Values{"action": {string(ActionDelete)}, "confirmed": {"1"}}
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/vocabulary/%d/action", id), strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/vocabulary" {
		t.Fatalf("response = %d %q, %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	assertVocabularyDeleted(t, db, id)
	var reviewStatus string
	if err := db.QueryRowContext(ctx, "SELECT status FROM review_sessions WHERE id = ?", reviewID).Scan(&reviewStatus); err != nil {
		t.Fatal(err)
	}
	if reviewStatus != "abandoned" {
		t.Fatalf("review status = %q, want abandoned", reviewStatus)
	}
}

func TestActionHidesStoreFailures(t *testing.T) {
	_, db := openTestDatabase(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	router := vocabularyTestRouter(t, NewStore(db))
	form := url.Values{"action": {string(ActionSuspend)}}

	request := httptest.NewRequest(http.MethodPost, "/vocabulary/1/action", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if body := response.Body.String(); !strings.Contains(body, "could not update vocabulary") || strings.Contains(body, "closed") {
		t.Fatalf("response exposed store failure: %s", body)
	}
}

func TestVocabularyFormErrorStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "malformed form", err: formError("invalid form"), want: http.StatusBadRequest},
		{name: "duplicate", err: duplicateError{id: 4}, want: http.StatusConflict},
		{name: "stale revision", err: revisionConflictError{}, want: http.StatusConflict},
		{name: "validation", err: validationError("invalid value"), want: http.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := vocabularyFormErrorStatus(test.err); got != test.want {
				t.Fatalf("status = %d, want %d", got, test.want)
			}
		})
	}
}

func formControlTag(t *testing.T, body, name string) string {
	t.Helper()
	namePosition := strings.Index(body, `name="`+name+`"`)
	if namePosition < 0 {
		t.Fatalf("form control %q not found: %s", name, body)
	}
	start := strings.LastIndex(body[:namePosition], "<")
	endOffset := strings.Index(body[namePosition:], ">")
	if start < 0 || endOffset < 0 {
		t.Fatalf("form control %q has incomplete markup: %s", name, body)
	}
	return body[start : namePosition+endOffset+1]
}

func vocabularyFormRequest(t *testing.T, path string, values url.Values) *http.Request {
	return vocabularyMultipartRequest(t, path, values, "", "", nil)
}

func vocabularyFormRequestWithFile(t *testing.T, path string, values url.Values, field, filename string, content []byte) *http.Request {
	return vocabularyMultipartRequest(t, path, values, field, filename, content)
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

func testPicture(t *testing.T) []byte {
	t.Helper()
	picture, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	return picture
}

func testAudio() []byte {
	return []byte{
		'R', 'I', 'F', 'F', 36, 0, 0, 0, 'W', 'A', 'V', 'E',
		'f', 'm', 't', ' ', 16, 0, 0, 0, 1, 0, 1, 0,
		0x40, 0x1f, 0, 0, 0x80, 0x3e, 0, 0, 2, 0, 16, 0,
		'd', 'a', 't', 'a', 0, 0, 0, 0,
	}
}
