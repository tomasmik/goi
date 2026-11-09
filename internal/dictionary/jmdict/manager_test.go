package jmdict

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestManagerRefreshPublishesCache(t *testing.T) {
	compressed := gzipBytes(t, testDictionary)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return testResponse(request, http.StatusOK, compressed, http.Header{"Etag": {`"snapshot-1"`}}), nil
	})}
	manager, err := NewManager(ManagerConfig{
		Path:            filepath.Join(t.TempDir(), "jmdict.sqlite"),
		Client:          client,
		MaxCompressed:   int64(len(compressed) + 1),
		MaxDecompressed: int64(len(testDictionary) + 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := manager.Status()
	if !status.Available || status.Metadata.EntryCount != 3 || status.Metadata.ETag != `"snapshot-1"` || status.RefreshRunning {
		t.Fatalf("status = %#v", status)
	}
	match, err := manager.Lookup(context.Background(), "食べる", "")
	if err != nil || match.State != MatchReady {
		t.Fatalf("lookup = %#v, %v", match, err)
	}
}

func TestManagerRefreshUsesConditionalHeaders(t *testing.T) {
	compressed := gzipBytes(t, testDictionary)
	path := filepath.Join(t.TempDir(), "jmdict.sqlite")
	first, err := NewManager(ManagerConfig{
		Path: path,
		Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return testResponse(request, http.StatusOK, compressed, http.Header{
				"Etag":          {`"snapshot-1"`},
				"Last-Modified": {"Sat, 25 Jul 2026 00:00:00 GMT"},
			}), nil
		})},
		MaxCompressed:   int64(len(compressed) + 1),
		MaxDecompressed: int64(len(testDictionary) + 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := NewManager(ManagerConfig{
		Path: path,
		Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Header.Get("If-None-Match") != `"snapshot-1"` || request.Header.Get("If-Modified-Since") != "Sat, 25 Jul 2026 00:00:00 GMT" {
				t.Errorf("conditional headers = %#v", request.Header)
			}
			return testResponse(request, http.StatusNotModified, nil, nil), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := second.Refresh(context.Background()); !errors.Is(err, ErrNotModified) {
		t.Fatalf("refresh error = %v", err)
	}
}

func TestManagerFailedRefreshPreservesPublishedCache(t *testing.T) {
	compressed := gzipBytes(t, testDictionary)
	path := filepath.Join(t.TempDir(), "jmdict.sqlite")
	responseBody := compressed
	manager, err := NewManager(ManagerConfig{
		Path: path,
		Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return testResponse(request, http.StatusOK, responseBody, nil), nil
		})},
		MaxCompressed:   int64(len(compressed) + 100),
		MaxDecompressed: int64(len(testDictionary) + 100),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	responseBody = []byte("not gzip")
	if err := manager.Refresh(context.Background()); err == nil {
		t.Fatal("corrupt refresh succeeded")
	}
	match, err := manager.Lookup(context.Background(), "食べる", "")
	if err != nil || match.State != MatchReady {
		t.Fatalf("lookup after failed refresh = %#v, %v", match, err)
	}
	status := manager.Status()
	if !status.Available || status.LastErrorCode != "gzip" || status.RefreshRunning {
		t.Fatalf("status = %#v", status)
	}
}

func testResponse(request *http.Request, status int, body []byte, header http.Header) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{
		StatusCode:    status,
		Body:          io.NopCloser(bytes.NewReader(body)),
		Header:        header,
		ContentLength: int64(len(body)),
		Request:       request,
	}
}

func gzipBytes(t *testing.T, value string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := gzip.NewWriter(&output)
	if _, err := io.Copy(writer, strings.NewReader(value)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestCurrentDistribution(t *testing.T) {
	sourcePath := os.Getenv("GOI_JMDICT_TEST_FILE")
	if sourcePath == "" {
		t.Skip("GOI_JMDICT_TEST_FILE is not set")
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	cachePath := filepath.Join(t.TempDir(), "jmdict.sqlite")
	metadata, err := BuildGzip(context.Background(), source, cachePath, Source{URL: SourceURL, DownloadedAt: time.Now().UTC()}, defaultMaxDecompressed)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.EntryCount < 100_000 || metadata.Version != Version {
		t.Fatalf("metadata = %#v", metadata)
	}
	cache, err := Open(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	match, err := cache.Lookup(context.Background(), "食べる", "たべる")
	if err != nil {
		t.Fatal(err)
	}
	if match.State == MatchNone {
		t.Fatal("current distribution has no 食べる/たべる match")
	}
}
