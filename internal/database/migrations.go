package database

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const (
	applicationID = 1196378417
	schemaVersion = 14
)

var errInvalidSchema = errors.New("invalid Goi database schema")

func Migrate(ctx context.Context, db *sql.DB) error {
	databaseApplicationID, err := migrationTargetApplicationID(ctx, db)
	if err != nil {
		return err
	}
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		db,
		files,
		goose.WithLogger(goose.NopLogger()),
	)
	if err != nil {
		return fmt.Errorf("initialize database migrations: %w", err)
	}
	currentVersion, targetVersion, err := provider.GetVersions(ctx)
	if err != nil {
		return fmt.Errorf("inspect database migration version: %w", err)
	}
	if currentVersion > targetVersion && databaseApplicationID == applicationID {
		return fmt.Errorf(
			"%w: database schema version %d is newer than the version %d supported by this release",
			errInvalidSchema,
			currentVersion,
			targetVersion,
		)
	}
	if currentVersion > 0 && databaseApplicationID != applicationID {
		return fmt.Errorf(
			"%w: database uses the retired pre-baseline schema (version %d); replace it with a fresh database or restore a backup created by this release",
			errInvalidSchema,
			currentVersion,
		)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("run database migrations: %w", err)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&databaseApplicationID); err != nil {
		return fmt.Errorf("verify database application ID: %w", err)
	}
	if databaseApplicationID != applicationID {
		return fmt.Errorf("database application ID is %d after migration, want %d", databaseApplicationID, applicationID)
	}
	return nil
}

func migrationTargetApplicationID(ctx context.Context, db *sql.DB) (int64, error) {
	var databaseApplicationID int64
	if err := db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&databaseApplicationID); err != nil {
		return 0, fmt.Errorf("inspect database application ID: %w", err)
	}
	if databaseApplicationID != 0 && databaseApplicationID != applicationID {
		return 0, fmt.Errorf(
			"%w: database application ID is %d, so this is not a Goi database",
			errInvalidSchema,
			databaseApplicationID,
		)
	}
	if databaseApplicationID == applicationID {
		return databaseApplicationID, nil
	}

	var objectName, objectType string
	err := db.QueryRowContext(ctx, `
		SELECT name, type
		FROM sqlite_schema
		WHERE name NOT LIKE 'sqlite_%'
		  AND name <> 'goose_db_version'
		ORDER BY name
		LIMIT 1`).Scan(&objectName, &objectType)
	if errors.Is(err, sql.ErrNoRows) {
		return databaseApplicationID, nil
	}
	if err != nil {
		return 0, fmt.Errorf("inspect database schema: %w", err)
	}
	return 0, fmt.Errorf(
		"%w: database has no Goi application ID but is not empty (%s %q exists); use a fresh database path",
		errInvalidSchema,
		objectType,
		objectName,
	)
}

func validateCurrentSchema(ctx context.Context, db *sql.DB) error {
	var databaseApplicationID int64
	if err := db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&databaseApplicationID); err != nil {
		return fmt.Errorf("inspect database application ID: %w", err)
	}
	if databaseApplicationID != applicationID {
		return fmt.Errorf("%w: database application ID is %d, want %d", errInvalidSchema, databaseApplicationID, applicationID)
	}

	var version int64
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(version_id), 0)
		FROM goose_db_version
		WHERE is_applied = 1`).Scan(&version); err != nil {
		return fmt.Errorf("inspect database schema version: %w", err)
	}
	if version != schemaVersion {
		return fmt.Errorf("%w: database schema version is %d, want %d", errInvalidSchema, version, schemaVersion)
	}
	return nil
}
