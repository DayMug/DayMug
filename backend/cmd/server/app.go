package main

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/pricing"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/handler"
	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// defaultListenAddr is where serve binds when neither DAYMUG_ADDR nor
// server.addr names an address.
const defaultListenAddr = ":8080"

// appOptions carries what buildApp needs from outside the config: where the
// database lives and what the binary embeds. cmdServe fills it from the
// running executable; tests point it at a temp dir.
type appOptions struct {
	DBPath     string
	FrontendFS fs.FS
	Version    string
}

// App is the assembled server: ready to listen, not yet listening.
type App struct {
	Addr    string
	Server  *http.Server
	Drainer *service.Drainer

	// closers release what buildApp acquired, in reverse order. The database
	// is first in, so it is the last thing closed.
	closers []func()
}

func (a *App) onClose(fn func()) { a.closers = append(a.closers, fn) }

// Close releases the app's resources. It must run only after the graceful
// drain: a run killed during shutdown still persists its result, and needs an
// open database to do it.
func (a *App) Close() {
	for i := len(a.closers) - 1; i >= 0; i-- {
		a.closers[i]()
	}
	a.closers = nil
}

// buildApp assembles everything `serve` runs from a loaded config. Every
// failure is returned rather than fatal, so resources acquired before it are
// released and the caller decides how to exit.
func buildApp(cfg *config.Config, opts appOptions) (_ *App, err error) {
	app := &App{}
	defer func() {
		if err != nil {
			app.Close()
		}
	}()

	// On macOS, auto-detect the launchd install variant so the upgrade
	// flow targets the right `launchctl` domain even if the operator
	// never adjusted upgrade.service_mode in YAML. The default config
	// ships with "user", which is correct for the user-level
	// LaunchAgent that `daymug bootstrap` writes; this override
	// catches the case where the binary was instead deployed as a
	// system-wide LaunchDaemon under /Library/LaunchDaemons.
	if detected := service.DetectLaunchdMode(); detected != "" && detected != cfg.Upgrade.ServiceMode {
		log.Printf("upgrade: detected launchd %s install (config had %q); overriding service_mode", detected, cfg.Upgrade.ServiceMode)
		cfg.Upgrade.ServiceMode = detected
	}

	if err := cfg.EnsureDefaultHomeRoot(); err != nil {
		return nil, fmt.Errorf("ensure default home root %s: %w", cfg.Users.DefaultHomeRoot, err)
	}

	db, err := openDatabase(opts.DBPath, cfg.Retention)
	if err != nil {
		return nil, err
	}
	service.SetUploadsRetention(cfg.Retention.Uploads.Duration)
	app.onClose(func() { _ = db.Close() })

	settings, err := loadAppSettings(db, cfg)
	if err != nil {
		return nil, err
	}

	probeCtx, probeCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = validateAgentRuntimePrerequisites(probeCtx, cfg, settings.transportFor)
	probeCancel()
	if err != nil {
		return nil, fmt.Errorf("agent runtime prerequisite: %w", err)
	}

	if err := bootstrapAdmins(db, cfg.Admin.BootstrapUsernames); err != nil {
		return nil, fmt.Errorf("bootstrap admins: %w", err)
	}
	if service.FirstAdminSetup(cfg) == service.SetupModeUnavailable {
		log.Printf("warning: neither auth.password_login_enabled nor oidc is enabled — nobody can sign in. Enable one in config.yaml and restart")
	}

	r := gin.Default()
	r.Use(middleware.CORS())

	app.Drainer = service.NewDrainer()
	// Adapters register their parked processes here. Without it a
	// graceful upgrade reads inFlight==0 as "idle" and restarts through
	// background work that no longer has a job to be counted by.
	app.Drainer.SetResidentProbe(agent.ResidentCount)

	// OIDC discovery happens at startup so misconfiguration (wrong issuer,
	// network unreachable) is caught loud and early. When oidc.enabled is
	// false NewOIDCService returns nil, nil and the routes silently skip
	// registration in RegisterRoutes.
	oidcCtx, oidcCancel := context.WithTimeout(context.Background(), 15*time.Second)
	oidcSvc, err := service.NewOIDCService(oidcCtx, &cfg.OIDC)
	oidcCancel()
	if err != nil {
		return nil, fmt.Errorf("oidc init: %w", err)
	}

	app.Addr = cfg.ListenAddr()
	if app.Addr == "" {
		app.Addr = defaultListenAddr
	}

	if err := settings.registerRoutes(r, db, app.Drainer, cfg, handler.RegisterRoutesOpts{
		OIDC:           oidcSvc,
		FrontendFS:     opts.FrontendFS,
		CurrentVersion: opts.Version,
		ListenAddr:     app.Addr,
	}); err != nil {
		return nil, fmt.Errorf("assemble runtime: %w", err)
	}

	app.Server = &http.Server{
		Addr:    app.Addr,
		Handler: r,
	}
	return app, nil
}

