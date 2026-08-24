package auth

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomasmik/goi/internal/database"
)

func openSessionTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "sessions.sqlite")
	db, err := database.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	if err := database.Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	return db
}

func TestSessionStorePersistsAcrossStoreInstances(t *testing.T) {
	db := openSessionTestDB(t)
	first := newSessionStore(db)
	if err := first.Commit("token", []byte("session-data"), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("commit session: %v", err)
	}
	second := newSessionStore(db)
	data, found, err := second.Find("token")
	if err != nil {
		t.Fatalf("find persisted session: %v", err)
	}
	if !found || string(data) != "session-data" {
		t.Fatalf("session = %q, found = %t", data, found)
	}
	var storedToken string
	if err := db.QueryRow("SELECT token FROM web_sessions").Scan(&storedToken); err != nil {
		t.Fatal(err)
	}
	if storedToken != sessionTokenDigest("token") {
		t.Fatalf("persisted session token = %q", storedToken)
	}
	if _, found, err := second.Find(storedToken); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("persisted token digest was accepted as a browser session token")
	}
}

func TestSessionStoreExpiresRowsOnLookup(t *testing.T) {
	db := openSessionTestDB(t)
	store := newSessionStore(db)
	if _, err := db.Exec(
		"INSERT INTO web_sessions (token, data, expiry_at) VALUES (?, ?, ?)",
		sessionTokenDigest("expired"),
		[]byte("data"),
		time.Now().Add(-time.Minute).Unix(),
	); err != nil {
		t.Fatalf("insert expired session: %v", err)
	}
	if _, found, err := store.Find("expired"); err != nil {
		t.Fatalf("find expired session: %v", err)
	} else if found {
		t.Fatal("expired session was found")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM web_sessions WHERE token = ?", sessionTokenDigest("expired")).Scan(&count); err != nil {
		t.Fatalf("count expired session: %v", err)
	}
	if count != 0 {
		t.Fatalf("expired session row count = %d, want 0", count)
	}
}

func TestSessionStoreHonorsCanceledContext(t *testing.T) {
	store := newSessionStore(openSessionTestDB(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := store.CommitCtx(ctx, "token", []byte("data"), time.Now().Add(time.Hour)); err == nil {
		t.Fatal("CommitCtx() succeeded with a canceled context")
	}
}

func TestSessionStoreCleansExpiredRowsInBoundedBatches(t *testing.T) {
	db := openSessionTestDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < sessionCleanupBatch+1; index++ {
		if _, err := tx.Exec(
			"INSERT INTO web_sessions (token, data, expiry_at) VALUES (?, ?, ?)",
			sessionTokenDigest(fmt.Sprintf("expired-%d", index)),
			[]byte("expired"),
			time.Now().Add(-time.Hour).Unix(),
		); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(
		"INSERT INTO web_sessions (token, data, expiry_at) VALUES (?, ?, ?)",
		sessionTokenDigest("active"),
		[]byte("active"),
		time.Now().Add(time.Hour).Unix(),
	); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	store := newSessionStore(db)
	data, found, err := store.Find("active")
	if err != nil {
		t.Fatal(err)
	}
	if !found || string(data) != "active" {
		t.Fatalf("active session = %q, found = %t", data, found)
	}

	var expired int
	if err := db.QueryRow("SELECT COUNT(*) FROM web_sessions WHERE expiry_at <= ?", time.Now().Unix()).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if expired != 1 {
		t.Fatalf("expired rows after cleanup = %d, want 1", expired)
	}
	if _, _, err := store.Find("active"); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.QueryRow("SELECT COUNT(*) FROM web_sessions WHERE expiry_at <= ?", time.Now().Unix()).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != expired {
		t.Fatalf("second cleanup ran before interval: rows = %d, want %d", remaining, expired)
	}
}
