package mining

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/vocabulary"
)

type captureForm struct {
	expression            string
	contextText           string
	sourceKind            SourceKind
	sourceTitle           string
	sourceURL             string
	sourcePositionSeconds string
	sourcePositionMS      *int64
	captureNonce          string
	fieldsParsed          bool
	directPronunciation   string
	directMeanings        string
	directNotes           string
}

type detailForm struct {
	captureForm
	revision               int64
	pronunciation          string
	meanings               string
	notes                  string
	exampleSentence        string
	exampleTranslation     string
	exampleTarget          string
	candidateID            int64
	submitted              bool
	captureFieldsSubmitted bool
}

func readCaptureForm(w http.ResponseWriter, r *http.Request) (captureForm, error) {
	if err := parseSmallForm(w, r); err != nil {
		return captureForm{}, err
	}
	form := captureForm{
		expression:            cleanLineEndings(r.FormValue("expression")),
		contextText:           cleanLineEndings(r.FormValue("context_text")),
		sourceKind:            SourceKind(r.FormValue("source_kind")),
		sourceTitle:           cleanLineEndings(r.FormValue("source_title")),
		sourceURL:             strings.TrimSpace(r.FormValue("source_url")),
		sourcePositionSeconds: strings.TrimSpace(r.FormValue("source_position_seconds")),
		captureNonce:          strings.TrimSpace(r.FormValue("capture_nonce")),
		fieldsParsed:          true,
		directPronunciation:   cleanLineEndings(r.FormValue("pronunciation")),
		directMeanings:        cleanLineEndings(r.FormValue("meanings")),
		directNotes:           cleanLineEndings(r.FormValue("notes")),
	}
	nonce, err := validateNonce(form.captureNonce)
	if err != nil {
		form.captureNonce = ""
		return form, err
	}
	form.captureNonce = nonce
	if form.sourceKind == "" {
		form.sourceKind = SourceManual
	}
	position, err := parsePosition(form.sourcePositionSeconds)
	if err != nil {
		return form, err
	}
	form.sourcePositionMS = position
	validated, err := validateUpdate(UpdateInput{
		Expression:       form.expression,
		ContextText:      form.contextText,
		SourceKind:       form.sourceKind,
		SourceTitle:      form.sourceTitle,
		SourceURL:        form.sourceURL,
		SourcePositionMS: form.sourcePositionMS,
	})
	if err != nil {
		return form, err
	}
	form.expression = validated.expression
	form.contextText = validated.contextText
	form.sourceKind = validated.sourceKind
	form.sourceTitle = validated.sourceTitle
	form.sourceURL = validated.sourceURL
	form.sourcePositionMS = validated.sourcePositionMS
	for _, field := range []struct {
		name  string
		value string
		max   int
	}{
		{name: "pronunciation", value: form.directPronunciation, max: 256},
		{name: "meanings", value: form.directMeanings, max: 2000},
		{name: "notes", value: form.directNotes, max: 2000},
	} {
		if err := validateText(field.name, field.value, field.max); err != nil {
			return form, err
		}
	}
	return form, nil
}

func readDetailForm(w http.ResponseWriter, r *http.Request) (detailForm, error) {
	capture, err := readCaptureForm(w, r)
	form := detailForm{captureForm: capture, captureFieldsSubmitted: capture.fieldsParsed}
	form.revision, _ = positiveInt64(r.FormValue("revision"))
	if err != nil {
		return form, err
	}
	return readVocabularyFields(r, form)
}

func readAcceptForm(w http.ResponseWriter, r *http.Request) (detailForm, error) {
	if err := parseCardForm(w, r); err != nil {
		return detailForm{}, err
	}
	form := detailForm{submitted: true}
	if candidate := strings.TrimSpace(r.FormValue("candidate_id")); candidate != "" {
		var ok bool
		form.candidateID, ok = positiveInt64(candidate)
		if !ok {
			return form, handlerValidationError("invalid dictionary candidate")
		}
	}
	return readVocabularyFields(r, form)
}

func parseCardForm(w http.ResponseWriter, r *http.Request) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return parseSmallForm(w, r)
	}
	r.Body = http.MaxBytesReader(w, r.Body, CaptureMediaBodyLimit)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		return handlerValidationError("the card form is too large or invalid")
	}
	return nil
}

func removeMultipartFiles(r *http.Request) {
	if r.MultipartForm != nil {
		r.MultipartForm.RemoveAll()
	}
}

