package wanikani

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tomasmik/goi/internal/vocabulary"
)

type serviceAPI struct {
	mu              sync.Mutex
	username        string
	userID          string
	level           int
	maxLevel        int
	assignments     string
	subjects        map[string]string
	failAssignments bool
	assignmentQuery []string
	subjectQuery    []string
}

func (api *serviceAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	api.mu.Lock()
	defer api.mu.Unlock()
	switch r.URL.Path {
	case "/v2/user":
		_, _ = fmt.Fprintf(w, `{"object":"user","data":{"id":%q,"username":%q,"level":%d,"subscription":{"active":true,"type":"lifetime","max_level_granted":%d}}}`,
			api.userID, api.username, api.level, api.maxLevel)
	case "/v2/assignments":
		api.assignmentQuery = append(api.assignmentQuery, r.URL.RawQuery)
		if api.failAssignments {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = fmt.Fprintf(w, `{"object":"collection","pages":{"next_url":null},"data":[%s]}`, api.assignments)
	case "/v2/subjects":
		ids := r.URL.Query().Get("ids")
		api.subjectQuery = append(api.subjectQuery, ids)
		_, _ = fmt.Fprintf(w, `{"object":"collection","pages":{"next_url":null},"data":[%s]}`, api.subjects[ids])
	default:
		http.NotFound(w, r)
	}
}

func assignmentJSON(id, subjectID int64, subjectType string, level int, started, hidden bool) string {
	startedAt := "null"
	if started {
		startedAt = `"2026-01-01T00:00:00Z"`
	}
	return fmt.Sprintf(`{"id":%d,"object":"assignment","data":{"subject_id":%d,"subject_type":%q,"level":%d,"started_at":%s,"hidden":%t}}`,
		id, subjectID, subjectType, level, startedAt, hidden)
}

func subjectJSON(id int64, subjectType string, level int, expression string, hidden bool) string {
	hiddenAt := "null"
	if hidden {
		hiddenAt = `"2026-01-01T00:00:00Z"`
	}
	return fmt.Sprintf(`{"id":%d,"object":%q,"data":{"level":%d,"hidden_at":%s,"characters":%q}}`,
		id, subjectType, level, hiddenAt, expression)
}

func newTestService(t *testing.T, api http.Handler, now time.Time) (*Service, *Store, *Credentials, *httptest.Server) {
	t.Helper()
	db := openTestDatabase(t)
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	credentials := NewCredentials(filepath.Join(t.TempDir(), TokenFilename))
	store := NewStore(db)
	service := NewService(ServiceConfig{
		Client:      newClient(server.Client(), server.URL+"/v2/"),
		Credentials: credentials,
		Store:       store,
		Vocabulary:  vocabulary.NewStore(db),
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:         func() time.Time { return now },
	})
	return service, store, credentials, server
}

func TestServiceFullAndIncrementalSync(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	api := &serviceAPI{
		userID: "user-1", username: "turtle", level: 12, maxLevel: 3,
		assignments: strings.Join([]string{
			assignmentJSON(1, 10, "vocabulary", 2, true, false),
			assignmentJSON(2, 11, "kana_vocabulary", 3, true, false),
			assignmentJSON(3, 12, "kanji", 2, true, false),
			assignmentJSON(4, 13, "vocabulary", 2, false, false),
			assignmentJSON(5, 14, "vocabulary", 2, true, true),
			assignmentJSON(6, 15, "vocabulary", 4, true, false),
		}, ","),
		subjects: map[string]string{
			"10,11": subjectJSON(10, "vocabulary", 2, "食べる", false) + "," + subjectJSON(11, "kana_vocabulary", 3, "おやつ", false),
		},
	}
	service, store, _, _ := newTestService(t, api, now)
	if _, _, err := service.Connect(t.Context(), testToken); err != nil {
		t.Fatal(err)
	}
	if err := service.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	status, err := store.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.SubjectCount != 2 || !status.CursorAt.Equal(now) || status.LastError != "" {
		t.Fatalf("status = %+v", status)
	}
	var knownCount, srsCount int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM vocabulary WHERE known_elsewhere_at IS NOT NULL").Scan(&knownCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow("SELECT COUNT(*) FROM srs_states").Scan(&srsCount); err != nil {
		t.Fatal(err)
	}
	if knownCount != 2 || srsCount != 0 {
		t.Fatalf("known = %d, SRS = %d; want 2 and 0", knownCount, srsCount)
	}

	api.mu.Lock()
	api.assignments = assignmentJSON(7, 10, "vocabulary", 2, true, false) + "," + assignmentJSON(8, 16, "vocabulary", 2, true, false)
	api.subjects["16"] = subjectJSON(16, "vocabulary", 2, "新しい", false)
	api.mu.Unlock()
	if err := service.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	assignmentQueries := append([]string(nil), api.assignmentQuery...)
	subjectQueries := append([]string(nil), api.subjectQuery...)
	api.mu.Unlock()
	if len(assignmentQueries) != 2 || !strings.Contains(assignmentQueries[1], "updated_after=2026-08-27T11%3A59%3A00Z") {
		t.Fatalf("assignment queries = %v", assignmentQueries)
	}
	if len(subjectQueries) != 2 || subjectQueries[1] != "16" {
		t.Fatalf("subject queries = %v", subjectQueries)
	}
	if err := store.db.QueryRow("SELECT COUNT(*) FROM vocabulary WHERE known_elsewhere_at IS NOT NULL").Scan(&knownCount); err != nil {
		t.Fatal(err)
	}
	if knownCount != 3 {
		t.Fatalf("known vocabulary = %d, want 3", knownCount)
	}
}

