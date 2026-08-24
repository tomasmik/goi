package captureapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/tomasmik/goi/internal/coverage"
	"github.com/tomasmik/goi/internal/dictionary/jmdict"
	"github.com/tomasmik/goi/internal/examplegen"
	"github.com/tomasmik/goi/internal/media"
	"github.com/tomasmik/goi/internal/mining"
	"github.com/tomasmik/goi/internal/vocabulary"
	internalweb "github.com/tomasmik/goi/internal/web"
)

const (
	captureBodyLimit            = 64 << 10
	CaptureMediaBodyLimit int64 = 2*media.MaxUploadBytes + 64<<10
)

type CaptureService interface {
	Create(context.Context, mining.CreateInput) (mining.Capture, bool, error)
	AttachMedia(context.Context, int64, int64, string, mining.CaptureMediaInput) error
}

type CoverageAnalyzer interface {
	Analyze(context.Context, []coverage.Block) (coverage.Result, error)
}

type DictionaryLookup interface {
	Lookup(context.Context, string, string) (jmdict.Match, error)
}

type KnownVocabulary interface {
	AddKnown(context.Context, string) (vocabulary.AddKnownResult, error)
}

type Handler struct {
	tokens     *Store
	captures   CaptureService
	coverage   CoverageAnalyzer
	dictionary DictionaryLookup
	vocabulary KnownVocabulary
	translator examplegen.Translator
	trustProxy bool
}

type captureRequest struct {
	RawText                string            `json:"raw_text"`
	Expression             string            `json:"expression"`
	ContextText            string            `json:"context_text"`
	SourceKind             mining.SourceKind `json:"source_kind"`
	SourceTitle            string            `json:"source_title"`
	SourceURL              string            `json:"source_url"`
	SourcePositionMS       *int64            `json:"source_position_ms"`
	SuggestedEntrySequence *int64            `json:"suggested_entry_sequence"`
	CaptureNonce           string            `json:"capture_nonce"`
}

type captureResponse struct {
	ID        int64         `json:"id"`
	Revision  int64         `json:"revision"`
	Status    mining.Status `json:"status"`
	Replayed  bool          `json:"replayed"`
	ReviewURL string        `json:"review_url"`
}

type errorResponse struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

func NewHandler(tokens *Store, captures CaptureService, analyzer CoverageAnalyzer, dictionary DictionaryLookup, knownVocabulary KnownVocabulary, translator examplegen.Translator, trustProxy bool) *Handler {
	return &Handler{
		tokens: tokens, captures: captures, coverage: analyzer, dictionary: dictionary,
		vocabulary: knownVocabulary, translator: translator, trustProxy: trustProxy,
	}
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/api/extension/v1/status", h.status)
	r.Post("/api/extension/v1/captures", h.create)
	r.Post("/api/extension/v1/captures/{id}/media", h.attachCaptureMedia)
	r.Post("/api/extension/v1/coverage", h.analyzeCoverage)
	r.Get("/api/extension/v1/dictionary", h.lookupDictionary)
	r.Post("/api/extension/v1/known", h.markKnown)
	r.Post("/api/extension/v1/translate", h.translate)
}

type translationRequest struct {
	Text string `json:"text"`
}

type translationResponse struct {
	Translation string `json:"translation"`
	Provider    string `json:"provider"`
}

func (h *Handler) translate(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	if h.translator == nil || !h.translator.TranslationAvailable() {
		writeAPIError(w, http.StatusServiceUnavailable, "translation_unavailable", "translation is not configured")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var input *translationRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input == nil || rejectTrailingJSON(decoder) != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "request body must contain one valid JSON object")
		return
	}
	input.Text = strings.TrimSpace(input.Text)
	if input.Text == "" || utf8.RuneCountInString(input.Text) > 8000 {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_text", "text must contain between 1 and 8000 characters")
		return
	}
	result, err := h.translator.Translate(r.Context(), input.Text)
	if err != nil {
		internalweb.LogError(r, "could not translate extension text", err)
		writeAPIError(w, http.StatusBadGateway, "translation_failed", "could not translate text")
		return
	}
	provider := result.Provider
	if provider == "" {
		provider = "goi"
	}
	writeJSON(w, http.StatusOK, translationResponse{Translation: result.Text, Provider: provider})
}

type knownRequest struct {
	Expression string `json:"expression"`
}

type knownResponse struct {
	State string `json:"state"`
}

