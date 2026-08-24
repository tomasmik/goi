package jmdict

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tomasmik/goi/internal/contextio"
)

const (
	defaultCheckInterval   = 7 * 24 * time.Hour
	defaultHTTPTimeout     = 2 * time.Minute
	defaultMaxCompressed   = int64(128 << 20)
	defaultMaxDecompressed = int64(1 << 30)
)

var (
	ErrNotModified = errors.New("JMdict has not changed")
	ErrUnavailable = errors.New("JMdict cache is unavailable")
)

type ManagerConfig struct {
	Path            string
	SourceURL       string
	Client          *http.Client
	CheckInterval   time.Duration
	MaxCompressed   int64
	MaxDecompressed int64
}

type ManagerStatus struct {
	Available      bool
	Metadata       Metadata
	LastCheck      time.Time
	LastSuccess    time.Time
	LastErrorCode  string
	RefreshRunning bool
}

type Manager struct {
	path            string
	sourceURL       string
	client          *http.Client
	checkInterval   time.Duration
	maxCompressed   int64
	maxDecompressed int64

	mu      sync.RWMutex
	cache   *Cache
	status  ManagerStatus
	closed  bool
	refresh sync.Mutex
}

func NewManager(config ManagerConfig) (*Manager, error) {
	if config.Path == "" {
		return nil, errors.New("JMdict cache path is required")
	}
	if config.SourceURL == "" {
		config.SourceURL = SourceURL
	}
	parsedURL, err := url.Parse(config.SourceURL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" {
		return nil, errors.New("JMdict source must be an HTTPS URL")
	}
	if config.Client == nil {
		config.Client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	client := *config.Client
	if client.Timeout <= 0 || client.Timeout > defaultHTTPTimeout {
		client.Timeout = defaultHTTPTimeout
	}
	previousRedirectCheck := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, previous []*http.Request) error {
		if request.URL.Scheme != "https" {
			return errors.New("JMdict redirect changed to a non-HTTPS URL")
		}
		if len(previous) >= 10 {
			return errors.New("too many JMdict redirects")
		}
		if previousRedirectCheck != nil {
			return previousRedirectCheck(request, previous)
		}
		return nil
	}
	if config.CheckInterval <= 0 {
		config.CheckInterval = defaultCheckInterval
	}
	if config.MaxCompressed <= 0 {
		config.MaxCompressed = defaultMaxCompressed
	}
	if config.MaxDecompressed <= 0 {
		config.MaxDecompressed = defaultMaxDecompressed
	}
	manager := &Manager{
		path:            config.Path,
		sourceURL:       config.SourceURL,
		client:          &client,
		checkInterval:   config.CheckInterval,
		maxCompressed:   config.MaxCompressed,
		maxDecompressed: config.MaxDecompressed,
	}
	_, statErr := os.Stat(config.Path)
	if statErr == nil {
		cache, err := Open(config.Path)
		if err != nil {
			manager.status.LastErrorCode = "cache_unavailable"
			if cleanupErr := discardInvalidCache(config.Path); cleanupErr != nil {
				return nil, errors.Join(
					fmt.Errorf("open existing JMdict cache: %w", err),
					cleanupErr,
				)
			}
			return manager, nil
		}
		metadata := cache.Metadata()
		manager.cache = cache
		manager.status.Available = true
		manager.status.Metadata = metadata
		manager.status.LastSuccess = metadata.DownloadedAt
		return manager, nil
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		manager.status.LastErrorCode = "cache_unavailable"
	}
	return manager, nil
}

func discardInvalidCache(path string) error {
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove invalid JMdict cache: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync JMdict cache directory: %w", err)
	}
	return nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if m.cache == nil {
		return nil
	}
	err := m.cache.Close()
	m.cache = nil
	m.status.Available = false
	return err
}

func (m *Manager) Lookup(ctx context.Context, expression, reading string) (Match, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed || m.cache == nil {
		return Match{}, ErrUnavailable
	}
	return m.cache.Lookup(ctx, expression, reading)
}

func (m *Manager) Status() ManagerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

func (m *Manager) Run(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if m.Status().LastCheck.IsZero() {
		_ = m.Refresh(ctx)
	}
	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = m.Refresh(ctx)
		}
	}
}