func TestServiceFailureDoesNotAdvanceCursorOrLoseVocabulary(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	api := &serviceAPI{
		userID: "user-1", username: "turtle", level: 3, maxLevel: 3,
		assignments: assignmentJSON(1, 10, "vocabulary", 2, true, false),
		subjects:    map[string]string{"10": subjectJSON(10, "vocabulary", 2, "食べる", false)},
	}
	service, store, _, _ := newTestService(t, api, now)
	if _, _, err := service.Connect(t.Context(), testToken); err != nil {
		t.Fatal(err)
	}
	if err := service.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	api.failAssignments = true
	api.mu.Unlock()
	if err := service.Sync(t.Context()); err == nil {
		t.Fatal("Sync() succeeded with a failed assignment request")
	}
	status, err := store.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !status.CursorAt.Equal(now) || status.LastError == "" || status.SubjectCount != 1 {
		t.Fatalf("status after failure = %+v", status)
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM vocabulary WHERE expression = '食べる' AND known_elsewhere_at IS NOT NULL").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("known vocabulary count = %d, want 1", count)
	}
}

func TestServiceDisconnectedSyncDoesNotRecordFailure(t *testing.T) {
	api := &serviceAPI{userID: "user-1", username: "turtle", level: 3, maxLevel: 3, subjects: map[string]string{}}
	service, store, _, _ := newTestService(t, api, time.Now())
	if _, _, err := service.Connect(t.Context(), testToken); err != nil {
		t.Fatal(err)
	}
	if err := service.Disconnect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := service.Sync(t.Context()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Sync() error = %v, want ErrNotConnected", err)
	}
	status, err := store.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.UserID != "" || !status.LastAttemptAt.IsZero() || status.LastError != "" {
		t.Fatalf("status after disconnected sync = %+v", status)
	}
}

func TestServiceRecordedSubjectDoesNotRecreateDeletedVocabulary(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	api := &serviceAPI{
		userID: "user-1", username: "turtle", level: 3, maxLevel: 3,
		assignments: assignmentJSON(1, 10, "vocabulary", 2, true, false),
		subjects:    map[string]string{"10": subjectJSON(10, "vocabulary", 2, "食べる", false)},
	}
	service, store, _, _ := newTestService(t, api, now)
	if _, _, err := service.Connect(t.Context(), testToken); err != nil {
		t.Fatal(err)
	}
	if err := service.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("DELETE FROM vocabulary WHERE expression = '食べる'"); err != nil {
		t.Fatal(err)
	}
	if err := service.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM vocabulary WHERE expression = '食べる'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("deleted vocabulary was recreated %d times", count)
	}
}

func TestServiceRejectsOverlappingSync(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once atomic.Bool
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/user" && once.CompareAndSwap(false, true) {
			close(started)
			<-release
		}
		if r.URL.Path == "/v2/user" {
			_, _ = fmt.Fprint(w, `{"object":"user","data":{"id":"user-1","username":"turtle","level":3,"subscription":{"active":true,"type":"free","max_level_granted":3}}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"object":"collection","pages":{"next_url":null},"data":[]}`)
	})
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	service, store, credentials, _ := newTestService(t, api, now)
	if err := credentials.Save(testToken); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfigureAccount(t.Context(), User{ID: "user-1", Username: "turtle", Level: 3, MaxLevelGranted: 3}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- service.Sync(t.Context()) }()
	<-started
	if err := service.Sync(t.Context()); !errors.Is(err, ErrSyncInProgress) {
		t.Fatalf("overlapping Sync() error = %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServiceRunStartsConnectedSyncAndStopsWithContext(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/user" {
			http.NotFound(w, r)
			return
		}
		close(requestStarted)
		<-r.Context().Done()
		close(requestCanceled)
	})
	service, store, credentials, _ := newTestService(t, api, time.Now())
	if err := credentials.Save(testToken); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfigureAccount(t.Context(), User{ID: "user-1", Username: "turtle", Level: 3, MaxLevelGranted: 3}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		service.Run(ctx)
		close(done)
	}()
	<-requestStarted
	cancel()
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("worker cancellation did not reach the API request")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}
