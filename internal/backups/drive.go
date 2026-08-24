package backups

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tomasmik/goi/internal/contextio"
	"github.com/tomasmik/goi/internal/securefile"
)

const googleDriveScope = "https://www.googleapis.com/auth/drive.file"

const (
	backupProperty           = "goi_backup"
	installationProperty     = "goi_installation"
	remoteListPageLimit      = 10
	maxGoogleCredentialBytes = 16 << 10
)

type RemoteBackup struct {
	ID                  string            `json:"id"`
	Name                string            `json:"name"`
	Size                int64             `json:"size,string"`
	CreatedAt           time.Time         `json:"createdTime"`
	AppProperties       map[string]string `json:"appProperties"`
	CurrentInstallation bool              `json:"-"`
}

type DriveClient interface {
	Configured() bool
	Connected() bool
	AuthorizationURL() (string, error)
	Connect(context.Context, string, string) error
	Disconnect() error
	Upload(context.Context, string, string) (RemoteBackup, error)
	List(context.Context) ([]RemoteBackup, error)
	ListCurrent(context.Context) ([]RemoteBackup, error)
	Download(context.Context, string, io.Writer) error
	Delete(context.Context, string) error
}

type GoogleDriveConfig struct {
	ClientID       string
	ClientSecret   string
	RedirectURL    string
	CredentialPath string
	InstallationID string
	HTTPClient     *http.Client
	AuthURL        string
	TokenURL       string
	APIURL         string
	UploadURL      string
	Now            func() time.Time
}

type GoogleDrive struct {
	config GoogleDriveConfig

	mu           sync.Mutex
	refreshToken string
	accessToken  string
	accessExpiry time.Time
	authState    string
	stateExpiry  time.Time
	folderMu     sync.Mutex
	folderID     string
}

type storedGoogleCredentials struct {
	RefreshToken string `json:"refresh_token"`
}

type googleTokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func NewGoogleDrive(config GoogleDriveConfig) (*GoogleDrive, error) {
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 30 * time.Minute}
	}
	if config.AuthURL == "" {
		config.AuthURL = "https://accounts.google.com/o/oauth2/v2/auth"
	}
	if config.TokenURL == "" {
		config.TokenURL = "https://oauth2.googleapis.com/token"
	}
	if config.APIURL == "" {
		config.APIURL = "https://www.googleapis.com/drive/v3"
	}
	if config.UploadURL == "" {
		config.UploadURL = "https://www.googleapis.com/upload/drive/v3"
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.ClientID != "" && !validInstallationID(config.InstallationID) {
		return nil, errors.New("Google Drive installation ID is invalid")
	}
	drive := &GoogleDrive{config: config}
	if !drive.Configured() {
		return drive, nil
	}
	credentials, err := readGoogleCredentials(config.CredentialPath)
	if err != nil {
		return nil, err
	}
	drive.refreshToken = credentials.RefreshToken
	return drive, nil
}

func (g *GoogleDrive) Configured() bool {
	return g.config.ClientID != "" && g.config.ClientSecret != "" && g.config.RedirectURL != "" &&
		g.config.CredentialPath != "" && validInstallationID(g.config.InstallationID)
}

func (g *GoogleDrive) Connected() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.refreshToken != ""
}

func (g *GoogleDrive) AuthorizationURL() (string, error) {
	if !g.Configured() {
		return "", errors.New("Google Drive is not configured")
	}
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", fmt.Errorf("create OAuth state: %w", err)
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)
	g.mu.Lock()
	g.authState = state
	g.stateExpiry = g.config.Now().Add(10 * time.Minute)
	g.mu.Unlock()
	values := url.Values{
		"client_id":              {g.config.ClientID},
		"redirect_uri":           {g.config.RedirectURL},
		"response_type":          {"code"},
		"scope":                  {googleDriveScope},
		"access_type":            {"offline"},
		"include_granted_scopes": {"true"},
		"prompt":                 {"consent"},
		"state":                  {state},
	}
	return g.config.AuthURL + "?" + values.Encode(), nil
}

