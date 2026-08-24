package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/tomasmik/goi/internal/backups"
	"github.com/tomasmik/goi/internal/config"
	"github.com/tomasmik/goi/internal/database"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := openApplication(ctx, cfg, logger)
	if err != nil {
		return err
	}
	waitForWorkers := app.startWorkers(ctx, logger)
	serveErr := serve(ctx, stop, newHTTPServer(cfg, logger, app.handler(cfg, logger)), waitForWorkers, logger)
	return errors.Join(serveErr, app.Close())
}

func pendingRestoreStartupError(restored bool, err error) error {
	if errors.Is(err, backups.ErrPendingRestoreRetry) || errors.Is(err, database.ErrDatabaseInUse) {
		return fmt.Errorf("apply pending restore before starting server: %w", err)
	}
	if restored && err != nil {
		return fmt.Errorf("finish applied pending restore before starting server: %w", err)
	}
	return nil
}
