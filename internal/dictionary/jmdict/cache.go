package jmdict

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"

	"github.com/tomasmik/goi/internal/textnorm"
)

const cacheSchema = `
CREATE TABLE jmdict_meta (
    id INTEGER PRIMARY KEY CHECK (id = 1),
	cache_schema_version INTEGER NOT NULL,
    source_url TEXT NOT NULL,
    created TEXT NOT NULL,
    version TEXT NOT NULL,
    downloaded_at INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    etag TEXT NOT NULL,
    last_modified TEXT NOT NULL,
    entry_count INTEGER NOT NULL CHECK (entry_count > 0)
);
CREATE TABLE jmdict_entries (
    ent_seq INTEGER PRIMARY KEY,
    source_order INTEGER NOT NULL UNIQUE
);
CREATE TABLE jmdict_kanji (
    id INTEGER PRIMARY KEY,
    ent_seq INTEGER NOT NULL REFERENCES jmdict_entries(ent_seq) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    text TEXT NOT NULL,
    normalized_text TEXT NOT NULL,
    priority_rank INTEGER NOT NULL,
    UNIQUE (ent_seq, position),
    UNIQUE (ent_seq, text)
);
CREATE TABLE jmdict_readings (
    id INTEGER PRIMARY KEY,
    ent_seq INTEGER NOT NULL REFERENCES jmdict_entries(ent_seq) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    text TEXT NOT NULL,
    normalized_hiragana TEXT NOT NULL,
    no_kanji INTEGER NOT NULL CHECK (no_kanji IN (0, 1)),
    priority_rank INTEGER NOT NULL,
    UNIQUE (ent_seq, position),
    UNIQUE (ent_seq, text)
);
CREATE TABLE jmdict_reading_kanji (
    reading_id INTEGER NOT NULL REFERENCES jmdict_readings(id) ON DELETE CASCADE,
    kanji_id INTEGER NOT NULL REFERENCES jmdict_kanji(id) ON DELETE CASCADE,
    PRIMARY KEY (reading_id, kanji_id)
);
CREATE TABLE jmdict_senses (
    id INTEGER PRIMARY KEY,
    ent_seq INTEGER NOT NULL REFERENCES jmdict_entries(ent_seq) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    UNIQUE (ent_seq, position)
);
CREATE TABLE jmdict_sense_kanji (
    sense_id INTEGER NOT NULL REFERENCES jmdict_senses(id) ON DELETE CASCADE,
    kanji_id INTEGER NOT NULL REFERENCES jmdict_kanji(id) ON DELETE CASCADE,
    PRIMARY KEY (sense_id, kanji_id)
);
CREATE TABLE jmdict_sense_reading (
    sense_id INTEGER NOT NULL REFERENCES jmdict_senses(id) ON DELETE CASCADE,
    reading_id INTEGER NOT NULL REFERENCES jmdict_readings(id) ON DELETE CASCADE,
    PRIMARY KEY (sense_id, reading_id)
);
CREATE TABLE jmdict_glosses (
    id INTEGER PRIMARY KEY,
    sense_id INTEGER NOT NULL REFERENCES jmdict_senses(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    text TEXT NOT NULL,
    language TEXT NOT NULL,
    gloss_type TEXT NOT NULL,
    UNIQUE (sense_id, position)
);
CREATE TABLE jmdict_pos (
    sense_id INTEGER NOT NULL REFERENCES jmdict_senses(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    value TEXT NOT NULL,
    PRIMARY KEY (sense_id, position)
);`

const cacheIndexes = `
CREATE INDEX jmdict_kanji_lookup ON jmdict_kanji(normalized_text);
CREATE INDEX jmdict_reading_lookup ON jmdict_readings(normalized_hiragana);
CREATE INDEX jmdict_reading_pairs_by_kanji ON jmdict_reading_kanji(kanji_id, reading_id);
CREATE INDEX jmdict_senses_by_entry ON jmdict_senses(ent_seq, position);`

const unrankedPriority = 1000

var requiredCacheObjects = []struct {
	name string
	kind string
}{
	{name: "jmdict_meta", kind: "table"},
	{name: "jmdict_entries", kind: "table"},
	{name: "jmdict_kanji", kind: "table"},
	{name: "jmdict_readings", kind: "table"},
	{name: "jmdict_reading_kanji", kind: "table"},
	{name: "jmdict_senses", kind: "table"},
	{name: "jmdict_sense_kanji", kind: "table"},
	{name: "jmdict_sense_reading", kind: "table"},
	{name: "jmdict_glosses", kind: "table"},
	{name: "jmdict_pos", kind: "table"},
	{name: "jmdict_kanji_lookup", kind: "index"},
	{name: "jmdict_reading_lookup", kind: "index"},
	{name: "jmdict_reading_pairs_by_kanji", kind: "index"},
	{name: "jmdict_senses_by_entry", kind: "index"},
}

