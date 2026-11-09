package dashboard

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomasmik/goi/internal/database"
)

func TestDashboardWarnsOnlyForUnhealthyAutomaticBackups(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "backup-warning.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db, time.UTC, nil, nil, nil)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	for _, test := range []struct {
		name        string
		enabled     int
		status      string
		lastSuccess any
		errorText   string
		want        string
	}{
		{name: "disabled", enabled: 0, status: "failed", errorText: "disk full"},
		{name: "healthy", enabled: 1, status: "success", lastSuccess: now.Add(-24 * time.Hour).Unix()},
		{name: "failed", enabled: 1, status: "failed", lastSuccess: now.Add(-24 * time.Hour).Unix(), errorText: "disk full", want: "latest automatic backup failed"},
		{name: "overdue", enabled: 1, status: "success", lastSuccess: now.Add(-72 * time.Hour).Unix(), want: "Automatic backups are overdue"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := db.Exec(`UPDATE backup_settings SET enabled = ? WHERE id = 1`, test.enabled); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE backup_state SET status = ?, last_success_at = ?, error_message = ? WHERE id = 1`, test.status, test.lastSuccess, test.errorText); err != nil {
				t.Fatal(err)
			}
			var summary Summary
			if err := store.loadBackupWarning(ctx, now, time.UTC, &summary); err != nil {
				t.Fatal(err)
			}
			if test.want == "" && summary.BackupWarning != "" {
				t.Fatalf("warning = %q", summary.BackupWarning)
			}
			if test.want != "" && !strings.Contains(summary.BackupWarning, test.want) {
				t.Fatalf("warning = %q, want %q", summary.BackupWarning, test.want)
			}
		})
	}
}
