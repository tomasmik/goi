package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gabriel-vasile/mimetype"
)

const MaxUploadBytes int64 = 10 << 20

type Kind string

const (
	KindAudio Kind = "audio"
	KindImage Kind = "image"
)

type Upload struct {
	Kind        Kind
	MimeType    string
	Content     []byte
	SourceName  string
	SourceURL   string
	LicenseName string
	LicenseURL  string
}

type Content struct {
	MimeType  string
	CreatedAt time.Time
	Bytes     []byte
}

type invalidUploadError struct {
	message string
	cause   error
}

func (err invalidUploadError) Error() string {
	if err.cause == nil {
		return err.message
	}
	return fmt.Sprintf("%s: %v", err.message, err.cause)
}

func (err invalidUploadError) UserMessage() string {
	return err.message
}

func (err invalidUploadError) Unwrap() error {
	return err.cause
}

func ReadUpload(file multipart.File, header *multipart.FileHeader, kind Kind) (Upload, error) {
	content, err := io.ReadAll(io.LimitReader(file, MaxUploadBytes+1))
	if err != nil {
		return Upload{}, fmt.Errorf("read upload: %w", err)
	}
	if int64(len(content)) > MaxUploadBytes {
		return Upload{}, invalidUploadError{message: fmt.Sprintf("upload exceeds the %d byte limit", MaxUploadBytes)}
	}
	return Prepare(kind, header.Filename, content)
}

func Prepare(kind Kind, originalName string, content []byte) (Upload, error) {
	if int64(len(content)) > MaxUploadBytes {
		return Upload{}, invalidUploadError{message: fmt.Sprintf("upload exceeds the %d byte limit", MaxUploadBytes)}
	}

	detected := mimetype.Detect(content)
	if !isAllowed(kind, detected.String(), filepath.Ext(originalName)) {
		return Upload{}, invalidUploadError{message: fmt.Sprintf("unsupported %s media type %q", kind, detected.String())}
	}

	upload := Upload{
		Kind:     kind,
		MimeType: detected.String(),
		Content:  content,
	}
	if kind == KindImage {
		config, _, err := image.DecodeConfig(bytes.NewReader(content))
		if err != nil {
			return Upload{}, invalidUploadError{message: "image file could not be decoded", cause: err}
		}
		if config.Width <= 0 || config.Height <= 0 || config.Width > 8000 || config.Height > 8000 {
			return Upload{}, invalidUploadError{message: "image dimensions are outside the supported range"}
		}
	}
	return upload, nil
}

func SaveInTx(ctx context.Context, tx *sql.Tx, upload Upload, now time.Time) (int64, error) {
	hash := sha256.Sum256(upload.Content)
	checksum := hex.EncodeToString(hash[:])

	var id int64
	err := tx.QueryRowContext(
		ctx,
		"SELECT id FROM media WHERE kind = ? AND sha256 = ?",
		string(upload.Kind), checksum,
	).Scan(&id)
	if err == nil {
		if err := addAttribution(ctx, tx, id, upload); err != nil {
			return 0, err
		}
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("find duplicate media: %w", err)
	}

	result, err := tx.ExecContext(
		ctx,
		`INSERT INTO media (
			kind, mime_type, sha256, created_at,
			source_name, source_url, license_name, license_url
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(upload.Kind), upload.MimeType, checksum, now.Unix(),
		upload.SourceName, upload.SourceURL, upload.LicenseName, upload.LicenseURL,
	)
	if err != nil {
		return 0, fmt.Errorf("insert media metadata: %w", err)
	}
	id, err = result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get media ID: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO media_content (media_id, content) VALUES (?, ?)", id, upload.Content); err != nil {
		return 0, fmt.Errorf("insert media content: %w", err)
	}
	return id, nil
}

func addAttribution(ctx context.Context, tx *sql.Tx, mediaID int64, upload Upload) error {
	if upload.SourceName == "" && upload.SourceURL == "" && upload.LicenseName == "" && upload.LicenseURL == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE media SET
			source_name = ?, source_url = ?, license_name = ?, license_url = ?
		WHERE id = ?
		  AND source_name = '' AND source_url = ''
		  AND license_name = '' AND license_url = ''`,
		upload.SourceName, upload.SourceURL, upload.LicenseName, upload.LicenseURL, mediaID)
	if err != nil {
		return fmt.Errorf("add media attribution: %w", err)
	}
	return nil
}

func CollectUnusedInTx(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM media
		WHERE NOT EXISTS (SELECT 1 FROM vocabulary_media vm WHERE vm.media_id = media.id)
		  AND NOT EXISTS (SELECT 1 FROM mining_capture_media mcm WHERE mcm.media_id = media.id)`); err != nil {
		return fmt.Errorf("collect unreferenced media: %w", err)
	}
	return nil
}

func Load(ctx context.Context, db *sql.DB, id int64) (Content, error) {
	var content Content
	var createdAt int64
	err := db.QueryRowContext(ctx, `
		SELECT m.mime_type, m.created_at, mc.content
		FROM media m
		JOIN media_content mc ON mc.media_id = m.id
		WHERE m.id = ?`, id).Scan(
		&content.MimeType, &createdAt, &content.Bytes,
	)
	if err != nil {
		return Content{}, err
	}
	content.CreatedAt = time.Unix(createdAt, 0)
	return content, nil
}

func isAllowed(kind Kind, mimeType, name string) bool {
	mimeType = strings.ToLower(mimeType)
	extension := strings.ToLower(filepath.Ext(name))
	switch kind {
	case KindImage:
		return strings.HasPrefix(mimeType, "image/") && slices.Contains(
			[]string{".jpg", ".jpeg", ".png", ".gif"}, extension,
		)
	case KindAudio:
		isAudio := strings.HasPrefix(mimeType, "audio/") || mimeType == "video/webm"
		return isAudio && slices.Contains(
			[]string{".mp3", ".wav", ".ogg", ".oga", ".m4a", ".aac", ".flac", ".webm"}, extension,
		)
	default:
		return false
	}
}
