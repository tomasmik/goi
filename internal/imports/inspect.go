package imports

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"

	"github.com/tomasmik/goi/internal/contextio"
)

const (
	maxModelMetadataBytes int64 = 16 << 20
	maxNoteCount          int64 = 100_000
	maxNoteTextBytes      int64 = 64 << 20
	maxNoteFieldCount           = 256
)

func Inspect(ctx context.Context, path string) (Preview, error) {
	if err := ctx.Err(); err != nil {
		return Preview{}, err
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		return Preview{}, invalidPackage("The selected file is not a valid Anki package.", err)
	}
	defer archive.Close()
	collection, mediaManifest, err := inspectArchive(ctx, archive.File)
	if err != nil {
		return Preview{}, err
	}
	mediaCount, err := mediaEntryCount(ctx, mediaManifest)
	if err != nil {
		return Preview{}, err
	}
	collectionPath, err := extractCollection(ctx, collection)
	if err != nil {
		return Preview{}, err
	}
	defer os.Remove(collectionPath)
	return inspectCollection(ctx, collectionPath, mediaCount)
}

func inspectArchive(ctx context.Context, entries []*zip.File) (*zip.File, *zip.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if len(entries) > 100000 {
		return nil, nil, invalidPackage("Anki archive contains too many entries", nil)
	}
	var collection *zip.File
	var modernCollection string
	var mediaManifest *zip.File
	var uncompressed uint64
	var compressed uint64
	seenEntries := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if _, exists := seenEntries[entry.Name]; exists {
			return nil, nil, invalidPackage(fmt.Sprintf("Anki archive contains duplicate entry %q", entry.Name), nil)
		}
		seenEntries[entry.Name] = struct{}{}
		if entry.Name == "collection.anki2" {
			collection = entry
		}
		if entry.Name == "collection.anki21" || entry.Name == "collection.anki21b" {
			modernCollection = entry.Name
		}
		if entry.Name == "media" {
			mediaManifest = entry
		}
		if !safeArchivePath(entry.Name) {
			return nil, nil, invalidPackage(fmt.Sprintf("unsafe Anki archive path %q", entry.Name), nil)
		}
		maximum := uint64(MaxArchiveBytes)
		if entry.UncompressedSize64 > maximum-uncompressed {
			return nil, nil, invalidPackage("Anki archive expands beyond the configured limit", nil)
		}
		uncompressed += entry.UncompressedSize64
		if entry.CompressedSize64 > maximum-compressed {
			return nil, nil, invalidPackage("Anki archive compressed size exceeds the configured limit", nil)
		}
		compressed += entry.CompressedSize64
		if compressed > 0 && uncompressed > compressed*100 {
			return nil, nil, invalidPackage("Anki archive compression ratio exceeds the configured limit", nil)
		}
	}
	if collection == nil {
		if modernCollection != "" {
			return nil, nil, invalidPackage("This package uses Anki's modern package format. Export it again with 'Support older Anki versions' enabled.", nil)
		}
		return nil, nil, invalidPackage("Anki archive does not contain a supported collection database", nil)
	}
	return collection, mediaManifest, nil
}

func mediaEntryCount(ctx context.Context, mediaManifest *zip.File) (int, error) {
	if mediaManifest == nil {
		return 0, nil
	}
	manifestBytes, err := readZipEntryContext(ctx, mediaManifest, 16<<20)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, ctxErr
		}
		return 0, invalidPackage("Anki media manifest is invalid", err)
	}
	var manifest map[string]string
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return 0, invalidPackage("Anki media manifest is invalid", err)
	}
	return len(manifest), nil
}

