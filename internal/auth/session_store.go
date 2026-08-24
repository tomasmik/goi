package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/alexedwards/scs/v2"
)

type sessionStore struct {
	db            *sql.DB
	cleanupMu     sync.Mutex
	nextCleanupAt time.Time
}

const (
	sessionCleanupBatch    = 100
	sessionCleanupInterval = time.Minute
)

var _ scs.CtxStore = (*sessionStore)(nil)

func newSessionStore(db *sql.DB) *sessionStore {
	return &sessionStore{db: db}
}

func (s *sessionStore) Commit(token string, data []byte, expiry time.Time) error {
	return s.CommitCtx(context.Background(), token, data, expiry)
}

func (s *sessionStore) CommitCtx(ctx context.Context, token string, data []byte, expiry time.Time) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO web_sessions (token, data, expiry_at) VALUES (?, ?, ?)
		ON CONFLICT(token) DO UPDATE SET data = excluded.data, expiry_at = excluded.expiry_at`, sessionTokenDigest(token), data, expiry.Unix()); err != nil {
		return fmt.Errorf("commit session: %w", err)
	}
	s.maybeDeleteExpired(ctx)
	return nil
}

func (s *sessionStore) Find(token string) ([]byte, bool, error) {
	return s.FindCtx(context.Background(), token)
}

func (s *sessionStore) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	persistedToken := sessionTokenDigest(token)
	var data []byte
	var expiryAt int64
	err := s.db.QueryRowContext(ctx, `SELECT data, expiry_at FROM web_sessions WHERE token = ?`, persistedToken).Scan(&data, &expiryAt)
	if errors.Is(err, sql.ErrNoRows) {
		s.maybeDeleteExpired(ctx)
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("find session: %w", err)
	}
	now := time.Now().Unix()
	if expiryAt <= now {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM web_sessions WHERE token = ? AND expiry_at <= ?", persistedToken, now); err != nil {
			return nil, false, fmt.Errorf("delete expired session: %w", err)
		}
		s.maybeDeleteExpired(ctx)
		return nil, false, nil
	}
	s.maybeDeleteExpired(ctx)
	return data, true, nil
}

func (s *sessionStore) Delete(token string) error {
	return s.DeleteCtx(context.Background(), token)
}

func (s *sessionStore) DeleteCtx(ctx context.Context, token string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM web_sessions WHERE token = ?", sessionTokenDigest(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	s.maybeDeleteExpired(ctx)
	return nil
}

func (s *sessionStore) maybeDeleteExpired(ctx context.Context) {
	now := time.Now()
	s.cleanupMu.Lock()
	if now.Before(s.nextCleanupAt) {
		s.cleanupMu.Unlock()
		return
	}
	s.nextCleanupAt = now.Add(sessionCleanupInterval)
	s.cleanupMu.Unlock()

	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM web_sessions
		WHERE token IN (
			SELECT token FROM web_sessions
			WHERE expiry_at <= ?
			ORDER BY expiry_at
			LIMIT ?
		)`, now.Unix(), sessionCleanupBatch); err != nil {
		slog.WarnContext(ctx, "could not clean up expired web sessions", "error", err)
	}
}

func sessionTokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}
