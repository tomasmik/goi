package captureapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	tokenMarker       = "goi_ext_v1_"
	tokenSecretBytes  = 32
	tokenSecretLength = 43
	tokenPrefixLength = 19
	maxTokenNameRunes = 100
	lastUsedInterval  = time.Hour
)

var (
	ErrInvalidTokenName = errors.New("invalid extension token name")
	ErrUnauthorized     = errors.New("invalid extension token")
	ErrTokenNotFound    = errors.New("extension token not found")
)

type Store struct {
	db  *sql.DB
	now func() time.Time
}

type Token struct {
	ID         int64
	Name       string
	Prefix     string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

type CreatedToken struct {
	Token
	Plaintext string
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now}
}

func (s *Store) Create(ctx context.Context, name string) (CreatedToken, error) {
	name, err := cleanTokenName(name)
	if err != nil {
		return CreatedToken{}, err
	}
	plaintext, err := newToken()
	if err != nil {
		return CreatedToken{}, err
	}
	digest := sha256.Sum256([]byte(plaintext))
	createdAt := s.now().UTC().Truncate(time.Second)
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO extension_tokens (name, token_hash, token_prefix, created_at)
		VALUES (?, ?, ?, ?)`, name, digest[:], plaintext[:tokenPrefixLength], createdAt.Unix())
	if err != nil {
		return CreatedToken{}, fmt.Errorf("insert extension token: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return CreatedToken{}, fmt.Errorf("get extension token ID: %w", err)
	}
	return CreatedToken{
		Token: Token{
			ID:        id,
			Name:      name,
			Prefix:    plaintext[:tokenPrefixLength],
			CreatedAt: createdAt,
		},
		Plaintext: plaintext,
	}, nil
}

func (s *Store) List(ctx context.Context) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, token_prefix, created_at, last_used_at
		FROM extension_tokens
		ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list extension tokens: %w", err)
	}
	defer rows.Close()

	tokens := make([]Token, 0)
	for rows.Next() {
		token, err := scanToken(rows)
		if err != nil {
			return nil, fmt.Errorf("scan extension token: %w", err)
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate extension tokens: %w", err)
	}
	return tokens, nil
}

func (s *Store) Authenticate(ctx context.Context, plaintext string) (Token, error) {
	if !validToken(plaintext) {
		return Token{}, ErrUnauthorized
	}
	digest := sha256.Sum256([]byte(plaintext))
	token, err := scanToken(s.db.QueryRowContext(ctx, `
		SELECT id, name, token_prefix, created_at, last_used_at
		FROM extension_tokens
		WHERE token_hash = ?`, digest[:]))
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrUnauthorized
	}
	if err != nil {
		return Token{}, fmt.Errorf("authenticate extension token: %w", err)
	}

	usedAt := s.now().UTC().Truncate(time.Second)
	if usedAt.Before(token.CreatedAt) {
		usedAt = token.CreatedAt
	}
	if token.LastUsedAt != nil && usedAt.Before(*token.LastUsedAt) {
		usedAt = *token.LastUsedAt
	}
	if token.LastUsedAt == nil || usedAt.Sub(*token.LastUsedAt) >= lastUsedInterval {
		result, err := s.db.ExecContext(ctx, `
			UPDATE extension_tokens
			SET last_used_at = ?
			WHERE id = ?
			  AND (last_used_at IS NULL OR last_used_at <= ?)`,
			usedAt.Unix(), token.ID, usedAt.Add(-lastUsedInterval).Unix())
		if err != nil {
			return Token{}, fmt.Errorf("record extension token use: %w", err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return Token{}, fmt.Errorf("read extension token use count: %w", err)
		}
		if updated == 1 {
			token.LastUsedAt = timePointer(usedAt)
		} else {
			var exists int
			if err := s.db.QueryRowContext(ctx, `
				SELECT 1 FROM extension_tokens WHERE id = ?`, token.ID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
				return Token{}, ErrUnauthorized
			} else if err != nil {
				return Token{}, fmt.Errorf("verify extension token state: %w", err)
			}
		}
	}
	return token, nil
}

func (s *Store) Revoke(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM extension_tokens WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("revoke extension token: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read revoked extension token count: %w", err)
	}
	if updated == 0 {
		return ErrTokenNotFound
	}
	return nil
}

type tokenScanner interface {
	Scan(dest ...any) error
}

func scanToken(scanner tokenScanner) (Token, error) {
	var token Token
	var createdAt int64
	var lastUsedAt sql.NullInt64
	if err := scanner.Scan(&token.ID, &token.Name, &token.Prefix, &createdAt, &lastUsedAt); err != nil {
		return Token{}, err
	}
	token.CreatedAt = time.Unix(createdAt, 0).UTC()
	if lastUsedAt.Valid {
		token.LastUsedAt = timePointer(time.Unix(lastUsedAt.Int64, 0).UTC())
	}
	return token, nil
}

func cleanTokenName(name string) (string, error) {
	if !utf8.ValidString(name) {
		return "", ErrInvalidTokenName
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxTokenNameRunes {
		return "", ErrInvalidTokenName
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return "", ErrInvalidTokenName
		}
	}
	return name, nil
}

func newToken() (string, error) {
	secret := make([]byte, tokenSecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate extension token: %w", err)
	}
	return tokenMarker + base64.RawURLEncoding.EncodeToString(secret), nil
}

func validToken(token string) bool {
	if len(token) != len(tokenMarker)+tokenSecretLength || !strings.HasPrefix(token, tokenMarker) {
		return false
	}
	encoded := token[len(tokenMarker):]
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	return err == nil && len(decoded) == tokenSecretBytes && base64.RawURLEncoding.EncodeToString(decoded) == encoded
}

func timePointer(value time.Time) *time.Time {
	return &value
}