func readVocabularyFields(r *http.Request, form detailForm) (detailForm, error) {
	form.revision, _ = positiveInt64(r.FormValue("revision"))
	form.pronunciation = cleanLineEndings(r.FormValue("pronunciation"))
	form.meanings = cleanLineEndings(r.FormValue("meanings"))
	form.notes = cleanLineEndings(r.FormValue("notes"))
	form.exampleSentence = cleanLineEndings(r.FormValue("example_sentence"))
	form.exampleTranslation = cleanLineEndings(r.FormValue("example_translation"))
	form.exampleTarget = cleanLineEndings(r.FormValue("example_target"))
	if form.revision == 0 {
		return form, handlerValidationError("invalid capture revision")
	}
	for _, field := range []struct {
		name  string
		value string
		max   int
	}{
		{name: "pronunciation", value: form.pronunciation, max: 256},
		{name: "meanings", value: form.meanings, max: 2000},
		{name: "notes", value: form.notes, max: 2000},
		{name: "example sentence", value: form.exampleSentence, max: 2000},
		{name: "example translation", value: form.exampleTranslation, max: 2000},
		{name: "example target", value: form.exampleTarget, max: 256},
	} {
		if err := validateText(field.name, field.value, field.max); err != nil {
			return form, err
		}
	}
	return form, nil
}

func parseSmallForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, captureBodyLimit)
	if err := r.ParseForm(); err != nil {
		return handlerValidationError("the form is too large or invalid")
	}
	return nil
}

func validateText(name, value string, maxRunes int) error {
	if !utf8.ValidString(value) {
		return handlerValidationError(fmt.Sprintf("%s must be valid UTF-8", name))
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return handlerValidationError(fmt.Sprintf("%s must be at most %s characters", name, strconv.Itoa(maxRunes)))
	}
	for _, character := range value {
		if character == '\n' || character == '\t' {
			continue
		}
		if character == 0 || unicode.IsControl(character) {
			return handlerValidationError(fmt.Sprintf("%s contains an unsupported control character", name))
		}
	}
	return nil
}

func parsePosition(value string) (*int64, error) {
	if value == "" {
		return nil, nil
	}
	if strings.Contains(value, ":") {
		parts := strings.Split(value, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return nil, handlerValidationError("video time must look like 12:34 or 1:12:34")
		}
		var total float64
		for _, part := range parts {
			component, err := strconv.ParseFloat(part, 64)
			if err != nil || component < 0 || math.IsNaN(component) || math.IsInf(component, 0) {
				return nil, handlerValidationError("video time must look like 12:34 or 1:12:34")
			}
			total = total*60 + component
		}
		milliseconds := int64(math.Round(total * 1000))
		return &milliseconds, nil
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > float64(math.MaxInt64)/1000 {
		return nil, handlerValidationError("video time must look like 12:34 or be a number of seconds")
	}
	milliseconds := int64(math.Round(seconds * 1000))
	return &milliseconds, nil
}

func formatTimestamp(milliseconds *int64) string {
	if milliseconds == nil {
		return ""
	}
	totalSeconds := *milliseconds / 1000
	hours := totalSeconds / 3600
	minutes := totalSeconds % 3600 / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}

func formatAge(createdAt, now time.Time) string {
	age := now.Sub(createdAt)
	if age < time.Minute {
		return "just now"
	}
	if age < time.Hour {
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	}
	if age < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(age.Hours()/24))
}

func newCaptureNonce() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generate capture nonce: %w", err)
	}
	return hex.EncodeToString(nonce[:]), nil
}

func captureID(r *http.Request) (int64, bool) {
	return positiveInt64(chi.URLParam(r, "id"))
}

func positiveInt64(value string) (int64, bool) {
	number, err := strconv.ParseInt(value, 10, 64)
	return number, err == nil && number > 0
}

func capturePath(id int64) string {
	return "/mining/captures/" + strconv.FormatInt(id, 10)
}

func responseStatus(err error) int {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return http.StatusNotFound
	case errors.Is(err, ErrNonceConflict),
		errors.Is(err, ErrCaptureDeleted),
		errors.Is(err, ErrRevisionConflict),
		errors.Is(err, ErrInvalidTransition),
		errors.Is(err, ErrStaleEnrichment),
		errors.Is(err, vocabulary.ErrDuplicate):
		return http.StatusConflict
	case errors.Is(err, ErrInvalidInput), errors.Is(err, vocabulary.ErrInvalidInput):
		return http.StatusUnprocessableEntity
	default:
		var validationErr handlerValidationError
		if errors.As(err, &validationErr) {
			return http.StatusUnprocessableEntity
		}
		return http.StatusInternalServerError
	}
}

func publicError(err error) string {
	if errors.Is(err, ErrCaptureDeleted) {
		return "This capture was deleted. Create a new capture to add it again."
	}
	if errors.Is(err, ErrRevisionConflict) {
		return "This capture changed in another tab. The latest version is shown; review it before trying again."
	}
	if responseStatus(err) == http.StatusInternalServerError {
		return "could not complete the request"
	}
	return err.Error()
}

func cleanLineEndings(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
}

func validPublicOrigin(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func bookmarklet(origin string) template.URL {
	if origin == "" {
		return ""
	}
	script := strings.Replace(bookmarkletScript, "__GOI_ORIGIN__", strconv.QuoteToASCII(origin), 1)
	return template.URL("javascript:" + script)
}
