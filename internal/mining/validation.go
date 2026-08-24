package mining

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tomasmik/goi/internal/textnorm"
)

const (
	maxExpressionRunes = 256
	maxContextRunes    = 2000
	maxTitleRunes      = 300
	maxURLBytes        = 2048
)

type validatedCapture struct {
	rawText                string
	expression             string
	normalizedExpression   string
	contextText            string
	sourceKind             SourceKind
	sourceTitle            string
	sourceURL              string
	sourcePositionMS       *int64
	suggestedEntrySequence *int64
	captureNonce           string
}

func validateCreate(input CreateInput) (validatedCapture, error) {
	rawText, err := cleanText(input.RawText, maxExpressionRunes, "captured text")
	if err != nil {
		return validatedCapture{}, err
	}
	expression, err := cleanText(input.Expression, maxExpressionRunes, "expression")
	if err != nil {
		return validatedCapture{}, err
	}
	if rawText == "" {
		rawText = expression
	}
	if expression == "" {
		expression = rawText
	}
	if expression == "" {
		return validatedCapture{}, validationError("expression is required")
	}
	if rawText == "" {
		return validatedCapture{}, validationError("captured text is required")
	}
	common, err := validateEditable(UpdateInput{
		Expression:       expression,
		ContextText:      input.ContextText,
		SourceKind:       input.SourceKind,
		SourceTitle:      input.SourceTitle,
		SourceURL:        input.SourceURL,
		SourcePositionMS: input.SourcePositionMS,
	})
	if err != nil {
		return validatedCapture{}, err
	}
	nonce, err := validateNonce(input.CaptureNonce)
	if err != nil {
		return validatedCapture{}, err
	}
	common.rawText = rawText
	if input.SuggestedEntrySequence != nil && *input.SuggestedEntrySequence <= 0 {
		return validatedCapture{}, validationError("suggested dictionary entry is invalid")
	}
	common.suggestedEntrySequence = cloneInt64(input.SuggestedEntrySequence)
	common.captureNonce = nonce
	return common, nil
}

func validateUpdate(input UpdateInput) (validatedCapture, error) {
	return validateEditable(input)
}

func validateEditable(input UpdateInput) (validatedCapture, error) {
	expression, err := cleanText(input.Expression, maxExpressionRunes, "expression")
	if err != nil {
		return validatedCapture{}, err
	}
	if expression == "" {
		return validatedCapture{}, validationError("expression is required")
	}
	contextText, err := cleanText(input.ContextText, maxContextRunes, "context")
	if err != nil {
		return validatedCapture{}, err
	}
	sourceTitle, err := cleanText(input.SourceTitle, maxTitleRunes, "source title")
	if err != nil {
		return validatedCapture{}, err
	}
	sourceKind := input.SourceKind
	if sourceKind == "" {
		sourceKind = SourceManual
	}
	if !validSourceKind(sourceKind) {
		return validatedCapture{}, validationError("source kind is invalid")
	}
	sourceURL, err := cleanSourceURL(input.SourceURL)
	if err != nil {
		return validatedCapture{}, err
	}
	if input.SourcePositionMS != nil && *input.SourcePositionMS < 0 {
		return validatedCapture{}, validationError("source position cannot be negative")
	}
	return validatedCapture{
		expression:           expression,
		normalizedExpression: textnorm.Normalize(expression),
		contextText:          contextText,
		sourceKind:           sourceKind,
		sourceTitle:          sourceTitle,
		sourceURL:            sourceURL,
		sourcePositionMS:     cloneInt64(input.SourcePositionMS),
	}, nil
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
	if len(value) > maxURLBytes {
		return "", validationError(fmt.Sprintf("source URL must be at most %d bytes", maxURLBytes))
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", validationError("source URL must use http or https")
	}
	parsed.User = nil
	return parsed.String(), nil
}

func validateNonce(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 16 {
		return "", validationError("capture nonce must be a 128-bit hexadecimal value")
	}
	return value, nil
}

func validSourceKind(value SourceKind) bool {
	switch value {
	case SourceManual, SourceWeb, SourceVideo, SourceEbook, SourceOther:
		return true
	default:
		return false
	}
}

func requestHash(input validatedCapture) string {
	hash := sha256.New()
	fields := []string{
		input.rawText,
		input.expression,
		input.contextText,
		string(input.sourceKind),
		input.sourceTitle,
		input.sourceURL,
	}
	if input.sourcePositionMS == nil {
		fields = append(fields, "-")
	} else {
		fields = append(fields, strconv.FormatInt(*input.sourcePositionMS, 10))
	}
	if input.suggestedEntrySequence == nil {
		fields = append(fields, "-")
	} else {
		fields = append(fields, strconv.FormatInt(*input.suggestedEntrySequence, 10))
	}
	for _, field := range fields {
		hash.Write([]byte(strconv.Itoa(len(field))))
		hash.Write([]byte{':'})
		hash.Write([]byte(field))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
