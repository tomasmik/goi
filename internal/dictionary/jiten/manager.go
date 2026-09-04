package jiten

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tomasmik/goi/internal/contextio"
)

type ManagerConfig struct {
	Path   string
	Client *http.Client
}

type Manager struct {
	path    string
	client  *http.Client
	refresh sync.Mutex
	mu      sync.RWMutex
	cache   *cache
	status  [2]SourceStatus
	closed  bool
}

func NewManager(config ManagerConfig) (*Manager, error) {
	if config.Path == "" {
		return nil, errors.New("Jiten cache path is required")
	}
	client := http.Client{Timeout: 2 * time.Minute}
	if config.Client != nil {
		client = *config.Client
		if client.Timeout <= 0 || client.Timeout > 2*time.Minute {
			client.Timeout = 2 * time.Minute
		}
	}
	previousRedirect := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, previous []*http.Request) error {
		if request.URL.Scheme != "https" {
			return errors.New("Jiten redirect changed to non-HTTPS URL")
		}
		if len(previous) >= 10 {
			return errors.New("too many Jiten redirects")
		}
		if previousRedirect != nil {
			return previousRedirect(request, previous)
		}
		return nil
	}
	m := &Manager{path: config.Path, client: &client, status: [2]SourceStatus{{Corpus: Global}, {Corpus: Novel}}}
	if err := m.open(); err != nil {
		for index := range m.status {
			m.status[index].Error = err.Error()
		}
		slog.Warn("Jiten frequency cache is unavailable", "error", err)
	}
	return m, nil
}

// open is called before publication or while holding mu exclusively.
func (m *Manager) open() error {
	c, err := openCache(m.path)
	if err != nil {
		return fmt.Errorf("open Jiten cache: %w", err)
	}
	sources, err := c.sources(context.Background())
	if err != nil {
		return errors.Join(fmt.Errorf("read Jiten cache metadata: %w", err), c.db.Close())
	}
	for _, source := range sources {
		for index := range m.status {
			if m.status[index].Corpus == source.Corpus {
				m.status[index] = source
			}
		}
	}
	m.cache = c
	return nil
}

func (m *Manager) Status() [2]SourceStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

func (m *Manager) Lookup(ctx context.Context, pairs []Pair) ([]Ranks, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed || m.cache == nil {
		return nil, ErrUnavailable
	}
	return m.cache.lookup(ctx, pairs)
}

func (m *Manager) Close() error {
	m.refresh.Lock()
	defer m.refresh.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for index := range m.status {
		m.status[index].Available = false
	}
	if m.cache == nil {
		return nil
	}
	err := m.cache.db.Close()
	m.cache = nil
	return err
}

func (m *Manager) Run(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	_, _ = m.refreshSources(ctx, false)
	ticker := time.NewTicker(7 * 24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = m.refreshSources(ctx, false)
		}
	}
}

func (m *Manager) Refresh(ctx context.Context) (RefreshResult, error) {
	return m.refreshSources(ctx, true)
}

func (m *Manager) refreshSources(ctx context.Context, force bool) (RefreshResult, error) {
	m.refresh.Lock()
	defer m.refresh.Unlock()
	if err := ctx.Err(); err != nil {
		return Failed, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Failed, errors.New("Jiten manager is closed")
	}
	if m.cache == nil {
		if err := m.open(); err != nil {
			for index := range m.status {
				m.status[index].Error = err.Error()
				m.status[index].LastCheck = time.Now().UTC()
			}
			m.mu.Unlock()
			return Failed, err
		}
	}
	m.mu.Unlock()
	var failures error
	succeeded, changed := 0, false
	for index := range m.status {
		m.mu.Lock()
		m.status[index].LastCheck = time.Now().UTC()
		m.status[index].RefreshRunning = true
		source := m.status[index]
		m.mu.Unlock()

		updated, err := m.refreshSource(ctx, source, force)
		m.mu.Lock()
		if err != nil {
			m.status[index].Error = err.Error()
			failures = errors.Join(failures, fmt.Errorf("Jiten %s: %w", source.Corpus, err))
		} else {
			changed = changed || updated.SHA256 != source.SHA256 || updated.Revision != source.Revision
			updated.Error = ""
			m.status[index] = updated
			succeeded++
		}
		m.status[index].RefreshRunning = false
		m.mu.Unlock()
		if err != nil {
			slog.WarnContext(ctx, "Jiten frequency update failed", "corpus", source.Corpus, "error", err)
		}
	}
	if failures != nil {
		if succeeded > 0 {
			return Partial, failures
		}
		return Failed, failures
	}
	if changed {
		return Updated, nil
	}
	return Unchanged, nil
}

