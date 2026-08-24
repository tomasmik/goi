package captureapi

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"unicode/utf8"

	"github.com/tomasmik/goi/internal/coverage"
	internalweb "github.com/tomasmik/goi/internal/web"
)

const (
	CoverageBodyLimit      = 2 << 20
	coverageBlockLimit     = 1000
	coverageTextRuneLimit  = 200_000
	coverageBlockRuneLimit = 20_000
)

type coverageRequest struct {
	Blocks []coverage.Block `json:"blocks"`
}

func (h *Handler) analyzeCoverage(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	if h.coverage == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "coverage_unavailable", "coverage analysis is unavailable")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, CoverageBodyLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request *coverageRequest
	if err := decoder.Decode(&request); err != nil {
		writeCoverageDecodeError(w, err)
		return
	}
	if request == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "request body must contain one valid JSON object")
		return
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		writeCoverageDecodeError(w, err)
		return
	}
	if message := validateCoverageBlocks(request.Blocks); message != "" {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_coverage", message)
		return
	}

	result, err := h.coverage.Analyze(r.Context(), request.Blocks)
	if err != nil {
		internalweb.LogError(r, "could not analyze extension coverage", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "could not analyze text")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeCoverageDecodeError(w http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds 2 MiB")
		return
	}
	writeAPIError(w, http.StatusBadRequest, "invalid_json", "request body must contain one valid JSON object")
}

func validateCoverageBlocks(blocks []coverage.Block) string {
	if len(blocks) == 0 {
		return "include at least one text block"
	}
	if len(blocks) > coverageBlockLimit {
		return "include at most 1000 text blocks"
	}
	seenIDs := make(map[int]struct{}, len(blocks))
	totalRunes := 0
	for _, block := range blocks {
		if block.ID <= 0 {
			return "text block IDs must be positive"
		}
		if _, duplicate := seenIDs[block.ID]; duplicate {
			return "text block IDs must be unique"
		}
		seenIDs[block.ID] = struct{}{}
		if !utf8.ValidString(block.Text) {
			return "text blocks must contain valid UTF-8"
		}
		blockRunes := utf8.RuneCountInString(block.Text)
		if blockRunes > coverageBlockRuneLimit {
			return "each text block must be at most 20000 characters"
		}
		totalRunes += blockRunes
		if totalRunes > coverageTextRuneLimit {
			return "text blocks must contain at most 200000 characters in total"
		}
	}
	return ""
}
