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

	"github.com/tomasmik/goi/internal/contextio"
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

func TestApplyResultCapsDisplayedErrors(t *testing.T) {
	result := ApplyResult{}
	for index := range applyResultErrorLimit + 2 {
		result.addError(fmt.Sprintf("error %d", index))
	}

	if len(result.Errors) != applyResultErrorLimit || result.OmittedErrors != 2 {
		t.Fatalf("errors = %d, omitted = %d", len(result.Errors), result.OmittedErrors)
	}
}

func TestCompatibleModelFieldsRejectsDifferentLayouts(t *testing.T) {
	_, err := compatibleModelFields(map[string][]string{
		"1": {"Expression", "Reading", "Meaning"},
		"2": {"Front", "Back"},
	})
	if err == nil {
		t.Fatal("incompatible Anki models were accepted")
	}
}

func TestCombinedModelFieldsKeepsMixedNoteTypesMappable(t *testing.T) {
	fields := combinedModelFields(map[string][]string{
		"2": {"Front", "Back"},
		"1": {"Expression", "Reading", "Meaning"},
	})
	want := []string{"Expression / Front", "Reading", "Meaning / Back"}
	if fmt.Sprint(fields) != fmt.Sprint(want) {
		t.Fatalf("combined fields = %#v, want %#v", fields, want)
	}
	notes := []Note{{ModelID: 1}, {ModelID: 2}, {ModelID: 1}}
	if got := distinctModelCount(notes); got != 2 {
		t.Fatalf("model count = %d, want 2", got)
	}
}

func TestSplitNoteFieldsValidatesCountBeforeSplitting(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		expected      []string
		knownLayout   bool
		wantCount     int
		wantErrorText string
	}{
		{
			name:        "known layout",
			raw:         "front\x1fback",
			expected:    []string{"Front", "Back"},
			knownLayout: true,
			wantCount:   2,
		},
		{
			name:          "known layout mismatch",
			raw:           "front\x1fback\x1fextra",
			expected:      []string{"Front", "Back"},
			knownLayout:   true,
			wantErrorText: "incompatible field layouts",
		},
		{
			name:          "unknown layout over limit",
			raw:           strings.Repeat("value\x1f", maxNoteFieldCount),
			wantErrorText: "more than 256 fields",
		},
		{
			name:      "unknown layout at limit",
			raw:       strings.Repeat("value\x1f", maxNoteFieldCount-1) + "value",
			wantCount: maxNoteFieldCount,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fields, err := splitNoteFields(42, test.raw, test.expected, test.knownLayout)
			if test.wantErrorText != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErrorText) {
					t.Fatalf("splitNoteFields error = %v, want %q", err, test.wantErrorText)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(fields) != test.wantCount {
				t.Fatalf("field count = %d, want %d", len(fields), test.wantCount)
			}
		})
	}
}

func TestModelFieldMapRejectsExcessiveFieldCount(t *testing.T) {
	fields := make([]map[string]string, maxNoteFieldCount+1)
	for index := range fields {
		fields[index] = map[string]string{"name": fmt.Sprintf("Field %d", index+1)}
	}
	models, err := json.Marshal(map[string]any{"1": map[string]any{"flds": fields}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := modelFieldMap(string(models)); err == nil || !strings.Contains(err.Error(), "more than 256 fields") {
		t.Fatalf("modelFieldMap error = %v, want field-count error", err)
	}
}

func TestCopyWithContextStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelingReader{cancel: cancel}
	var destination bytes.Buffer

	_, err := contextio.Copy(ctx, &destination, reader)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("contextio.Copy error = %v, want context cancellation", err)
	}
	if reader.reads != 1 || destination.Len() != 0 {
		t.Fatalf("reads = %d, bytes written = %d; want one bounded read and no write", reader.reads, destination.Len())
	}
}

func TestUploadStopsReadingAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	file := &cancelingMultipartFile{
		reader: *bytes.NewReader(bytes.Repeat([]byte("x"), 128<<10)),
		cancel: cancel,
	}
	stagingDir := t.TempDir()
	store := &Store{stagingDir: stagingDir}

	_, _, err := store.Upload(ctx, file, "deck.apkg")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Upload error = %v, want context cancellation", err)
	}
	if file.reads != 1 {
		t.Fatalf("source reads = %d, want one bounded read", file.reads)
	}
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staged files = %d, want canceled upload cleaned up", len(entries))
	}
}

