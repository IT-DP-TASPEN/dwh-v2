package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/auth"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/config"
	"github.com/ibldzn/go-admin/internal/coordinator"
	"github.com/ibldzn/go-admin/internal/database"
	"github.com/ibldzn/go-admin/internal/dwhschema"
	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/navigation"
	"github.com/ibldzn/go-admin/internal/render"
	"github.com/ibldzn/go-admin/internal/scheduler"
	"github.com/ibldzn/go-admin/internal/server"
	"github.com/ibldzn/go-admin/internal/user"
	webfiles "github.com/ibldzn/go-admin/web"
)

func Run(ctx context.Context) error {
	applicationConfig, err := config.LoadRuntime()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	logger := NewLogger(applicationConfig.App.Environment)
	slog.SetDefault(logger)
	logger.Info("application starting",
		"name", applicationConfig.App.Name,
		"environment", applicationConfig.App.Environment,
	)
	if applicationConfig.Fincloud.InsecureSkipVerify {
		logger.Warn("Fincloud TLS certificate verification is disabled", "scope", "fincloud_client")
	}

	databaseContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	databaseConnection, err := database.Open(databaseContext, applicationConfig.Database)
	cancel()
	if err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	defer func() {
		if err := databaseConnection.Close(); err != nil {
			logger.Error("close database", "error", err)
		}
	}()
	logger.Info("database connection initialized",
		"host", applicationConfig.Database.Host,
		"port", applicationConfig.Database.Port,
		"database", applicationConfig.Database.Name,
	)
	schemaContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = dwhschema.VerifyRuntime(schemaContext, databaseConnection)
	cancel()
	if err != nil {
		return fmt.Errorf("verify database schema: %w", err)
	}
	logger.Info("database schema compatible", "goose_version", dwhschema.CurrentVersion)

	bootstrapContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = access.Bootstrap(bootstrapContext, databaseConnection, PermissionDefinitions(), time.Now().UTC())
	cancel()
	if err != nil {
		return fmt.Errorf("initialize access control: %w", err)
	}
	logger.Info("access control initialized")

	fincloudClient, err := fincloud.NewClient(fincloud.Config{
		BaseURL:            applicationConfig.Fincloud.BaseURL,
		Username:           applicationConfig.Fincloud.Username,
		Password:           applicationConfig.Fincloud.Password,
		LocationID:         applicationConfig.Fincloud.LocationID,
		RoleID:             applicationConfig.Fincloud.RoleID,
		HTTPTimeout:        applicationConfig.Fincloud.HTTPTimeout,
		InsecureSkipVerify: applicationConfig.Fincloud.InsecureSkipVerify,
	})
	if err != nil {
		return fmt.Errorf("initialize Fincloud client: %w", err)
	}
	defer fincloudClient.CloseIdleConnections()
	runtimeContext := context.WithoutCancel(ctx)
	ingestionCoordinator, err := coordinator.New(ctx, databaseConnection, fincloudClient, logger)
	if err != nil {
		return fmt.Errorf("initialize ingestion coordinator: %w", err)
	}
	scheduleService, err := scheduler.New(databaseConnection, ingestionCoordinator.SubmitInTx, logger)
	if err != nil {
		return fmt.Errorf("initialize ingestion scheduler: %w", err)
	}

	userRepository := user.NewRepository(databaseConnection)
	accessRepository := access.NewRepository(databaseConnection)
	sessionRepository := auth.NewSessionRepository(databaseConnection)
	authenticationService, err := browserauth.NewService(
		userRepository,
		accessRepository,
		sessionRepository,
		applicationConfig.Session.Lifetime,
		applicationConfig.Session.RememberLifetime,
		logger,
	)
	if err != nil {
		return fmt.Errorf("initialize browser authentication: %w", err)
	}
	var contentFiles fs.FS = webfiles.Files
	reloadTemplates := false
	if applicationConfig.App.IsDevelopment() {
		contentFiles = os.DirFS("web")
		reloadTemplates = true
	}

	renderer, err := render.New(contentFiles, reloadTemplates)
	if err != nil {
		return fmt.Errorf("initialize renderer: %w", err)
	}
	staticFiles, err := fs.Sub(contentFiles, "static")
	if err != nil {
		return fmt.Errorf("initialize static files: %w", err)
	}
	errorResponder := render.NewErrorResponder(renderer, applicationConfig.App.Name, logger)

	cookieManager := browserauth.NewCookieManager(
		applicationConfig.Session.CookieName,
		applicationConfig.Session.Secure,
		applicationConfig.Session.RememberLifetime,
	)
	authenticationHTTP := browserauth.NewHTTP(
		authenticationService,
		renderer,
		cookieManager,
		applicationConfig.App.Name,
		applicationConfig.App.AllowRegistration,
		logger,
		func(ctx context.Context, event audit.Event) error {
			return audit.Append(ctx, databaseConnection, event)
		},
		errorResponder,
	)
	navigationRegistry, err := navigation.NewRegistry(navigationGroups(), PermissionDefinitions())
	if err != nil {
		return fmt.Errorf("initialize admin navigation: %w", err)
	}
	adminHTTP := adminshell.New(renderer, navigationRegistry, applicationConfig.App.Name, errorResponder)
	handler := server.NewRouter(server.RouterDependencies{
		StaticFiles:       staticFiles,
		AllowRegistration: applicationConfig.App.AllowRegistration,
		Authentication:    authenticationHTTP,
		RegisterAuthenticated: func(router chi.Router) {
			registerFeatureRoutes(router, featureDependencies{
				database: databaseConnection, users: userRepository, access: accessRepository,
				admin: adminHTTP, cookies: cookieManager, coordinator: ingestionCoordinator, scheduler: scheduleService,
			})
		},
		Ready: func(ctx context.Context) error {
			if err := databaseConnection.PingContext(ctx); err != nil {
				return err
			}
			return dwhschema.VerifyRuntime(ctx, databaseConnection)
		},
		Errors: errorResponder,
	})
	coordinatorContext, stopCoordinator := context.WithCancel(runtimeContext)
	coordinatorDone := make(chan struct{})
	go func() {
		defer close(coordinatorDone)
		ingestionCoordinator.Run(coordinatorContext)
	}()
	logger.Info("ingestion coordinator initialized", "owner_id", ingestionCoordinator.OwnerID())
	schedulerContext, stopScheduler := context.WithCancel(runtimeContext)
	schedulerDone := make(chan struct{})
	go func() {
		defer close(schedulerDone)
		scheduleService.Run(schedulerContext)
	}()
	logger.Info("ingestion scheduler initialized")
	cleanupContext, stopCleanup := context.WithCancel(runtimeContext)
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		auth.RunSessionCleanup(cleanupContext, sessionRepository, time.Hour, logger)
	}()
	httpServer := server.NewHTTPServer(applicationConfig.App.Address(), handler, logger)
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpServer.Serve() }()

	var serveErr error
	select {
	case serveErr = <-serveDone:
	case <-ctx.Done():
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), applicationConfig.App.ShutdownTimeout)
	defer cancel()
	stopScheduler()
	httpShutdownDone := make(chan error, 1)
	go func() { httpShutdownDone <- httpServer.Shutdown(shutdownContext) }()
	stopCoordinator()
	stopCleanup()

	componentsDone := make(chan struct{})
	go func() {
		<-schedulerDone
		<-coordinatorDone
		<-cleanupDone
		close(componentsDone)
	}()
	select {
	case <-componentsDone:
	case <-shutdownContext.Done():
		_ = httpServer.Close()
		return fmt.Errorf("application shutdown exceeded %s", applicationConfig.App.ShutdownTimeout)
	}
	var shutdownErr error
	select {
	case shutdownErr = <-httpShutdownDone:
	case <-shutdownContext.Done():
		_ = httpServer.Close()
		return fmt.Errorf("application shutdown exceeded %s", applicationConfig.App.ShutdownTimeout)
	}
	if serveErr == nil {
		select {
		case serveErr = <-serveDone:
		default:
		}
	}
	return errors.Join(serveErr, shutdownErr)
}

func NewLogger(environment string) *slog.Logger {
	if environment == "development" {
		return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
}
