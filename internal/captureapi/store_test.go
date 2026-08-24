package captureapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tomasmik/goi/internal/database"
)

func TestTokenLifecycle(t *testing.T) {
	ctx, db := openCaptureAPITestDatabase(t)
	store := NewStore(db)
	clock := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }

	created, err := store.Create(ctx, "  Reading laptop  ")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^goi_ext_v1_[A-Za-z0-9_-]{43}$`).MatchString(created.Plaintext) {
		t.Fatalf("token has unexpected format: %q", created.Plaintext)
	}
	if created.Name != "Reading laptop" || created.Prefix != created.Plaintext[:tokenPrefixLength] {
		t.Fatalf("created token = %#v", created)
	}

	digest := sha256.Sum256([]byte(created.Plaintext))
	var storedHash []byte
	var storedPrefix string
	if err := db.QueryRowContext(ctx, `SELECT token_hash, token_prefix FROM extension_tokens WHERE id = ?`, created.ID).Scan(&storedHash, &storedPrefix); err != nil {
		t.Fatal(err)
	}
	if string(storedHash) != string(digest[:]) || storedPrefix != created.Prefix {
		t.Fatalf("stored digest or prefix does not match created token")
	}
	var plaintextMatches int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM extension_tokens WHERE token_hash = ?`, []byte(created.Plaintext)).Scan(&plaintextMatches); err != nil {
		t.Fatal(err)
	}
	if plaintextMatches != 0 {
		t.Fatal("plaintext token was persisted")
	}

	authenticated, err := store.Authenticate(ctx, created.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if authenticated.ID != created.ID || authenticated.LastUsedAt == nil || !authenticated.LastUsedAt.Equal(clock) {
		t.Fatalf("authenticated token = %#v", authenticated)
	}

	clock = clock.Add(30 * time.Minute)
	if _, err := store.Authenticate(ctx, created.Plaintext); err != nil {
		t.Fatal(err)
	}
	listed, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].LastUsedAt == nil || !listed[0].LastUsedAt.Equal(created.CreatedAt) {
		t.Fatalf("tokens after recent reuse = %#v", listed)
	}

	clock = clock.Add(31 * time.Minute)
	if _, err := store.Authenticate(ctx, created.Plaintext); err != nil {
		t.Fatal(err)
	}
	listed, err = store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if listed[0].LastUsedAt == nil || !listed[0].LastUsedAt.Equal(clock) {
		t.Fatalf("last used time = %v, want %v", listed[0].LastUsedAt, clock)
	}

	if err := store.Revoke(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Hour)
	if err := store.Revoke(ctx, created.ID); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("second revoke error = %v", err)
	}
	if _, err := store.Authenticate(ctx, created.Plaintext); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("authenticate revoked token error = %v", err)
	}
	listed, err = store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("tokens after revoke = %#v", listed)
	}
}

func TestTokenStoreRejectsInvalidInput(t *testing.T) {
	ctx, db := openCaptureAPITestDatabase(t)
	store := NewStore(db)
	for _, name := range []string{"", " \n ", "bad\x00name", strings.Repeat("a", 101)} {
		if _, err := store.Create(ctx, name); !errors.Is(err, ErrInvalidTokenName) {
			t.Fatalf("Create(%q) error = %v", name, err)
		}
	}
	for _, plaintext := range []string{"", "goi_ext_v1_short", "goi_ext_v1_!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"} {
		if _, err := store.Authenticate(ctx, plaintext); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("Authenticate(%q) error = %v", plaintext, err)
		}
	}
	if err := store.Revoke(ctx, 999); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("Revoke missing token error = %v", err)
	}
}

func TestTokenTimesRemainValidWhenClockMovesBackward(t *testing.T) {
	ctx, db := openCaptureAPITestDatabase(t)
	store := NewStore(db)
	createdAt := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return createdAt }
	created, err := store.Create(ctx, "Laptop")
	if err != nil {
		t.Fatal(err)
	}

	store.now = func() time.Time { return createdAt.Add(-time.Hour) }
	authenticated, err := store.Authenticate(ctx, created.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if authenticated.LastUsedAt == nil || !authenticated.LastUsedAt.Equal(createdAt) {
		t.Fatalf("last used = %v, want %v", authenticated.LastUsedAt, createdAt)
	}
	if err := store.Revoke(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	listed, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("tokens = %#v", listed)
	}
}

func openCaptureAPITestDatabase(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx := t.Context()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "goi.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return ctx, db
}
