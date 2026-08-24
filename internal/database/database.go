package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/ncruces/go-sqlite3"
	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

func Open(ctx context.Context, path string) (*sql.DB, error) {
	dataSource, databasePath, err := sqliteDataSource(path)
	if err != nil {
		return nil, fmt.Errorf("resolve sqlite database path: %w", err)
	}
	if err := prepareDatabaseFiles(databasePath); err != nil {
		return nil, fmt.Errorf("prepare sqlite database: %w", err)
	}
	db, err := sqliteDriver.Open(dataSource, func(conn *sqlite3.Conn) error {
		pragmas := []string{
			"PRAGMA foreign_keys = ON",
			"PRAGMA journal_mode = WAL",
			"PRAGMA synchronous = FULL",
			"PRAGMA busy_timeout = 5000",
		}
		for _, pragma := range pragmas {
			if err := conn.Exec(pragma); err != nil {
				return fmt.Errorf("configure sqlite with %q: %w", pragma, err)
			}
		}
		// Create persistent sidecars up front so SQLite keeps their private permissions.
		if _, err := conn.FileControl("", sqlite3.FCNTL_PERSIST_WAL, true); err != nil {
			return fmt.Errorf("keep sqlite WAL file: %w", err)
		}
		if err := createPrivateFileIfMissing(databasePath + "-wal"); err != nil {
			return err
		}
		if err := createPrivateFileIfMissing(databasePath + "-shm"); err != nil {
			return err
		}
		return tightenDatabasePermissions(databasePath)
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	// One connection is enough for a single-user app and keeps SQLite pragmas consistent.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("ping sqlite database: %w", err), db.Close())
	}
	if err := tightenDatabasePermissions(databasePath); err != nil {
		return nil, errors.Join(fmt.Errorf("secure sqlite database: %w", err), db.Close())
	}
	return db, nil
}

func sqliteDataSource(path string) (string, string, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	databaseURL := url.URL{Scheme: "file", Path: filepath.ToSlash(absolutePath)}
	parameters := url.Values{}
	parameters.Set("modeof", absolutePath)
	// SQLite URI parameters do not decode '+' as a space.
	databaseURL.RawQuery = strings.ReplaceAll(parameters.Encode(), "+", "%20")
	return databaseURL.String(), absolutePath, nil
}

func prepareDatabaseFiles(path string) error {
	for _, filePath := range []string{path, path + "-wal", path + "-shm"} {
		if err := createPrivateFileIfMissing(filePath); err != nil {
			return err
		}
	}
	return tightenDatabasePermissions(path)
}

func createPrivateFileIfMissing(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o640)
	if err == nil {
		if err := file.Close(); err != nil {
			removeErr := os.Remove(path)
			if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				removeErr = fmt.Errorf("remove incomplete database file: %w", removeErr)
			} else {
				removeErr = nil
			}
			return errors.Join(fmt.Errorf("close new database file: %w", err), removeErr)
		}
		return nil
	}
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}

func tightenDatabasePermissions(path string) error {
	for _, filePath := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(filePath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%q is not a regular file", filePath)
		}
		currentMode := info.Mode().Perm()
		restrictedMode := currentMode & 0o640
		if restrictedMode != currentMode {
			if err := os.Chmod(filePath, restrictedMode); err != nil {
				return err
			}
		}
	}
	return nil
}
