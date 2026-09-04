package jiten

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/ncruces/go-sqlite3"
	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"

	"github.com/tomasmik/goi/internal/kana"
	"github.com/tomasmik/goi/internal/textnorm"
)

const (
	maxCSVBytes        = 64 << 20
	maxRecords         = 2_000_000
	maxWordRunes       = 1024
	cacheApplicationID = 0x474f4946
)

const cacheSchema = `
CREATE TABLE frequency_sources (
    corpus TEXT PRIMARY KEY CHECK (corpus IN ('global', 'novel')),
    revision TEXT NOT NULL,
    downloaded_at INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    row_count INTEGER NOT NULL CHECK (row_count > 0)
);
CREATE TABLE frequency_ranks (
    corpus TEXT NOT NULL REFERENCES frequency_sources(corpus),
    written TEXT NOT NULL,
    reading TEXT NOT NULL,
    rank INTEGER NOT NULL CHECK (rank > 0 AND rank <= 2147483647),
    PRIMARY KEY (corpus, written, reading)
) WITHOUT ROWID;
PRAGMA application_id = 1196378438;
PRAGMA user_version = 1;`

type cache struct {
	db *sql.DB
}

func openCache(path string) (*cache, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o640)
	created := err == nil
	if created {
		if err := file.Close(); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute), RawQuery: "mode=rw"}
	db, err := sqliteDriver.Open(dsn.String(), func(conn *sqlite3.Conn) error {
		return conn.Exec("PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000; PRAGMA synchronous = FULL;")
	})
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	fail := func(err error) (*cache, error) { return nil, errors.Join(err, db.Close()) }
	if created {
		tx, err := db.Begin()
		if err != nil {
			return fail(err)
		}
		if _, err := tx.Exec(cacheSchema); err != nil {
			return fail(errors.Join(err, tx.Rollback()))
		}
		if err := tx.Commit(); err != nil {
			return fail(err)
		}
	}
	var applicationID, version int
	if err := db.QueryRow("PRAGMA application_id").Scan(&applicationID); err != nil {
		return fail(err)
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if applicationID != cacheApplicationID || version != 1 {
		return fail(errors.New("unrecognized Jiten cache; existing file was preserved"))
	}
	if _, err := db.Exec("SELECT corpus, written, reading, rank FROM frequency_ranks LIMIT 0"); err != nil {
		return fail(err)
	}
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode = WAL").Scan(&mode); err != nil {
		return fail(err)
	}
	if mode != "wal" {
		return fail(errors.New("Jiten cache requires WAL journal mode"))
	}
	return &cache{db: db}, nil
}