func (g *GoogleDrive) Connect(ctx context.Context, state, code string) error {
	if !g.Configured() {
		return errors.New("Google Drive is not configured")
	}
	g.mu.Lock()
	wantState := g.authState
	stateExpiry := g.stateExpiry
	g.authState = ""
	g.stateExpiry = time.Time{}
	g.mu.Unlock()
	if wantState == "" || g.config.Now().After(stateExpiry) || subtle.ConstantTimeCompare([]byte(state), []byte(wantState)) != 1 {
		return errors.New("Google authorization expired; start the connection again")
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return errors.New("Google did not return an authorization code")
	}
	values := url.Values{
		"client_id":     {g.config.ClientID},
		"client_secret": {g.config.ClientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {g.config.RedirectURL},
	}
	token, err := g.exchangeToken(ctx, values)
	if err != nil {
		return err
	}
	if token.RefreshToken == "" {
		return errors.New("Google did not return offline access; reconnect and grant access")
	}
	if err := writeGoogleCredentials(g.config.CredentialPath, storedGoogleCredentials{RefreshToken: token.RefreshToken}); err != nil {
		return fmt.Errorf("save Google Drive credentials: %w", err)
	}
	g.mu.Lock()
	g.refreshToken = token.RefreshToken
	g.accessToken = token.AccessToken
	g.accessExpiry = g.config.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	g.mu.Unlock()
	g.folderMu.Lock()
	g.folderID = ""
	g.folderMu.Unlock()
	return nil
}

func (g *GoogleDrive) Disconnect() error {
	g.mu.Lock()
	if g.config.CredentialPath != "" {
		if err := os.Remove(g.config.CredentialPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			g.mu.Unlock()
			return fmt.Errorf("remove Google Drive credentials: %w", err)
		}
		if err := syncDirectory(filepath.Dir(g.config.CredentialPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
			g.mu.Unlock()
			return fmt.Errorf("sync Google Drive credential directory: %w", err)
		}
	}
	g.refreshToken = ""
	g.accessToken = ""
	g.accessExpiry = time.Time{}
	g.mu.Unlock()
	g.folderMu.Lock()
	g.folderID = ""
	g.folderMu.Unlock()
	return nil
}

func (g *GoogleDrive) Upload(ctx context.Context, path, name string) (RemoteBackup, error) {
	if !validLocalName(name) {
		return RemoteBackup{}, errors.New("invalid Google Drive backup filename")
	}
	file, err := os.Open(path)
	if err != nil {
		return RemoteBackup{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return RemoteBackup{}, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 {
		return RemoteBackup{}, errors.New("Google Drive backup is not a non-empty regular file")
	}
	return g.uploadFile(ctx, file, info.Size(), name, true)
}

func (g *GoogleDrive) uploadFile(ctx context.Context, file *os.File, size int64, name string, retryStaleFolder bool) (RemoteBackup, error) {
	folderID, err := g.backupFolder(ctx)
	if err != nil {
		return RemoteBackup{}, err
	}
	metadata := map[string]any{
		"name":     name,
		"mimeType": "application/zip",
		"parents":  []string{folderID},
		"appProperties": map[string]string{
			backupProperty:       "1",
			installationProperty: g.config.InstallationID,
			"format":             "1",
		},
	}
	body, err := json.Marshal(metadata)
	if err != nil {
		return RemoteBackup{}, err
	}
	requestURL := g.config.UploadURL + "/files?uploadType=resumable&fields=id,name,size,createdTime"
	response, err := g.authorizedRequest(ctx, func(token string) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json; charset=UTF-8")
		request.Header.Set("X-Upload-Content-Type", "application/zip")
		request.Header.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
		return request, nil
	})
	if err != nil {
		return RemoteBackup{}, err
	}
	location := response.Header.Get("Location")
	if response.StatusCode != http.StatusOK || location == "" {
		if retryStaleFolder && response.StatusCode == http.StatusNotFound {
			response.Body.Close()
			g.clearBackupFolder(folderID)
			return g.uploadFile(ctx, file, size, name, false)
		}
		return RemoteBackup{}, googleResponseError(response, "start Google Drive upload")
	}
	response.Body.Close()
	return g.uploadContent(ctx, location, file, size)
}

func (g *GoogleDrive) List(ctx context.Context) ([]RemoteBackup, error) {
	query := fmt.Sprintf(
		"trashed = false and appProperties has { key='%s' and value='1' }",
		backupProperty,
	)
	files, err := g.list(ctx, query)
	if err != nil {
		return nil, err
	}
	g.classifyInstallations(files)
	return files, nil
}

func (g *GoogleDrive) ListCurrent(ctx context.Context) ([]RemoteBackup, error) {
	query := fmt.Sprintf(
		"trashed = false and appProperties has { key='%s' and value='1' } and appProperties has { key='%s' and value='%s' }",
		backupProperty,
		installationProperty,
		g.config.InstallationID,
	)
	files, err := g.list(ctx, query)
	if err != nil {
		return nil, err
	}
	g.classifyInstallations(files)
	return files, nil
}

func (g *GoogleDrive) list(ctx context.Context, query string) ([]RemoteBackup, error) {
	values := url.Values{
		"q":        {query},
		"spaces":   {"drive"},
		"orderBy":  {"createdTime desc"},
		"pageSize": {"100"},
		"fields":   {"nextPageToken,files(id,name,size,createdTime,appProperties)"},
	}
	type listResponse struct {
		NextPageToken string         `json:"nextPageToken"`
		Files         []RemoteBackup `json:"files"`
	}
	files := make([]RemoteBackup, 0)
	for page := 0; page < remoteListPageLimit; page++ {
		requestURL := g.config.APIURL + "/files?" + values.Encode()
		response, err := g.authorizedRequest(ctx, func(token string) (*http.Request, error) {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
			if err == nil {
				request.Header.Set("Authorization", "Bearer "+token)
			}
			return request, err
		})
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK {
			return nil, googleResponseError(response, "list Google Drive backups")
		}
		var result listResponse
		if err := decodeGoogleJSON(response, &result); err != nil {
			return nil, err
		}
		files = append(files, result.Files...)
		if result.NextPageToken == "" {
			return files, nil
		}
		values.Set("pageToken", result.NextPageToken)
	}
	return nil, errors.New("Google Drive returned too many backup pages")
}

func (g *GoogleDrive) classifyInstallations(files []RemoteBackup) {
	for index := range files {
		files[index].CurrentInstallation = files[index].AppProperties[installationProperty] == g.config.InstallationID
	}
}

func (g *GoogleDrive) Download(ctx context.Context, id string, destination io.Writer) error {
	if !validDriveID(id) {
		return errors.New("invalid Google Drive file ID")
	}
	requestURL := g.config.APIURL + "/files/" + url.PathEscape(id) + "?alt=media"
	response, err := g.authorizedRequest(ctx, func(token string) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if err == nil {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		return request, err
	})
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return googleResponseError(response, "download Google Drive backup")
	}
	defer response.Body.Close()
	limited := &io.LimitedReader{R: response.Body, N: MaxBundleBytes + 1}
	written, err := contextio.Copy(ctx, destination, limited)
	if err != nil {
		return err
	}
	if written > MaxBundleBytes {
		return errors.New("Google Drive backup exceeds the size limit")
	}
	return nil
}

func (g *GoogleDrive) Delete(ctx context.Context, id string) error {
	if !validDriveID(id) {
		return errors.New("invalid Google Drive file ID")
	}
	requestURL := g.config.APIURL + "/files/" + url.PathEscape(id)
	response, err := g.authorizedRequest(ctx, func(token string) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodDelete, requestURL, nil)
		if err == nil {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		return request, err
	})
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusNoContent {
		return googleResponseError(response, "delete old Google Drive backup")
	}
	return response.Body.Close()
}

func (g *GoogleDrive) backupFolder(ctx context.Context) (string, error) {
	g.folderMu.Lock()
	defer g.folderMu.Unlock()
	if g.folderID != "" {
		return g.folderID, nil
	}
	query := fmt.Sprintf(
		"trashed = false and mimeType = 'application/vnd.google-apps.folder' and appProperties has { key='goi_type' and value='backup_folder' } and appProperties has { key='%s' and value='%s' }",
		installationProperty,
		g.config.InstallationID,
	)
	values := url.Values{
		"q":        {query},
		"spaces":   {"drive"},
		"pageSize": {"2"},
		"fields":   {"files(id)"},
	}
	requestURL := g.config.APIURL + "/files?" + values.Encode()
	response, err := g.authorizedRequest(ctx, func(token string) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if err == nil {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		return request, err
	})
	if err != nil {
		return "", err
	}
	if response.StatusCode != http.StatusOK {
		return "", googleResponseError(response, "find Google Drive backup folder")
	}
	var result struct {
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if err := decodeGoogleJSON(response, &result); err != nil {
		return "", err
	}
	if len(result.Files) > 0 && validDriveID(result.Files[0].ID) {
		g.folderID = result.Files[0].ID
		return g.folderID, nil
	}
	metadata, err := json.Marshal(map[string]any{
		"name":     "Goi Backups",
		"mimeType": "application/vnd.google-apps.folder",
		"appProperties": map[string]string{
			"goi_type":           "backup_folder",
			installationProperty: g.config.InstallationID,
		},
	})
	if err != nil {
		return "", err
	}
	requestURL = g.config.APIURL + "/files?fields=id"
	response, err = g.authorizedRequest(ctx, func(token string) (*http.Request, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(metadata))
		if err == nil {
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json; charset=UTF-8")
		}
		return request, err
	})
	if err != nil {
		return "", err
	}
	if response.StatusCode != http.StatusOK {
		return "", googleResponseError(response, "create Google Drive backup folder")
	}
	var folder struct {
		ID string `json:"id"`
	}
	if err := decodeGoogleJSON(response, &folder); err != nil {
		return "", err
	}
	if !validDriveID(folder.ID) {
		return "", errors.New("Google Drive returned an invalid folder ID")
	}
	g.folderID = folder.ID
	return g.folderID, nil
}

func (g *GoogleDrive) clearBackupFolder(folderID string) {
	g.folderMu.Lock()
	if g.folderID == folderID {
		g.folderID = ""
	}
	g.folderMu.Unlock()
}

func (g *GoogleDrive) uploadContent(ctx context.Context, sessionURL string, file *os.File, size int64) (RemoteBackup, error) {
	offset := int64(0)
	for attempt := 0; attempt < 4; attempt++ {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return RemoteBackup{}, err
		}
		token, err := g.accessTokenFor(ctx)
		if err != nil {
			return RemoteBackup{}, err
		}
		length := size - offset
		request, err := http.NewRequestWithContext(ctx, http.MethodPut, sessionURL, io.NewSectionReader(file, offset, length))
		if err != nil {
			return RemoteBackup{}, err
		}
		request.ContentLength = length
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/zip")
		request.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, size-1, size))
		response, err := g.config.HTTPClient.Do(request)
		if err != nil {
			newOffset, completed, statusErr := g.queryUpload(ctx, sessionURL, size)
			if statusErr == nil && completed != nil {
				return *completed, nil
			}
			if statusErr == nil && newOffset >= offset && newOffset < size {
				offset = newOffset
			}
			continue
		}
		if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusCreated {
			return decodeCompletedUpload(response)
		}
		if response.StatusCode == http.StatusUnauthorized {
			response.Body.Close()
			g.invalidateAccessToken()
			continue
		}
		if response.StatusCode == 308 {
			newOffset := int64(0)
			var err error
			if received := response.Header.Get("Range"); received != "" {
				newOffset, err = uploadOffset(received)
			}
			response.Body.Close()
			if err != nil || newOffset < offset || newOffset > size {
				return RemoteBackup{}, errors.New("Google Drive returned an invalid upload position")
			}
			if newOffset == size {
				_, completed, err := g.queryUpload(ctx, sessionURL, size)
				if err != nil || completed == nil {
					return RemoteBackup{}, errors.New("Google Drive did not confirm the completed upload")
				}
				return *completed, nil
			}
			offset = newOffset
			continue
		}
		if response.StatusCode >= 500 {
			response.Body.Close()
			newOffset, completed, err := g.queryUpload(ctx, sessionURL, size)
			if err == nil && completed != nil {
				return *completed, nil
			}
			if err == nil && newOffset >= offset && newOffset < size {
				offset = newOffset
				continue
			}
			return RemoteBackup{}, errors.New("Google Drive upload was interrupted; the local backup was kept")
		}
		return RemoteBackup{}, googleResponseError(response, "upload Google Drive backup")
	}
	return RemoteBackup{}, errors.New("Google Drive upload was interrupted; the local backup was kept")
}