type Cache struct {
	db       *sql.DB
	metadata Metadata
}

func Build(ctx context.Context, reader io.ReadSeeker, path string, source Source) (metadata Metadata, returnErr error) {
	if err := ensureNewPrivateFile(path); err != nil {
		return Metadata{}, err
	}
	db, err := openSQLite(path, false)
	if err != nil {
		return Metadata{}, errors.Join(
			fmt.Errorf("open staging JMdict cache: %w", err),
			removeFile(path, "incomplete JMdict cache"),
		)
	}
	succeeded := false
	closed := false
	defer func() {
		var cleanupErrors []error
		if !closed {
			if err := db.Close(); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("close incomplete JMdict cache: %w", err))
			}
		}
		if !succeeded {
			if err := removeFile(path, "incomplete JMdict cache"); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
		returnErr = errors.Join(returnErr, errors.Join(cleanupErrors...))
	}()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Metadata{}, fmt.Errorf("begin JMdict import: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, cacheSchema); err != nil {
		return Metadata{}, fmt.Errorf("create JMdict cache schema: %w", err)
	}
	writer := cacheWriter{ctx: ctx, tx: tx}
	metadata, err = Parse(reader, writer.insertEntry)
	if err != nil {
		return Metadata{}, err
	}
	metadata.Source.URL = source.URL
	metadata.Source.DownloadedAt = source.DownloadedAt
	metadata.Source.SHA256 = source.SHA256
	metadata.Source.ETag = source.ETag
	metadata.Source.LastModified = source.LastModified
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO jmdict_meta (
			id, cache_schema_version, source_url, created, version, downloaded_at, sha256, etag, last_modified, entry_count
		) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		CacheSchemaVersion, metadata.URL, metadata.Created, metadata.Version, metadata.DownloadedAt.Unix(), metadata.SHA256,
		metadata.ETag, metadata.LastModified, metadata.EntryCount); err != nil {
		return Metadata{}, fmt.Errorf("insert JMdict metadata: %w", err)
	}
	if _, err := tx.ExecContext(ctx, cacheIndexes); err != nil {
		return Metadata{}, fmt.Errorf("index JMdict cache: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Metadata{}, fmt.Errorf("commit JMdict import: %w", err)
	}
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return Metadata{}, fmt.Errorf("check JMdict cache integrity: %w", err)
	}
	if integrity != "ok" {
		return Metadata{}, fmt.Errorf("JMdict cache integrity check returned %q", integrity)
	}
	closeErr := db.Close()
	closed = true
	if closeErr != nil {
		return Metadata{}, fmt.Errorf("close JMdict cache: %w", closeErr)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		return Metadata{}, fmt.Errorf("secure JMdict cache: %w", err)
	}
	if err := syncFile(path); err != nil {
		return Metadata{}, fmt.Errorf("sync JMdict cache: %w", err)
	}
	succeeded = true
	return metadata, nil
}

func Open(path string) (*Cache, error) {
	db, err := openSQLite(path, true)
	if err != nil {
		return nil, fmt.Errorf("open JMdict cache: %w", err)
	}
	cache := &Cache{db: db}
	metadata, err := cache.validate(context.Background())
	if err != nil {
		closeErr := db.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("close invalid JMdict cache: %w", closeErr)
		}
		return nil, errors.Join(fmt.Errorf("validate JMdict cache: %w", err), closeErr)
	}
	cache.metadata = metadata
	return cache, nil
}

