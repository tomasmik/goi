package jiten

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

type downloadFixture struct {
	global, novel         string
	revision              string
	failNovel             bool
	changeRevision        bool
	indexCalls, downloads int
}

func (f *downloadFixture) response(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.Host != "api.jiten.moe" || request.Header.Get("Authorization") != "" {
		return nil, errors.New("unexpected destination or credentials")
	}
	novel := request.URL.Query().Get("mediaType") == "Novel"
	status, body := http.StatusOK, f.global
	if novel {
		body = f.novel
	}
	if f.failNovel && novel {
		status, body = http.StatusTooManyRequests, "rate limited"
	} else if strings.HasSuffix(request.URL.Path, "/index") {
		f.indexCalls++
		title := "Jiten"
		if novel {
			title = "Jiten (Novel)"
		}
		revision := f.revision
		if f.changeRevision && f.indexCalls%2 == 0 {
			revision += "-changed"
		}
		data, _ := json.Marshal(map[string]string{"title": title, "revision": revision, "frequencyMode": "rank-based"})
		body = string(data)
	} else if strings.HasSuffix(request.URL.Path, "/download") && request.URL.Query().Get("downloadType") == "csv" {
		f.downloads++
	} else {
		return nil, errors.New("unexpected endpoint")
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

func newFixtureManager(t *testing.T, path string, f *downloadFixture) *Manager {
	t.Helper()
	m, err := NewManager(ManagerConfig{Path: path, Client: &http.Client{Transport: roundTripFunc(f.response)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestManagerUpdatesIndependentlyAndReopensOffline(t *testing.T) {
	path := filepath.Join(t.TempDir(), CacheFilename)
	f := &downloadFixture{global: "Word,Form,Rank\n猫,ねこ,1536\n", novel: "Word,Form,Rank\n猫,ねこ,1468\n", revision: "one"}
	m := newFixtureManager(t, path, f)
	if result, err := m.Refresh(context.Background()); err != nil || result != Updated {
		t.Fatalf("refresh = %s, %v", result, err)
	}
	if result, err := m.refreshSources(context.Background(), false); err != nil || result != Unchanged || f.downloads != 2 {
		t.Fatalf("scheduled refresh = %s, %v, downloads %d", result, err, f.downloads)
	}
	f.global = "Word,Form,Rank\n猫,ねこ,1500\n"
	f.failNovel = true
	if result, err := m.Refresh(context.Background()); err == nil || result != Partial {
		t.Fatalf("partial refresh = %s, %v", result, err)
	}
	status := m.Status()
	if !status[1].Available || status[1].Error == "" || status[1].RefreshRunning {
		t.Fatalf("status = %#v", status)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	m = newFixtureManager(t, path, f)
	ranks, err := m.Lookup(context.Background(), []Pair{{"猫", "ねこ"}})
	if err != nil {
		t.Fatal(err)
	}
	requireRank(t, ranks[0].Global, 1500)
	requireRank(t, ranks[0].Novel, 1468)
	files, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".jiten-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary files = %v, %v", files, err)
	}
}