func (g *GoogleDrive) queryUpload(ctx context.Context, sessionURL string, size int64) (int64, *RemoteBackup, error) {
	token, err := g.accessTokenFor(ctx)
	if err != nil {
		return 0, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, sessionURL, http.NoBody)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
	request.ContentLength = 0
	response, err := g.config.HTTPClient.Do(request)
	if err != nil {
		return 0, nil, err
	}
	if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusCreated {
		completed, err := decodeCompletedUpload(response)
		if err != nil {
			return 0, nil, err
		}
		return size, &completed, nil
	}
	defer response.Body.Close()
	if response.StatusCode != 308 {
		return 0, nil, fmt.Errorf("upload status returned HTTP %d", response.StatusCode)
	}
	if response.Header.Get("Range") == "" {
		return 0, nil, nil
	}
	offset, err := uploadOffset(response.Header.Get("Range"))
	return offset, nil, err
}

func decodeCompletedUpload(response *http.Response) (RemoteBackup, error) {
	var completed RemoteBackup
	if err := decodeGoogleJSON(response, &completed); err != nil {
		return RemoteBackup{}, err
	}
	if !validDriveID(completed.ID) {
		return RemoteBackup{}, errors.New("Google Drive returned an invalid backup file ID")
	}
	return completed, nil
}

