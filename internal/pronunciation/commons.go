package pronunciation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/tomasmik/goi/internal/media"
)

const (
	commonsAPI         = "https://commons.wikimedia.org/w/api.php"
	commonsUploadHost  = "upload.wikimedia.org"
	commonsSourceName  = "Lingua Libre"
	commonsLicenseName = "CC0"
	commonsLicenseURL  = "https://creativecommons.org/publicdomain/zero/1.0/"
	maximumResults     = 6
	userAgent          = "Goi/1.0 (self-hosted Japanese study app)"
)

var ErrRecordingUnavailable = errors.New("pronunciation recording is unavailable")

type Recording struct {
	ID          int64
	Label       string
	SourceName  string
	SourceURL   string
	LicenseName string
	LicenseURL  string
}

type Commons struct {
	client     *http.Client
	apiURL     string
	uploadHost string
}

func NewCommons(client *http.Client) *Commons {
	if client == nil {
		client = defaultClient()
	}
	return &Commons{client: client, apiURL: commonsAPI, uploadHost: commonsUploadHost}
}

func (c *Commons) Search(ctx context.Context, expression, reading string) ([]Recording, error) {
	terms := uniqueTerms(expression, reading)
	found := make(map[int64]Recording)
	for _, term := range terms {
		pages, err := c.search(ctx, term)
		if err != nil {
			return nil, err
		}
		for _, page := range pages {
			recording, ok := c.recording(page, terms)
			if ok {
				found[recording.ID] = recording
			}
		}
	}

	results := make([]Recording, 0, len(found))
	for _, recording := range found {
		results = append(results, recording)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ID < results[j].ID })
	if len(results) > maximumResults {
		results = results[:maximumResults]
	}
	return results, nil
}

func (c *Commons) Download(ctx context.Context, pageID int64, expression, reading string) (media.Upload, error) {
	if pageID <= 0 {
		return media.Upload{}, ErrRecordingUnavailable
	}
	page, err := c.page(ctx, pageID)
	if err != nil {
		return media.Upload{}, err
	}
	_, ok := c.recording(page, uniqueTerms(expression, reading))
	if !ok {
		return media.Upload{}, ErrRecordingUnavailable
	}

	audioURL, err := url.Parse(page.ImageInfo[0].URL)
	if err != nil || audioURL.Scheme != "https" || !strings.EqualFold(audioURL.Hostname(), c.uploadHost) {
		return media.Upload{}, ErrRecordingUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, audioURL.String(), nil)
	if err != nil {
		return media.Upload{}, fmt.Errorf("create pronunciation download request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	response, err := c.client.Do(req)
	if err != nil {
		return media.Upload{}, fmt.Errorf("download pronunciation recording: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return media.Upload{}, fmt.Errorf("download pronunciation recording: server returned %s", response.Status)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, media.MaxUploadBytes+1))
	if err != nil {
		return media.Upload{}, fmt.Errorf("read pronunciation recording: %w", err)
	}
	if int64(len(content)) > media.MaxUploadBytes {
		return media.Upload{}, fmt.Errorf("pronunciation recording exceeds the %d byte limit", media.MaxUploadBytes)
	}
	upload, err := media.Prepare(media.KindAudio, path.Base(audioURL.Path), content)
	if err != nil {
		return media.Upload{}, fmt.Errorf("prepare pronunciation recording: %w", err)
	}
	upload.SourceName = commonsSourceName
	upload.SourceURL = page.ImageInfo[0].DescriptionURL
	upload.LicenseName = commonsLicenseName
	upload.LicenseURL = commonsLicenseURL
	return upload, nil
}

func (c *Commons) search(ctx context.Context, term string) ([]commonsPage, error) {
	query := url.Values{
		"action":        {"query"},
		"format":        {"json"},
		"formatversion": {"2"},
		"generator":     {"search"},
		"gsrnamespace":  {"6"},
		"gsrlimit":      {"20"},
		"gsrsearch":     {`intitle:"` + term + `" incategory:"Lingua Libre pronunciation-jpn"`},
		"prop":          {"imageinfo"},
		"iiprop":        {"url|mime|extmetadata"},
	}
	var response commonsResponse
	if err := c.getJSON(ctx, query, &response); err != nil {
		return nil, err
	}
	return response.Query.Pages, nil
}

func (c *Commons) page(ctx context.Context, pageID int64) (commonsPage, error) {
	query := url.Values{
		"action":        {"query"},
		"format":        {"json"},
		"formatversion": {"2"},
		"pageids":       {strconv.FormatInt(pageID, 10)},
		"prop":          {"imageinfo"},
		"iiprop":        {"url|mime|extmetadata"},
	}
	var response commonsResponse
	if err := c.getJSON(ctx, query, &response); err != nil {
		return commonsPage{}, err
	}
	if len(response.Query.Pages) != 1 || response.Query.Pages[0].Missing {
		return commonsPage{}, ErrRecordingUnavailable
	}
	return response.Query.Pages[0], nil
}

func (c *Commons) getJSON(ctx context.Context, query url.Values, target any) error {
	endpoint, err := url.Parse(c.apiURL)
	if err != nil {
		return fmt.Errorf("parse Commons API address: %w", err)
	}
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("create Commons API request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	response, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("query Wikimedia Commons: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("query Wikimedia Commons: server returned %s", response.Status)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2<<20))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode Wikimedia Commons response: %w", err)
	}
	return nil
}

func (c *Commons) recording(page commonsPage, terms []string) (Recording, bool) {
	if page.PageID <= 0 || len(page.ImageInfo) != 1 {
		return Recording{}, false
	}
	info := page.ImageInfo[0]
	if !strings.HasPrefix(info.MIME, "audio/") || !strings.Contains(page.Title, "(jpn)") {
		return Recording{}, false
	}
	if !metadataContains(info.Metadata.Categories.Value, "Lingua Libre pronunciation-jpn") ||
		!strings.EqualFold(strings.TrimSpace(info.Metadata.License.Value), "cc0") {
		return Recording{}, false
	}
	if !titleMatches(page.Title, terms) {
		return Recording{}, false
	}
	audioURL, err := url.Parse(info.URL)
	if err != nil || audioURL.Scheme != "https" || !strings.EqualFold(audioURL.Hostname(), c.uploadHost) {
		return Recording{}, false
	}
	return Recording{
		ID:          page.PageID,
		Label:       recordingLabel(page.Title, terms),
		SourceName:  commonsSourceName,
		SourceURL:   info.DescriptionURL,
		LicenseName: commonsLicenseName,
		LicenseURL:  commonsLicenseURL,
	}, true
}

func uniqueTerms(values ...string) []string {
	seen := make(map[string]bool)
	var terms []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			terms = append(terms, value)
		}
	}
	return terms
}

