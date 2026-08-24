package mining

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

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
