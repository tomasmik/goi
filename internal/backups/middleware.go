package backups

import (
	"encoding/json"
	"net/http"
	"strings"

	internalweb "github.com/tomasmik/goi/internal/web"
)

func PendingRestoreGuard(dataDir string, renderer *internalweb.Renderer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if safeDuringPendingRestore(r) {
				next.ServeHTTP(w, r)
				return
			}

			status, err := ReadRestoreStatus(dataDir)
			if err != nil {
				http.Error(w, "Could not verify whether Goi is ready for changes.", http.StatusServiceUnavailable)
				return
			}
			if status.State != "pending" {
				next.ServeHTTP(w, r)
				return
			}

			if strings.HasPrefix(r.URL.Path, "/api/") {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"code":  "restore_pending",
					"error": "restart Goi to apply the pending restore, or cancel it in backup settings",
				})
				return
			}

			renderer.RenderStatus(w, http.StatusConflict, "not-found.html", internalweb.NotFoundPage{
				Title:       "Restore waiting for restart",
				Heading:     "Restart or cancel the restore first",
				Message:     "Goi has paused changes so they are not lost when the backup is applied.",
				ReturnURL:   "/settings/backups",
				ReturnLabel: "Open backup settings",
			})
		})
	}
}

func safeDuringPendingRestore(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return r.URL.Path == "/settings/backups/restore/cancel" || r.URL.Path == "/login" || r.URL.Path == "/logout"
}
