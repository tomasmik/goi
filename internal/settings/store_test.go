package settings

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tomasmik/goi/internal/database"
)

func TestStoreUpdate(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "settings.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	values := Values{
		TimeZone:            "Asia/Tokyo",
		LessonWindowHours:   8,
		ExtraStudyLimit:     6,
		RetryCount:          2,
		ReviewMode:          "self_grade",
		ReviewOrder:         "stage_descending",
		ReviewCardOrder:     "spaced",
		ReviewAutoAdvance:   true,
		LeechFailureCount:   6,
		LeechSuspendCount:   4,
		LeechRecoveryStreak: 2,
		SixMonthReview:      true,
		Theme:               "system",
		AudioEnabled:        false,
	}
	if err := store.Update(ctx, values); err == nil {
		t.Fatal("Update() succeeded without a settings row")
	}
	if err := store.Ensure(ctx, "UTC"); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, values); err != nil {
		t.Fatal(err)
	}
	if err := store.Ensure(ctx, "UTC"); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != values {
		t.Fatalf("Get() = %+v, want %+v", got, values)
	}
}

func TestEnsureUsesConfiguredTimeZoneForNewSettings(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "settings-timezone.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	if err := store.Ensure(ctx, "Europe/Vilnius"); err != nil {
		t.Fatal(err)
	}
	values, err := store.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if values.TimeZone != "Europe/Vilnius" {
		t.Fatalf("time zone = %q, want Europe/Vilnius", values.TimeZone)
	}
}

func TestStoreRejectsInvalidReviewPreferences(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "settings-validation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	if err := store.Ensure(ctx, "UTC"); err != nil {
		t.Fatal(err)
	}
	valid, err := store.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		change func(*Values)
		want   string
	}{
		{name: "review mode", change: func(values *Values) { values.ReviewMode = "fast" }, want: "review mode"},
		{name: "review order", change: func(values *Values) { values.ReviewOrder = "oldest" }, want: "review order"},
		{name: "card order", change: func(values *Values) { values.ReviewCardOrder = "mixed" }, want: "review card order"},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := valid
			test.change(&values)
			err := store.Update(ctx, values)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Update() error = %v, want %q", err, test.want)
			}
		})
	}
}
