package vocabulary

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func vocabularyPage(value string, total int) (int, int) {
	page := 1
	if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
		page = parsed
	}
	pageCount := 1
	if total > 0 {
		pageCount = 1 + (total-1)/vocabularyListPageSize
	}
	if page > pageCount {
		page = pageCount
	}
	return page, pageCount
}

func vocabularyListURL(search string, filter ListFilter, page int) string {
	return vocabularyListURLSorted(search, filter, ListSortUpdated, page)
}

func vocabularyListURLSorted(search string, filter ListFilter, sort ListSort, page int) string {
	query := url.Values{}
	if search != "" {
		query.Set("q", search)
	}
	if page > 1 {
		query.Set("page", strconv.Itoa(page))
	}
	if filter != ListFilterAll {
		query.Set("status", string(filter))
	}
	if sort = normalizeListSort(string(sort)); sort != ListSortUpdated {
		query.Set("sort", string(sort))
	}
	if len(query) == 0 {
		return "/vocabulary"
	}
	return "/vocabulary?" + query.Encode()
}

func (h *Handler) vocabularyFilterLinks(ctx context.Context, search string, active ListFilter, sort ListSort) []ListFilterLink {
	filters := []struct {
		value ListFilter
		label string
	}{
		{ListFilterAll, "All"},
		{ListFilterLearning, "Studying"},
		{ListFilterNotStarted, "Not started"},
		{ListFilterKnown, "Known elsewhere"},
		{ListFilterSuspended, "Suspended"},
		{ListFilterArchived, "Archived"},
	}
	links := make([]ListFilterLink, 0, len(filters))
	for _, filter := range filters {
		count, err := h.store.StatusCount(ctx, filter.value)
		if err != nil {
			count = 0
		}
		links = append(links, ListFilterLink{
			Label:  filter.label,
			URL:    vocabularyListURLSorted(search, filter.value, sort, 1),
			Active: filter.value == active,
			Count:  count,
		})
	}
	return links
}

func knownVocabularyNotice(r *http.Request) string {
	added := queryCount(r, "known_added")
	existing := queryCount(r, "known_existing")
	reserved := queryCount(r, "known_reserved")
	if added == 0 && existing == 0 && reserved == 0 {
		return ""
	}
	messages := make([]string, 0, 3)
	if added > 0 {
		messages = append(messages, fmt.Sprintf("%d %s added to your known vocabulary.", added, pluralWord(added, "word", "words")))
	}
	if existing > 0 {
		messages = append(messages, fmt.Sprintf("%d %s already counted as known.", existing, pluralWord(existing, "was", "were")))
	}
	if reserved > 0 {
		messages = append(messages, fmt.Sprintf("%d %s skipped because an active lesson is using it.", reserved, pluralWord(reserved, "word was", "words were")))
	}
	return strings.Join(messages, " ")
}

func queryCount(r *http.Request, name string) int {
	value, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func pluralWord(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}
