package wanikani

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	apiBaseURL       = "https://api.wanikani.com/v2/"
	apiRevision      = "20170710"
	assignmentLimit  = 4 << 20
	subjectLimit     = 16 << 20
	userLimit        = 1 << 20
	maximumPages     = 100
	subjectGroupSize = 250
)

var ErrAuthentication = errors.New("WaniKani authentication failed")

type Client struct {
	httpClient *http.Client
	baseURL    *url.URL
	now        func() time.Time
	wait       func(context.Context, time.Duration) error
}

type User struct {
	ID                 string
	Username           string
	Level              int
	SubscriptionActive bool
	SubscriptionType   string
	MaxLevelGranted    int
}

type Assignment struct {
	SubjectID   int64
	SubjectType string
	Started     bool
	Hidden      bool
}

type Subject struct {
	ID         int64
	Type       string
	Level      int
	Hidden     bool
	Expression string
}

type collection[T any] struct {
	Object string `json:"object"`
	Pages  *struct {
		NextURL *string `json:"next_url"`
	} `json:"pages"`
	Data []T `json:"data"`
}

type assignmentResource struct {
	ID     int64  `json:"id"`
	Object string `json:"object"`
	Data   struct {
		SubjectID   int64      `json:"subject_id"`
		SubjectType string     `json:"subject_type"`
		StartedAt   *time.Time `json:"started_at"`
		Hidden      bool       `json:"hidden"`
	} `json:"data"`
}

type subjectResource struct {
	ID     int64  `json:"id"`
	Object string `json:"object"`
	Data   struct {
		Level      int        `json:"level"`
		HiddenAt   *time.Time `json:"hidden_at"`
		Characters string     `json:"characters"`
	} `json:"data"`
}

func NewClient(httpClient *http.Client) *Client {
	return newClient(httpClient, apiBaseURL)
}

func newClient(httpClient *http.Client, baseURL string) *Client {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		panic(err)
	}
	client := *httpClient
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{
		httpClient: &client,
		baseURL:    parsed,
		now:        time.Now,
		wait: func(ctx context.Context, duration time.Duration) error {
			timer := time.NewTimer(duration)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	}
}