func (h *Handler) markKnown(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	if h.vocabulary == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "vocabulary_unavailable", "vocabulary is unavailable")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var input *knownRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input == nil || rejectTrailingJSON(decoder) != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "request body must contain one valid JSON object")
		return
	}

	result, err := h.vocabulary.AddKnown(r.Context(), input.Expression)
	if err != nil {
		if errors.Is(err, vocabulary.ErrInvalidInput) {
			writeAPIError(w, http.StatusUnprocessableEntity, "invalid_word", err.Error())
			return
		}
		internalweb.LogError(r, "could not mark extension word as known", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "could not mark word as known")
		return
	}

	state := "already_known"
	switch {
	case result.Added() > 0:
		state = "marked_known"
	case result.SkippedActiveLesson > 0:
		state = "in_lessons"
	}
	writeJSON(w, http.StatusOK, knownResponse{State: state})
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		OK bool `json:"ok"`
	}{OK: true})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}

	var input *captureRequest
	r.Body = http.MaxBytesReader(w, r.Body, captureBodyLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds 64 KiB")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "request body must contain one valid JSON object")
		return
	}
	if input == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "request body must contain one valid JSON object")
		return
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds 64 KiB")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "request body must contain one valid JSON object")
		return
	}

	capture, replayed, err := h.captures.Create(r.Context(), mining.CreateInput{
		RawText:                input.RawText,
		Expression:             input.Expression,
		ContextText:            input.ContextText,
		SourceKind:             input.SourceKind,
		SourceTitle:            input.SourceTitle,
		SourceURL:              input.SourceURL,
		SourcePositionMS:       input.SourcePositionMS,
		SuggestedEntrySequence: input.SuggestedEntrySequence,
		CaptureNonce:           input.CaptureNonce,
	})
	if err != nil {
		switch {
		case errors.Is(err, mining.ErrInvalidInput):
			writeAPIError(w, http.StatusUnprocessableEntity, "invalid_capture", err.Error())
		case errors.Is(err, mining.ErrNonceConflict):
			writeAPIError(w, http.StatusConflict, "nonce_conflict", "capture nonce was already used for different input")
		case errors.Is(err, mining.ErrCaptureDeleted):
			writeAPIError(w, http.StatusConflict, "capture_deleted", "capture was permanently deleted")
		default:
			internalweb.LogError(r, "could not save extension capture", err)
			writeAPIError(w, http.StatusInternalServerError, "internal_error", "could not save capture")
		}
		return
	}

	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, captureResponse{
		ID:        capture.ID,
		Revision:  capture.Revision,
		Status:    capture.Status,
		Replayed:  replayed,
		ReviewURL: "/mining/captures/" + strconv.FormatInt(capture.ID, 10),
	})
}

var errCaptureMediaTooLarge = errors.New("capture media is too large")
var errCaptureMediaType = errors.New("capture media must use multipart/form-data")

func (h *Handler) attachCaptureMedia(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_capture", "capture ID is invalid")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, CaptureMediaBodyLimit)
	nonce, expectedRevision, input, err := readCaptureMedia(r)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.Is(err, errCaptureMediaTooLarge) || errors.As(err, &maxBytesError) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "media_too_large", "each media file must be at most 10 MiB")
			return
		}
		if errors.Is(err, errCaptureMediaType) {
			writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", err.Error())
			return
		}
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_media", err.Error())
		return
	}
	if err := h.captures.AttachMedia(r.Context(), id, expectedRevision, nonce, input); err != nil {
		switch {
		case errors.Is(err, mining.ErrInvalidInput):
			writeAPIError(w, http.StatusUnprocessableEntity, "invalid_capture", err.Error())
		case errors.Is(err, mining.ErrRevisionConflict), errors.Is(err, mining.ErrInvalidTransition), errors.Is(err, sql.ErrNoRows):
			writeAPIError(w, http.StatusConflict, "capture_unavailable", "capture is no longer available for media attachment")
		default:
			internalweb.LogError(r, "could not attach extension capture media", err)
			writeAPIError(w, http.StatusInternalServerError, "internal_error", "could not save capture media")
		}
		return
	}
	writeJSON(w, http.StatusOK, struct {
		ID int64 `json:"id"`
	}{ID: id})
}

