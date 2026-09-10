package mining

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	internalweb "github.com/tomasmik/goi/internal/web"
)

type ListPage struct {
	Title          string
	Status         Status
	Items          []ListCapture
	Page           int
	PageCount      int
	PreviousURL    string
	NextURL        string
	CSRFToken      string
	Search         string
	Source         string
	PendingCount   int
	AcceptedCount  int
	DiscardedCount int
	Notice         string
	Error          string
	PendingURL     string
	AcceptedURL    string
	DiscardedURL   string
}

type ListCapture struct {
	Capture
	Age              string
	PositionLabel    string
	ClearMatch       bool
	SuggestedReading string
	MeaningPreview   string
	MatchCount       int
	MatchState       EnrichmentState
}

type CandidatePreview struct {
	State   EnrichmentState
	Reading string
	Meaning string
	Count   int
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	status := Status(r.URL.Query().Get("status"))
	if status == "" {
		status = StatusPending
	}
	if status != StatusPending && status != StatusAccepted && status != StatusDiscarded {
		h.renderer.RenderStatus(w, http.StatusBadRequest, "not-found.html", internalweb.NotFoundPage{
			Title:       "Invalid inbox filter",
			Heading:     "Invalid inbox filter",
			Message:     "Choose pending, accepted, or discarded captures.",
			ReturnURL:   "/mining",
			ReturnLabel: "Back to mining",
		})
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	source := strings.TrimSpace(r.URL.Query().Get("source"))
	totalCount, err := h.store.ListCountFiltered(r.Context(), status, search, source)
	if err != nil {
		internalweb.InternalError(w, r, "could not load mining inbox", err)
		return
	}
	page, pageCount := miningPage(r.URL.Query().Get("page"), totalCount)
	captures, err := h.store.ListPageFiltered(r.Context(), status, search, source, maximumCapturePageSize, (page-1)*maximumCapturePageSize)
	if err != nil {
		internalweb.InternalError(w, r, "could not load mining inbox", err)
		return
	}
	candidatePreviews := map[int64]CandidatePreview{}
	if status == StatusPending {
		candidatePreviews = h.candidatePreviews(r.Context(), captures)
	}
	items := make([]ListCapture, 0, len(captures))
	for _, capture := range captures {
		preview := candidatePreviews[capture.ID]
		items = append(items, ListCapture{
			Capture:          capture,
			Age:              formatAge(capture.CreatedAt, time.Now()),
			PositionLabel:    formatTimestamp(capture.SourcePositionMS),
			ClearMatch:       preview.State == EnrichmentReady && preview.Count == 1,
			SuggestedReading: preview.Reading,
			MeaningPreview:   preview.Meaning,
			MatchCount:       preview.Count,
			MatchState:       preview.State,
		})
	}
	var previousURL, nextURL string
	if page > 1 {
		previousURL = miningFilteredListURL(status, page-1, search, source)
	}
	if page < pageCount {
		nextURL = miningFilteredListURL(status, page+1, search, source)
	}
	counts := make(map[Status]int, 3)
	for _, captureStatus := range []Status{StatusPending, StatusAccepted, StatusDiscarded} {
		counts[captureStatus], err = h.store.ListCount(r.Context(), captureStatus)
		if err != nil {
			internalweb.InternalError(w, r, "could not load mining counts", err)
			return
		}
	}
	h.renderer.Render(w, "mining.html", ListPage{
		Title:          "Mining inbox",
		Status:         status,
		Items:          items,
		Page:           page,
		PageCount:      pageCount,
		PreviousURL:    previousURL,
		NextURL:        nextURL,
		CSRFToken:      internalweb.CSRFToken(r),
		Search:         search,
		Source:         source,
		PendingCount:   counts[StatusPending],
		AcceptedCount:  counts[StatusAccepted],
		DiscardedCount: counts[StatusDiscarded],
		Notice:         bulkMiningNotice(r),
		PendingURL:     miningFilteredListURL(StatusPending, 1, search, source),
		AcceptedURL:    miningFilteredListURL(StatusAccepted, 1, search, source),
		DiscardedURL:   miningFilteredListURL(StatusDiscarded, 1, search, source),
	})
}

func (h *Handler) candidatePreviews(ctx context.Context, captures []Capture) map[int64]CandidatePreview {
	previews := make(map[int64]CandidatePreview, len(captures))
	for _, capture := range captures {
		enrichment := lookupEnrichment(ctx, h.dictionary, capture)
		preview := CandidatePreview{State: enrichment.State, Count: len(enrichment.Candidates)}
		if len(enrichment.Candidates) > 0 {
			candidate := enrichment.Candidates[0]
			for _, choice := range enrichment.Candidates {
				if capture.SuggestedEntrySequence != nil && choice.EntrySequence == *capture.SuggestedEntrySequence {
					candidate = choice
					break
				}
			}
			preview.Reading = candidate.Reading
			meanings := candidateMeanings(candidate)
			if len(meanings) > 0 {
				preview.Meaning = meanings[0]
			}
		}
		previews[capture.ID] = preview
	}
	return previews
}

func (h *Handler) bulk(w http.ResponseWriter, r *http.Request) {
	if err := parseSmallForm(w, r); err != nil {
		h.renderer.RenderStatus(w, http.StatusBadRequest, "not-found.html", internalweb.NotFoundPage{Title: "Bulk action failed", Heading: "Bulk action failed", Message: publicError(err), ReturnURL: "/mining", ReturnLabel: "Back to mining"})
		return
	}
	ids := make([]int64, 0, len(r.Form["capture_id"]))
	for _, value := range r.Form["capture_id"] {
		id, ok := positiveInt64(value)
		if !ok {
			h.renderer.RenderStatus(w, http.StatusBadRequest, "not-found.html", internalweb.NotFoundPage{Title: "Bulk action failed", Heading: "Bulk action failed", Message: "The capture selection is invalid.", ReturnURL: "/mining", ReturnLabel: "Back to mining"})
			return
		}
		ids = append(ids, id)
	}
	action := r.FormValue("action")
	if action == "accept_ready" {
		result, err := h.bulkAccept(r.Context(), ids)
		if err != nil {
			h.renderer.RenderStatus(w, http.StatusConflict, "not-found.html", internalweb.NotFoundPage{Title: "Bulk action failed", Heading: "Bulk action failed", Message: publicError(err), ReturnURL: "/mining", ReturnLabel: "Back to mining"})
			return
		}
		query := url.Values{}
		query.Set("bulk_added", strconv.Itoa(result.Added))
		query.Set("bulk_attached", strconv.Itoa(result.Attached))
		query.Set("bulk_review", strconv.Itoa(result.NeedsReview))
		http.Redirect(w, r, "/mining?"+query.Encode(), http.StatusSeeOther)
		return
	}
	var from, to Status
	switch action {
	case "discard":
		from, to = StatusPending, StatusDiscarded
	case "restore":
		from, to = StatusDiscarded, StatusPending
	default:
		h.renderer.RenderStatus(w, http.StatusBadRequest, "not-found.html", internalweb.NotFoundPage{Title: "Bulk action failed", Heading: "Bulk action failed", Message: "That bulk action is not available.", ReturnURL: "/mining", ReturnLabel: "Back to mining"})
		return
	}
	if err := h.store.BulkTransition(r.Context(), ids, from, to); err != nil {
		h.renderer.RenderStatus(w, http.StatusConflict, "not-found.html", internalweb.NotFoundPage{Title: "Bulk action failed", Heading: "Bulk action failed", Message: publicError(err), ReturnURL: "/mining", ReturnLabel: "Back to mining"})
		return
	}
	http.Redirect(w, r, miningListURL(to, 1), http.StatusSeeOther)
}

func (h *Handler) bulkAccept(ctx context.Context, ids []int64) (BulkAcceptResult, error) {
	ids, err := validatedCaptureIDs(ids)
	if err != nil {
		return BulkAcceptResult{}, err
	}
	candidates := make(map[int64]resolvedCandidate, len(ids))
	for _, id := range ids {
		capture, err := h.store.Get(ctx, id)
		if err != nil {
			return BulkAcceptResult{}, err
		}
		if capture.Status != StatusPending {
			return BulkAcceptResult{}, validationError("some selected captures changed; reload and try again")
		}
		enrichment := lookupEnrichment(ctx, h.dictionary, capture)
		if enrichment.State != EnrichmentReady || len(enrichment.Candidates) != 1 {
			continue
		}
		candidate, err := candidateAcceptanceFor(enrichment, candidateID(enrichment.Candidates[0]))
		if err != nil {
			continue
		}
		candidates[id] = resolvedCandidate{revision: capture.Revision, candidate: candidate}
	}
	return h.store.BulkAcceptCandidates(ctx, ids, candidates)
}

func bulkMiningNotice(r *http.Request) string {
	added := boundedQueryCount(r, "bulk_added")
	attached := boundedQueryCount(r, "bulk_attached")
	remaining := boundedQueryCount(r, "bulk_review")
	if added+attached+remaining == 0 {
		return ""
	}
	parts := make([]string, 0, 3)
	if added > 0 {
		parts = append(parts, fmt.Sprintf("%d added", added))
	}
	if attached > 0 {
		parts = append(parts, fmt.Sprintf("%d attached to existing words", attached))
	}
	if remaining > 0 {
		parts = append(parts, fmt.Sprintf("%d still need a choice", remaining))
	}
	return strings.Join(parts, " · ") + "."
}

func boundedQueryCount(r *http.Request, name string) int {
	count, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || count < 0 || count > maximumCapturePageSize {
		return 0
	}
	return count
}

func miningPage(value string, total int) (int, int) {
	page := 1
	if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
		page = parsed
	}
	pageCount := 1
	if total > 0 {
		pageCount = 1 + (total-1)/maximumCapturePageSize
	}
	if page > pageCount {
		page = pageCount
	}
	return page, pageCount
}

func miningListURL(status Status, page int) string {
	return miningFilteredListURL(status, page, "", "")
}

func miningFilteredListURL(status Status, page int, search, source string) string {
	query := url.Values{}
	if status != StatusPending {
		query.Set("status", string(status))
	}
	if page > 1 {
		query.Set("page", strconv.Itoa(page))
	}
	if search != "" {
		query.Set("q", search)
	}
	if source != "" {
		query.Set("source", source)
	}
	if len(query) == 0 {
		return "/mining"
	}
	return "/mining?" + query.Encode()
}