func (m *Manager) Refresh(ctx context.Context) error {
	m.refresh.Lock()
	defer m.refresh.Unlock()
	m.mu.RLock()
	closed := m.closed
	m.mu.RUnlock()
	if closed {
		return errors.New("JMdict manager is closed")
	}
	m.setRefreshStarted()

	metadata := m.currentMetadata()
	response, err := m.download(ctx, metadata)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL.Scheme != "https" {
		m.setRefreshFailed("insecure_redirect")
		return errors.New("JMdict response did not use HTTPS")
	}
	if response.StatusCode == http.StatusNotModified {
		if !m.cacheAvailable() {
			m.setRefreshFailed("http_status")
			return errors.New("JMdict returned not modified without an available cache")
		}
		m.setNotModified()
		return ErrNotModified
	}
	if response.StatusCode != http.StatusOK {
		m.setRefreshFailed("http_status")
		return fmt.Errorf("download JMdict: unexpected HTTP status %d", response.StatusCode)
	}
	if response.ContentLength > m.maxCompressed {
		m.setRefreshFailed("size_limit")
		return errors.New("compressed JMdict exceeds size limit")
	}

	compressedPath, sum, err := m.saveCompressed(response.Body)
	if err != nil {
		return err
	}
	defer os.Remove(compressedPath)
	source := Source{
		URL:          m.sourceURL,
		DownloadedAt: time.Now().UTC(),
		SHA256:       sum,
		ETag:         response.Header.Get("ETag"),
		LastModified: response.Header.Get("Last-Modified"),
	}
	if metadata.SHA256 != "" && metadata.SHA256 == source.SHA256 &&
		metadata.ETag == source.ETag && metadata.LastModified == source.LastModified {
		m.setNotModified()
		return ErrNotModified
	}
	return m.installCompressed(ctx, compressedPath, source)
}

func (m *Manager) download(ctx context.Context, metadata Metadata) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.sourceURL, nil)
	if err != nil {
		m.setRefreshFailed("request")
		return nil, fmt.Errorf("create JMdict request: %w", err)
	}
	request.Header.Set("Accept", "application/gzip")
	if metadata.ETag != "" {
		request.Header.Set("If-None-Match", metadata.ETag)
	}
	if metadata.LastModified != "" {
		request.Header.Set("If-Modified-Since", metadata.LastModified)
	}
	response, err := m.client.Do(request)
	if err != nil {
		m.setRefreshFailed("network")
		return nil, fmt.Errorf("download JMdict: %w", err)
	}
	return response, nil
}

func (m *Manager) saveCompressed(body io.Reader) (string, string, error) {
	directory := filepath.Dir(m.path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		m.setRefreshFailed("storage")
		return "", "", fmt.Errorf("create JMdict directory: %w", err)
	}
	compressed, err := os.CreateTemp(directory, ".jmdict-*.gz")
	if err != nil {
		m.setRefreshFailed("storage")
		return "", "", fmt.Errorf("create compressed JMdict staging file: %w", err)
	}
	compressedPath := compressed.Name()
	if err := compressed.Chmod(0o640); err != nil {
		compressed.Close()
		os.Remove(compressedPath)
		m.setRefreshFailed("storage")
		return "", "", fmt.Errorf("secure compressed JMdict staging file: %w", err)
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(compressed, hash), io.LimitReader(body, m.maxCompressed+1))
	closeErr := compressed.Close()
	if copyErr != nil {
		os.Remove(compressedPath)
		m.setRefreshFailed("network")
		return "", "", fmt.Errorf("save compressed JMdict: %w", copyErr)
	}
	if closeErr != nil {
		os.Remove(compressedPath)
		m.setRefreshFailed("storage")
		return "", "", fmt.Errorf("close compressed JMdict: %w", closeErr)
	}
	if written > m.maxCompressed {
		os.Remove(compressedPath)
		m.setRefreshFailed("size_limit")
		return "", "", errors.New("compressed JMdict exceeds size limit")
	}
	return compressedPath, fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func (m *Manager) installCompressed(ctx context.Context, compressedPath string, source Source) error {
	compressed, err := os.Open(compressedPath)
	if err != nil {
		m.setRefreshFailed("storage")
		return fmt.Errorf("reopen compressed JMdict: %w", err)
	}
	defer compressed.Close()
	directory := filepath.Dir(m.path)
	stagingPath, err := reserveStagingPath(directory)
	if err != nil {
		m.setRefreshFailed("storage")
		return err
	}
	defer os.Remove(stagingPath)
	built, err := BuildGzip(ctx, compressed, stagingPath, source, m.maxDecompressed)
	if err != nil {
		m.setRefreshFailed(refreshErrorCode(err))
		return err
	}
	if err := m.publish(stagingPath, built); err != nil {
		m.setRefreshFailed("publish")
		return err
	}
	return nil
}

func BuildGzip(ctx context.Context, reader io.Reader, path string, source Source, maxDecompressed int64) (metadata Metadata, returnErr error) {
	if maxDecompressed <= 0 {
		return Metadata{}, errors.New("decompressed size limit must be positive")
	}
	compressed, err := gzip.NewReader(reader)
	if err != nil {
		return Metadata{}, fmt.Errorf("open JMdict gzip: %w", err)
	}
	compressedClosed := false
	defer func() {
		if !compressedClosed {
			if err := compressed.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close JMdict gzip: %w", err))
			}
		}
	}()
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return Metadata{}, fmt.Errorf("create JMdict staging directory: %w", err)
	}
	xmlFile, err := os.CreateTemp(directory, ".jmdict-*.xml")
	if err != nil {
		return Metadata{}, fmt.Errorf("create JMdict XML staging file: %w", err)
	}
	xmlPath := xmlFile.Name()
	defer os.Remove(xmlPath)
	xmlClosed := false
	defer func() {
		if !xmlClosed {
			if err := xmlFile.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close JMdict XML: %w", err))
			}
		}
	}()
	if err := xmlFile.Chmod(0o640); err != nil {
		return Metadata{}, fmt.Errorf("secure JMdict XML staging file: %w", err)
	}
	written, copyErr := contextio.Copy(ctx, xmlFile, io.LimitReader(compressed, maxDecompressed+1))
	gzipErr := compressed.Close()
	compressedClosed = true
	if copyErr != nil {
		if gzipErr != nil {
			gzipErr = fmt.Errorf("verify JMdict gzip: %w", gzipErr)
		}
		return Metadata{}, errors.Join(fmt.Errorf("decompress JMdict: %w", copyErr), gzipErr)
	}
	if gzipErr != nil {
		return Metadata{}, fmt.Errorf("verify JMdict gzip: %w", gzipErr)
	}
	if written > maxDecompressed {
		return Metadata{}, errors.New("decompressed JMdict exceeds size limit")
	}
	if err := xmlFile.Sync(); err != nil {
		return Metadata{}, fmt.Errorf("sync JMdict XML: %w", err)
	}
	if _, err := xmlFile.Seek(0, io.SeekStart); err != nil {
		return Metadata{}, fmt.Errorf("rewind JMdict XML: %w", err)
	}
	metadata, err = Build(ctx, xmlFile, path, source)
	closeErr := xmlFile.Close()
	xmlClosed = true
	if err != nil {
		if closeErr != nil {
			closeErr = fmt.Errorf("close JMdict XML: %w", closeErr)
		}
		return Metadata{}, errors.Join(err, closeErr)
	}
	if closeErr != nil {
		return Metadata{}, fmt.Errorf("close JMdict XML: %w", closeErr)
	}
	return metadata, nil
}

