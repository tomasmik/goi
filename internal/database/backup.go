package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type pendingImport struct {
	ID     int64  `json:"id"`
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

func BackupWithImports(ctx context.Context, sourcePath, outputPath string) (string, error) {
	return createBackup(ctx, sourcePath, outputPath, true)
}

func exportPendingImports(ctx context.Context, snapshotPath, outputPath string) error {
	if err := os.RemoveAll(outputPath); err != nil {
		return fmt.Errorf("replace pending import backup directory: %w", err)
	}
	if err := os.MkdirAll(outputPath, 0o750); err != nil {
		return fmt.Errorf("create pending import backup directory: %w", err)
	}
	lock, err := AcquireLock(snapshotPath, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	db, err := Open(ctx, snapshotPath)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `
		SELECT id, archive_path
		FROM import_runs
		WHERE status = 'previewed'
		   OR (status = 'failed' AND archive_path <> '')
	`)
	if err != nil {
		return fmt.Errorf("load pending imports: %w", err)
	}
	imports := make([]pendingImport, 0)
	for rows.Next() {
		var item pendingImport
		var source string
		if err := rows.Scan(&item.ID, &source); err != nil {
			rows.Close()
			return fmt.Errorf("scan pending import: %w", err)
		}
		if source == "" {
			rows.Close()
			return fmt.Errorf("pending import %d has no archive", item.ID)
		}
		item.File = fmt.Sprintf("run-%d.apkg", item.ID)
		checksum, err := copyFile(ctx, source, filepath.Join(outputPath, item.File))
		if err != nil {
			rows.Close()
			return fmt.Errorf("copy pending import %d: %w", item.ID, err)
		}
		item.SHA256 = checksum
		imports = append(imports, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate pending imports: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close pending imports: %w", err)
	}
	if len(imports) == 0 {
		if err := os.RemoveAll(outputPath); err != nil {
			return fmt.Errorf("remove empty pending import backup: %w", err)
		}
		return nil
	}
	manifest, err := json.Marshal(imports)
	if err != nil {
		return fmt.Errorf("encode pending import manifest: %w", err)
	}
	if err := writeNewFileSynced(filepath.Join(outputPath, "manifest.json"), manifest, 0o640); err != nil {
		return fmt.Errorf("write pending import manifest: %w", err)
	}
	return nil
}

func createBackup(ctx context.Context, sourcePath, outputPath string, includeImports bool) (string, error) {
	if err := requireRegularFile(sourcePath); err != nil {
		return "", fmt.Errorf("inspect source database: %w", err)
	}
	if err := validateBackupPaths(sourcePath, outputPath, includeImports); err != nil {
		return "", fmt.Errorf("validate backup output: %w", err)
	}
	outputDirectory := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDirectory, 0o750); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}
	outputLock, err := acquireBackupOutputLock(outputPath)
	if err != nil {
		return "", err
	}
	defer outputLock.Close()
	workspace, err := os.MkdirTemp(outputDirectory, "."+filepath.Base(outputPath)+".backup-")
	if err != nil {
		return "", fmt.Errorf("create backup workspace: %w", err)
	}
	removeWorkspace := true
	defer func() {
		if removeWorkspace {
			_ = os.RemoveAll(workspace)
		}
	}()

	stagingDirectory := filepath.Join(workspace, "staged")
	if err := os.Mkdir(stagingDirectory, 0o750); err != nil {
		return "", fmt.Errorf("create backup staging directory: %w", err)
	}
	stagedPath := filepath.Join(stagingDirectory, filepath.Base(outputPath))
	checksum, err := createSnapshot(ctx, sourcePath, stagedPath)
	if err != nil {
		return "", err
	}
	if includeImports {
		if err := exportPendingImports(ctx, stagedPath, stagedPath+".imports"); err != nil {
			return "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := publishBackup(stagedPath, outputPath, includeImports); err != nil {
		removeWorkspace = false
		return "", fmt.Errorf("%w; recovery files remain in %s", err, workspace)
	}
	return checksum, nil
}

func createSnapshot(ctx context.Context, sourcePath, stagedPath string) (string, error) {
	lock, err := AcquireLock(sourcePath, false)
	if err != nil {
		return "", err
	}
	defer lock.Close()

	source, err := Open(ctx, sourcePath)
	if err != nil {
		return "", err
	}
	defer source.Close()
	if err := validateCurrentSchema(ctx, source); err != nil {
		return "", fmt.Errorf("validate source database: %w", err)
	}
	if err := IntegrityCheck(ctx, source); err != nil {
		return "", fmt.Errorf("check source database: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}
	// The SQLite driver keeps an interrupt statement active for cancelable
	// contexts, while VACUUM requires the connection to have no active statement.
	if _, err := source.ExecContext(context.WithoutCancel(ctx), "VACUUM INTO ?", stagedPath); err != nil {
		return "", fmt.Errorf("create database snapshot: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	verified, err := Open(ctx, stagedPath)
	if err != nil {
		return "", fmt.Errorf("open database snapshot: %w", err)
	}
	if err := IntegrityCheck(ctx, verified); err != nil {
		verified.Close()
		return "", fmt.Errorf("check database snapshot: %w", err)
	}
	if err := validateCurrentSchema(ctx, verified); err != nil {
		verified.Close()
		return "", fmt.Errorf("validate database snapshot: %w", err)
	}
	if err := verified.Close(); err != nil {
		return "", fmt.Errorf("close database snapshot: %w", err)
	}
	if err := syncFile(stagedPath); err != nil {
		return "", fmt.Errorf("sync database snapshot: %w", err)
	}

	checksum, err := fileChecksum(ctx, stagedPath)
	if err != nil {
		return "", err
	}
	if err := writeNewFileSynced(stagedPath+".sha256", []byte(checksum+"  "+filepath.Base(stagedPath)+"\n"), 0o640); err != nil {
		return "", fmt.Errorf("stage backup checksum: %w", err)
	}
	return checksum, nil
}

func publishBackup(stagedPath, outputPath string, includeImports bool) error {
	type artifact struct {
		name      string
		staged    string
		output    string
		required  bool
		previous  string
		movedOld  bool
		published bool
	}

	previousDirectory, err := os.MkdirTemp(filepath.Dir(stagedPath), ".previous-")
	if err != nil {
		return fmt.Errorf("create previous backup workspace: %w", err)
	}
	artifacts := []artifact{
		{name: "checksum", staged: stagedPath + ".sha256", output: outputPath + ".sha256", required: true, previous: filepath.Join(previousDirectory, "checksum")},
		{name: "database", staged: stagedPath, output: outputPath, required: true, previous: filepath.Join(previousDirectory, "database")},
	}
	if includeImports {
		artifacts = append([]artifact{{
			name:     "pending imports",
			staged:   stagedPath + ".imports",
			output:   outputPath + ".imports",
			previous: filepath.Join(previousDirectory, "imports"),
		}}, artifacts...)
	}

	for _, item := range artifacts {
		info, err := os.Stat(item.staged)
		if item.required && err != nil {
			return fmt.Errorf("inspect staged backup %s: %w", item.name, err)
		}
		if !item.required && err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect staged backup %s: %w", item.name, err)
		}
		if err == nil && item.name == "pending imports" && !info.IsDir() {
			return errors.New("staged pending imports are not a directory")
		}
		if err == nil && item.name != "pending imports" && !info.Mode().IsRegular() {
			return fmt.Errorf("staged backup %s is not a regular file", item.name)
		}
	}
	for _, item := range artifacts {
		info, err := os.Lstat(item.output)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect previous backup %s: %w", item.name, err)
		}
		if item.name == "pending imports" && !info.IsDir() {
			return errors.New("previous pending import backup is not a directory")
		}
		if item.name != "pending imports" && !info.Mode().IsRegular() {
			return fmt.Errorf("previous backup %s is not a regular file", item.name)
		}
	}

	rollback := func(cause error) error {
		var rollbackErrors []error
		for i := len(artifacts) - 1; i >= 0; i-- {
			item := &artifacts[i]
			if item.published {
				if err := os.Rename(item.output, item.staged); err != nil {
					rollbackErrors = append(rollbackErrors, fmt.Errorf("remove new %s: %w", item.name, err))
				}
			}
		}
		for i := len(artifacts) - 1; i >= 0; i-- {
			item := &artifacts[i]
			if item.movedOld {
				if err := os.Rename(item.previous, item.output); err != nil {
					rollbackErrors = append(rollbackErrors, fmt.Errorf("restore previous %s: %w", item.name, err))
				}
			}
		}
		if err := syncParentDirectories(outputPath, stagedPath); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
		return errors.Join(append([]error{cause}, rollbackErrors...)...)
	}

	for i := range artifacts {
		item := &artifacts[i]
		if _, err := os.Lstat(item.output); err == nil {
			if err := os.Rename(item.output, item.previous); err != nil {
				return rollback(fmt.Errorf("preserve previous backup %s: %w", item.name, err))
			}
			item.movedOld = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return rollback(fmt.Errorf("inspect previous backup %s: %w", item.name, err))
		}
	}
	for i := range artifacts {
		item := &artifacts[i]
		if _, err := os.Stat(item.staged); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return rollback(fmt.Errorf("inspect new backup %s: %w", item.name, err))
		}
		if err := os.Rename(item.staged, item.output); err != nil {
			return rollback(fmt.Errorf("publish backup %s: %w", item.name, err))
		}
		item.published = true
	}
	if err := syncDirectory(filepath.Dir(outputPath)); err != nil {
		return rollback(fmt.Errorf("sync backup directory: %w", err))
	}
	return nil
}
