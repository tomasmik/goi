package vocabulary

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	internalweb "github.com/tomasmik/goi/internal/web"
)

const (
	KnownWordsBodyLimit    int64 = 2 << 20
	vocabularyListPageSize       = 40
)

type ListPage struct {
	Title               string
	CSRFToken           string
	Search              string
	Filter              ListFilter
	Sort                ListSort
	Filters             []ListFilterLink
	Items               []ListItem
	TotalCount          int
	KnownCount          int
	LearningCount       int
	KnownElsewhereCount int
	KnownWords          string
	Page                int
	PageCount           int
	PreviousURL         string
	NextURL             string
	Notice              string
	Error               string
}

type ListFilterLink struct {
	Label  string
	URL    string
	Active bool
	Count  int
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	h.renderList(w, r, http.StatusOK, "", "")
}

func (h *Handler) renderList(w http.ResponseWriter, r *http.Request, status int, knownWords, message string) {
	search := r.URL.Query().Get("q")
	filter := normalizeListFilter(r.URL.Query().Get("status"))
	sort := normalizeListSort(r.URL.Query().Get("sort"))
	totalCount, err := h.store.ListCountFiltered(r.Context(), search, filter)
	if err != nil {
		internalweb.InternalError(w, r, "could not load vocabulary", err)
		return
	}
	page, pageCount := vocabularyPage(r.URL.Query().Get("page"), totalCount)
	items, err := h.store.ListPageSorted(r.Context(), search, filter, sort, vocabularyListPageSize, (page-1)*vocabularyListPageSize)
	if err != nil {
		internalweb.InternalError(w, r, "could not load vocabulary", err)
		return
	}
	knownCount, err := h.store.KnownCount(r.Context())
	if err != nil {
		internalweb.InternalError(w, r, "could not load known vocabulary count", err)
		return
	}
	learningCount, err := h.store.StatusCount(r.Context(), ListFilterLearning)
	if err != nil {
		internalweb.InternalError(w, r, "could not load learning vocabulary count", err)
		return
	}
	knownElsewhereCount, err := h.store.StatusCount(r.Context(), ListFilterKnown)
	if err != nil {
		internalweb.InternalError(w, r, "could not load known-elsewhere count", err)
		return
	}
	var previousURL, nextURL string
	if page > 1 {
		previousURL = vocabularyListURLSorted(search, filter, sort, page-1)
	}
	if page < pageCount {
		nextURL = vocabularyListURLSorted(search, filter, sort, page+1)
	}
	h.renderer.RenderStatus(w, status, "vocabulary.html", ListPage{
		Title:               "Vocabulary",
		CSRFToken:           internalweb.CSRFToken(r),
		Search:              search,
		Filter:              filter,
		Sort:                sort,
		Filters:             h.vocabularyFilterLinks(r.Context(), search, filter, sort),
		Items:               items,
		TotalCount:          totalCount,
		KnownCount:          knownCount,
		LearningCount:       learningCount,
		KnownElsewhereCount: knownElsewhereCount,
		KnownWords:          knownWords,
		Page:                page,
		PageCount:           pageCount,
		PreviousURL:         previousURL,
		NextURL:             nextURL,
		Notice:              knownVocabularyNotice(r),
		Error:               message,
	})
}

func (h *Handler) exportKnown(w http.ResponseWriter, r *http.Request) {
	expressions, err := h.store.KnownExpressions(r.Context())
	if err != nil {
		internalweb.InternalError(w, r, "could not export known vocabulary", err)
		return
	}

	var contentType, filename string
	var body []byte
	switch r.URL.Query().Get("format") {
	case "json":
		body, err = json.Marshal(expressions)
		contentType = "application/json; charset=utf-8"
		filename = "goi-known-words.json"
	case "comma":
		body = []byte(strings.Join(expressions, ", "))
		contentType = "text/plain; charset=utf-8"
		filename = "goi-known-words.txt"
	default:
		http.Error(w, "Choose JSON or comma-separated format.", http.StatusBadRequest)
		return
	}
	if err != nil {
		internalweb.InternalError(w, r, "could not encode known vocabulary", err)
		return
	}
	body = append(body, '\n')
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (h *Handler) addKnown(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, KnownWordsBodyLimit)
	if err := r.ParseForm(); err != nil {
		h.renderList(w, r, http.StatusBadRequest, "", "The list is too large or invalid.")
		return
	}
	words := r.FormValue("known_words")
	result, err := h.store.AddKnown(r.Context(), words)
	if err != nil {
		if message, ok := internalweb.UserErrorMessage(err); ok {
			h.renderList(w, r, http.StatusUnprocessableEntity, words, message)
			return
		}
		internalweb.InternalError(w, r, "could not add known vocabulary", err)
		return
	}
	location := fmt.Sprintf(
		"/vocabulary?known_added=%d&known_existing=%d&known_reserved=%d",
		result.Added(), result.AlreadyKnown, result.SkippedActiveLesson,
	)
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (h *Handler) previewKnown(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, KnownWordsBodyLimit)
	if err := r.ParseForm(); err != nil {
		h.writeKnownPreviewError(w, http.StatusBadRequest, "The list is too large or invalid.")
		return
	}
	result, err := h.store.PreviewKnown(r.Context(), r.FormValue("known_words"))
	if err != nil {
		if message, ok := internalweb.UserErrorMessage(err); ok {
			h.writeKnownPreviewError(w, http.StatusUnprocessableEntity, message)
			return
		}
		internalweb.InternalError(w, r, "could not preview known vocabulary", err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(struct {
		Created             int `json:"created"`
		MarkedKnown         int `json:"markedKnown"`
		AlreadyKnown        int `json:"alreadyKnown"`
		SkippedActiveLesson int `json:"skippedActiveLesson"`
	}{result.Created, result.MarkedKnown, result.AlreadyKnown, result.SkippedActiveLesson})
}

func (h *Handler) writeKnownPreviewError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{message})
}

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
