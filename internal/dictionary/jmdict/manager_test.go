package jmdict

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
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

func TestManagerRefreshSkipsIdenticalDownloadWithoutValidators(t *testing.T) {
	compressed := gzipBytes(t, testDictionary)
	path := filepath.Join(t.TempDir(), "jmdict.sqlite")
	manager := newTestManager(t, path, compressed)
	if err := manager.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Refresh(context.Background()); !errors.Is(err, ErrNotModified) {
		t.Fatalf("second refresh error = %v", err)
	}
	if status := manager.Status(); !status.Available || status.LastErrorCode != "" || status.RefreshRunning {
		t.Fatalf("status = %#v", status)
	}
	manager.Close()
}

func TestManagerPersistsChangedValidatorsForIdenticalContent(t *testing.T) {
	compressed := gzipBytes(t, testDictionary)
	etag := `"snapshot-1"`
	manager, err := NewManager(ManagerConfig{
		Path: filepath.Join(t.TempDir(), "jmdict.sqlite"),
		Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return testResponse(request, http.StatusOK, compressed, http.Header{"Etag": {etag}}), nil
		})},
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
	etag = `"snapshot-2"`
	if err := manager.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := manager.Status().Metadata.ETag; got != etag {
		t.Fatalf("stored ETag = %q, want %q", got, etag)
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

func TestManagerRejectsNotModifiedWithoutCache(t *testing.T) {
	manager, err := NewManager(ManagerConfig{
		Path: filepath.Join(t.TempDir(), "jmdict.sqlite"),
		Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return testResponse(request, http.StatusNotModified, nil, nil), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	if err := manager.Refresh(context.Background()); err == nil || errors.Is(err, ErrNotModified) {
		t.Fatalf("Refresh error = %v, want unavailable-cache error", err)
	}
	status := manager.Status()
	if status.Available || status.RefreshRunning || status.LastErrorCode != "http_status" {
		t.Fatalf("status = %#v", status)
	}
}

func TestManagerRebuildsInvalidCacheWithoutConditionalRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jmdict.sqlite")
	source := Source{
		URL:          SourceURL,
		DownloadedAt: time.Now().UTC(),
		ETag:         `"invalid-cache"`,
		LastModified: "Sat, 25 Jul 2026 00:00:00 GMT",
	}
	if _, err := Build(context.Background(), strings.NewReader(testDictionary), path, source); err != nil {
		t.Fatal(err)
	}
	db, err := openSQLite(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE jmdict_pos"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	compressed := gzipBytes(t, testDictionary)
	manager, err := NewManager(ManagerConfig{
		Path: path,
		Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Header.Get("If-None-Match") != "" || request.Header.Get("If-Modified-Since") != "" {
				t.Errorf("invalid cache validators were reused: %#v", request.Header)
			}
			return testResponse(request, http.StatusOK, compressed, nil), nil
		})},
		MaxCompressed:   int64(len(compressed) + 1),
		MaxDecompressed: int64(len(testDictionary) + 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if status := manager.Status(); status.Available || status.LastErrorCode != "cache_unavailable" {
		t.Fatalf("initial status = %#v", status)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid cache still exists: %v", err)
	}

	if err := manager.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	match, err := manager.Lookup(context.Background(), "食べる", "")
	if err != nil || match.State != MatchReady {
		t.Fatalf("lookup after rebuild = %#v, %v", match, err)
	}
}

func TestManagerReportsInvalidCacheCleanupFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jmdict.sqlite")
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "keep"), []byte("not a cache"), 0o640); err != nil {
		t.Fatal(err)
	}

	manager, err := NewManager(ManagerConfig{Path: path})
	if err == nil || !strings.Contains(err.Error(), "open existing JMdict cache") ||
		!strings.Contains(err.Error(), "remove invalid JMdict cache") {
		t.Fatalf("NewManager error = %v, want cache-open and cleanup failures", err)
	}
	if manager != nil {
		t.Fatal("NewManager returned a manager after cache cleanup failed")
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

func TestManagerRejectsNonHTTPSResponse(t *testing.T) {
	compressed := gzipBytes(t, testDictionary)
	manager, err := NewManager(ManagerConfig{
		Path: filepath.Join(t.TempDir(), "jmdict.sqlite"),
		Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			response := testResponse(request, http.StatusOK, compressed, nil)
			response.Request = request.Clone(request.Context())
			response.Request.URL = &url.URL{Scheme: "http", Host: "example.test", Path: "/jmdict.gz"}
			return response, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Refresh(context.Background()); err == nil {
		t.Fatal("insecure response succeeded")
	}
	if status := manager.Status(); status.Available || status.LastErrorCode != "insecure_redirect" {
		t.Fatalf("status = %#v", status)
	}
}

func TestBuildGzipRejectsTruncationAndSizeLimit(t *testing.T) {
	compressed := gzipBytes(t, testDictionary)
	tests := []struct {
		name  string
		bytes []byte
		limit int64
	}{
		{name: "truncated", bytes: compressed[:len(compressed)-4], limit: int64(len(testDictionary) + 1)},
		{name: "too large", bytes: compressed, limit: int64(len(testDictionary) - 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jmdict.sqlite")
			if _, err := BuildGzip(context.Background(), bytes.NewReader(test.bytes), path, Source{}, test.limit); err == nil {
				t.Fatal("BuildGzip succeeded")
			}
		})
	}
}

func TestBuildGzipStopsDecompressionWhenContextCanceled(t *testing.T) {
	payload := make([]byte, 256<<10)
	state := uint32(0x9e3779b9)
	for index := range payload {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		payload[index] = byte(state)
	}
	compressed := gzipBytes(t, string(payload))
	if len(compressed) <= 4096 {
		t.Fatalf("compressed fixture is only %d bytes", len(compressed))
	}

	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelOnFirstRead{
		reader: bytes.NewReader(compressed),
		cancel: cancel,
	}
	path := filepath.Join(t.TempDir(), "jmdict.sqlite")
	_, err := BuildGzip(ctx, reader, path, Source{}, int64(len(payload)+1))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("BuildGzip error = %v, want context cancellation", err)
	}
	if reader.bytesRead >= len(compressed) {
		t.Fatal("canceled decompression drained the compressed input")
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("canceled build left a cache file: %v", statErr)
	}
}

type cancelOnFirstRead struct {
	reader    io.Reader
	cancel    context.CancelFunc
	bytesRead int
	canceled  bool
}

func (reader *cancelOnFirstRead) Read(buffer []byte) (int, error) {
	read, err := reader.reader.Read(buffer)
	reader.bytesRead += read
	if !reader.canceled {
		reader.cancel()
		reader.canceled = true
	}
	return read, err
}

func newTestManager(t *testing.T, path string, compressed []byte) *Manager {
	t.Helper()
	manager, err := NewManager(ManagerConfig{
		Path: path,
		Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return testResponse(request, http.StatusOK, compressed, nil), nil
		})},
		MaxCompressed:   int64(len(compressed) + 1),
		MaxDecompressed: int64(len(testDictionary) + 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
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

func TestManagerRunStopsWithContext(t *testing.T) {
	manager, err := NewManager(ManagerConfig{
		Path:          filepath.Join(t.TempDir(), "jmdict.sqlite"),
		CheckInterval: time.Hour,
		Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return nil, context.Canceled
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		manager.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Manager.Run did not stop")
	}
}

func TestManagerRunChecksAnExistingCacheOnStartup(t *testing.T) {
	compressed := gzipBytes(t, testDictionary)
	path := filepath.Join(t.TempDir(), "jmdict.sqlite")
	manager := newTestManager(t, path, compressed)
	if err := manager.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.Close()

	ctx, cancel := context.WithCancel(context.Background())
	requests := 0
	manager, err := NewManager(ManagerConfig{
		Path:          path,
		CheckInterval: time.Hour,
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			cancel()
			return nil, context.Canceled
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	manager.Run(ctx)

	if requests != 1 {
		t.Fatalf("startup refresh requests = %d, want 1", requests)
	}
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
