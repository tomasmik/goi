package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tomasmik/goi/internal/securefile"
)

const GoogleDriveClientFilename = "google-drive-client.json"

const maxGoogleClientFileBytes = 64 << 10

type DriveManager interface {
	DriveClient
	CallbackURL() string
	EnvironmentManaged() bool
	SaveClient(io.Reader) error
	RemoveClient() error
}

type GoogleDriveManagerConfig struct {
	Drive            GoogleDriveConfig
	ClientConfigPath string
}

type GoogleDriveManager struct {
	mu                 sync.RWMutex
	drive              *GoogleDrive
	baseConfig         GoogleDriveConfig
	clientConfigPath   string
	environmentManaged bool
}

type storedGoogleClient struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

type downloadedGoogleClient struct {
	Web struct {
		ClientID     string   `json:"client_id"`
		ClientSecret string   `json:"client_secret"`
		RedirectURIs []string `json:"redirect_uris"`
	} `json:"web"`
}

func NewGoogleDriveManager(config GoogleDriveManagerConfig) (*GoogleDriveManager, error) {
	base := config.Drive
	environmentManaged := strings.TrimSpace(base.ClientID) != "" || strings.TrimSpace(base.ClientSecret) != ""
	if environmentManaged {
		client := storedGoogleClient{
			ClientID:     strings.TrimSpace(base.ClientID),
			ClientSecret: strings.TrimSpace(base.ClientSecret),
		}
		if err := validateGoogleClient(client); err != nil {
			return nil, fmt.Errorf("validate environment Google client: %w", err)
		}
		base.ClientID = client.ClientID
		base.ClientSecret = client.ClientSecret
	} else {
		client, err := readGoogleClient(config.ClientConfigPath)
		if err != nil {
			return nil, err
		}
		base.ClientID = client.ClientID
		base.ClientSecret = client.ClientSecret
	}
	drive, err := NewGoogleDrive(base)
	if err != nil {
		return nil, err
	}
	base.ClientID = ""
	base.ClientSecret = ""
	return &GoogleDriveManager{
		drive:              drive,
		baseConfig:         base,
		clientConfigPath:   config.ClientConfigPath,
		environmentManaged: environmentManaged,
	}, nil
}

func (m *GoogleDriveManager) Configured() bool {
	return m.current().Configured()
}

func (m *GoogleDriveManager) Connected() bool {
	return m.current().Connected()
}

func (m *GoogleDriveManager) AuthorizationURL() (string, error) {
	return m.current().AuthorizationURL()
}

func (m *GoogleDriveManager) Connect(ctx context.Context, state, code string) error {
	return m.current().Connect(ctx, state, code)
}

func (m *GoogleDriveManager) Disconnect() error {
	return m.current().Disconnect()
}

func (m *GoogleDriveManager) Upload(ctx context.Context, path, name string) (RemoteBackup, error) {
	return m.current().Upload(ctx, path, name)
}

func (m *GoogleDriveManager) List(ctx context.Context) ([]RemoteBackup, error) {
	return m.current().List(ctx)
}

func (m *GoogleDriveManager) ListCurrent(ctx context.Context) ([]RemoteBackup, error) {
	return m.current().ListCurrent(ctx)
}

func (m *GoogleDriveManager) Download(ctx context.Context, id string, destination io.Writer) error {
	return m.current().Download(ctx, id, destination)
}

func (m *GoogleDriveManager) Delete(ctx context.Context, id string) error {
	return m.current().Delete(ctx, id)
}

func (m *GoogleDriveManager) CallbackURL() string {
	return m.baseConfig.RedirectURL
}

func (m *GoogleDriveManager) EnvironmentManaged() bool {
	return m.environmentManaged
}

