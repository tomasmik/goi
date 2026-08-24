package vocabulary

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tomasmik/goi/internal/kana"
	"github.com/tomasmik/goi/internal/media"
	"github.com/tomasmik/goi/internal/textnorm"
)

func validateInput(input CreateInput) (validatedInput, error) {
	return validateInputMode(input, false)
}

func validateInputForUpdate(input CreateInput, allowSparse bool) (validatedInput, error) {
	return validateInputMode(input, allowSparse)
}

func validateInputMode(input CreateInput, allowSparse bool) (validatedInput, error) {
	expression, err := cleanInputText(input.Expression, maxExpressionRunes, "expression", false)
	if err != nil {
		return validatedInput{}, err
	}
	pronunciation, err := cleanInputText(input.Pronunciation, maxPronunciationRunes, "pronunciation", false)
	if err != nil {
		return validatedInput{}, err
	}
	meaningsText, err := cleanInputText(strings.Join(input.Meanings, "\n"), maxMeaningsRunes, "meanings", true)
	if err != nil {
		return validatedInput{}, err
	}
	notes, err := cleanInputText(input.Notes, maxNotesRunes, "notes", true)
	if err != nil {
		return validatedInput{}, err
	}
	sourceLabel, err := cleanInputText(input.SourceLabel, maxSourceLabelRunes, "source label", false)
	if err != nil {
		return validatedInput{}, err
	}
	validated := validatedInput{
		expression:    expression,
		meanings:      cleanLines(strings.Split(meaningsText, "\n")),
		notes:         notes,
		sourceLabel:   sourceLabel,
		audio:         input.Audio,
		picture:       input.Picture,
		removeAudio:   input.RemoveAudio,
		removePicture: input.RemovePicture,
	}
	if validated.expression == "" {
		return validatedInput{}, validationError("expression is required")
	}
	if pronunciation == "" && !allowSparse {
		return validatedInput{}, validationError("pronunciation is required")
	}
	if len(validated.meanings) == 0 && !allowSparse {
		return validatedInput{}, validationError("at least one meaning is required")
	}
	if pronunciation != "" {
		canonical, err := kana.Convert(pronunciation)
		if err != nil {
			return validatedInput{}, validationError(fmt.Sprintf("pronunciation: %v", err))
		}
		validated.pronunciation = canonical
	}
	if validated.audio != nil && validated.audio.Kind != media.KindAudio {
		return validatedInput{}, validationError("audio upload has the wrong media kind")
	}
	if validated.picture != nil && validated.picture.Kind != media.KindImage {
		return validatedInput{}, validationError("picture upload has the wrong media kind")
	}
	if validated.audio != nil && validated.removeAudio {
		return validatedInput{}, validationError("choose either a replacement audio file or remove the current audio")
	}
	if validated.picture != nil && validated.removePicture {
		return validatedInput{}, validationError("choose either a replacement image or remove the current image")
	}
	return validated, nil
}

func cleanInputText(value string, maxRunes int, name string, multiline bool) (string, error) {
	if !utf8.ValidString(value) {
		return "", validationError(name + " must be valid UTF-8")
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.TrimSpace(value)
	for _, character := range value {
		if !unicode.IsControl(character) {
			continue
		}
		if multiline && (character == '\n' || character == '\t') {
			continue
		}
		return "", validationError(name + " contains a control character")
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return "", validationError(fmt.Sprintf("%s must be at most %d characters", name, maxRunes))
	}
	return value, nil
}

func insertVocabularyText(ctx context.Context, tx *sql.Tx, id int64, input validatedInput) error {
	for index, meaning := range input.meanings {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO meanings (vocabulary_id, position, text, normalized_text)
			VALUES (?, ?, ?, ?)`, id, index, meaning, textnorm.Normalize(meaning)); err != nil {
			return fmt.Errorf("insert meaning: %w", err)
		}
	}
	return nil
}

func normalizedPronunciation(value string) string {
	if value == "" {
		return ""
	}
	return kana.ToHiragana(value)
}

func cleanLines(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return cleaned
}
