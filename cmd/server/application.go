package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/justinas/nosurf"

	"github.com/tomasmik/goi/internal/auth"
	"github.com/tomasmik/goi/internal/backups"
	"github.com/tomasmik/goi/internal/captureapi"
	"github.com/tomasmik/goi/internal/config"
	"github.com/tomasmik/goi/internal/coverage"
	"github.com/tomasmik/goi/internal/dashboard"
	"github.com/tomasmik/goi/internal/database"
	"github.com/tomasmik/goi/internal/dictionary/jmdict"
	"github.com/tomasmik/goi/internal/examplegen"
	appimports "github.com/tomasmik/goi/internal/imports"
	"github.com/tomasmik/goi/internal/lessons"
	"github.com/tomasmik/goi/internal/media"
	"github.com/tomasmik/goi/internal/mining"
	"github.com/tomasmik/goi/internal/reviews"
	"github.com/tomasmik/goi/internal/settings"
	"github.com/tomasmik/goi/internal/statistics"
	"github.com/tomasmik/goi/internal/vocabulary"
	internalweb "github.com/tomasmik/goi/internal/web"
	appweb "github.com/tomasmik/goi/web"
)

type application struct {
	db               *sql.DB
	databaseLock     *database.Lock
	renderer         *internalweb.Renderer
	dictionary       *jmdict.Manager
	exampleGenerator *examplegen.Manager
	vocabulary       *vocabulary.Store
	mining           *mining.Store
	reviews          *reviews.Store
	lessons          *lessons.Store
	statistics       *statistics.Store
	settings         *settings.Store
	extensionTokens  *captureapi.Store
	coverage         *coverage.Analyzer
	googleDrive      *backups.GoogleDriveManager
	backupStore      *backups.Store
	backupService    *backups.Service
	anki             *appimports.Store
}

func openApplication(ctx context.Context, cfg config.Config, logger *slog.Logger) (_ *application, returnErr error) {
	if err := prepareStorage(ctx, cfg, logger); err != nil {
		return nil, err
	}
	app := &application{}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, app.Close())
		}
	}()

	var err error
	app.databaseLock, err = database.AcquireLock(cfg.DatabasePath, false)
	if err != nil {
		return nil, err
	}
	app.db, err = database.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return nil, err
	}
	if err := database.Migrate(ctx, app.db); err != nil {
		return nil, err
	}
	if err := app.initializeCore(ctx, cfg, logger); err != nil {
		return nil, err
	}
	if err := app.initializeBackups(cfg, logger); err != nil {
		return nil, err
	}
	app.anki, err = appimports.NewStore(ctx, app.db, filepath.Join(cfg.DataDir, "imports"), app.vocabulary)
	if err != nil {
		return nil, err
	}
	return app, nil
}

func prepareStorage(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DatabasePath), 0o750); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	if err := backups.PrepareLocalDirectory(cfg.BackupDir); err != nil {
		return fmt.Errorf("prepare backup directory: %w", err)
	}
	restored, restoreErr := backups.ApplyPendingRestore(ctx, cfg.DataDir, cfg.DatabasePath, time.Now())
	if err := pendingRestoreStartupError(restored, restoreErr); err != nil {
		return err
	}
	if restoreErr != nil {
		logger.Error("pending restore was not applied; continuing with current data", "error", restoreErr)
	} else if restored {
		logger.Info("pending restore applied")
	}
	return nil
}

func (app *application) initializeCore(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	app.vocabulary = vocabulary.NewStore(app.db)
	app.mining = mining.NewStore(app.db)
	app.reviews = reviews.NewStore(app.db)
	app.lessons = lessons.NewStore(app.db)
	app.statistics = statistics.NewStore(app.db, cfg.TimeZone)
	app.settings = settings.NewStore(app.db)
	if err := app.settings.Ensure(ctx, cfg.TimeZoneName); err != nil {
		return err
	}
	app.extensionTokens = captureapi.NewStore(app.db)
	var err error
	app.coverage, err = coverage.NewAnalyzer(app.vocabulary)
	if err != nil {
		return fmt.Errorf("initialize coverage analyzer: %w", err)
	}

	app.dictionary, err = jmdict.NewManager(jmdict.ManagerConfig{Path: filepath.Join(cfg.DataDir, "jmdict.sqlite")})
	if err != nil {
		return fmt.Errorf("initialize JMdict: %w", err)
	}
	if !app.dictionary.Status().Available {
		logger.Info("preparing local dictionary")
		if err := app.dictionary.Refresh(ctx); err != nil && !errors.Is(err, jmdict.ErrNotModified) {
			logger.Warn("local dictionary is unavailable", "error", err)
		}
	}
	app.renderer, err = internalweb.NewRenderer()
	if err != nil {
		return fmt.Errorf("parse templates: %w", err)
	}
	app.exampleGenerator, err = examplegen.NewManager(
		filepath.Join(cfg.DataDir, examplegen.SettingsFilename),
		examplegen.ProviderSettings{BaseURL: cfg.LLMBaseURL, Model: cfg.LLMModel, APIKey: cfg.LLMAPIKey},
	)
	if err != nil {
		return fmt.Errorf("initialize example generator: %w", err)
	}
	return nil
}