func (m *GoogleDriveManager) SaveClient(source io.Reader) error {
	if m.environmentManaged {
		return errors.New("Google Drive is managed by the server environment")
	}
	downloaded, err := parseDownloadedGoogleClient(source)
	if err != nil {
		return err
	}
	callbackFound := false
	for _, redirect := range downloaded.Web.RedirectURIs {
		if redirect == m.baseConfig.RedirectURL {
			callbackFound = true
			break
		}
	}
	if !callbackFound {
		return fmt.Errorf("add %s as an authorized redirect URI in Google, then download the client JSON again", m.baseConfig.RedirectURL)
	}
	client := storedGoogleClient{
		ClientID:     strings.TrimSpace(downloaded.Web.ClientID),
		ClientSecret: strings.TrimSpace(downloaded.Web.ClientSecret),
	}
	if err := validateGoogleClient(client); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.drive.Connected() {
		return errors.New("disconnect Google Drive before replacing its OAuth client")
	}
	nextConfig := m.baseConfig
	nextConfig.ClientID = client.ClientID
	nextConfig.ClientSecret = client.ClientSecret
	next, err := NewGoogleDrive(nextConfig)
	if err != nil {
		return err
	}
	if err := writeGoogleClient(m.clientConfigPath, client); err != nil {
		return fmt.Errorf("save Google OAuth client: %w", err)
	}
	m.drive = next
	return nil
}

func (m *GoogleDriveManager) RemoveClient() error {
	if m.environmentManaged {
		return errors.New("Google Drive is managed by the server environment")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.drive.Connected() {
		return errors.New("disconnect Google Drive before removing its OAuth client")
	}
	if err := os.Remove(m.clientConfigPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove Google OAuth client: %w", err)
	}
	if err := syncDirectory(filepath.Dir(m.clientConfigPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sync Google OAuth client directory: %w", err)
	}
	next, err := NewGoogleDrive(m.baseConfig)
	if err != nil {
		return err
	}
	m.drive = next
	return nil
}

func (m *GoogleDriveManager) current() *GoogleDrive {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.drive
}

func parseDownloadedGoogleClient(source io.Reader) (downloadedGoogleClient, error) {
	contents, err := io.ReadAll(io.LimitReader(source, maxGoogleClientFileBytes+1))
	if err != nil {
		return downloadedGoogleClient{}, fmt.Errorf("read Google OAuth client: %w", err)
	}
	if len(contents) > maxGoogleClientFileBytes {
		return downloadedGoogleClient{}, errors.New("Google OAuth client JSON is too large")
	}
	var downloaded downloadedGoogleClient
	if err := json.Unmarshal(contents, &downloaded); err != nil {
		return downloadedGoogleClient{}, errors.New("Google OAuth client JSON is invalid")
	}
	if downloaded.Web.ClientID == "" && downloaded.Web.ClientSecret == "" {
		return downloadedGoogleClient{}, errors.New("choose an OAuth client JSON for a Web application")
	}
	return downloaded, nil
}

func validateGoogleClient(client storedGoogleClient) error {
	client.ClientID = strings.TrimSpace(client.ClientID)
	client.ClientSecret = strings.TrimSpace(client.ClientSecret)
	if client.ClientID == "" || client.ClientSecret == "" {
		return errors.New("Google OAuth client ID and secret are required")
	}
	if len(client.ClientID) > 4096 || len(client.ClientSecret) > 4096 {
		return errors.New("Google OAuth client credentials are too long")
	}
	if strings.ContainsAny(client.ClientID, "\r\n") || strings.ContainsAny(client.ClientSecret, "\r\n") {
		return errors.New("Google OAuth client credentials must each be one line")
	}
	return nil
}

func readGoogleClient(path string) (storedGoogleClient, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return storedGoogleClient{}, nil
	}
	if err != nil {
		return storedGoogleClient{}, fmt.Errorf("inspect Google OAuth client: %w", err)
	}
	if !info.Mode().IsRegular() {
		return storedGoogleClient{}, errors.New("Google OAuth client is not a regular file")
	}
	if info.Size() > maxGoogleClientFileBytes {
		return storedGoogleClient{}, errors.New("Google OAuth client is too large")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return storedGoogleClient{}, fmt.Errorf("secure Google OAuth client: %w", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return storedGoogleClient{}, fmt.Errorf("read Google OAuth client: %w", err)
	}
	var client storedGoogleClient
	if err := json.Unmarshal(contents, &client); err != nil {
		return storedGoogleClient{}, fmt.Errorf("parse Google OAuth client: %w", err)
	}
	client.ClientID = strings.TrimSpace(client.ClientID)
	client.ClientSecret = strings.TrimSpace(client.ClientSecret)
	if err := validateGoogleClient(client); err != nil {
		return storedGoogleClient{}, fmt.Errorf("validate Google OAuth client: %w", err)
	}
	return client, nil
}

func writeGoogleClient(path string, client storedGoogleClient) error {
	contents, err := json.Marshal(client)
	if err != nil {
		return err
	}
	return securefile.Write(path, contents)
}