func extractCollection(ctx context.Context, collection *zip.File) (string, error) {
	collectionFile, err := os.CreateTemp("", "goi-anki-collection-*.sqlite")
	if err != nil {
		return "", fmt.Errorf("create Anki collection temporary file: %w", err)
	}
	collectionPath := collectionFile.Name()
	collectionLimit := MaxArchiveBytes
	if collection.CompressedSize64 > 0 && collection.CompressedSize64 < uint64(MaxArchiveBytes/100) {
		collectionLimit = int64(collection.CompressedSize64 * 100)
	}
	if err := copyZipEntry(ctx, collection, collectionFile, collectionLimit); err != nil {
		collectionFile.Close()
		os.Remove(collectionPath)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if errors.Is(err, zip.ErrChecksum) || errors.Is(err, errArchiveEntryTooLarge) {
			return "", invalidPackage("Anki collection data is invalid", err)
		}
		return "", fmt.Errorf("extract Anki collection: %w", err)
	}
	if err := collectionFile.Close(); err != nil {
		os.Remove(collectionPath)
		return "", fmt.Errorf("close Anki collection: %w", err)
	}
	return collectionPath, nil
}

var errArchiveEntryTooLarge = errors.New("archive entry exceeds configured limit")

func copyZipEntry(ctx context.Context, entry *zip.File, destination io.Writer, limit int64) error {
	reader, err := entry.Open()
	if err != nil {
		return err
	}
	written, copyErr := contextio.Copy(ctx, destination, io.LimitReader(reader, limit+1))
	closeErr := reader.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > limit {
		return fmt.Errorf("%w: %d bytes", errArchiveEntryTooLarge, limit)
	}
	return nil
}

func safeArchivePath(name string) bool {
	if name == "" || path.IsAbs(name) || strings.Contains(name, `\`) {
		return false
	}
	cleaned := path.Clean(name)
	return cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

func inspectCollection(ctx context.Context, path string, mediaCount int) (Preview, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return Preview{}, fmt.Errorf("open Anki SQLite collection: %w", err)
	}
	defer db.Close()

	modelFields, err := loadModelFields(ctx, db)
	if err != nil {
		return Preview{}, err
	}
	noteCount, err := loadNoteBudget(ctx, db)
	if err != nil {
		return Preview{}, err
	}
	hasModelID, err := notesHaveModelID(ctx, db)
	if err != nil {
		return Preview{}, err
	}
	fields, notes, err := loadNotes(ctx, db, noteCount, hasModelID, modelFields)
	if err != nil {
		return Preview{}, err
	}
	return Preview{
		Fields:     fields,
		Notes:      notes,
		MediaCount: mediaCount,
		ModelCount: distinctModelCount(notes),
	}, nil
}

func loadModelFields(ctx context.Context, db *sql.DB) (map[string][]string, error) {
	var modelMetadataBytes int64
	if err := db.QueryRowContext(ctx, "SELECT length(CAST(models AS BLOB)) FROM col LIMIT 1").Scan(&modelMetadataBytes); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, invalidPackage("Anki package contains an unsupported collection database", err)
	}
	if err := validateModelMetadataBudget(modelMetadataBytes); err != nil {
		return nil, err
	}
	var rawModels string
	if err := db.QueryRowContext(ctx, "SELECT models FROM col LIMIT 1").Scan(&rawModels); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, invalidPackage("Anki package contains an unsupported collection database", err)
	}
	return modelFieldMap(rawModels)
}

func loadNoteBudget(ctx context.Context, db *sql.DB) (int64, error) {
	var noteCount, noteTextBytes int64
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(length(CAST(flds AS BLOB))), 0)
		FROM notes`).Scan(&noteCount, &noteTextBytes); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, ctxErr
		}
		return 0, invalidPackage("Anki package contains an unsupported notes table", err)
	}
	if err := validateNoteBudget(noteCount, noteTextBytes); err != nil {
		return 0, err
	}
	return noteCount, nil
}