func (app *application) initializeBackups(cfg config.Config, logger *slog.Logger) error {
	installationID, err := backups.LoadOrCreateInstallationID(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("initialize backup installation ID: %w", err)
	}
	app.googleDrive, err = backups.NewGoogleDriveManager(backups.GoogleDriveManagerConfig{
		Drive: backups.GoogleDriveConfig{
			ClientID:       cfg.GoogleDriveClientID,
			ClientSecret:   cfg.GoogleDriveClientSecret,
			RedirectURL:    cfg.BaseURL + "/settings/backups/google/callback",
			CredentialPath: filepath.Join(cfg.DataDir, "google-drive.json"),
			InstallationID: installationID,
		},
		ClientConfigPath: filepath.Join(cfg.DataDir, backups.GoogleDriveClientFilename),
	})
	if err != nil {
		return fmt.Errorf("initialize Google Drive backups: %w", err)
	}
	app.backupStore = backups.NewStore(app.db)
	app.backupService = backups.NewService(backups.ServiceConfig{
		DataDir:      cfg.DataDir,
		DatabasePath: cfg.DatabasePath,
		BackupDir:    cfg.BackupDir,
		Store:        app.backupStore,
		Drive:        app.googleDrive,
		Logger:       logger,
	})
	return nil
}

func (app *application) Close() error {
	var dictionaryErr error
	if app.dictionary != nil {
		dictionaryErr = app.dictionary.Close()
	}
	var databaseErr error
	if app.db != nil {
		databaseErr = app.db.Close()
	}
	var lockErr error
	if app.databaseLock != nil {
		lockErr = app.databaseLock.Close()
	}
	return errors.Join(dictionaryErr, databaseErr, lockErr)
}

func (app *application) handler(cfg config.Config, logger *slog.Logger) http.Handler {
	router := chi.NewRouter()
	router.Use(backups.PendingRestoreGuard(cfg.DataDir, app.renderer))
	router.With(cacheControl("no-cache")).Handle("/static/*", http.FileServer(http.FS(appweb.StaticFiles)))
	app.registerRoutes(router, cfg)
	app.registerHealthRoutes(router)
	app.registerNotFound(router)

	csrf := nosurf.New(router)
	csrf.SetBaseCookie(http.Cookie{
		Path:     "/",
		HttpOnly: true,
		Secure:   cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
	csrf.SetIsTLSFunc(func(r *http.Request) bool {
		return r.TLS != nil || (cfg.TrustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
	})
	csrf.SetFailureHandler(csrfFailureHandler(logger, app.renderer))
	sessions := auth.NewSessionManager(app.db, cfg.SecureCookies)
	authHandler := auth.NewHandler(auth.NewStore(cfg.AuthUsername, cfg.AuthPassword), sessions, app.renderer, cfg.AuthEnabled)
	authHandler.Routes(router)
	browserApplication := sessions.LoadAndSave(authHandler.Require(csrf))
	applicationHandler := limitRequestBody(withoutBrowserSession(router, browserApplication))
	return securityHeaders(cfg.SecureCookies)(
		middleware.RequestID(requestLogger(logger)(requestTimeout(middleware.Recoverer(applicationHandler)))),
	)
}

func (app *application) registerRoutes(router chi.Router, cfg config.Config) {
	dashboardHandler := dashboard.NewHandler(
		dashboard.NewStore(app.db, cfg.TimeZone, app.lessons, app.reviews, app.statistics),
		app.renderer,
	)
	router.Get("/", dashboardHandler.Dashboard)
	router.Get("/dashboard", dashboardHandler.Dashboard)
	router.Get("/dashboard/reviews/{date}", dashboardHandler.ReviewScheduleRedirect)
	vocabulary.NewHandler(app.vocabulary, app.renderer, app.exampleGenerator).Routes(router)
	examplegen.NewSettingsHandler(app.exampleGenerator, app.renderer).Routes(router)
	media.NewHandler(app.db).Routes(router)
	mining.NewHandler(app.mining, app.renderer, cfg.BaseURL, app.exampleGenerator, app.dictionary).Routes(router)

	captureapi.NewHandler(
		app.extensionTokens, app.mining, app.coverage, app.dictionary, app.vocabulary, app.exampleGenerator, cfg.TrustProxy,
	).Routes(router)
	captureapi.NewSettingsHandler(app.extensionTokens, app.renderer, func(ctx context.Context) (*time.Location, error) {
		values, err := app.settings.Get(ctx)
		if err != nil {
			return nil, err
		}
		return time.LoadLocation(values.TimeZone)
	}, cfg.BaseURL).Routes(router)
	lessons.NewHandler(app.lessons, app.reviews, app.renderer).Routes(router)
	reviews.NewHandler(app.reviews, app.lessons, app.renderer).Routes(router)
	settings.NewHandler(app.settings, app.renderer, app.dictionary, cfg.AuthEnabled).Routes(router)
	backups.NewHandler(app.backupStore, app.backupService, app.googleDrive, cfg.DataDir, app.renderer).Routes(router)
	statistics.NewHandler(app.statistics, app.renderer).Routes(router)
	appimports.NewHandler(app.anki, app.renderer).Routes(router)
}

func (app *application) registerHealthRoutes(router chi.Router) {
	router.With(cacheControl("no-store")).Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	router.With(cacheControl("no-store")).Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := app.db.PingContext(r.Context()); err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
}

func (app *application) registerNotFound(router chi.Router) {
	router.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		app.renderer.NotFound(w, internalweb.NotFoundPage{
			Title:       "Page not found",
			Heading:     "Page not found",
			Message:     "The page may have been removed, or the link may be out of date.",
			ReturnURL:   "/dashboard",
			ReturnLabel: "Back to dashboard",
		})
	})
}

func (app *application) startWorkers(ctx context.Context, logger *slog.Logger) func() {
	var workers sync.WaitGroup
	start := func(run func()) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			run()
		}()
	}
	start(func() { app.dictionary.Run(ctx) })
	start(func() { app.backupService.Run(ctx) })
	return workers.Wait
}