func titleMatches(title string, terms []string) bool {
	name := strings.TrimPrefix(title, "File:")
	extension := path.Ext(name)
	name = strings.TrimSuffix(name, extension)
	for _, term := range terms {
		if strings.HasSuffix(name, "-"+term) || strings.HasSuffix(name, "("+term+")") ||
			(strings.Contains(name, "-"+term+" (") && strings.HasSuffix(name, ")")) {
			return true
		}
	}
	return false
}

func recordingLabel(title string, terms []string) string {
	name := strings.TrimSuffix(strings.TrimPrefix(title, "File:"), path.Ext(title))
	if opening := strings.LastIndex(name, "("); opening >= 0 && strings.HasSuffix(name, ")") {
		label := strings.TrimSpace(name[opening+1 : len(name)-1])
		if label != "" {
			return label
		}
	}
	for _, term := range terms {
		if strings.HasSuffix(name, "-"+term) {
			return term
		}
	}
	return "Japanese recording"
}

func metadataContains(categories, category string) bool {
	for _, value := range strings.Split(categories, "|") {
		if strings.TrimSpace(value) == category {
			return true
		}
	}
	return false
}

type commonsResponse struct {
	Query struct {
		Pages []commonsPage `json:"pages"`
	} `json:"query"`
}

type commonsPage struct {
	PageID    int64              `json:"pageid"`
	Title     string             `json:"title"`
	Missing   bool               `json:"missing"`
	ImageInfo []commonsImageInfo `json:"imageinfo"`
}

type commonsImageInfo struct {
	URL            string          `json:"url"`
	DescriptionURL string          `json:"descriptionurl"`
	MIME           string          `json:"mime"`
	Metadata       commonsMetadata `json:"extmetadata"`
}

type commonsMetadata struct {
	Categories metadataValue `json:"Categories"`
	License    metadataValue `json:"License"`
}

type metadataValue struct {
	Value string `json:"value"`
}
