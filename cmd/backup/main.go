package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/tomasmik/goi/internal/config"
	"github.com/tomasmik/goi/internal/database"
)

func main() {
	mode := flag.String("mode", "backup", "backup or restore")
	output := flag.String("output", "", "backup output path")
	input := flag.String("input", "", "restore input path")
	keep := flag.Int("keep", 7, "number of distinct backup files to retain")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *mode, *input, *output, *keep); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, mode, input, output string, keep int) error {
	switch mode {
	case "backup":
		if output == "" {
			return errors.New("-output is required for backup")
		}
		if keep < 1 {
			return errors.New("-keep must be at least one")
		}
	case "restore":
		if input == "" {
			return errors.New("-input is required for restore")
		}
	default:
		return fmt.Errorf("unsupported mode %q", mode)
	}

	cfg, err := config.LoadStorage()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	switch mode {
	case "backup":
		checksum, err := database.BackupWithImports(ctx, cfg.DatabasePath, output)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := database.PruneBackups(filepath.Dir(output), keep, cfg.DatabasePath); err != nil {
			return err
		}
		fmt.Println(checksum)
		return nil
	case "restore":
		return database.RestoreWithImports(ctx, input, cfg.DatabasePath, filepath.Join(cfg.DataDir, "imports"))
	}
	return nil
}