func uploadOffset(value string) (int64, error) {
	const prefix = "bytes=0-"
	if !strings.HasPrefix(value, prefix) {
		return 0, errors.New("missing upload range")
	}
	last, err := strconv.ParseInt(strings.TrimPrefix(value, prefix), 10, 64)
	if err != nil || last < 0 {
		return 0, errors.New("invalid upload range")
	}
	return last + 1, nil
}

func (g *GoogleDrive) authorizedRequest(ctx context.Context, create func(string) (*http.Request, error)) (*http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		token, err := g.accessTokenFor(ctx)
		if err != nil {
			return nil, err
		}
		request, err := create(token)
		if err != nil {
			return nil, err
		}
		response, err := g.config.HTTPClient.Do(request)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusUnauthorized || attempt == 1 {
			return response, nil
		}
		response.Body.Close()
		g.invalidateAccessToken()
	}
	return nil, errors.New("Google Drive authorization failed")
}

func (g *GoogleDrive) accessTokenFor(ctx context.Context) (string, error) {
	g.mu.Lock()
	if g.accessToken != "" && g.config.Now().Add(time.Minute).Before(g.accessExpiry) {
		token := g.accessToken
		g.mu.Unlock()
		return token, nil
	}
	refreshToken := g.refreshToken
	g.mu.Unlock()
	if refreshToken == "" {
		return "", errors.New("Google Drive is not connected")
	}
	values := url.Values{
		"client_id":     {g.config.ClientID},
		"client_secret": {g.config.ClientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}
	token, err := g.exchangeToken(ctx, values)
	if err != nil {
		return "", err
	}
	g.mu.Lock()
	if g.refreshToken != refreshToken {
		g.mu.Unlock()
		return "", errors.New("Google Drive was disconnected while authorization was refreshing")
	}
	g.accessToken = token.AccessToken
	g.accessExpiry = g.config.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	g.mu.Unlock()
	return token.AccessToken, nil
}

func (g *GoogleDrive) exchangeToken(ctx context.Context, values url.Values) (googleTokenResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, g.config.TokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return googleTokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := g.config.HTTPClient.Do(request)
	if err != nil {
		return googleTokenResponse{}, fmt.Errorf("request Google access token: %w", err)
	}
	var token googleTokenResponse
	if err := decodeGoogleJSON(response, &token); err != nil {
		return googleTokenResponse{}, err
	}
	if response.StatusCode != http.StatusOK || token.AccessToken == "" || token.ExpiresIn < 1 {
		message := token.Description
		if message == "" {
			message = token.Error
		}
		if message == "" {
			message = fmt.Sprintf("HTTP %d", response.StatusCode)
		}
		return googleTokenResponse{}, fmt.Errorf("Google authorization failed: %s", message)
	}
	return token, nil
}

func (g *GoogleDrive) invalidateAccessToken() {
	g.mu.Lock()
	g.accessToken = ""
	g.accessExpiry = time.Time{}
	g.mu.Unlock()
}

func googleResponseError(response *http.Response, action string) error {
	defer response.Body.Close()
	contents, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	var result struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(contents, &result)
	if result.Error.Message == "" {
		result.Error.Message = http.StatusText(response.StatusCode)
	}
	return fmt.Errorf("%s: %s", action, result.Error.Message)
}

func decodeGoogleJSON(response *http.Response, destination any) error {
	defer response.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode Google response: %w", err)
	}
	return nil
}