func readCaptureMedia(r *http.Request) (string, int64, mining.CaptureMediaInput, error) {
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
		return "", 0, mining.CaptureMediaInput{}, errCaptureMediaType
	}
	reader := multipart.NewReader(r.Body, parameters["boundary"])
	var nonce string
	var expectedRevision int64
	var input mining.CaptureMediaInput
	seen := make(map[string]bool, 4)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", 0, mining.CaptureMediaInput{}, fmt.Errorf("read multipart request: %w", err)
		}
		name := part.FormName()
		if name == "" || seen[name] {
			part.Close()
			return "", 0, mining.CaptureMediaInput{}, errors.New("media fields must be named and appear once")
		}
		seen[name] = true
		switch name {
		case "capture_nonce":
			if part.FileName() != "" {
				part.Close()
				return "", 0, mining.CaptureMediaInput{}, errors.New("capture_nonce must be a text field")
			}
			value, err := io.ReadAll(io.LimitReader(part, 65))
			part.Close()
			if err != nil {
				return "", 0, mining.CaptureMediaInput{}, fmt.Errorf("read capture nonce: %w", err)
			}
			if len(value) > 64 {
				return "", 0, mining.CaptureMediaInput{}, errors.New("capture nonce is invalid")
			}
			nonce = strings.TrimSpace(string(value))
		case "expected_revision":
			if part.FileName() != "" {
				part.Close()
				return "", 0, mining.CaptureMediaInput{}, errors.New("expected_revision must be a text field")
			}
			value, err := io.ReadAll(io.LimitReader(part, 21))
			part.Close()
			if err != nil {
				return "", 0, mining.CaptureMediaInput{}, fmt.Errorf("read expected revision: %w", err)
			}
			if len(value) > 20 {
				return "", 0, mining.CaptureMediaInput{}, errors.New("expected revision is invalid")
			}
			expectedRevision, err = strconv.ParseInt(strings.TrimSpace(string(value)), 10, 64)
			if err != nil || expectedRevision <= 0 {
				return "", 0, mining.CaptureMediaInput{}, errors.New("expected revision is invalid")
			}
		case "sentence_audio", "video_frame":
			filename := part.FileName()
			if filename == "" {
				part.Close()
				return "", 0, mining.CaptureMediaInput{}, fmt.Errorf("%s must be a file", name)
			}
			content, err := io.ReadAll(io.LimitReader(part, media.MaxUploadBytes+1))
			part.Close()
			if err != nil {
				return "", 0, mining.CaptureMediaInput{}, fmt.Errorf("read %s: %w", name, err)
			}
			if int64(len(content)) > media.MaxUploadBytes {
				return "", 0, mining.CaptureMediaInput{}, errCaptureMediaTooLarge
			}
			kind := media.KindAudio
			if name == "video_frame" {
				kind = media.KindImage
			}
			upload, err := media.Prepare(kind, filename, content)
			if err != nil {
				if message, ok := internalweb.UserErrorMessage(err); ok {
					return "", 0, mining.CaptureMediaInput{}, errors.New(message)
				}
				return "", 0, mining.CaptureMediaInput{}, fmt.Errorf("validate %s: %w", name, err)
			}
			if name == "sentence_audio" {
				input.SentenceAudio = &upload
			} else {
				input.VideoFrame = &upload
			}
		default:
			part.Close()
			return "", 0, mining.CaptureMediaInput{}, fmt.Errorf("unsupported media field %q", name)
		}
	}
	if nonce == "" {
		return "", 0, mining.CaptureMediaInput{}, errors.New("capture_nonce is required")
	}
	if expectedRevision == 0 {
		return "", 0, mining.CaptureMediaInput{}, errors.New("expected_revision is required")
	}
	if input.SentenceAudio == nil && input.VideoFrame == nil {
		return "", 0, mining.CaptureMediaInput{}, errors.New("at least one media file is required")
	}
	return nonce, expectedRevision, input, nil
}

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) bool {
	secureTransport := r.TLS != nil
	if !secureTransport && h.trustProxy {
		secureTransport = strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	} else if !secureTransport {
		secureTransport = remoteAllowsPlainHTTP(r.RemoteAddr)
	}
	if !secureTransport {
		writeAPIError(w, http.StatusForbidden, "secure_transport_required", "HTTPS is required for this connection")
		return false
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		unauthorized(w)
		return false
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		unauthorized(w)
		return false
	}
	if _, err := h.tokens.Authenticate(r.Context(), parts[1]); err != nil {
		if errors.Is(err, ErrUnauthorized) {
			unauthorized(w)
		} else {
			internalweb.LogError(r, "could not authenticate extension token", err)
			writeAPIError(w, http.StatusInternalServerError, "authentication_unavailable", "could not authenticate extension token")
		}
		return false
	}
	return true
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}

func remoteAllowsPlainHTTP(remoteAddress string) bool {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		host = remoteAddress
	}
	if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	address := net.ParseIP(host)
	return address != nil && (address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast())
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="goi-extension"`)
	writeAPIError(w, http.StatusUnauthorized, "unauthorized", "a valid extension token is required")
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Code: code, Error: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