func (c *cache) sources(ctx context.Context) ([]SourceStatus, error) {
	rows, err := c.db.QueryContext(ctx, "SELECT corpus, revision, downloaded_at, sha256, row_count FROM frequency_sources")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sources []SourceStatus
	for rows.Next() {
		var source SourceStatus
		var downloaded int64
		if err := rows.Scan(&source.Corpus, &source.Revision, &downloaded, &source.SHA256, &source.RowCount); err != nil {
			return nil, err
		}
		source.Available = true
		source.DownloadedAt = time.Unix(downloaded, 0).UTC()
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

func (c *cache) importCSV(ctx context.Context, reader io.Reader, source SourceStatus) (SourceStatus, error) {
	if source.Corpus != Global && source.Corpus != Novel {
		return source, errors.New("invalid Jiten corpus")
	}
	limited := &io.LimitedReader{R: reader, N: maxCSVBytes + 1}
	buffer := bufio.NewReader(limited)
	if prefix, _ := buffer.Peek(3); string(prefix) == "\xef\xbb\xbf" {
		_, _ = buffer.Discard(3)
	}
	parser := csv.NewReader(buffer)
	parser.FieldsPerRecord = 3
	parser.ReuseRecord = true
	header, err := parser.Read()
	if err != nil {
		return source, fmt.Errorf("read Jiten CSV header: %w", err)
	}
	if len(header) != 3 || header[0] != "Word" || header[1] != "Form" || header[2] != "Rank" {
		return source, errors.New("invalid Jiten CSV header: expected Word,Form,Rank")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return source, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO frequency_sources (corpus, revision, downloaded_at, sha256, row_count)
		VALUES (?, ?, ?, ?, 1) ON CONFLICT(corpus) DO UPDATE SET
		revision = excluded.revision, downloaded_at = excluded.downloaded_at, sha256 = excluded.sha256`,
		source.Corpus, source.Revision, source.DownloadedAt.Unix(), source.SHA256); err != nil {
		return source, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM frequency_ranks WHERE corpus = ?", source.Corpus); err != nil {
		return source, err
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO frequency_ranks (corpus, written, reading, rank) VALUES (?, ?, ?, ?)
		ON CONFLICT(corpus, written, reading) DO UPDATE SET rank = min(rank, excluded.rank)`)
	if err != nil {
		return source, err
	}
	defer statement.Close()
	for record := 1; ; record++ {
		if err := ctx.Err(); err != nil {
			return source, err
		}
		row, err := parser.Read()
		if limited.N == 0 {
			return source, errors.New("Jiten CSV exceeds size limit")
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return source, fmt.Errorf("Jiten %s CSV record %d: %w", source.Corpus, record, err)
		}
		if record > maxRecords {
			return source, errors.New("Jiten CSV exceeds record limit")
		}
		if !utf8.ValidString(row[0]) || !utf8.ValidString(row[1]) ||
			utf8.RuneCountInString(row[0]) > maxWordRunes || utf8.RuneCountInString(row[1]) > maxWordRunes {
			return source, fmt.Errorf("Jiten %s CSV record %d: invalid word or reading", source.Corpus, record)
		}
		written, reading := textnorm.Normalize(row[0]), normalizeReading(row[1])
		rank, err := strconv.ParseInt(row[2], 10, 32)
		if err != nil || rank <= 0 || written == "" || reading == "" {
			return source, fmt.Errorf("Jiten %s CSV record %d: expected word, reading, and positive integer rank", source.Corpus, record)
		}
		// Identical forms can belong to several entries; their order in the export is not significant.
		if _, err := statement.ExecContext(ctx, source.Corpus, written, reading, rank); err != nil {
			return source, fmt.Errorf("insert Jiten %s CSV record %d: %w", source.Corpus, record, err)
		}
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM frequency_ranks WHERE corpus = ?", source.Corpus).Scan(&source.RowCount); err != nil {
		return source, err
	}
	if source.RowCount == 0 {
		return source, errors.New("Jiten CSV contains no ranks")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE frequency_sources SET row_count = ? WHERE corpus = ?", source.RowCount, source.Corpus); err != nil {
		return source, err
	}
	if err := tx.Commit(); err != nil {
		return source, err
	}
	source.Available = true
	return source, nil
}

func (c *cache) lookup(ctx context.Context, pairs []Pair) ([]Ranks, error) {
	result := make([]Ranks, len(pairs))
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, `SELECT
		(SELECT rank FROM frequency_ranks WHERE corpus = 'global' AND written = ?1 AND reading = ?2),
		(SELECT rank FROM frequency_ranks WHERE corpus = 'novel' AND written = ?1 AND reading = ?2)`)
	if err != nil {
		return nil, err
	}
	defer statement.Close()
	for index, pair := range pairs {
		written := textnorm.Normalize(pair.Written)
		if written == "" {
			written = textnorm.Normalize(pair.Reading)
		}
		if err := statement.QueryRowContext(ctx, written, normalizeReading(pair.Reading)).Scan(&result[index].Global, &result[index].Novel); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func normalizeReading(value string) string {
	return kana.ToHiragana(textnorm.Normalize(value))
}