func sourceURL(corpus, endpoint string) string {
	address := "https://api.jiten.moe/api/frequency-list/" + endpoint
	if endpoint == "download" {
		address += "?downloadType=csv"
	}
	if corpus == Novel {
		if strings.Contains(address, "?") {
			address += "&mediaType=Novel"
		} else {
			address += "?mediaType=Novel"
		}
	}
	return address
}

func (m *Manager) get(ctx context.Context, address string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	response, err := m.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.Request == nil || response.Request.URL.Scheme != "https" || response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("Jiten download requires HTTPS and status 200 (received %d)", response.StatusCode)
	}
	return response, nil
}

func (m *Manager) revision(ctx context.Context, corpus string) (string, error) {
	response, err := m.get(ctx, sourceURL(corpus, "index"))
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil {
		return "", err
	}
	if len(data) > 64<<10 {
		return "", errors.New("Jiten index exceeds size limit")
	}
	var index struct {
		Title         string `json:"title"`
		Revision      string `json:"revision"`
		FrequencyMode string `json:"frequencyMode"`
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return "", fmt.Errorf("decode Jiten index: %w", err)
	}
	title := "Jiten"
	if corpus == Novel {
		title = "Jiten (Novel)"
	}
	if index.Title != title || strings.TrimSpace(index.Revision) == "" || len(index.Revision) > 256 || index.FrequencyMode != "rank-based" {
		return "", errors.New("Jiten index has an unexpected title, revision, or frequency mode")
	}
	return index.Revision, nil
}

func (m *Manager) refreshSource(ctx context.Context, source SourceStatus, force bool) (SourceStatus, error) {
	revision, err := m.revision(ctx, source.Corpus)
	if err != nil {
		return source, err
	}
	if !force && source.Available && revision == source.Revision {
		return source, nil
	}
	response, err := m.get(ctx, sourceURL(source.Corpus, "download"))
	if err != nil {
		return source, err
	}
	defer response.Body.Close()
	if response.ContentLength > maxCSVBytes {
		return source, errors.New("Jiten CSV exceeds size limit")
	}
	file, err := os.CreateTemp(filepath.Dir(m.path), ".jiten-*.csv")
	if err != nil {
		return source, fmt.Errorf("create Jiten download file: %w", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	hash := sha256.New()
	written, err := contextio.Copy(ctx, io.MultiWriter(file, hash), io.LimitReader(response.Body, maxCSVBytes+1))
	if err != nil {
		return source, fmt.Errorf("download Jiten CSV: %w", err)
	}
	if written > maxCSVBytes {
		return source, errors.New("Jiten CSV exceeds size limit")
	}
	after, err := m.revision(ctx, source.Corpus)
	if err != nil {
		return source, err
	}
	if revision != after {
		return source, errors.New("Jiten revision changed during download; previous ranks retained")
	}
	sum := fmt.Sprintf("%x", hash.Sum(nil))
	if source.Available && source.SHA256 == sum && source.Revision == revision {
		return source, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return source, err
	}
	source.Revision, source.SHA256, source.DownloadedAt = revision, sum, time.Now().UTC()
	return m.cache.importCSV(ctx, file, source)
}
