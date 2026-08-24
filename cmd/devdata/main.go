package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/tomasmik/goi/internal/database"
	"github.com/tomasmik/goi/internal/devdata"
	"github.com/tomasmik/goi/internal/srs"
)

const (
	defaultDataDirectory = "data/test"
	devdataMarker        = ".goi-devdata"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, defaultDataDirectory); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer, dataDirectory string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "lessons":
		if len(args) != 1 {
			return usageError()
		}
		return rebuild(ctx, dataDirectory, devdata.ScenarioLessons, output)
	case "reviews":
		if len(args) != 1 {
			return usageError()
		}
		return rebuild(ctx, dataDirectory, devdata.ScenarioReviews, output)
	case "mixed":
		if len(args) != 1 {
			return usageError()
		}
		return rebuild(ctx, dataDirectory, devdata.ScenarioMixed, output)
	case "qa":
		if len(args) != 1 {
			return usageError()
		}
		return rebuild(ctx, dataDirectory, devdata.ScenarioQA, output)
	case "list":
		if len(args) != 1 {
			return usageError()
		}
		return listWords(ctx, dataDirectory, output)
	case "due":
		flags := flag.NewFlagSet("due", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		count := flags.Int("count", 20, "number of words to make due")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return errors.New("usage: devdata due [-count 20]")
		}
		return makeDue(ctx, dataDirectory, *count, output)
	case "stage":
		flags := flag.NewFlagSet("stage", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		id := flags.Int64("id", 0, "vocabulary ID")
		stage := flags.Int("stage", -1, "SRS stage from 0 to 9")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return errors.New("usage: devdata stage -id ID -stage 0..9")
		}
		return setStage(ctx, dataDirectory, *id, *stage, output)
	case "unlearn":
		flags := flag.NewFlagSet("unlearn", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		id := flags.Int64("id", 0, "vocabulary ID")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return errors.New("usage: devdata unlearn -id ID")
		}
		return unlearn(ctx, dataDirectory, *id, output)
	case "help", "-h", "--help":
		_, err := fmt.Fprintln(output, usageText())
		return err
	default:
		return usageError()
	}
}

func rebuild(ctx context.Context, dataDirectory string, scenario devdata.Scenario, output io.Writer) error {
	databasePath, err := prepareDataDirectory(dataDirectory)
	if err != nil {
		return err
	}
	lock, err := database.AcquireLock(databasePath, true)
	if err != nil {
		return fmt.Errorf("lock test database for rebuild: %w", err)
	}
	defer lock.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := clearDataDirectory(filepath.Dir(databasePath), filepath.Base(databasePath)+".lock"); err != nil {
		return err
	}

	db, err := database.Open(ctx, databasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		return err
	}
	summary, err := devdata.Populate(ctx, db, scenario, time.Now().UTC())
	if err != nil {
		return err
	}
	return printSummary(output, databasePath, scenario, summary)
}

func listWords(ctx context.Context, dataDirectory string, output io.Writer) error {
	db, lock, databasePath, err := openTestDatabase(ctx, dataDirectory)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer db.Close()

	entries, err := devdata.List(ctx, db)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Database: %s\n", databasePath)
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tWORD\tSTATUS\tKNOWN ELSEWHERE\tSTAGE\tNAME\tDUE")
	for _, entry := range entries {
		stage := "-"
		if entry.HasStage {
			stage = strconv.Itoa(entry.Stage)
		}
		dueAt := "-"
		if entry.HasDue {
			dueAt = entry.DueAt.Format(time.RFC3339)
		}
		knownElsewhere := "-"
		if entry.KnownElsewhere {
			knownElsewhere = "yes"
		}
		fmt.Fprintf(writer, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			entry.ID, entry.Expression, entry.Status, knownElsewhere, stage, entry.StageName, dueAt)
	}
	return writer.Flush()
}

func makeDue(ctx context.Context, dataDirectory string, count int, output io.Writer) error {
	db, lock, _, err := openTestDatabase(ctx, dataDirectory)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer db.Close()

	updated, err := devdata.MakeDue(ctx, db, count, time.Now().UTC())
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "%d words due.\n", updated)
	return nil
}

func setStage(ctx context.Context, dataDirectory string, id int64, stage int, output io.Writer) error {
	db, lock, _, err := openTestDatabase(ctx, dataDirectory)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer db.Close()

	if err := devdata.SetStage(ctx, db, id, srs.Stage(stage), time.Now().UTC()); err != nil {
		return err
	}
	fmt.Fprintf(output, "Word %d set to stage %d.\n", id, stage)
	return nil
}

func unlearn(ctx context.Context, dataDirectory string, id int64, output io.Writer) error {
	db, lock, _, err := openTestDatabase(ctx, dataDirectory)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer db.Close()

	if err := devdata.Unlearn(ctx, db, id, time.Now().UTC()); err != nil {
		return err
	}
	fmt.Fprintf(output, "Word %d returned to lessons.\n", id)
	return nil
}

