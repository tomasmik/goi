package backups

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testInstallationID = "0123456789abcdef0123456789abcdef"

func TestGoogleDriveRequiresInstallationIDWhenConfigured(t *testing.T) {
	_, err := NewGoogleDrive(GoogleDriveConfig{
		ClientID: "client", ClientSecret: "secret", RedirectURL: "https://goi.example/callback",
		CredentialPath: filepath.Join(t.TempDir(), "google-drive.json"),
	})
	if err == nil || !strings.Contains(err.Error(), "installation ID") {
		t.Fatalf("NewGoogleDrive() error = %v", err)
	}
}

func TestGoogleDriveOAuthStoresOfflineCredentialPrivately(t *testing.T) {
	var tokenRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		tokenRequests++
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("grant_type") != "authorization_code" || r.FormValue("code") != "code1" {
			t.Fatalf("token form = %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"access1","expires_in":3600,"refresh_token":"refresh1"}`)
	}))
	defer server.Close()
	credentialPath := filepath.Join(t.TempDir(), "google-drive.json")
	drive, err := NewGoogleDrive(GoogleDriveConfig{
		ClientID: "client", ClientSecret: "secret", RedirectURL: "https://goi.example/settings/backups/google/callback",
		CredentialPath: credentialPath, InstallationID: testInstallationID,
		HTTPClient: server.Client(), AuthURL: server.URL + "/auth", TokenURL: server.URL + "/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	authorizationURL, err := drive.AuthorizationURL()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("scope") != googleDriveScope || query.Get("access_type") != "offline" || query.Get("prompt") != "consent" {
		t.Fatalf("authorization query = %v", query)
	}
	if err := drive.Connect(context.Background(), query.Get("state"), "code1"); err != nil {
		t.Fatal(err)
	}
	if !drive.Connected() || tokenRequests != 1 {
		t.Fatalf("connected = %v, token requests = %d", drive.Connected(), tokenRequests)
	}
	info, err := os.Stat(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %o", info.Mode().Perm())
	}
	contents, err := os.ReadFile(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "access1") || !strings.Contains(string(contents), "refresh1") {
		t.Fatalf("stored credentials = %s", contents)
	}
}

