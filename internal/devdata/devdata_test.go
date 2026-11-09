package devdata_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomasmik/goi/internal/database"
	"github.com/tomasmik/goi/internal/devdata"
)

var fixtureNow = time.Now().UTC().Truncate(time.Second)

func TestPopulateScenarios(t *testing.T) {
	tests := []struct {
		scenario devdata.Scenario
		want     devdata.Summary
	}{
		{
			scenario: devdata.ScenarioLessons,
			want:     devdata.Summary{Vocabulary: 20, LessonsAvailable: 20},
		},
		{
			scenario: devdata.ScenarioReviews,
			want:     devdata.Summary{Vocabulary: 20, Due: 19, Evergreen: 1, ReviewSessions: 7},
		},
		{
			scenario: devdata.ScenarioMixed,
			want:     devdata.Summary{Vocabulary: 20, LessonsAvailable: 10, Due: 6, Future: 3, Evergreen: 1, ReviewSessions: 7},
		},
		{
			scenario: devdata.ScenarioQA,
			want: devdata.Summary{
				Vocabulary: 24, LessonsAvailable: 10, KnownElsewhere: 3,
				Due: 7, Future: 3, Evergreen: 1, ReviewSessions: 7,
				PendingCaptures: 3, AcceptedCaptures: 1, DiscardedCaptures: 1,
				Examples: 3, MediaAssets: 2,
			},
		},
	}
	for _, test := range tests {
		t.Run(string(test.scenario), func(t *testing.T) {
			ctx, db := openTestDatabase(t)
			got, err := devdata.Populate(ctx, db, test.scenario, fixtureNow)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("summary = %+v, want %+v", got, test.want)
			}
		})
	}
}

func openTestDatabase(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "devdata.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return ctx, db
}