func (m *Manager) publish(stagingPath string, metadata Metadata) error {
	stagedCache, err := Open(stagingPath)
	if err != nil {
		return fmt.Errorf("validate staged JMdict cache: %w", err)
	}
	if err := stagedCache.Close(); err != nil {
		return fmt.Errorf("close validated JMdict cache: %w", err)
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("JMdict manager is closed")
	}
	if err := os.Rename(stagingPath, m.path); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("publish JMdict cache: %w", err)
	}
	if err := syncDirectory(filepath.Dir(m.path)); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("sync JMdict cache directory: %w", err)
	}
	newCache, err := Open(m.path)
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("open published JMdict cache: %w", err)
	}
	oldCache := m.cache
	m.cache = newCache
	m.status.Available = true
	m.status.Metadata = metadata
	m.status.LastCheck = time.Now().UTC()
	m.status.LastSuccess = metadata.DownloadedAt
	m.status.LastErrorCode = ""
	m.status.RefreshRunning = false
	m.mu.Unlock()
	if oldCache != nil {
		_ = oldCache.Close()
	}
	return nil
}

func (m *Manager) currentMetadata() Metadata {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status.Metadata
}

func (m *Manager) cacheAvailable() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return !m.closed && m.cache != nil
}

func (m *Manager) setRefreshStarted() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.LastCheck = time.Now().UTC()
	m.status.RefreshRunning = true
}

func (m *Manager) setNotModified() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.LastCheck = time.Now().UTC()
	m.status.LastErrorCode = ""
	m.status.RefreshRunning = false
}

func (m *Manager) setRefreshFailed(code string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.LastCheck = time.Now().UTC()
	m.status.LastErrorCode = code
	m.status.RefreshRunning = false
}

func reserveStagingPath(directory string) (string, error) {
	file, err := os.CreateTemp(directory, ".jmdict-*.sqlite")
	if err != nil {
		return "", fmt.Errorf("reserve JMdict staging path: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		return "", errors.Join(
			fmt.Errorf("close JMdict staging reservation: %w", err),
			removeFile(path, "failed JMdict staging reservation"),
		)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("release JMdict staging reservation: %w", err)
	}
	return path, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		return errors.Join(err, directory.Close())
	}
	return directory.Close()
}

func removeFile(path, description string) error {
	err := os.Remove(path)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("remove %s: %w", description, err)
}

func refreshErrorCode(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "gzip") || strings.Contains(message, "decompress"):
		return "gzip"
	case strings.Contains(message, "size limit"):
		return "size_limit"
	case strings.Contains(message, "XML") || strings.Contains(message, "JMdict entry") || strings.Contains(message, "JMdict version"):
		return "xml"
	default:
		return "cache_build"
	}
}
