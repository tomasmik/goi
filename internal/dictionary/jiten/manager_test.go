package jiten

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
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

func TestManagerPreservesRanksAfterInvalidOrChangingDownload(t *testing.T) {
	f := &downloadFixture{global: "Word,Form,Rank\n猫,ねこ,1536\n", novel: "Word,Form,Rank\n猫,ねこ,1468\n", revision: "one"}
	m := newFixtureManager(t, filepath.Join(t.TempDir(), CacheFilename), f)
	if _, err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.global, f.novel = "Word,Form,Rank\n猫,ねこ,0\n", "bad CSV"
	if result, err := m.Refresh(context.Background()); result != Failed || err == nil {
		t.Fatalf("invalid refresh = %s, %v", result, err)
	}
	f.changeRevision = true
	f.global, f.novel = "Word,Form,Rank\n猫,ねこ,7\n", "Word,Form,Rank\n猫,ねこ,8\n"
	if result, err := m.Refresh(context.Background()); result != Failed || err == nil {
		t.Fatalf("changing refresh = %s, %v", result, err)
	}
	ranks, err := m.Lookup(context.Background(), []Pair{{"猫", "ねこ"}})
	if err != nil {
		t.Fatal(err)
	}
	requireRank(t, ranks[0].Global, 1536)
	requireRank(t, ranks[0].Novel, 1468)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Refresh(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled refresh = %v", err)
	}
}

type endlessReader struct{}

func (endlessReader) Read(data []byte) (int, error) {
	for index := range data {
		data[index] = 'x'
	}
	return len(data), nil
}

func TestManagerRejectsOversizedOrInsecureResponses(t *testing.T) {
	for _, kind := range []string{"index", "csv", "csv-stream", "redirect"} {
		t.Run(kind, func(t *testing.T) {
			f := &downloadFixture{global: "Word,Form,Rank\n猫,ねこ,1\n", novel: "Word,Form,Rank\n猫,ねこ,2\n", revision: "one"}
			m := newFixtureManager(t, filepath.Join(t.TempDir(), CacheFilename), f)
			m.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				response, err := f.response(request)
				if kind == "index" {
					response.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", (64<<10)+1)))
				}
				if kind == "csv" && strings.HasSuffix(request.URL.Path, "/download") {
					response.ContentLength = maxCSVBytes + 1
				}
				if kind == "csv-stream" && strings.HasSuffix(request.URL.Path, "/download") {
					response.ContentLength = -1
					response.Body = io.NopCloser(endlessReader{})
				}
				if kind == "redirect" {
					response.StatusCode = http.StatusFound
					response.Header = http.Header{"Location": {"http://example.invalid/data"}}
				}
				return response, err
			})
			if result, err := m.Refresh(context.Background()); result != Failed || err == nil {
				t.Fatalf("refresh = %s, %v", result, err)
			}
			if m.Status()[0].Available || m.Status()[1].Available {
				t.Fatal("invalid response published ranks")
			}
		})
	}
}

func TestManagerKeepsUnrecognizedCacheUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), CacheFilename)
	if err := os.WriteFile(path, []byte("user data"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newFixtureManager(t, path, &downloadFixture{})
	if m.Status()[0].Error == "" {
		t.Fatal("missing cache error")
	}
	if _, err := m.Lookup(context.Background(), []Pair{{"猫", "ねこ"}}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("lookup error = %v", err)
	}
	if _, err := m.Refresh(context.Background()); err == nil {
		t.Fatal("overwrote unrecognized file")
	}
}