func TestGoogleDriveRejectsExpiredOAuthStateBeforeTokenExchange(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	drive, err := NewGoogleDrive(GoogleDriveConfig{
		ClientID: "client", ClientSecret: "secret", RedirectURL: "https://goi.example/callback",
		CredentialPath: filepath.Join(t.TempDir(), "google-drive.json"), InstallationID: testInstallationID,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	authorizationURL, err := drive.AuthorizationURL()
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(authorizationURL)
	now = now.Add(11 * time.Minute)
	if err := drive.Connect(context.Background(), parsed.Query().Get("state"), "code"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("Connect() error = %v", err)
	}
}

func TestGoogleDriveUploadsListsDownloadsAndDeletesAppFiles(t *testing.T) {
	var uploaded []byte
	var deleted bool
	var folderCreated bool
	var recoveryListed bool
	var currentListed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"access","expires_in":3600}`)
		case r.URL.Path == "/drive/files" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			query := r.URL.Query().Get("q")
			if strings.Contains(query, "backup_folder") {
				if !strings.Contains(query, "goi_installation") || !strings.Contains(query, testInstallationID) {
					t.Fatalf("folder query is not scoped to this installation: %s", query)
				}
				_, _ = io.WriteString(w, `{"files":[]}`)
			} else if strings.Contains(query, "goi_installation") {
				if !strings.Contains(query, testInstallationID) {
					t.Fatalf("retention query is not scoped to this installation: %s", query)
				}
				if strings.Contains(query, "in parents") {
					t.Fatalf("retention query is limited to one backup folder: %s", query)
				}
				if !strings.Contains(r.URL.Query().Get("fields"), "appProperties") {
					t.Fatalf("retention fields omit app properties: %s", r.URL.RawQuery)
				}
				currentListed = true
				_, _ = io.WriteString(w, `{"files":[{"id":"file1","name":"goi-test.goi-backup.zip","size":"6","createdTime":"2026-08-05T12:00:00Z","appProperties":{"goi_installation":"`+testInstallationID+`"}}]}`)
			} else {
				if !strings.Contains(query, "goi_backup") {
					t.Fatalf("recovery query does not require a Goi backup tag: %s", query)
				}
				if !strings.Contains(r.URL.Query().Get("fields"), "appProperties") {
					t.Fatalf("recovery fields omit app properties: %s", r.URL.RawQuery)
				}
				recoveryListed = true
				_, _ = io.WriteString(w, `{"files":[`+
					`{"id":"file1","name":"goi-current.goi-backup.zip","size":"6","createdTime":"2026-08-05T12:00:00Z","appProperties":{"goi_installation":"`+testInstallationID+`"}},`+
					`{"id":"file2","name":"goi-other.goi-backup.zip","size":"7","createdTime":"2026-08-05T11:00:00Z","appProperties":{"goi_installation":"fedcba9876543210fedcba9876543210"}}]}`)
			}
		case r.URL.Path == "/drive/files" && r.Method == http.MethodPost:
			metadata, _ := io.ReadAll(r.Body)
			if !bytes.Contains(metadata, []byte(`"goi_type":"backup_folder"`)) ||
				!bytes.Contains(metadata, []byte(`"goi_installation":"`+testInstallationID+`"`)) {
				t.Fatalf("folder metadata = %s", metadata)
			}
			folderCreated = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"folder1"}`)
		case r.URL.Path == "/upload/files" && r.Method == http.MethodPost:
			metadata, _ := io.ReadAll(r.Body)
			if !bytes.Contains(metadata, []byte(`"folder1"`)) ||
				!bytes.Contains(metadata, []byte(`"goi_backup":"1"`)) ||
				!bytes.Contains(metadata, []byte(`"goi_installation":"`+testInstallationID+`"`)) {
				t.Fatalf("upload metadata = %s", metadata)
			}
			w.Header().Set("Location", serverURL(r)+"/upload-session")
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/upload-session" && r.Method == http.MethodPut:
			uploaded, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"file1","name":"goi-test.goi-backup.zip","size":"6","createdTime":"2026-08-05T12:00:00Z"}`)
		case r.URL.Path == "/drive/files/file1" && r.Method == http.MethodGet && r.URL.Query().Get("alt") == "media":
			_, _ = io.WriteString(w, "remote")
		case r.URL.Path == "/drive/files/file1" && r.Method == http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	credentialPath := filepath.Join(t.TempDir(), "google-drive.json")
	if err := writeGoogleCredentials(credentialPath, storedGoogleCredentials{RefreshToken: "refresh"}); err != nil {
		t.Fatal(err)
	}
	drive, err := NewGoogleDrive(GoogleDriveConfig{
		ClientID: "client", ClientSecret: "secret", RedirectURL: "https://goi.example/callback",
		CredentialPath: credentialPath, InstallationID: testInstallationID,
		HTTPClient: server.Client(), TokenURL: server.URL + "/token",
		APIURL: server.URL + "/drive", UploadURL: server.URL + "/upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "goi-test"+BundleSuffix)
	if err := os.WriteFile(backupPath, []byte("bundle"), 0o640); err != nil {
		t.Fatal(err)
	}
	remote, err := drive.Upload(t.Context(), backupPath, filepath.Base(backupPath))
	if err != nil {
		t.Fatal(err)
	}
	if remote.ID != "file1" || string(uploaded) != "bundle" {
		t.Fatalf("remote = %+v, uploaded = %q", remote, uploaded)
	}
	files, err := drive.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].ID != "file1" || files[0].Size != 6 {
		t.Fatalf("files = %+v", files)
	}
	if !files[0].CurrentInstallation || files[1].CurrentInstallation {
		t.Fatalf("installation classification = %+v", files)
	}
	if files[0].AppProperties[installationProperty] != testInstallationID {
		t.Fatalf("installation property = %q", files[0].AppProperties[installationProperty])
	}
	currentFiles, err := drive.ListCurrent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(currentFiles) != 1 || currentFiles[0].ID != "file1" || !currentFiles[0].CurrentInstallation {
		t.Fatalf("current installation files = %+v", currentFiles)
	}
	var download bytes.Buffer
	if err := drive.Download(t.Context(), "file1", &download); err != nil {
		t.Fatal(err)
	}
	if download.String() != "remote" {
		t.Fatalf("download = %q", download.String())
	}
	if err := drive.Delete(t.Context(), "file1"); err != nil {
		t.Fatal(err)
	}
	if !deleted || !folderCreated || !recoveryListed || !currentListed {
		t.Fatalf(
			"deleted = %v, folder created = %v, recovery listed = %v, current listed = %v",
			deleted,
			folderCreated,
			recoveryListed,
			currentListed,
		)
	}
}

func TestGoogleDriveUploadRefreshesMissingCachedFolder(t *testing.T) {
	var uploadParents []string
	var folderQueries int
	var folderCreates int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"access","expires_in":3600}`)
		case r.URL.Path == "/drive/files" && r.Method == http.MethodGet:
			folderQueries++
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"files":[]}`)
		case r.URL.Path == "/drive/files" && r.Method == http.MethodPost:
			folderCreates++
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"folder-new"}`)
		case r.URL.Path == "/upload/files" && r.Method == http.MethodPost:
			var metadata struct {
				Parents []string `json:"parents"`
			}
			if err := json.NewDecoder(r.Body).Decode(&metadata); err != nil {
				t.Fatal(err)
			}
			if len(metadata.Parents) != 1 {
				t.Fatalf("upload parents = %v", metadata.Parents)
			}
			uploadParents = append(uploadParents, metadata.Parents[0])
			if metadata.Parents[0] == "folder-old" {
				http.Error(w, "cached folder no longer exists", http.StatusNotFound)
				return
			}
			w.Header().Set("Location", serverURL(r)+"/upload-session")
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/upload-session" && r.Method == http.MethodPut:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"file1","name":"goi-test.goi-backup.zip","size":"6","createdTime":"2026-08-05T12:00:00Z"}`)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()

	credentialPath := filepath.Join(t.TempDir(), "google-drive.json")
	if err := writeGoogleCredentials(credentialPath, storedGoogleCredentials{RefreshToken: "refresh"}); err != nil {
		t.Fatal(err)
	}
	drive, err := NewGoogleDrive(GoogleDriveConfig{
		ClientID: "client", ClientSecret: "secret", RedirectURL: "https://goi.example/callback",
		CredentialPath: credentialPath, InstallationID: testInstallationID,
		HTTPClient: server.Client(), TokenURL: server.URL + "/token",
		APIURL: server.URL + "/drive", UploadURL: server.URL + "/upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	drive.folderID = "folder-old"
	backupPath := filepath.Join(t.TempDir(), "goi-test"+BundleSuffix)
	if err := os.WriteFile(backupPath, []byte("bundle"), 0o640); err != nil {
		t.Fatal(err)
	}

	remote, err := drive.Upload(t.Context(), backupPath, filepath.Base(backupPath))
	if err != nil {
		t.Fatal(err)
	}
	if remote.ID != "file1" {
		t.Fatalf("remote = %+v", remote)
	}
	if len(uploadParents) != 2 || uploadParents[0] != "folder-old" || uploadParents[1] != "folder-new" {
		t.Fatalf("upload parents = %v", uploadParents)
	}
	if folderQueries != 1 || folderCreates != 1 {
		t.Fatalf("folder queries = %d, creates = %d", folderQueries, folderCreates)
	}
}

func TestGoogleDriveUploadRetriesWhenDriveReportsNoReceivedBytes(t *testing.T) {
	contentAttempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"access","expires_in":3600}`)
		case r.URL.Path == "/drive/files" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"files":[{"id":"folder1"}]}`)
		case r.URL.Path == "/upload/files" && r.Method == http.MethodPost:
			w.Header().Set("Location", serverURL(r)+"/upload-session")
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/upload-session" && r.Method == http.MethodPut:
			contentAttempts++
			if contentAttempts == 1 {
				w.WriteHeader(308)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"file1","name":"goi-test.goi-backup.zip","size":"6","createdTime":"2026-08-05T12:00:00Z"}`)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()

	credentialPath := filepath.Join(t.TempDir(), "google-drive.json")
	if err := writeGoogleCredentials(credentialPath, storedGoogleCredentials{RefreshToken: "refresh"}); err != nil {
		t.Fatal(err)
	}
	drive, err := NewGoogleDrive(GoogleDriveConfig{
		ClientID: "client", ClientSecret: "secret", RedirectURL: "https://goi.example/callback",
		CredentialPath: credentialPath, InstallationID: testInstallationID,
		HTTPClient: server.Client(), TokenURL: server.URL + "/token",
		APIURL: server.URL + "/drive", UploadURL: server.URL + "/upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "goi-test"+BundleSuffix)
	if err := os.WriteFile(backupPath, []byte("bundle"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := drive.Upload(t.Context(), backupPath, filepath.Base(backupPath)); err != nil {
		t.Fatal(err)
	}
	if contentAttempts != 2 {
		t.Fatalf("upload attempts = %d, want 2", contentAttempts)
	}
}

func TestGoogleDriveRejectsMalformedCompletedUploadResponses(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		query  bool
	}{
		{name: "direct completion without ID", status: http.StatusOK, body: `{}`},
		{name: "queried completion with malformed ID", status: http.StatusCreated, body: `{"id":"not/a/drive/id"}`, query: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			now := time.Now()
			drive := &GoogleDrive{
				config:      GoogleDriveConfig{HTTPClient: server.Client(), Now: func() time.Time { return now }},
				accessToken: "access", accessExpiry: now.Add(time.Hour),
			}
			var err error
			if test.query {
				_, _, err = drive.queryUpload(t.Context(), server.URL, 6)
			} else {
				path := filepath.Join(t.TempDir(), "bundle")
				if err := os.WriteFile(path, []byte("bundle"), 0o640); err != nil {
					t.Fatal(err)
				}
				file, openErr := os.Open(path)
				if openErr != nil {
					t.Fatal(openErr)
				}
				_, err = drive.uploadContent(t.Context(), server.URL, file, 6)
				file.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "invalid backup file ID") {
				t.Fatalf("completion error = %v", err)
			}
		})
	}
}

func TestGoogleDriveCredentialJSONExcludesUnknownSecrets(t *testing.T) {
	contents, err := json.Marshal(storedGoogleCredentials{RefreshToken: "refresh"})
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != `{"refresh_token":"refresh"}` {
		t.Fatalf("credential JSON = %s", contents)
	}
}

func TestReadGoogleCredentialsRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "google-drive.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxGoogleCredentialBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readGoogleCredentials(path); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("readGoogleCredentials error = %v, want size error", err)
	}
}

func TestWriteGoogleCredentialsRejectsOversizedValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "google-drive.json")
	err := writeGoogleCredentials(path, storedGoogleCredentials{
		RefreshToken: strings.Repeat("x", maxGoogleCredentialBytes),
	})
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("writeGoogleCredentials error = %v, want size error", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential file exists after rejected write: %v", err)
	}
}

func TestUploadOffsetRequiresAContiguousRangeFromZero(t *testing.T) {
	if offset, err := uploadOffset("bytes=0-41"); err != nil || offset != 42 {
		t.Fatalf("valid offset = %d, %v", offset, err)
	}
	for _, value := range []string{"41", "bytes=5-41", "items=0-41", "bytes=0-nope"} {
		if _, err := uploadOffset(value); err == nil {
			t.Fatalf("uploadOffset(%q) succeeded", value)
		}
	}
}

func TestGoogleDriveDisconnectKeepsMemoryConnectedWhenCredentialRemovalFails(t *testing.T) {
	credentialPath := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(credentialPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credentialPath, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	drive := &GoogleDrive{
		config:       GoogleDriveConfig{CredentialPath: credentialPath},
		refreshToken: "refresh",
		accessToken:  "access",
	}
	if err := drive.Disconnect(); err == nil {
		t.Fatal("Disconnect() succeeded while the credential path was not removable")
	}
	if !drive.Connected() {
		t.Fatal("failed disconnect cleared the in-memory credential")
	}
}

func TestGoogleDriveRefreshCannotReconnectAfterDisconnect(t *testing.T) {
	refreshStarted := make(chan struct{})
	finishRefresh := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(refreshStarted)
		<-finishRefresh
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"late-access","expires_in":3600}`)
	}))
	defer server.Close()

	credentialPath := filepath.Join(t.TempDir(), "google-drive.json")
	if err := writeGoogleCredentials(credentialPath, storedGoogleCredentials{RefreshToken: "refresh"}); err != nil {
		t.Fatal(err)
	}
	drive, err := NewGoogleDrive(GoogleDriveConfig{
		ClientID: "client", ClientSecret: "secret", RedirectURL: "https://goi.example/callback",
		CredentialPath: credentialPath, InstallationID: testInstallationID,
		HTTPClient: server.Client(), TokenURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := drive.accessTokenFor(t.Context())
		result <- err
	}()
	<-refreshStarted
	if err := drive.Disconnect(); err != nil {
		t.Fatal(err)
	}
	close(finishRefresh)
	if err := <-result; err == nil || !strings.Contains(err.Error(), "disconnected") {
		t.Fatalf("refresh after disconnect error = %v", err)
	}

	drive.mu.Lock()
	defer drive.mu.Unlock()
	if drive.refreshToken != "" || drive.accessToken != "" || !drive.accessExpiry.IsZero() {
		t.Fatalf("credentials revived after disconnect: refresh %q, access %q, expiry %v", drive.refreshToken, drive.accessToken, drive.accessExpiry)
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