func TestUploadReportsFailedPublicationCleanup(t *testing.T) {
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
	if _, err := db.Exec(`
		CREATE TRIGGER block_import_run_delete
		BEFORE DELETE ON import_runs
		BEGIN
			SELECT RAISE(ABORT, 'blocked cleanup');
		END`); err != nil {
		t.Fatal(err)
	}

	stagingDir := filepath.Join(directory, "imports")
	store, err := NewStore(ctx, db, stagingDir, vocabulary.NewStore(db))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(stagingDir, "run-1.apkg"), 0o750); err != nil {
		t.Fatal(err)
	}
	archivePath := writeTextAnkiPackage(t, directory, []string{"食べる\x1fたべる\x1fto eat"})
	archive, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()

	_, _, err = store.Upload(ctx, archive, "deck.apkg")
	if err == nil || !strings.Contains(err.Error(), "publish staged Anki file") ||
		!strings.Contains(err.Error(), "remove failed Anki import run 1") {
		t.Fatalf("Upload error = %v, want publication and cleanup failures", err)
	}
	var runs int
	if err := db.QueryRow("SELECT COUNT(*) FROM import_runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("import runs = %d, want failed cleanup row retained", runs)
	}
}

func TestAppliedArchiveCleanupKeepsRecoverableState(t *testing.T) {
	t.Run("archive removal fails", func(t *testing.T) {
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
		stagingDir := filepath.Join(directory, "imports")
		store, err := NewStore(ctx, db, stagingDir, vocabulary.NewStore(db))
		if err != nil {
			t.Fatal(err)
		}
		archivePath := filepath.Join(stagingDir, "run-1.apkg")
		if err := os.Mkdir(archivePath, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(archivePath, "keep"), []byte("not removable as a file"), 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
			INSERT INTO import_runs (filename, archive_path, status, created_at, completed_at)
			VALUES ('deck.apkg', ?, 'applied', ?, ?)`, archivePath, time.Now().Unix(), time.Now().Unix()); err != nil {
			t.Fatal(err)
		}

		if err := store.cleanupAppliedArchive(ctx, 1, archivePath); err == nil || !strings.Contains(err.Error(), "remove applied Anki archive") {
			t.Fatalf("cleanupAppliedArchive error = %v, want removal failure", err)
		}
		var storedPath string
		if err := db.QueryRow("SELECT archive_path FROM import_runs WHERE id = 1").Scan(&storedPath); err != nil {
			t.Fatal(err)
		}
		if storedPath != archivePath {
			t.Fatalf("archive path = %q, want failed removal retained", storedPath)
		}
	})

	t.Run("database cleanup fails", func(t *testing.T) {
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
		stagingDir := filepath.Join(directory, "imports")
		store, err := NewStore(ctx, db, stagingDir, vocabulary.NewStore(db))
		if err != nil {
			t.Fatal(err)
		}
		archivePath := filepath.Join(stagingDir, "run-1.apkg")
		if err := os.WriteFile(archivePath, []byte("archive"), 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
			INSERT INTO import_runs (filename, archive_path, status, created_at, completed_at)
			VALUES ('deck.apkg', ?, 'applied', ?, ?)`, archivePath, time.Now().Unix(), time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
			CREATE TRIGGER block_archive_path_clear
			BEFORE UPDATE OF archive_path ON import_runs
			WHEN NEW.archive_path = ''
			BEGIN
				SELECT RAISE(ABORT, 'blocked cleanup');
			END`); err != nil {
			t.Fatal(err)
		}

		if err := store.cleanupAppliedArchive(ctx, 1, archivePath); err == nil || !strings.Contains(err.Error(), "clear applied Anki archive path") {
			t.Fatalf("cleanupAppliedArchive error = %v, want database cleanup failure", err)
		}
		if _, err := os.Stat(archivePath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("applied archive still exists or could not be checked: %v", err)
		}
		var storedPath string
		if err := db.QueryRow("SELECT archive_path FROM import_runs WHERE id = 1").Scan(&storedPath); err != nil {
			t.Fatal(err)
		}
		if storedPath != archivePath {
			t.Fatalf("archive path = %q, want missing file retained for startup repair", storedPath)
		}
		if _, err := db.Exec("DROP TRIGGER block_archive_path_clear"); err != nil {
			t.Fatal(err)
		}
		if _, err := NewStore(ctx, db, stagingDir, vocabulary.NewStore(db)); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow("SELECT archive_path FROM import_runs WHERE id = 1").Scan(&storedPath); err != nil {
			t.Fatal(err)
		}
		if storedPath != "" {
			t.Fatalf("archive path after startup repair = %q, want empty", storedPath)
		}
	})
}

type cancelingReader struct {
	cancel context.CancelFunc
	reads  int
}

type cancelingMultipartFile struct {
	reader bytes.Reader
	cancel context.CancelFunc
	reads  int
}

func (file *cancelingMultipartFile) Read(buffer []byte) (int, error) {
	file.reads++
	count, err := file.reader.Read(buffer)
	file.cancel()
	return count, err
}

func (file *cancelingMultipartFile) ReadAt(buffer []byte, offset int64) (int, error) {
	return file.reader.ReadAt(buffer, offset)
}

func (file *cancelingMultipartFile) Seek(offset int64, whence int) (int64, error) {
	return file.reader.Seek(offset, whence)
}

func (file *cancelingMultipartFile) Close() error {
	return nil
}

func (reader *cancelingReader) Read(buffer []byte) (int, error) {
	reader.reads++
	count := min(len(buffer), 32)
	for index := range count {
		buffer[index] = 'x'
	}
	reader.cancel()
	return count, nil
}

func TestInspectHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Inspect(ctx, filepath.Join(t.TempDir(), "unused.apkg"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Inspect error = %v, want context cancellation", err)
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

func TestApplyContinuesAfterInvalidVocabularyInput(t *testing.T) {
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
		"見る\x1fq\x1fto see",
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
	if result.Created != 1 || result.Failed != 1 || result.Skipped != 0 {
		t.Fatalf("apply result = %+v", result)
	}
	if !result.Retryable {
		t.Fatal("partially failed import was not retryable")
	}

	rows, err := db.Query(`
		SELECT action, COALESCE(error_message, '')
		FROM import_notes
		WHERE run_id = ?
		ORDER BY id`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions, messages []string
	for rows.Next() {
		var action, message string
		if err := rows.Scan(&action, &message); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action)
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(actions); got != "[created failed]" {
		t.Fatalf("actions = %s", got)
	}
	if len(messages) != 2 || !strings.Contains(messages[1], `pronunciation: unsupported romaji near "q"`) {
		t.Fatalf("messages = %#v", messages)
	}
	var vocabularyCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary").Scan(&vocabularyCount); err != nil {
		t.Fatal(err)
	}
	if vocabularyCount != 1 {
		t.Fatalf("vocabulary count = %d, want 1", vocabularyCount)
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

func TestFailedApplyKeepsArchiveForRetry(t *testing.T) {
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

	archivePath := writeTextAnkiPackage(t, directory, []string{"見る\x1f\x1fto see"})
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
	if result.Failed != 1 || result.Created != 0 {
		t.Fatalf("apply result = %+v", result)
	}
	run, err := store.loadRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(run.Path); err != nil {
		t.Fatalf("staged archive was removed: %v", err)
	}

	expiredAt := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(run.Path, expiredAt, expiredAt); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(ctx, db, filepath.Join(directory, "imports"), vocabulary.NewStore(db)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(run.Path); err != nil {
		t.Fatalf("recently failed archive with an old file time was removed: %v", err)
	}
	if _, err := db.Exec(
		"UPDATE import_runs SET created_at = ?, completed_at = ? WHERE id = ?",
		expiredAt.Unix(),
		expiredAt.Unix(),
		runID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(ctx, db, filepath.Join(directory, "imports"), vocabulary.NewStore(db)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(run.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired failed archive still exists or could not be checked: %v", err)
	}
	var remaining int
	if err := db.QueryRow("SELECT COUNT(*) FROM import_runs WHERE id = ?", runID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("expired failed import count = %d, want 0", remaining)
	}
}

func TestAppliedImportResultExpires(t *testing.T) {
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

	completedAt := time.Now().Add(-25 * time.Hour).Unix()
	result, err := db.Exec(`
		INSERT INTO import_runs (filename, archive_path, status, created_at, completed_at)
		VALUES ('deck.apkg', '', 'applied', ?, ?)`, completedAt, completedAt)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO import_notes (run_id, action)
		VALUES (?, 'created')`, runID); err != nil {
		t.Fatal(err)
	}

	if _, err := NewStore(ctx, db, filepath.Join(directory, "imports"), vocabulary.NewStore(db)); err != nil {
		t.Fatal(err)
	}
	var runs, notes int
	if err := db.QueryRow("SELECT COUNT(*) FROM import_runs WHERE id = ?", runID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM import_notes WHERE run_id = ?", runID).Scan(&notes); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || notes != 0 {
		t.Fatalf("expired import retained %d runs and %d notes", runs, notes)
	}
}

func TestValidateNoteBudget(t *testing.T) {
	for _, test := range []struct {
		name      string
		notes     int64
		textBytes int64
		wantError bool
	}{
		{name: "within limits", notes: maxNoteCount, textBytes: maxNoteTextBytes},
		{name: "too many notes", notes: maxNoteCount + 1, wantError: true},
		{name: "too much note text", notes: 1, textBytes: maxNoteTextBytes + 1, wantError: true},
		{name: "invalid aggregate", notes: -1, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateNoteBudget(test.notes, test.textBytes)
			if (err != nil) != test.wantError {
				t.Fatalf("validateNoteBudget(%d, %d) error = %v", test.notes, test.textBytes, err)
			}
			if err != nil && !errors.Is(err, errInvalidPackage) {
				t.Fatalf("error = %v, want invalid package", err)
			}
		})
	}
}

func TestValidateModelMetadataBudget(t *testing.T) {
	for _, test := range []struct {
		name      string
		size      int64
		wantError bool
	}{
		{name: "within limit", size: maxModelMetadataBytes},
		{name: "too much metadata", size: maxModelMetadataBytes + 1, wantError: true},
		{name: "invalid size", size: -1, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateModelMetadataBudget(test.size)
			if (err != nil) != test.wantError {
				t.Fatalf("validateModelMetadataBudget(%d) error = %v", test.size, err)
			}
			if err != nil && !errors.Is(err, errInvalidPackage) {
				t.Fatalf("error = %v, want invalid package", err)
			}
		})
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

func TestApplyClaimRejectsAnOlderFailedResult(t *testing.T) {
	if completedAt := nextCompletionTime(sql.NullInt64{Int64: 100, Valid: true}, 100); completedAt != 101 {
		t.Fatalf("next completion time = %d, want 101", completedAt)
	}
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
	archivePath := store.runPath(1)
	if _, err := db.Exec(`
		INSERT INTO import_runs (id, filename, archive_path, status, created_at, completed_at)
		VALUES (1, 'deck.apkg', ?, 'failed', 100, 100)`, archivePath); err != nil {
		t.Fatal(err)
	}

	first, err := store.loadRun(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := store.loadRun(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	firstTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := claimRunForApply(ctx, firstTx, 1, first); err != nil {
		firstTx.Rollback()
		t.Fatal(err)
	}
	if _, err := firstTx.ExecContext(ctx, `UPDATE import_runs SET completed_at = 101 WHERE id = 1`); err != nil {
		firstTx.Rollback()
		t.Fatal(err)
	}
	if err := firstTx.Commit(); err != nil {
		t.Fatal(err)
	}

	staleTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer staleTx.Rollback()
	if err := claimRunForApply(ctx, staleTx, 1, stale); !errors.Is(err, errRunUnavailable) {
		t.Fatalf("stale claim error = %v, want unavailable run", err)
	}
}

func TestRestoredPendingRunCanBeAppliedWithoutReplacingExistingArchive(t *testing.T) {
	ctx := context.Background()
	sourceDirectory := t.TempDir()
	sourceDatabasePath := filepath.Join(sourceDirectory, "goi.sqlite")
	sourceDB, err := database.Open(ctx, sourceDatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, sourceDB); err != nil {
		sourceDB.Close()
		t.Fatal(err)
	}
	sourceStore, err := NewStore(ctx, sourceDB, filepath.Join(sourceDirectory, "imports"), vocabulary.NewStore(sourceDB))
	if err != nil {
		sourceDB.Close()
		t.Fatal(err)
	}
	archivePath := writeTextAnkiPackage(t, sourceDirectory, []string{"食べる\x1fたべる\x1fto eat"})
	archive, err := os.Open(archivePath)
	if err != nil {
		sourceDB.Close()
		t.Fatal(err)
	}
	runID, _, uploadErr := sourceStore.Upload(ctx, archive, "deck.apkg")
	closeErr := archive.Close()
	if uploadErr != nil {
		sourceDB.Close()
		t.Fatal(uploadErr)
	}
	if closeErr != nil {
		sourceDB.Close()
		t.Fatal(closeErr)
	}
	if err := sourceDB.Close(); err != nil {
		t.Fatal(err)
	}

	backupPath := filepath.Join(t.TempDir(), "backup.sqlite")
	if _, err := database.BackupWithImports(ctx, sourceDatabasePath, backupPath); err != nil {
		t.Fatal(err)
	}

	restoreDirectory := t.TempDir()
	restoredDatabasePath := filepath.Join(restoreDirectory, "goi.sqlite")
	restoredImportsPath := filepath.Join(restoreDirectory, "imports")
	if err := os.MkdirAll(restoredImportsPath, 0o750); err != nil {
		t.Fatal(err)
	}
	canonicalArchivePath := filepath.Join(restoredImportsPath, fmt.Sprintf("run-%d.apkg", runID))
	if err := os.WriteFile(canonicalArchivePath, []byte("existing archive"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := database.RestoreWithImports(ctx, backupPath, restoredDatabasePath, restoredImportsPath); err != nil {
		t.Fatal(err)
	}

	restoredDB, err := database.Open(ctx, restoredDatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredDB.Close()
	var restoredArchivePath string
	if err := restoredDB.QueryRow("SELECT archive_path FROM import_runs WHERE id = ?", runID).Scan(&restoredArchivePath); err != nil {
		t.Fatal(err)
	}
	if restoredArchivePath == canonicalArchivePath || !strings.Contains(filepath.Base(restoredArchivePath), "-restored-") {
		t.Fatalf("restored archive path = %q, want a collision-safe restored name", restoredArchivePath)
	}

	restoredStore, err := NewStore(ctx, restoredDB, restoredImportsPath, vocabulary.NewStore(restoredDB))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := restoredStore.Preview(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Notes) != 1 {
		t.Fatalf("restored preview contains %d notes, want 1", len(preview.Notes))
	}
	result, err := restoredStore.Apply(ctx, runID, Mapping{
		ExpressionField:    0,
		PronunciationField: 1,
		MeaningField:       2,
		AudioField:         -1,
		PictureField:       -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.Skipped != 0 || result.Failed != 0 {
		t.Fatalf("restored apply result = %+v", result)
	}
	var expression string
	if err := restoredDB.QueryRow("SELECT expression FROM vocabulary WHERE normalized_expression = ?", "食べる").Scan(&expression); err != nil {
		t.Fatal(err)
	}
	if expression != "食べる" {
		t.Fatalf("restored expression = %q", expression)
	}
	contents, err := os.ReadFile(canonicalArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "existing archive" {
		t.Fatalf("existing archive was replaced: %q", contents)
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
}

func TestInspectRejectsDuplicateArchiveEntries(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for range 2 {
		entry, err := writer.Create("collection.anki2")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte("duplicate")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "duplicate.apkg")
	if err := os.WriteFile(archivePath, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Inspect(context.Background(), archivePath); err == nil || !strings.Contains(err.Error(), "duplicate entry") {
		t.Fatalf("Inspect error = %v, want duplicate-entry error", err)
	}
}

func TestInspectExplainsModernAnkiFormat(t *testing.T) {
	for _, collectionName := range []string{"collection.anki21", "collection.anki21b"} {
		t.Run(collectionName, func(t *testing.T) {
			var archive bytes.Buffer
			writer := zip.NewWriter(&archive)
			entry, err := writer.Create(collectionName)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := entry.Write([]byte("modern collection")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			archivePath := filepath.Join(t.TempDir(), "modern.apkg")
			if err := os.WriteFile(archivePath, archive.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err = Inspect(context.Background(), archivePath)
			if err == nil || !strings.Contains(err.Error(), "Support older Anki versions") {
				t.Fatalf("Inspect error = %v, want compatible-export guidance", err)
			}
		})
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
