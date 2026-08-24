package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/tomasmik/goi/internal/config"
)

func newHTTPServer(cfg config.Config, logger *slog.Logger, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       longRequestTimeout + time.Minute,
		WriteTimeout:      longRequestTimeout + time.Minute,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
}

func serve(
	ctx context.Context,
	stop context.CancelFunc,
	server *http.Server,
	waitForWorkers func(),
	logger *slog.Logger,
) error {
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("server listening", "addr", server.Addr)
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	var serveErr error
	select {
	case err := <-serverErr:
		stop()
		serveErr = fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	shutdownErr := server.Shutdown(shutdownCtx)
	cancel()
	if shutdownErr != nil {
		shutdownErr = fmt.Errorf("shutdown HTTP server: %w", shutdownErr)
		if err := server.Close(); err != nil {
			shutdownErr = errors.Join(shutdownErr, fmt.Errorf("close HTTP server: %w", err))
		}
	}
	waitForWorkers()
	return errors.Join(serveErr, shutdownErr)
}