func (c *Client) User(ctx context.Context, token string) (User, error) {
	endpoint := c.baseURL.ResolveReference(&url.URL{Path: "user"})
	var response struct {
		Object string `json:"object"`
		Data   struct {
			ID           string `json:"id"`
			Username     string `json:"username"`
			Level        int    `json:"level"`
			Subscription struct {
				Active          bool   `json:"active"`
				Type            string `json:"type"`
				MaxLevelGranted int    `json:"max_level_granted"`
			} `json:"subscription"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, token, endpoint, userLimit, &response); err != nil {
		return User{}, err
	}
	user := User{
		ID:                 strings.TrimSpace(response.Data.ID),
		Username:           strings.TrimSpace(response.Data.Username),
		Level:              response.Data.Level,
		SubscriptionActive: response.Data.Subscription.Active,
		SubscriptionType:   response.Data.Subscription.Type,
		MaxLevelGranted:    response.Data.Subscription.MaxLevelGranted,
	}
	if response.Object != "user" || user.ID == "" || len(user.ID) > 200 ||
		user.Username == "" || len(user.Username) > 200 ||
		user.Level < 1 || user.Level > 60 ||
		user.MaxLevelGranted < 1 || user.MaxLevelGranted > 60 {
		return User{}, errors.New("WaniKani returned invalid user information")
	}
	return user, nil
}

func (c *Client) Assignments(ctx context.Context, token string, updatedAfter time.Time, maxLevelGranted int) ([]Assignment, error) {
	if maxLevelGranted < 1 || maxLevelGranted > 60 {
		return nil, errors.New("WaniKani maximum granted level must be between 1 and 60")
	}
	levels := make([]string, 0, maxLevelGranted)
	for level := 1; level <= maxLevelGranted; level++ {
		levels = append(levels, strconv.Itoa(level))
	}
	query := url.Values{
		"started":       {"true"},
		"hidden":        {"false"},
		"levels":        {strings.Join(levels, ",")},
		"subject_types": {"vocabulary,kana_vocabulary"},
	}
	if !updatedAfter.IsZero() {
		query.Set("updated_after", updatedAfter.UTC().Format(time.RFC3339Nano))
	}
	endpoint := c.baseURL.ResolveReference(&url.URL{Path: "assignments", RawQuery: query.Encode()})
	resources, err := fetchCollection[assignmentResource](ctx, c, token, endpoint, assignmentLimit)
	if err != nil {
		return nil, err
	}
	assignments := make([]Assignment, 0, len(resources))
	for _, resource := range resources {
		validType := resource.Data.SubjectType == "vocabulary" || resource.Data.SubjectType == "kana_vocabulary" ||
			resource.Data.SubjectType == "kanji" || resource.Data.SubjectType == "radical"
		if resource.Object != "assignment" || resource.ID <= 0 || resource.Data.SubjectID <= 0 ||
			!validType {
			return nil, errors.New("WaniKani returned an invalid assignment")
		}
		assignments = append(assignments, Assignment{
			SubjectID:   resource.Data.SubjectID,
			SubjectType: resource.Data.SubjectType,
			Started:     resource.Data.StartedAt != nil,
			Hidden:      resource.Data.Hidden,
		})
	}
	return assignments, nil
}

func (c *Client) Subjects(ctx context.Context, token string, ids []int64) ([]Subject, error) {
	unique := make([]int64, 0, len(ids))
	requested := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, errors.New("WaniKani subject ID must be positive")
		}
		if _, exists := requested[id]; exists {
			continue
		}
		requested[id] = struct{}{}
		unique = append(unique, id)
	}

	subjects := make([]Subject, 0, len(unique))
	seen := make(map[int64]struct{}, len(unique))
	for start := 0; start < len(unique); start += subjectGroupSize {
		end := min(start+subjectGroupSize, len(unique))
		values := make([]string, 0, end-start)
		for _, id := range unique[start:end] {
			values = append(values, strconv.FormatInt(id, 10))
		}
		endpoint := c.baseURL.ResolveReference(&url.URL{Path: "subjects", RawQuery: url.Values{"ids": {strings.Join(values, ",")}}.Encode()})
		resources, err := fetchCollection[subjectResource](ctx, c, token, endpoint, subjectLimit)
		if err != nil {
			return nil, err
		}
		for _, resource := range resources {
			if resource.ID <= 0 || (resource.Object != "vocabulary" && resource.Object != "kana_vocabulary") ||
				resource.Data.Level < 1 || resource.Data.Level > 60 ||
				!utf8.ValidString(resource.Data.Characters) || strings.TrimSpace(resource.Data.Characters) == "" ||
				utf8.RuneCountInString(resource.Data.Characters) > 256 {
				return nil, errors.New("WaniKani returned an invalid vocabulary subject")
			}
			if _, wanted := requested[resource.ID]; !wanted {
				return nil, errors.New("WaniKani returned an unrequested subject")
			}
			if _, duplicate := seen[resource.ID]; duplicate {
				return nil, errors.New("WaniKani returned a duplicate subject")
			}
			seen[resource.ID] = struct{}{}
			subjects = append(subjects, Subject{
				ID:         resource.ID,
				Type:       resource.Object,
				Level:      resource.Data.Level,
				Hidden:     resource.Data.HiddenAt != nil,
				Expression: strings.TrimSpace(resource.Data.Characters),
			})
		}
	}
	if len(seen) != len(requested) {
		return nil, errors.New("WaniKani did not return every requested subject")
	}
	return subjects, nil
}

func fetchCollection[T any](ctx context.Context, client *Client, token string, endpoint *url.URL, limit int64) ([]T, error) {
	var all []T
	visited := make(map[string]struct{})
	for page := 0; ; page++ {
		if page >= maximumPages {
			return nil, errors.New("WaniKani collection exceeded the page limit")
		}
		if err := client.validateEndpoint(endpoint); err != nil {
			return nil, err
		}
		key := endpoint.String()
		if _, exists := visited[key]; exists {
			return nil, errors.New("WaniKani pagination loop detected")
		}
		visited[key] = struct{}{}

		var response collection[T]
		if err := client.getJSON(ctx, token, endpoint, limit, &response); err != nil {
			return nil, err
		}
		if response.Object != "collection" || response.Pages == nil || response.Data == nil {
			return nil, errors.New("WaniKani returned an invalid collection")
		}
		all = append(all, response.Data...)
		if response.Pages.NextURL == nil {
			return all, nil
		}
		next, err := url.Parse(*response.Pages.NextURL)
		if err != nil {
			return nil, errors.New("WaniKani returned an invalid pagination URL")
		}
		endpoint = client.baseURL.ResolveReference(next)
	}
}

func (c *Client) validateEndpoint(endpoint *url.URL) error {
	if endpoint.Scheme != c.baseURL.Scheme || endpoint.Host != c.baseURL.Host || endpoint.User != nil ||
		!strings.HasPrefix(endpoint.EscapedPath(), "/v2/") {
		return errors.New("WaniKani returned an untrusted pagination URL")
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, token string, endpoint *url.URL, limit int64, destination any) error {
	if err := c.validateEndpoint(endpoint); err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return fmt.Errorf("create WaniKani request: %w", err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Wanikani-Revision", apiRevision)
		request.Header.Set("Accept", "application/json")
		response, err := c.httpClient.Do(request)
		if err != nil {
			return fmt.Errorf("request WaniKani: %w", err)
		}
		if response.StatusCode == http.StatusTooManyRequests && attempt == 0 {
			reset := response.Header.Get("RateLimit-Reset")
			_ = response.Body.Close()
			wait := time.Second
			if seconds, err := strconv.ParseInt(reset, 10, 64); err == nil {
				wait = time.Unix(seconds, 0).Sub(c.now())
				if wait < 0 {
					wait = 0
				}
			}
			wait = min(wait, time.Minute)
			if err := c.wait(ctx, wait); err != nil {
				return err
			}
			continue
		}
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			_ = response.Body.Close()
			return ErrAuthentication
		}
		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			return fmt.Errorf("WaniKani returned HTTP %d", response.StatusCode)
		}
		contents, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
		closeErr := response.Body.Close()
		if err != nil {
			return fmt.Errorf("read WaniKani response: %w", err)
		}
		if closeErr != nil {
			return fmt.Errorf("close WaniKani response: %w", closeErr)
		}
		if int64(len(contents)) > limit {
			return errors.New("WaniKani response exceeded the size limit")
		}
		if !utf8.Valid(contents) {
			return errors.New("WaniKani returned invalid JSON")
		}
		if err := json.Unmarshal(contents, destination); err != nil {
			return errors.New("WaniKani returned invalid JSON")
		}
		return nil
	}
	return errors.New("WaniKani rate limit retry failed")
}
