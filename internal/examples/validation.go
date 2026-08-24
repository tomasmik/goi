package examples

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxSentenceRunes      = 2000
	maxTranslationRunes   = 2000
	maxTargetSurfaceRunes = 256
	maxSourceTitleRunes   = 300
	maxSourceURLBytes     = 2048
	maxProvenanceRunes    = 200
)

type validatedInput struct {
	miningCaptureID  *int64
	origin           Origin
	sentence         string
	translation      string
	targetSurface    string
	sourceTitle      string
	sourceURL        string
	sourcePositionMS *int64
	provider         string
	model            string
}

func validateInput(input Input) (validatedInput, error) {
	origin := input.Origin
	if origin == "" {
		origin = OriginManual
	}
	if !validOrigin(origin) {
		return validatedInput{}, validationError("example origin is invalid")
	}
	if origin == OriginMined && input.MiningCaptureID == nil {
		return validatedInput{}, validationError("mined examples require a capture")
	}
	if origin != OriginMined && input.MiningCaptureID != nil {
		return validatedInput{}, validationError("only mined examples can reference a capture")
	}
	if input.MiningCaptureID != nil && *input.MiningCaptureID <= 0 {
		return validatedInput{}, validationError("mining capture is invalid")
	}

	sentence, err := cleanText(input.Sentence, maxSentenceRunes, "sentence")
	if err != nil {
		return validatedInput{}, err
	}
	if sentence == "" {
		return validatedInput{}, validationError("sentence is required")
	}
	translation, err := cleanText(input.Translation, maxTranslationRunes, "translation")
	if err != nil {
		return validatedInput{}, err
	}
	targetSurface, err := cleanText(input.TargetSurface, maxTargetSurfaceRunes, "target surface")
	if err != nil {
		return validatedInput{}, err
	}
	sourceTitle, err := cleanText(input.SourceTitle, maxSourceTitleRunes, "source title")
	if err != nil {
		return validatedInput{}, err
	}
	sourceURL, err := cleanSourceURL(input.SourceURL)
	if err != nil {
		return validatedInput{}, err
	}
	if input.SourcePositionMS != nil && *input.SourcePositionMS < 0 {
		return validatedInput{}, validationError("source position cannot be negative")
	}
	provider, err := cleanText(input.Provider, maxProvenanceRunes, "provider")
	if err != nil {
		return validatedInput{}, err
	}
	model, err := cleanText(input.Model, maxProvenanceRunes, "model")
	if err != nil {
		return validatedInput{}, err
	}

	return validatedInput{
		miningCaptureID:  cloneInt64(input.MiningCaptureID),
		origin:           origin,
		sentence:         sentence,
		translation:      translation,
		targetSurface:    targetSurface,
		sourceTitle:      sourceTitle,
		sourceURL:        sourceURL,
		sourcePositionMS: cloneInt64(input.SourcePositionMS),
		provider:         provider,
		model:            model,
	}, nil
}

func validOrigin(origin Origin) bool {
	switch origin {
	case OriginManual, OriginMined, OriginGenerated:
		return true
	default:
		return false
	}
}

func cleanText(value string, maxRunes int, name string) (string, error) {
	if !utf8.ValidString(value) {
		return "", validationError(name + " must be valid UTF-8")
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.TrimSpace(value)
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\t' {
			return "", validationError(name + " contains a control character")
		}
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return "", validationError(fmt.Sprintf("%s must be at most %d characters", name, maxRunes))
	}
	return value, nil
}

func cleanSourceURL(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", validationError("source URL must be valid UTF-8")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > maxSourceURLBytes {
		return "", validationError(fmt.Sprintf("source URL must be at most %d bytes", maxSourceURLBytes))
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", validationError("source URL must use http or https")
	}
	parsed.User = nil
	return parsed.String(), nil
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
