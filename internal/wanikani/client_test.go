package wanikani

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

func TestClientAssignmentsFollowsPagesAndFiltersRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		query := r.URL.Query()
		if query.Get("started") != "true" || query.Get("hidden") != "false" ||
			query.Get("levels") != "1,2,3" || query.Get("subject_types") != "vocabulary,kana_vocabulary" ||
			query.Get("updated_after") != "2026-08-27T09:29:00Z" {
			t.Errorf("query = %v", query)
		}
		next := "null"
		id := 1
		if query.Get("page_after_id") == "" {
			next = strconv.Quote(serverURL(r) + "/v2/assignments?" + query.Encode() + "&page_after_id=1")
		} else {
			id = 2
		}
		_, _ = fmt.Fprintf(w, `{"object":"collection","pages":{"next_url":%s},"data":[{
			"id":%d,"object":"assignment","data":{"subject_id":%d,"subject_type":"vocabulary","started_at":"2026-01-01T00:00:00Z","hidden":false}
		}]}`, next, id, id+10)
	}))
	defer server.Close()

	client := newClient(server.Client(), server.URL+"/v2/")
	assignments, err := client.Assignments(t.Context(), testToken, time.Date(2026, 8, 27, 9, 29, 0, 0, time.UTC), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 2 || requests.Load() != 2 {
		t.Fatalf("assignments = %+v, requests = %d", assignments, requests.Load())
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}

func TestClientRejectsPaginationLoopAndForeignHost(t *testing.T) {
	tests := []struct {
		name string
		next func(*http.Request) string
	}{
		{name: "loop", next: func(r *http.Request) string { return serverURL(r) + r.URL.RequestURI() }},
		{name: "foreign host", next: func(*http.Request) string { return "https://example.com/v2/assignments?page=2" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintf(w, `{"object":"collection","pages":{"next_url":%q},"data":[]}`, test.next(r))
			}))
			defer server.Close()
			_, err := newClient(server.Client(), server.URL+"/v2/").Assignments(t.Context(), testToken, time.Time{}, 60)
			if err == nil {
				t.Fatal("Assignments() accepted unsafe pagination")
			}
		})
	}
}

func TestClientDoesNotFollowRedirectsWithAuthorization(t *testing.T) {
	var redirectedRequests atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		redirectedRequests.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("redirected request contained authorization")
		}
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", destination.URL+"/v2/user")
		w.WriteHeader(http.StatusFound)
	}))
	defer origin.Close()
	if _, err := newClient(origin.Client(), origin.URL+"/v2/").User(t.Context(), testToken); err == nil {
		t.Fatal("User() followed an API redirect")
	}
	if redirectedRequests.Load() != 0 {
		t.Fatalf("redirect destination received %d requests", redirectedRequests.Load())
	}
}

func TestClientRetriesRateLimitOnce(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("RateLimit-Reset", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = fmt.Fprint(w, `{"object":"user","data":{"id":"user-1","username":"turtle","level":1,"subscription":{"active":false,"type":"free","max_level_granted":3}}}`)
	}))
	defer server.Close()
	if _, err := newClient(server.Client(), server.URL+"/v2/").User(t.Context(), testToken); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
}