func (c *Cache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

func (c *Cache) validate(ctx context.Context) (Metadata, error) {
	var integrity string
	if err := c.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil {
		return Metadata{}, fmt.Errorf("run integrity check: %w", err)
	}
	if integrity != "ok" {
		return Metadata{}, fmt.Errorf("integrity check returned %q", integrity)
	}

	for _, object := range requiredCacheObjects {
		var kind string
		err := c.db.QueryRowContext(ctx, "SELECT type FROM sqlite_schema WHERE name = ?", object.name).Scan(&kind)
		if errors.Is(err, sql.ErrNoRows) {
			return Metadata{}, fmt.Errorf("required %s %q is missing", object.kind, object.name)
		}
		if err != nil {
			return Metadata{}, fmt.Errorf("inspect required schema object %q: %w", object.name, err)
		}
		if kind != object.kind {
			return Metadata{}, fmt.Errorf("schema object %q is a %s, want %s", object.name, kind, object.kind)
		}
	}

	metadata, err := c.loadMetadata(ctx)
	if err != nil {
		return Metadata{}, err
	}
	var entryCount int
	if err := c.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM jmdict_entries").Scan(&entryCount); err != nil {
		return Metadata{}, fmt.Errorf("count JMdict entries: %w", err)
	}
	if entryCount != metadata.EntryCount {
		return Metadata{}, fmt.Errorf("JMdict entry count is %d, metadata records %d", entryCount, metadata.EntryCount)
	}

	rows, err := c.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return Metadata{}, fmt.Errorf("run foreign key check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowID any
		var foreignKeyID int
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			return Metadata{}, fmt.Errorf("scan foreign key check: %w", err)
		}
		return Metadata{}, fmt.Errorf(
			"foreign key check failed for table %q row %v referencing %q (constraint %d)",
			table,
			rowID,
			parent,
			foreignKeyID,
		)
	}
	if err := rows.Err(); err != nil {
		return Metadata{}, fmt.Errorf("iterate foreign key check: %w", err)
	}
	return metadata, nil
}

func (c *Cache) Metadata() Metadata {
	return c.metadata
}

func (c *Cache) loadMetadata(ctx context.Context) (Metadata, error) {
	var metadata Metadata
	var downloadedAt int64
	var schemaVersion int
	err := c.db.QueryRowContext(ctx, `
		SELECT cache_schema_version, source_url, created, version, downloaded_at, sha256, etag, last_modified, entry_count
		FROM jmdict_meta WHERE id = 1`).Scan(
		&schemaVersion, &metadata.URL, &metadata.Created, &metadata.Version, &downloadedAt, &metadata.SHA256,
		&metadata.ETag, &metadata.LastModified, &metadata.EntryCount,
	)
	if err != nil {
		return Metadata{}, err
	}
	metadata.DownloadedAt = unixTime(downloadedAt)
	if schemaVersion != CacheSchemaVersion || metadata.Version != Version || metadata.EntryCount <= 0 {
		return Metadata{}, errors.New("JMdict cache has invalid metadata")
	}
	return metadata, nil
}

type cacheWriter struct {
	ctx context.Context
	tx  *sql.Tx
}