// openDatabase opens and migrates the SQLite database, creating its directory
// on first start so a fresh install works without any extra prep. Retention is
// set before Init because Init starts the maintenance sweep that reads it.
func openDatabase(dbPath string, retention config.RetentionConfig) (*store.SQLiteStore, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	db, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetInactiveRetention(retention.InactiveConversations.Duration)
	db.SetPurgeRetention(retention.DeletedConversations.Duration)
	db.SetInactiveDeletedHook(func(ctx context.Context, refs []store.ConversationRef) {
		service.RemoveConversationsUploads(ctx, db, refs)
	})
	if err := db.Init(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init database: %w", err)
	}
	return db, nil
}

// appSettings is what loadAppSettings hands back: proof that the admin-edited
// app_settings rows have been read into the process-wide caches the runtime
// consults — provider accounts (on cfg), transports, pricing overrides and the
// model registry. The steps that read those caches are reached through it, so
// they cannot run before the load; that ordering used to rest on comments.
type appSettings struct {
	// transportFor reports the transport an admin selected for a provider type.
	transportFor func(provider string) string
}

// settingLoadTimeout bounds each app_settings read at startup.
const settingLoadTimeout = 5 * time.Second

// loadAppSettings reads the app_settings rows startup depends on. Only the
// provider accounts are fatal: without them nothing can run, whereas a
// malformed transport, pricing or model row degrades to the defaults and is
// logged loudly.
func loadAppSettings(st service.AppSettingStore, cfg *config.Config) (appSettings, error) {
	// Provider accounts live in SQLite and are managed from the admin UI.
	if err := withTimeout(func(ctx context.Context) error {
		return service.LoadProviderSettings(ctx, st, cfg)
	}); err != nil {
		return appSettings{}, fmt.Errorf("load provider settings: %w", err)
	}

	// The admin-selected transports decide which runtimes the prerequisite
	// probe requires and which adapter each backend is built on.
	if err := withTimeout(func(ctx context.Context) error {
		return service.LoadTransports(ctx, st)
	}); err != nil {
		log.Printf("load transports: %v (continuing with default transports)", err)
	}

	// Pricing overrides must be in place before the first turn so a freshly
	// restarted server bills the operator's rates from line 1. The only way
	// to fail is a malformed JSON row, which falls back to the defaults.
	_ = withTimeout(func(ctx context.Context) error {
		for _, provider := range pricing.ProvidersWithDefaults() {
			if err := pricing.Load(ctx, st, provider); err != nil {
				log.Printf("load %s pricing override: %v (continuing with defaults)", provider, err)
			}
		}
		return nil
	})

	// The model registry feeds the picker and the title generators. A
	// malformed row leaves every account with no models.
	if err := withTimeout(func(ctx context.Context) error {
		return service.LoadModelOverrides(ctx, st)
	}); err != nil {
		log.Printf("load model overrides: %v (continuing with no configured models)", err)
	}

	return appSettings{transportFor: service.TransportFor}, nil
}

// registerRoutes assembles the runtime and mounts every route. It hangs off
// appSettings because the runtime builds each backend on the transport, prices
// and model registry loadAppSettings put in place.
func (appSettings) registerRoutes(r *gin.Engine, db store.Store, drainer *service.Drainer, cfg *config.Config, opts handler.RegisterRoutesOpts) error {
	return handler.RegisterRoutes(r, db, drainer, cfg, opts)
}

func withTimeout(fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), settingLoadTimeout)
	defer cancel()
	return fn(ctx)
}
