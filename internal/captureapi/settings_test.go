package captureapi

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestExtensionSettingsDownloadContainsLoadableManifest(t *testing.T) {
	router := chi.NewRouter()
	NewSettingsHandler(nil, nil, nil, "").Routes(router)
	request := httptest.NewRequest(http.MethodGet, "/settings/extension/download", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("download response = %d, Content-Type = %q", response.Code, response.Header().Get("Content-Type"))
	}
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	manifestFound := false
	licenseFound := false
	requiredFiles := map[string]bool{
		"goi-browser-extension/background/capture-delivery.js":        false,
		"goi-browser-extension/background/connection-manager.js":      false,
		"goi-browser-extension/background/youtube-transcript-page.js": false,
		"goi-browser-extension/player/player.html":                    false,
		"goi-browser-extension/player/player.css":                     false,
		"goi-browser-extension/player/player.js":                      false,
		"goi-browser-extension/player/player-state.js":                false,
		"goi-browser-extension/options/options.html":                  false,
		"goi-browser-extension/options/options.css":                   false,
		"goi-browser-extension/options/options.js":                    false,
	}
	for _, file := range archive.File {
		if file.Name == "goi-browser-extension/manifest.json" {
			manifestFound = true
		}
		if file.Name == "goi-browser-extension/LICENSE" {
			licenseFound = true
		}
		if _, required := requiredFiles[file.Name]; required {
			requiredFiles[file.Name] = true
		}
		if strings.Contains(file.Name, "README") || strings.Contains(file.Name, "/tests/") || strings.HasSuffix(file.Name, ".go") {
			t.Fatalf("development file included in extension download: %s", file.Name)
		}
	}
	if !manifestFound {
		t.Fatal("extension download does not contain manifest.json")
	}
	if !licenseFound {
		t.Fatal("extension download does not contain LICENSE")
	}
	for name, found := range requiredFiles {
		if !found {
			t.Errorf("extension download does not contain %s", name)
		}
	}
}