func (w cacheWriter) insertEntry(entry Entry) error {
	if _, err := w.tx.ExecContext(w.ctx, `INSERT INTO jmdict_entries (ent_seq, source_order) VALUES (?, ?)`, entry.Sequence, entry.Order); err != nil {
		return fmt.Errorf("insert JMdict entry %d: %w", entry.Sequence, err)
	}
	kanjiIDs := make(map[string]int64, len(entry.Kanji))
	for index, item := range entry.Kanji {
		result, err := w.tx.ExecContext(w.ctx, `
			INSERT INTO jmdict_kanji (ent_seq, position, text, normalized_text, priority_rank)
			VALUES (?, ?, ?, ?, ?)`, entry.Sequence, index, item.Text, textnorm.Normalize(item.Text), priorityRank(item.Priorities))
		if err != nil {
			return fmt.Errorf("insert written form for JMdict entry %d: %w", entry.Sequence, err)
		}
		kanjiIDs[item.Text], err = result.LastInsertId()
		if err != nil {
			return fmt.Errorf("read written form ID: %w", err)
		}
	}
	readingIDs := make(map[string]int64, len(entry.Readings))
	for index, item := range entry.Readings {
		result, err := w.tx.ExecContext(w.ctx, `
			INSERT INTO jmdict_readings (ent_seq, position, text, normalized_hiragana, no_kanji, priority_rank)
			VALUES (?, ?, ?, ?, ?, ?)`, entry.Sequence, index, item.Text, normalizeReading(item.Text), boolInt(item.NoKanji), priorityRank(item.Priorities))
		if err != nil {
			return fmt.Errorf("insert reading for JMdict entry %d: %w", entry.Sequence, err)
		}
		readingID, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("read JMdict reading ID: %w", err)
		}
		readingIDs[item.Text] = readingID
		if item.NoKanji && len(item.Restricted) > 0 {
			return fmt.Errorf("JMdict entry %d reading %q is both no-kanji and restricted", entry.Sequence, item.Text)
		}
		if item.NoKanji || len(kanjiIDs) == 0 {
			continue
		}
		allowed := kanjiIDs
		if len(item.Restricted) > 0 {
			allowed = make(map[string]int64, len(item.Restricted))
			for _, text := range item.Restricted {
				id, ok := kanjiIDs[text]
				if !ok {
					return fmt.Errorf("JMdict entry %d reading restriction %q has no written form", entry.Sequence, text)
				}
				allowed[text] = id
			}
		}
		for _, id := range allowed {
			if _, err := w.tx.ExecContext(w.ctx, `INSERT INTO jmdict_reading_kanji (reading_id, kanji_id) VALUES (?, ?)`, readingID, id); err != nil {
				return fmt.Errorf("insert reading pair for JMdict entry %d: %w", entry.Sequence, err)
			}
		}
	}
	for _, sense := range entry.Senses {
		result, err := w.tx.ExecContext(w.ctx, `INSERT INTO jmdict_senses (ent_seq, position) VALUES (?, ?)`, entry.Sequence, sense.Number)
		if err != nil {
			return fmt.Errorf("insert sense for JMdict entry %d: %w", entry.Sequence, err)
		}
		senseID, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("read JMdict sense ID: %w", err)
		}
		for _, text := range sense.RestrictedKanji {
			id, ok := kanjiIDs[text]
			if !ok {
				return fmt.Errorf("JMdict entry %d sense restriction %q has no written form", entry.Sequence, text)
			}
			if _, err := w.tx.ExecContext(w.ctx, `INSERT INTO jmdict_sense_kanji (sense_id, kanji_id) VALUES (?, ?)`, senseID, id); err != nil {
				return fmt.Errorf("insert JMdict sense written restriction: %w", err)
			}
		}
		for _, text := range sense.RestrictedReadings {
			id, ok := readingIDs[text]
			if !ok {
				return fmt.Errorf("JMdict entry %d sense restriction %q has no reading", entry.Sequence, text)
			}
			if _, err := w.tx.ExecContext(w.ctx, `INSERT INTO jmdict_sense_reading (sense_id, reading_id) VALUES (?, ?)`, senseID, id); err != nil {
				return fmt.Errorf("insert JMdict sense reading restriction: %w", err)
			}
		}
		for index, gloss := range sense.Glosses {
			if _, err := w.tx.ExecContext(w.ctx, `
				INSERT INTO jmdict_glosses (sense_id, position, text, language, gloss_type)
				VALUES (?, ?, ?, ?, ?)`, senseID, index, gloss.Text, gloss.Language, gloss.Type); err != nil {
				return fmt.Errorf("insert JMdict gloss: %w", err)
			}
		}
		for index, partOfSpeech := range sense.PartsOfSpeech {
			if _, err := w.tx.ExecContext(w.ctx, `INSERT INTO jmdict_pos (sense_id, position, value) VALUES (?, ?, ?)`, senseID, index, partOfSpeech); err != nil {
				return fmt.Errorf("insert JMdict part of speech: %w", err)
			}
		}
	}
	return nil
}

func ensureNewPrivateFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create JMdict cache directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o640)
	if err != nil {
		return fmt.Errorf("create JMdict cache: %w", err)
	}
	if err := file.Close(); err != nil {
		return errors.Join(
			fmt.Errorf("close new JMdict cache: %w", err),
			removeFile(path, "incomplete JMdict cache"),
		)
	}
	return nil
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	return file.Close()
}

func openSQLite(path string, readOnly bool) (*sql.DB, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	databaseURL := url.URL{Scheme: "file", Path: filepath.ToSlash(absolutePath)}
	parameters := url.Values{}
	if readOnly {
		parameters.Set("mode", "ro")
	}
	databaseURL.RawQuery = strings.ReplaceAll(parameters.Encode(), "+", "%20")
	db, err := sqliteDriver.Open(databaseURL.String(), func(connection *sqlite3.Conn) error {
		if err := connection.Exec("PRAGMA foreign_keys = ON"); err != nil {
			return err
		}
		if !readOnly {
			if err := connection.Exec("PRAGMA journal_mode = DELETE"); err != nil {
				return err
			}
			if err := connection.Exec("PRAGMA synchronous = FULL"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func priorityRank(values []string) int {
	rank := unrankedPriority
	for _, value := range values {
		candidate := unrankedPriority
		switch value {
		case "news1", "ichi1", "spec1", "gai1":
			candidate = 0
		case "news2", "ichi2", "spec2", "gai2":
			candidate = 10
		default:
			if len(value) == 4 && value[0] == 'n' && value[1] == 'f' &&
				value[2] >= '0' && value[2] <= '9' && value[3] >= '0' && value[3] <= '9' {
				number := int(value[2]-'0')*10 + int(value[3]-'0')
				if number >= 1 && number <= 48 {
					candidate = 20 + number
				}
			}
		}
		if candidate < rank {
			rank = candidate
		}
	}
	return rank
}

func unixTime(value int64) time.Time {
	return time.Unix(value, 0).UTC()
}