func loadNotes(
	ctx context.Context,
	db *sql.DB,
	noteCount int64,
	hasModelID bool,
	modelFields map[string][]string,
) ([]string, []Note, error) {
	selectedFields, modelColumns, err := noteLayout(hasModelID, modelFields)
	if err != nil {
		return nil, nil, err
	}
	noteQuery := "SELECT id, flds FROM notes ORDER BY id"
	if hasModelID {
		noteQuery = "SELECT id, mid, flds FROM notes ORDER BY id"
	}
	rows, err := db.QueryContext(ctx, noteQuery)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, ctxErr
		}
		return nil, nil, invalidPackage("Anki package contains an unsupported notes table", err)
	}
	defer rows.Close()
	notes := make([]Note, 0, int(noteCount))
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		note, err := scanNote(rows, hasModelID, selectedFields, modelFields, modelColumns)
		if err != nil {
			return nil, nil, err
		}
		notes = append(notes, note)
	}
	if err := rows.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, ctxErr
		}
		return nil, nil, invalidPackage("Anki package contains invalid note data", err)
	}
	if len(selectedFields) == 0 && len(notes) > 0 {
		selectedFields = make([]string, len(notes[0].Fields))
		for index := range selectedFields {
			selectedFields[index] = fmt.Sprintf("Field %d", index+1)
		}
	}
	return selectedFields, notes, nil
}

func noteLayout(hasModelID bool, modelFields map[string][]string) ([]string, map[string][]int, error) {
	if hasModelID {
		fields, columns := combinedModelLayout(modelFields)
		return fields, columns, nil
	}
	fields, err := compatibleModelFields(modelFields)
	return fields, nil, err
}

func scanNote(
	rows *sql.Rows,
	hasModelID bool,
	selectedFields []string,
	modelFields map[string][]string,
	modelColumns map[string][]int,
) (Note, error) {
	var note Note
	var rawFields string
	var err error
	if hasModelID {
		err = rows.Scan(&note.ID, &note.ModelID, &rawFields)
	} else {
		err = rows.Scan(&note.ID, &rawFields)
	}
	if err != nil {
		return Note{}, invalidPackage("Anki package contains an invalid note", err)
	}

	expectedFields := selectedFields
	knownLayout := len(selectedFields) > 0
	modelID := strconv.FormatInt(note.ModelID, 10)
	if hasModelID {
		var ok bool
		expectedFields, ok = modelFields[modelID]
		if !ok {
			return Note{}, invalidPackage(fmt.Sprintf("Anki note %d references unknown model %d", note.ID, note.ModelID), nil)
		}
		knownLayout = true
	}
	parts, err := splitNoteFields(note.ID, rawFields, expectedFields, knownLayout)
	if err != nil {
		return Note{}, err
	}
	for i := range parts {
		parts[i] = cleanField(parts[i])
	}
	if hasModelID {
		unified := make([]string, len(selectedFields))
		for source, destination := range modelColumns[modelID] {
			unified[destination] = parts[source]
		}
		parts = unified
	}
	note.Fields = parts
	return note, nil
}

func combinedModelFields(models map[string][]string) []string {
	fields, _ := combinedModelLayout(models)
	return fields
}

func combinedModelLayout(models map[string][]string) ([]string, map[string][]int) {
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	keys := make([]string, 0)
	keyIndex := make(map[string]int)
	labels := make(map[string][]string)
	columns := make(map[string][]int, len(models))
	for _, id := range ids {
		occurrences := make(map[string]int)
		columns[id] = make([]int, len(models[id]))
		for position, label := range models[id] {
			base := importedFieldRole(label)
			occurrences[base]++
			key := fmt.Sprintf("%s:%d", base, occurrences[base])
			destination, exists := keyIndex[key]
			if !exists {
				destination = len(keys)
				keyIndex[key] = destination
				keys = append(keys, key)
			}
			columns[id][position] = destination
			if !slices.Contains(labels[key], label) {
				labels[key] = append(labels[key], label)
			}
		}
	}
	combined := make([]string, len(keys))
	for index, key := range keys {
		combined[index] = strings.Join(labels[key], " / ")
	}
	return combined, columns
}

