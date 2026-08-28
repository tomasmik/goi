package wanikani

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

func TestClientUserSendsRequiredHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/user" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken ||
			r.Header.Get("Wanikani-Revision") != apiRevision || r.Header.Get("Accept") != "application/json" {
			t.Errorf("headers = %v", r.Header)
		}
		_, _ = fmt.Fprint(w, `{
			"object":"user","unknown":true,
			"data":{"id":"user-1","username":"turtle","level":12,
			"subscription":{"active":true,"type":"lifetime","max_level_granted":60}}
		}`)
	}))
	defer server.Close()

	user, err := newClient(server.Client(), server.URL+"/v2/").User(t.Context(), testToken)
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != "user-1" || user.Username != "turtle" || user.Level != 12 || user.MaxLevelGranted != 60 {
		t.Fatalf("user = %+v", user)
	}
}

func TestClientAssignmentsFollowsPagesAndFiltersRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		query := r.URL.Query()
		if query.Get("started") != "true" || query.Get("hidden") != "false" ||
			query.Get("subject_types") != "vocabulary,kana_vocabulary" || query.Get("updated_after") != "2026-08-27T09:29:00Z" {
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
			"id":%d,"object":"assignment","data":{"subject_id":%d,"subject_type":"vocabulary","level":3,"started_at":"2026-01-01T00:00:00Z","hidden":false}
		}]}`, next, id, id+10)
	}))
	defer server.Close()

	client := newClient(server.Client(), server.URL+"/v2/")
	assignments, err := client.Assignments(t.Context(), testToken, time.Date(2026, 8, 27, 9, 29, 0, 0, time.UTC))
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
			_, err := newClient(server.Client(), server.URL+"/v2/").Assignments(t.Context(), testToken, time.Time{})
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

func TestClientSubjectsValidateCompletenessAndExpressions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ids") != "10,11" {
			t.Errorf("ids = %q", r.URL.Query().Get("ids"))
		}
		_, _ = fmt.Fprint(w, `{"object":"collection","pages":{"next_url":null},"data":[
			{"id":10,"object":"vocabulary","data":{"level":2,"hidden_at":null,"characters":"食べる"}},
			{"id":11,"object":"kana_vocabulary","data":{"level":3,"hidden_at":null,"characters":"おやつ"}}
		]}`)
	}))
	defer server.Close()
	subjects, err := newClient(server.Client(), server.URL+"/v2/").Subjects(t.Context(), testToken, []int64{10, 11})
	if err != nil || len(subjects) != 2 || subjects[1].Expression != "おやつ" {
		t.Fatalf("Subjects() = %+v, %v", subjects, err)
	}

	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"object":"collection","pages":{"next_url":null},"data":[]}`)
	}))
	defer missing.Close()
	if _, err := newClient(missing.Client(), missing.URL+"/v2/").Subjects(t.Context(), testToken, []int64{10}); err == nil {
		t.Fatal("Subjects() accepted a missing subject")
	}
}

func TestClientAuthenticationSizeLimitAndRateLimitCancellation(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer auth.Close()
	if _, err := newClient(auth.Client(), auth.URL+"/v2/").User(t.Context(), testToken); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("User() error = %v", err)
	}

	oversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, strings.Repeat("x", userLimit+1))
	}))
	defer oversized.Close()
	if _, err := newClient(oversized.Client(), oversized.URL+"/v2/").User(t.Context(), testToken); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversized User() error = %v", err)
	}

	rateLimited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10))
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer rateLimited.Close()
	ctx, cancel := context.WithCancel(t.Context())
	client := newClient(rateLimited.Client(), rateLimited.URL+"/v2/")
	waiting := make(chan struct{})
	client.wait = func(ctx context.Context, _ time.Duration) error {
		close(waiting)
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		_, err := client.User(ctx, testToken)
		done <- err
	}()
	<-waiting
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("rate-limited User() error = %v", err)
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
