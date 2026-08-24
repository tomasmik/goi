package backups

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	internalweb "github.com/tomasmik/goi/internal/web"
)

func TestPendingRestoreGuardPausesChangesUntilRestartOrCancel(t *testing.T) {
	dataDir := t.TempDir()
	if err := writeTestPendingRestore(dataDir); err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	nextCalls := 0
	handler := PendingRestoreGuard(dataDir, renderer)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalls++
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, test := range []struct {
		method string
		path   string
		want   int
	}{
		{method: http.MethodGet, path: "/vocabulary", want: http.StatusNoContent},
		{method: http.MethodPost, path: "/settings/backups/restore/cancel", want: http.StatusNoContent},
		{method: http.MethodPost, path: "/login", want: http.StatusNoContent},
		{method: http.MethodPost, path: "/logout", want: http.StatusNoContent},
		{method: http.MethodPost, path: "/vocabulary", want: http.StatusConflict},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.want {
			t.Errorf("%s %s status = %d, want %d", test.method, test.path, response.Code, test.want)
		}
	}
	if nextCalls != 4 {
		t.Fatalf("next handler calls = %d, want 4", nextCalls)
	}
}

func TestPendingRestoreGuardReturnsExtensionError(t *testing.T) {
	dataDir := t.TempDir()
	if err := writeTestPendingRestore(dataDir); err != nil {
		t.Fatal(err)
	}
	renderer, err := internalweb.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	handler := PendingRestoreGuard(dataDir, renderer)(http.NotFoundHandler())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/extension/v1/captures", nil))

	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "60" || !strings.Contains(response.Body.String(), `"code":"restore_pending"`) {
		t.Fatalf("response = %d, %s", response.Code, response.Body.String())
	}
}

func writeTestPendingRestore(dataDir string) error {
	return os.WriteFile(filepath.Join(dataDir, pendingRestoreName), []byte("queued"), 0o600)
}