func openTestDatabase(ctx context.Context, dataDirectory string) (*sql.DB, *database.Lock, string, error) {
	databasePath, err := prepareDataDirectory(dataDirectory)
	if err != nil {
		return nil, nil, "", err
	}
	lock, err := database.AcquireLock(databasePath, false)
	if err != nil {
		return nil, nil, "", fmt.Errorf("lock test database: %w", err)
	}
	db, err := database.Open(ctx, databasePath)
	if err != nil {
		lock.Close()
		return nil, nil, "", err
	}
	if err := database.Migrate(ctx, db); err != nil {
		db.Close()
		lock.Close()
		return nil, nil, "", err
	}
	return db, lock, databasePath, nil
}

func prepareDataDirectory(dataDirectory string) (string, error) {
	if strings.TrimSpace(dataDirectory) == "" {
		return "", errors.New("test data directory must not be empty")
	}
	allowExisting := filepath.Clean(dataDirectory) == defaultDataDirectory
	absoluteDirectory, err := filepath.Abs(dataDirectory)
	if err != nil {
		return "", fmt.Errorf("resolve test data directory: %w", err)
	}
	root := filepath.VolumeName(absoluteDirectory) + string(filepath.Separator)
	workingDirectory, workingDirectoryErr := os.Getwd()
	if absoluteDirectory == root || (workingDirectoryErr == nil && absoluteDirectory == workingDirectory) {
		return "", fmt.Errorf("refuse broad test data directory %q", absoluteDirectory)
	}
	if info, err := os.Lstat(absoluteDirectory); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("test data directory must not be a symbolic link: %q", absoluteDirectory)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("test data path is not a directory: %q", absoluteDirectory)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect test data directory: %w", err)
	}
	if err := os.MkdirAll(absoluteDirectory, 0o750); err != nil {
		return "", fmt.Errorf("create test data directory: %w", err)
	}
	markerPath := filepath.Join(absoluteDirectory, devdataMarker)
	marker, err := os.Lstat(markerPath)
	if err == nil {
		if !marker.Mode().IsRegular() {
			return "", fmt.Errorf("test data marker is not a regular file: %q", markerPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect test data marker: %w", err)
	} else {
		entries, readErr := os.ReadDir(absoluteDirectory)
		if readErr != nil {
			return "", fmt.Errorf("read test data directory: %w", readErr)
		}
		if len(entries) != 0 && !allowExisting {
			return "", fmt.Errorf("refuse unmarked nonempty test data directory %q", absoluteDirectory)
		}
		if writeErr := os.WriteFile(markerPath, nil, 0o600); writeErr != nil {
			return "", fmt.Errorf("create test data marker: %w", writeErr)
		}
	}
	return filepath.Join(absoluteDirectory, "vocab.sqlite"), nil
}

func clearDataDirectory(dataDirectory, lockName string) error {
	info, err := os.Lstat(dataDirectory)
	if err != nil {
		return fmt.Errorf("inspect test data directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("refuse unsafe test data directory %q", dataDirectory)
	}
	entries, err := os.ReadDir(dataDirectory)
	if err != nil {
		return fmt.Errorf("read test data directory: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() == lockName || entry.Name() == devdataMarker {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dataDirectory, entry.Name())); err != nil {
			return fmt.Errorf("remove old test data %q: %w", entry.Name(), err)
		}
	}
	return nil
}

func printSummary(output io.Writer, databasePath string, scenario devdata.Scenario, summary devdata.Summary) error {
	_, err := fmt.Fprintf(output, `Scenario: %s
Database: %s
Vocabulary: %d
Lessons available: %d
Known elsewhere: %d
Due: %d
Future: %d
Burned: %d
Review sessions: %d
Mining captures: %d pending, %d accepted, %d discarded
Examples: %d
Media assets: %d
Run: APP_DATA_DIR=%q APP_AUTH_MODE=false go run ./cmd/server
`,
		scenario,
		databasePath,
		summary.Vocabulary,
		summary.LessonsAvailable,
		summary.KnownElsewhere,
		summary.Due,
		summary.Future,
		summary.Evergreen,
		summary.ReviewSessions,
		summary.PendingCaptures,
		summary.AcceptedCaptures,
		summary.DiscardedCaptures,
		summary.Examples,
		summary.MediaAssets,
		filepath.Dir(databasePath),
	)
	return err
}

func usageError() error {
	return errors.New(usageText())
}

func usageText() string {
	return `usage: devdata <command>

commands:
  lessons
  reviews
  mixed
  qa
  list
  due [-count 20]
  stage -id ID -stage 0..9
  unlearn -id ID`
}
