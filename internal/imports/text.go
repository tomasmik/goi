package imports

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tomasmik/goi/internal/textnorm"
	"github.com/tomasmik/goi/internal/vocabulary"
)

type TextImportOptions struct {
	KnownElsewhere bool
	AllowDuplicate bool
}

type TextImportResult struct {
	ApplyResult
	VocabularyIDs []int64
}

type TextImportPreview struct {
	Rows       []TextImportPreviewRow
	Total      int
	WillCreate int
	Duplicates int
	Invalid    int
	Omitted    int
}

type TextImportPreviewRow struct {
	Row           int
	Expression    string
	Pronunciation string
	Meaning       string
	Outcome       string
}

const textPreviewRowLimit = 25

func (s *Store) PreviewText(ctx context.Context, value string, options TextImportOptions) (TextImportPreview, error) {
	parsed, err := parseStructuredTextRows(value)
	if err != nil {
		return TextImportPreview{}, err
	}
	records := parsed.records
	existing, err := s.existingExpressions(ctx)
	if err != nil {
		return TextImportPreview{}, err
	}
	preview := TextImportPreview{Total: len(records)}
	seen := make(map[string]struct{}, len(records))
	for index, record := range records {
		normalized := textnorm.Normalize(record["expression"])
		_, duplicateInInput := seen[normalized]
		outcome := "Will be added"
		switch {
		case normalized == "" || (!options.KnownElsewhere && (record["reading"] == "" || len(splitImportedMeanings(record["meaning"])) == 0)):
			outcome = "Missing required fields"
			preview.Invalid++
		case !options.AllowDuplicate && (existing[normalized] || duplicateInInput):
			outcome = "Duplicate — will be skipped"
			preview.Duplicates++
		default:
			preview.WillCreate++
			seen[normalized] = struct{}{}
		}
		if len(preview.Rows) < textPreviewRowLimit {
			preview.Rows = append(preview.Rows, TextImportPreviewRow{
				Row:           index + parsed.firstDataRow,
				Expression:    record["expression"],
				Pronunciation: record["reading"],
				Meaning:       record["meaning"],
				Outcome:       outcome,
			})
		}
	}
	preview.Omitted = preview.Total - len(preview.Rows)
	return preview, nil
}