func importedFieldRole(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "expression", "word", "vocabulary", "japanese", "kanji", "front":
		return "expression"
	case "reading", "pronunciation", "kana", "furigana":
		return "pronunciation"
	case "meaning", "meanings", "definition", "definitions", "english", "gloss", "back":
		return "meaning"
	case "notes", "note":
		return "notes"
	case "example", "example sentence", "sentence":
		return "example"
	case "translation", "example translation":
		return "translation"
	case "audio", "sound":
		return "audio"
	case "picture", "image":
		return "picture"
	default:
		return "field:" + strings.ToLower(strings.TrimSpace(name))
	}
}

func distinctModelCount(notes []Note) int {
	models := make(map[int64]struct{})
	for _, note := range notes {
		if note.ModelID != 0 {
			models[note.ModelID] = struct{}{}
		}
	}
	if len(models) == 0 && len(notes) > 0 {
		return 1
	}
	return len(models)
}

func splitNoteFields(noteID int64, rawFields string, expectedFields []string, knownLayout bool) ([]string, error) {
	fieldCount := strings.Count(rawFields, "\x1f") + 1
	if knownLayout && fieldCount != len(expectedFields) {
		return nil, invalidPackage("Anki package contains notes with incompatible field layouts; import one note model at a time", nil)
	}
	if !knownLayout && fieldCount > maxNoteFieldCount {
		return nil, invalidPackage(fmt.Sprintf("Anki note %d contains more than %d fields", noteID, maxNoteFieldCount), nil)
	}
	return strings.Split(rawFields, "\x1f"), nil
}

func validateModelMetadataBudget(size int64) error {
	if size < 0 || size > maxModelMetadataBytes {
		return invalidPackage(fmt.Sprintf("Anki note-model metadata exceeds the %d byte limit", maxModelMetadataBytes), nil)
	}
	return nil
}

func validateNoteBudget(noteCount, noteTextBytes int64) error {
	if noteCount < 0 || noteCount > maxNoteCount {
		return invalidPackage(fmt.Sprintf("Anki package contains more than %d notes", maxNoteCount), nil)
	}
	if noteTextBytes < 0 || noteTextBytes > maxNoteTextBytes {
		return invalidPackage(fmt.Sprintf("Anki note text exceeds the %d byte limit", maxNoteTextBytes), nil)
	}
	return nil
}

func compatibleModelFields(models map[string][]string) ([]string, error) {
	var selected []string
	for _, fields := range models {
		if len(fields) == 0 {
			continue
		}
		if len(selected) == 0 {
			selected = slices.Clone(fields)
			continue
		}
		if !slices.Equal(selected, fields) {
			return nil, invalidPackage("Anki package contains multiple incompatible note models; import one note model at a time", nil)
		}
	}
	return selected, nil
}

func modelFieldMap(raw string) (map[string][]string, error) {
	var models map[string]struct {
		Flds []struct {
			Name string `json:"name"`
		} `json:"flds"`
	}
	if err := json.Unmarshal([]byte(raw), &models); err != nil {
		return nil, invalidPackage("Anki package contains invalid note-model metadata", err)
	}
	fieldsByID := make(map[string][]string, len(models))
	for id, model := range models {
		if len(model.Flds) == 0 {
			return nil, invalidPackage(fmt.Sprintf("Anki note model %q does not define any fields", id), nil)
		}
		if len(model.Flds) > maxNoteFieldCount {
			return nil, invalidPackage(fmt.Sprintf("Anki note model %q contains more than %d fields", id, maxNoteFieldCount), nil)
		}
		fields := make([]string, 0, len(model.Flds))
		for _, field := range model.Flds {
			fields = append(fields, field.Name)
		}
		fieldsByID[id] = fields
	}
	return fieldsByID, nil
}

func notesHaveModelID(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(notes)")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		return false, invalidPackage("Anki package contains an unsupported notes table", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, invalidPackage("Anki package contains an invalid notes table", err)
		}
		if name == "mid" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		return false, invalidPackage("Anki package contains an invalid notes table", err)
	}
	return false, nil
}