func readGoogleCredentials(path string) (storedGoogleCredentials, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return storedGoogleCredentials{}, nil
	}
	if err != nil {
		return storedGoogleCredentials{}, fmt.Errorf("inspect Google Drive credentials: %w", err)
	}
	if !info.Mode().IsRegular() {
		return storedGoogleCredentials{}, errors.New("Google Drive credentials are not a regular file")
	}
	if info.Size() > maxGoogleCredentialBytes {
		return storedGoogleCredentials{}, errors.New("Google Drive credential file is too large")
	}
	if info.Mode().Perm() != 0o600 {
		if err := os.Chmod(path, 0o600); err != nil {
			return storedGoogleCredentials{}, fmt.Errorf("secure Google Drive credentials: %w", err)
		}
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return storedGoogleCredentials{}, fmt.Errorf("read Google Drive credentials: %w", err)
	}
	if len(contents) > maxGoogleCredentialBytes {
		return storedGoogleCredentials{}, errors.New("Google Drive credential file is too large")
	}
	var credentials storedGoogleCredentials
	if err := json.Unmarshal(contents, &credentials); err != nil {
		return storedGoogleCredentials{}, fmt.Errorf("parse Google Drive credentials: %w", err)
	}
	return credentials, nil
}

func writeGoogleCredentials(path string, credentials storedGoogleCredentials) error {
	contents, err := json.Marshal(credentials)
	if err != nil {
		return err
	}
	if len(contents) > maxGoogleCredentialBytes {
		return errors.New("Google Drive credentials are too large")
	}
	return securefile.Write(path, contents)
}

func validDriveID(id string) bool {
	if len(id) < 1 || len(id) > 200 {
		return false
	}
	for _, character := range id {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}