func (s *Store) existingExpressions(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT normalized_expression FROM vocabulary`)
	if err != nil {
		return nil, fmt.Errorf("list existing vocabulary for import preview: %w", err)
	}
	defer rows.Close()
	expressions := make(map[string]bool)
	for rows.Next() {
		var expression string
		if err := rows.Scan(&expression); err != nil {
			return nil, fmt.Errorf("scan existing vocabulary for import preview: %w", err)
		}
		expressions[expression] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate existing vocabulary for import preview: %w", err)
	}
	return expressions, nil
}

func (s *Store) ImportText(ctx context.Context, value string, options TextImportOptions) (TextImportResult, error) {
	parsed, err := parseStructuredTextRows(value)
	if err != nil {
		return TextImportResult{}, err
	}
	records := parsed.records
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TextImportResult{}, fmt.Errorf("begin text import: %w", err)
	}
	defer tx.Rollback()

	result := TextImportResult{}
	for rowNumber, record := range records {
		input := vocabulary.CreateInput{
			Expression:         record["expression"],
			Pronunciation:      record["reading"],
			Meanings:           splitImportedMeanings(record["meaning"]),
			Notes:              record["notes"],
			SourceLabel:        record["source"],
			ExampleSentence:    record["example"],
			ExampleTranslation: record["translation"],
			AllowDuplicate:     options.AllowDuplicate,
			AllowSparse:        options.KnownElsewhere,
		}
		id, createErr := s.vocabulary.CreateInTx(ctx, tx, input)
		if createErr != nil {
			message := fmt.Sprintf("row %d: %v", rowNumber+parsed.firstDataRow, createErr)
			if errors.Is(createErr, vocabulary.ErrDuplicate) {
				result.Skipped++
			} else if errors.Is(createErr, vocabulary.ErrInvalidInput) {
				result.Failed++
			} else {
				return result, fmt.Errorf("import row %d: %w", rowNumber+parsed.firstDataRow, createErr)
			}
			result.addError(message)
			continue
		}
		if options.KnownElsewhere {
			if err := s.vocabulary.MarkKnownElsewhereInTx(ctx, tx, id); err != nil {
				return result, err
			}
		}
		result.Created++
		result.VocabularyIDs = append(result.VocabularyIDs, id)
	}
	if err := tx.Commit(); err != nil {
		return TextImportResult{}, fmt.Errorf("commit text import: %w", err)
	}
	return result, nil
}

type structuredTextRows struct {
	records      []map[string]string
	firstDataRow int
}

func parseStructuredTextRows(value string) (structuredTextRows, error) {
	value = strings.TrimSpace(strings.TrimPrefix(value, "\ufeff"))
	if value == "" {
		return structuredTextRows{}, invalidMapping("paste CSV or TSV data to import")
	}
	comma := ','
	firstLine := value
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		firstLine = value[:index]
	}
	if strings.Count(firstLine, "\t") > strings.Count(firstLine, ",") {
		comma = '\t'
	}
	reader := csv.NewReader(strings.NewReader(value))
	reader.Comma = comma
	reader.TrimLeadingSpace = comma != '\t'
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return structuredTextRows{}, invalidMapping("could not read the CSV or TSV data: " + err.Error())
	}
	if len(rows) == 0 {
		return structuredTextRows{}, invalidMapping("paste at least one word")
	}
	allowed := map[string]bool{
		"expression": true, "reading": true, "meaning": true, "notes": true,
		"example": true, "translation": true, "source": true,
	}
	headers := make([]string, len(rows[0]))
	for index, header := range rows[0] {
		headers[index] = normalizedTextHeader(header)
	}
	hasHeader := slices.Contains(headers, "expression")
	hasRecognizedHeader := false
	for _, header := range headers {
		hasRecognizedHeader = hasRecognizedHeader || allowed[header]
	}
	firstDataRow := 1
	if hasHeader {
		firstDataRow = 2
	} else if hasRecognizedHeader {
		return structuredTextRows{}, invalidMapping("the header row needs an expression column")
	} else {
		defaultHeaders := []string{"expression", "reading", "meaning", "notes", "example", "translation", "source"}
		if len(headers) > len(defaultHeaders) {
			return structuredTextRows{}, invalidMapping("headerless rows may contain at most seven columns")
		}
		headers = defaultHeaders[:len(headers)]
	}
	seenHeaders := make(map[string]bool, len(headers))
	for _, header := range headers {
		if header == "" || !allowed[header] {
			return structuredTextRows{}, invalidMapping("unknown column in header; use expression, reading, meaning, notes, example, translation, or source")
		}
		if seenHeaders[header] {
			return structuredTextRows{}, invalidMapping("the header row contains a duplicate column")
		}
		seenHeaders[header] = true
	}
	dataRows := rows
	if hasHeader {
		dataRows = rows[1:]
	}
	records := make([]map[string]string, 0, len(dataRows))
	for _, row := range dataRows {
		if len(row) > len(headers) {
			return structuredTextRows{}, invalidMapping("a data row has more columns than expected")
		}
		record := make(map[string]string, len(headers))
		for index, value := range row {
			record[headers[index]] = strings.TrimSpace(value)
		}
		if !recordIsEmpty(record) {
			records = append(records, record)
		}
	}
	if len(records) == 0 {
		return structuredTextRows{}, invalidMapping("no words were found")
	}
	if len(records) > 10_000 {
		return structuredTextRows{}, invalidMapping("import at most 10,000 words at a time")
	}
	return structuredTextRows{records: records, firstDataRow: firstDataRow}, nil
}

func recordIsEmpty(record map[string]string) bool {
	for _, value := range record {
		if value != "" {
			return false
		}
	}
	return true
}

func normalizedTextHeader(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "word", "term", "japanese":
		return "expression"
	case "pronunciation", "kana":
		return "reading"
	case "meanings", "definition", "definitions":
		return "meaning"
	case "sentence":
		return "example"
	case "example translation":
		return "translation"
	case "source label":
		return "source"
	default:
		return value
	}
}

func splitImportedMeanings(value string) []string {
	parts := strings.FieldsFunc(value, func(character rune) bool {
		return character == '|' || character == '\n'
	})
	meanings := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			meanings = append(meanings, part)
		}
	}
	return meanings
}
