package imports

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/database"
	"github.com/tomasmik/goi/internal/vocabulary"
	internalweb "github.com/tomasmik/goi/internal/web"
)

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
		count, err := store.vocabulary.ListCountFiltered(ctx, expression, vocabulary.ListFilterAll)
		if err != nil || count != 0 {
			t.Fatalf("preview saved %q: count = %d, err = %v", expression, count, err)
		}
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
