package imports

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"

	"github.com/tomasmik/goi/internal/database"
	"github.com/tomasmik/goi/internal/mining"
	"github.com/tomasmik/goi/internal/vocabulary"
)

func TestInspectAnkiPackage(t *testing.T) {
	directory := t.TempDir()
	collectionPath := filepath.Join(directory, "collection.anki2")
	db, err := sql.Open("sqlite3", collectionPath)
	if err != nil {
		t.Fatalf("open collection: %v", err)
	}
	models, _ := json.Marshal(map[string]any{
		"1": map[string]any{"flds": []map[string]string{{"name": "Expression"}, {"name": "Reading"}, {"name": "Meaning"}, {"name": "Picture"}}},
	})
	for _, statement := range []string{"CREATE TABLE col (models TEXT)", "CREATE TABLE notes (id INTEGER, flds TEXT, tags TEXT)"} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatalf("create collection table: %v", err)
		}
	}
	if _, err := db.Exec("INSERT INTO col(models) VALUES (?)", string(models)); err != nil {
		db.Close()
		t.Fatalf("insert models: %v", err)
	}
	if _, err := db.Exec("INSERT INTO notes(id, flds, tags) VALUES (42, ?, 'basic')", "食べる\x1fたべる\x1fto eat\x1f<img src=\"picture&amp;sample.png\">"); err != nil {
		db.Close()
		t.Fatalf("insert note: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close collection: %v", err)
	}

	archivePath := filepath.Join(directory, "sample.apkg")
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	writer := zip.NewWriter(archiveFile)
	entry, err := writer.Create("collection.anki2")
	if err != nil {
		t.Fatalf("create collection entry: %v", err)
	}
	collection, err := os.ReadFile(collectionPath)
	if err != nil {
		t.Fatalf("read collection: %v", err)
	}
	if _, err := entry.Write(collection); err != nil {
		t.Fatalf("write collection entry: %v", err)
	}
	manifestEntry, err := writer.Create("media")
	if err != nil {
		t.Fatalf("create media entry: %v", err)
	}
	if _, err := manifestEntry.Write([]byte(`{"0":"picture&sample.png"}`)); err != nil {
		t.Fatalf("write media manifest: %v", err)
	}
	imageEntry, err := writer.Create("0")
	if err != nil {
		t.Fatalf("create image entry: %v", err)
	}
	var imageBytes bytes.Buffer
	imageData := image.NewRGBA(image.Rect(0, 0, 1, 1))
	imageData.Set(0, 0, color.RGBA{R: 200, A: 255})
	if err := png.Encode(&imageBytes, imageData); err != nil {
		t.Fatalf("encode image: %v", err)
	}
	if _, err := imageEntry.Write(imageBytes.Bytes()); err != nil {
		t.Fatalf("write image entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive writer: %v", err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	preview, err := Inspect(context.Background(), archivePath)
	if err != nil {
		t.Fatalf("inspect archive: %v", err)
	}
	if len(preview.Fields) != 4 || preview.Fields[0] != "Expression" {
		t.Fatalf("fields = %#v, want mapped model fields", preview.Fields)
	}
	if len(preview.Notes) != 1 || preview.Notes[0].Fields[0] != "食べる" {
		t.Fatalf("notes = %#v, want one decoded note", preview.Notes)
	}
	if preview.MediaCount != 1 {
		t.Fatalf("media count = %d, want 1", preview.MediaCount)
	}
	resolver, err := newMediaResolver(context.Background(), archivePath)
	if err != nil {
		t.Fatalf("open media resolver: %v", err)
	}
	defer resolver.Close()
	_, picture, err := resolver.Resolve(context.Background(), preview.Notes[0], Mapping{PictureField: 3})
	if err != nil {
		t.Fatalf("resolve picture: %v", err)
	}
	if picture == nil || picture.Kind != "image" {
		t.Fatalf("picture = %#v, want image upload", picture)
	}
}

func TestInspectAnkiPackageWithMixedNoteTypes(t *testing.T) {
	directory := t.TempDir()
	collectionPath := filepath.Join(directory, "collection.anki2")
	db, err := sql.Open("sqlite3", collectionPath)
	if err != nil {
		t.Fatal(err)
	}
	models, err := json.Marshal(map[string]any{
		"1": map[string]any{"flds": []map[string]string{{"name": "Expression"}, {"name": "Reading"}, {"name": "Meaning"}}},
		"2": map[string]any{"flds": []map[string]string{{"name": "Front"}, {"name": "Back"}}},
	})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	for _, statement := range []string{"CREATE TABLE col (models TEXT)", "CREATE TABLE notes (id INTEGER, mid INTEGER, flds TEXT)"} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO col(models) VALUES (?)", string(models)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO notes(id, mid, flds) VALUES (1, 1, ?), (2, 2, ?)", "猫\x1fねこ\x1fcat", "犬\x1fdog"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(directory, "mixed.apkg")
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(archiveFile)
	entry, err := writer.Create("collection.anki2")
	if err != nil {
		archiveFile.Close()
		t.Fatal(err)
	}
	collection, err := os.ReadFile(collectionPath)
	if err != nil {
		archiveFile.Close()
		t.Fatal(err)
	}
	if _, err := entry.Write(collection); err != nil {
		archiveFile.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		archiveFile.Close()
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}

	preview, err := Inspect(context.Background(), archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if preview.ModelCount != 2 || fmt.Sprint(preview.Fields) != "[Expression / Front Reading Meaning / Back]" {
		t.Fatalf("preview models = %d, fields = %#v", preview.ModelCount, preview.Fields)
	}
	if len(preview.Notes) != 2 || len(preview.Notes[1].Fields) != 3 || preview.Notes[1].Fields[1] != "" || preview.Notes[1].Fields[2] != "dog" {
		t.Fatalf("preview notes = %#v", preview.Notes)
	}
}

func TestCleanImportFilename(t *testing.T) {
	if got, err := cleanImportFilename(" /tmp/deck.apkg "); err != nil || got != "deck.apkg" {
		t.Fatalf("cleanImportFilename() = %q, %v; want deck.apkg", got, err)
	}
	for _, value := range []string{"", "deck\n.apkg", strings.Repeat("a", maxImportFilenameRunes+1)} {
		if _, err := cleanImportFilename(value); !errors.Is(err, errInvalidPackage) {
			t.Fatalf("cleanImportFilename(%q) error = %v, want invalid package", value, err)
		}
	}
}

func TestMappingRequiresDistinctRequiredFields(t *testing.T) {
	mapping := Mapping{
		ExpressionField:    0,
		PronunciationField: 0,
		MeaningField:       1,
		AudioField:         -1,
		PictureField:       -1,
	}

	if err := mapping.validate(3); !errors.Is(err, errInvalidMapping) {
		t.Fatalf("mapping error = %v, want invalid mapping", err)
	}
}

func TestApplyRecordsEveryNoteOutcome(t *testing.T) {
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
	capture, replayed, err := mining.NewStore(db).Create(ctx, mining.CreateInput{
		Expression:   "食べる",
		ContextText:  "昨日、寿司を食べる。",
		SourceKind:   mining.SourceManual,
		CaptureNonce: "00000000000000000000000000000010",
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed {
		t.Fatal("new mining capture was reported as replayed")
	}

	archivePath := writeTextAnkiPackage(t, directory, []string{
		"食べる\x1fたべる\x1f<b>食べる</b> - to eat (food)<br>consume; to have a meal",
		"食べる\x1fたべる\x1fto consume",
		"見る\x1f\x1fto see",
	})
	source, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	store, err := NewStore(ctx, db, filepath.Join(directory, "imports"), vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	runID, _, err := store.Upload(ctx, source, "deck.apkg")
	if err != nil {
		t.Fatal(err)
	}
	stagedRun, err := store.loadRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Apply(ctx, runID, Mapping{
		ExpressionField:    0,
		PronunciationField: 1,
		MeaningField:       2,
		AudioField:         -1,
		PictureField:       -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.Skipped != 1 || result.Failed != 1 {
		t.Fatalf("apply result = %+v", result)
	}
	if !result.Retryable {
		t.Fatal("partially failed import was not retryable")
	}
	var persistedArchivePath string
	if err := db.QueryRow("SELECT archive_path FROM import_runs WHERE id = ?", runID).Scan(&persistedArchivePath); err != nil {
		t.Fatal(err)
	}
	if persistedArchivePath != stagedRun.Path {
		t.Fatalf("failed archive path = %q, want %q", persistedArchivePath, stagedRun.Path)
	}
	if _, err := os.Stat(stagedRun.Path); err != nil {
		t.Fatalf("retryable archive was removed: %v", err)
	}

	rows, err := db.Query(`
		SELECT id, action, COALESCE(error_message, '')
		FROM import_notes
		WHERE run_id = ?
		ORDER BY id`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var noteID int64
		var action, message string
		if err := rows.Scan(&noteID, &action, &message); err != nil {
			t.Fatal(err)
		}
		if action != "created" && message == "" {
			t.Fatalf("note %d action %q has no error message", noteID, action)
		}
		actions = append(actions, action)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(actions); got != "[created skipped failed]" {
		t.Fatalf("actions = %s", got)
	}
	var importedMeanings string
	if err := db.QueryRow(`
		SELECT group_concat(text, '|')
		FROM (
			SELECT text
			FROM meanings
			WHERE vocabulary_id = (SELECT id FROM vocabulary WHERE expression = '食べる')
			ORDER BY position
		)`).Scan(&importedMeanings); err != nil {
		t.Fatal(err)
	}
	if importedMeanings != "to eat (food)|consume|to have a meal" {
		t.Fatalf("imported meanings = %q", importedMeanings)
	}
	preservedCapture, err := mining.NewStore(db).Get(ctx, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preservedCapture.Status != mining.StatusPending || preservedCapture.ContextText != "昨日、寿司を食べる。" || preservedCapture.ExistingVocabularyID == nil {
		t.Fatalf("capture after Anki import = %#v", preservedCapture)
	}
}

func TestCleanImportedMeanings(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		value      string
		want       string
	}{
		{name: "expression prefix", expression: "食べる", value: "食べる - to eat (food)", want: "[to eat (food)]"},
		{name: "multiple separators", expression: "食べる", value: "to eat\nconsume; to have a meal", want: "[to eat consume to have a meal]"},
		{name: "bullets and duplicates", expression: "食べる", value: "• to eat；- To Eat", want: "[to eat]"},
		{name: "qualifier preserved", expression: "岸", value: "bank (river)", want: "[bank (river)]"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := cleanImportedMeanings(test.expression, test.value)
			if fmt.Sprint(got) != test.want {
				t.Fatalf("cleanImportedMeanings(%q, %q) = %q, want %s", test.expression, test.value, got, test.want)
			}
		})
	}
}

func TestCleanImportedText(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "raw literal", value: `1 < 2 > 0`, want: `1 < 2 > 0`},
		{name: "escaped literal", value: `1 &lt; 2 &gt; 0`, want: `1 < 2 > 0`},
		{name: "formatting", value: `<b>to</b> <i>eat</i>`, want: `to eat`},
		{name: "ruby", value: `<ruby>食<rt>た</rt><rp>(</rp>べる</ruby>`, want: `食べる`},
		{name: "ruby with line break", value: `<ruby>食<rt><br>た</rt>べる</ruby>`, want: `食べる`},
		{name: "line breaks", value: `<div>first</div><div>second<br class="x">third</div>`, want: "first\nsecond\nthird"},
		{name: "non-content", value: `word<style>.word{color:red}</style><script>alert(1)</script>`, want: `word`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := cleanImportedText(test.value); got != test.want {
				t.Fatalf("cleanImportedText(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestPartiallyFailedApplyCanBeRetried(t *testing.T) {
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

	archivePath := writeTextAnkiPackage(t, directory, []string{
		"食べる\x1fたべる\x1fto eat",
		"見る\x1fみる\x1fto see [sound:missing.mp3]",
	})
	source, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	store, err := NewStore(ctx, db, filepath.Join(directory, "imports"), vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	runID, _, err := store.Upload(ctx, source, "deck.apkg")
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.loadRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.Apply(ctx, runID, Mapping{
		ExpressionField:    0,
		PronunciationField: 1,
		MeaningField:       2,
		AudioField:         2,
		PictureField:       -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Created != 1 || first.Failed != 1 || !first.Retryable {
		t.Fatalf("first apply = %+v", first)
	}
	if _, err := os.Stat(run.Path); err != nil {
		t.Fatalf("retry archive is unavailable: %v", err)
	}

	second, err := store.Apply(ctx, runID, Mapping{
		ExpressionField:    0,
		PronunciationField: 1,
		MeaningField:       2,
		AudioField:         -1,
		PictureField:       -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Created != 1 || second.Skipped != 1 || second.Failed != 0 || second.Retryable {
		t.Fatalf("retry apply = %+v", second)
	}
	if _, err := os.Stat(run.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful retry archive still exists: %v", err)
	}
	var vocabularyCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary").Scan(&vocabularyCount); err != nil {
		t.Fatal(err)
	}
	if vocabularyCount != 2 {
		t.Fatalf("vocabulary count = %d, want 2", vocabularyCount)
	}
}

func TestAppliedRunCannotBeAppliedAgain(t *testing.T) {
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

	archivePath := writeTextAnkiPackage(t, directory, []string{"食べる\x1fたべる\x1fto eat"})
	source, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	store, err := NewStore(ctx, db, filepath.Join(directory, "imports"), vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	runID, _, err := store.Upload(ctx, source, "deck.apkg")
	if err != nil {
		t.Fatal(err)
	}
	stagedRun, err := store.loadRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	stagedArchive, err := os.ReadFile(stagedRun.Path)
	if err != nil {
		t.Fatal(err)
	}
	mapping := Mapping{
		ExpressionField:    0,
		PronunciationField: 1,
		MeaningField:       2,
		AudioField:         -1,
		PictureField:       -1,
	}
	if _, err := store.Apply(ctx, runID, mapping); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(stagedRun.Path, stagedArchive, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE import_runs SET archive_path = ? WHERE id = ?", stagedRun.Path, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(ctx, runID, mapping); err == nil {
		t.Fatal("applied import run was applied again")
	} else if !strings.Contains(err.Error(), "no longer available") {
		t.Fatalf("apply error = %q, want unavailable run", err)
	}

	var noteCount, vocabularyCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM import_notes WHERE run_id = ?", runID).Scan(&noteCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary").Scan(&vocabularyCount); err != nil {
		t.Fatal(err)
	}
	if noteCount != 1 || vocabularyCount != 1 {
		t.Fatalf("notes = %d, vocabulary = %d; want 1 of each", noteCount, vocabularyCount)
	}
}

func TestStoreNeverReadsOrDeletesArchivePathsOutsideStaging(t *testing.T) {
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

	victimPath := filepath.Join(directory, "keep.apkg")
	if err := os.WriteFile(victimPath, []byte("not an archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-25 * time.Hour).Unix()
	if _, err := db.Exec(`
		INSERT INTO import_runs (id, filename, archive_path, status, created_at)
		VALUES (1, 'old.apkg', ?, 'failed', ?)`, victimPath, old); err != nil {
		t.Fatal(err)
	}

	stagingDir := filepath.Join(directory, "imports")
	store, err := NewStore(ctx, db, stagingDir, vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(victimPath); err != nil {
		t.Fatalf("file outside staging was removed: %v", err)
	}

	if _, err := db.Exec(`
		INSERT INTO import_runs (id, filename, archive_path, status, created_at)
		VALUES (2, 'current.apkg', ?, 'previewed', ?)`, victimPath, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Preview(ctx, 2); err == nil || !strings.Contains(err.Error(), "invalid staged archive path") {
		t.Fatalf("Preview error = %v, want invalid staged archive path", err)
	}
}

func TestCopyZipEntryEnforcesLimit(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create("large")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(bytes.Repeat([]byte("x"), 64)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}

	var extracted bytes.Buffer
	if err := copyZipEntry(context.Background(), reader.File[0], &extracted, 10); err == nil {
		t.Fatal("oversized ZIP entry was accepted")
	}
	if extracted.Len() > 11 {
		t.Fatalf("extracted %d bytes, want at most 11", extracted.Len())
	}

	content, err := readZipEntryContext(context.Background(), reader.File[0], 64)
	if err != nil || !bytes.Equal(content, bytes.Repeat([]byte("x"), 64)) {
		t.Fatalf("read at limit = %q, %v; want the complete entry", content, err)
	}
	if content, err := readZipEntryContext(context.Background(), reader.File[0], 10); err == nil || content != nil {
		t.Fatalf("read beyond limit = %q, %v; want no content and an error", content, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if content, err := readZipEntryContext(ctx, reader.File[0], 64); !errors.Is(err, context.Canceled) || content != nil {
		t.Fatalf("canceled read = %q, %v; want no content and cancellation", content, err)
	}
}

func writeTextAnkiPackage(t *testing.T, directory string, notes []string) string {
	t.Helper()
	collectionPath := filepath.Join(directory, fmt.Sprintf("collection-%d.anki2", len(notes)))
	db, err := sql.Open("sqlite3", collectionPath)
	if err != nil {
		t.Fatal(err)
	}
	models, err := json.Marshal(map[string]any{
		"1": map[string]any{"flds": []map[string]string{
			{"name": "Expression"},
			{"name": "Reading"},
			{"name": "Meaning"},
		}},
	})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE TABLE col (models TEXT)",
		"CREATE TABLE notes (id INTEGER, mid INTEGER, flds TEXT)",
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO col(models) VALUES (?)", string(models)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	for index, fields := range notes {
		if _, err := db.Exec("INSERT INTO notes(id, mid, flds) VALUES (?, 1, ?)", index+1, fields); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(directory, fmt.Sprintf("deck-%d.apkg", len(notes)))
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(archiveFile)
	entry, err := writer.Create("collection.anki2")
	if err != nil {
		archiveFile.Close()
		t.Fatal(err)
	}
	collection, err := os.ReadFile(collectionPath)
	if err != nil {
		archiveFile.Close()
		t.Fatal(err)
	}
	if _, err := entry.Write(collection); err != nil {
		archiveFile.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		archiveFile.Close()
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}
