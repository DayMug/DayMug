package handler

import (
	"compress/gzip"
	"context"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/service/filewatch"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/service/imbridge"
)

// newStartupCtx returns a context with a generous budget for any
// blocking work we want to do during route registration (database
// scans, recovery passes). Kept short enough that a wedged DB doesn't
// silently delay server startup forever, long enough that a healthy
// SQLite file with a few thousand rows finishes well within budget.
func newStartupCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// RegisterRoutesOpts bundles the optional inputs to RegisterRoutes so we
// can grow the parameter list without rewriting every test call site.
type RegisterRoutesOpts struct {
	OIDC           *service.OIDCService
	FrontendFS     fs.FS
	CurrentVersion string // compile-time Version, surfaced via /api/admin/upgrade/*
	// ListenAddr is the resolved listen address (after DAYMUG_ADDR /
	// cfg.Server.Addr / built-in default), used by the upgrade watchdog
	// to compute its localhost health URL.
	ListenAddr string
}

// routeBuilder threads the assembly state through the per-domain registration
// steps below.
//
// The shared runtime (backends, pool, sandbox, broadcaster, dispatcher, ...) is
// assembled completely before the first route is mounted, so every handler
// receives its collaborators at construction time — no step has to come back
// and patch a handler built earlier.
//
// Fields fall into three classes:
//   - rt / imBridge: the process-wide runtime and the IM bridge wired into it;
//   - groups (api/authed/admin): created by RegisterRoutes, because a group's
//     middleware must be attached before anything mounts on it;
//   - handlers: owned by the step that constructs them, and retained here only
//     when a *later* step also needs them.
type routeBuilder struct {
	engine *gin.Engine
	store  store.Store
	cfg    *config.Config
	opts   RegisterRoutesOpts

	rt *service.Runtime
	// imBridge is built with the runtime because the runtime's PromptObserver
	// is the bridge itself; startIMBridge only brings its connectors online.
	imBridge *imbridge.IMBridge

	api    *gin.RouterGroup
	authed *gin.RouterGroup
	admin  *gin.RouterGroup

	// auth is mounted on both the public and the authed group.
	auth *AuthHandler
	// usage and help are each mounted on both the authed and the admin group.
	usage *UsageHandler
	help  *HelpHandler
	// imManager/bots are created with the runtime (the IM bridge needs them)
	// and reused by the agent routes (saving an agent hot-reloads its
	// connectors) and the admin step.
	imManager *imbot.Manager
	bots      store.BotStore
}

// RegisterRoutes wires every HTTP route. cfg may be nil in unit tests that
// only exercise a subset of handlers; production always passes a loaded
// config from cmd_serve. opts.OIDC is non-nil only when cfg.OIDC.Enabled
// is true; the SSO routes are registered conditionally on that.
//
// The runtime is assembled first and, with a real config, validated: an
// unwired collaborator refuses startup instead of degrading to a nil no-op.
// Route mounting order after that only matters for gin's middleware chains.
func RegisterRoutes(r *gin.Engine, s store.Store, drainer *service.Drainer, cfg *config.Config, opts RegisterRoutesOpts) error {
	b := &routeBuilder{
		engine: r,
		store:  s,
		cfg:    cfg,
		opts:   opts,
	}
	b.assembleRuntime(drainer)
	// Tests that only exercise the SPA/static surface pass no config and no
	// store; production always passes both.
	if cfg != nil {
		if err := b.rt.Validate(); err != nil {
			return err
		}
	}

	b.api = r.Group("/api")
	b.registerPublicRoutes()

	// Everything below requires a valid session. RequireAuth must be attached
	// before anything mounts on the group: gin snapshots the chain per route.
	b.authed = b.api.Group("")
	b.authed.Use(middleware.RequireAuth(s))

	b.registerAccountRoutes()
	b.registerAgentRoutes()
	b.registerConversationRoutes()
	b.registerUploadRoutes()
	b.registerUsageRoutes()
	b.registerMarketplaceRoutes()
	b.registerHelpRoutes()
	b.registerFileRoutes()

	b.recoverPendingPrompts()

	b.registerCronRoutes()
	b.registerTerminalRoutes()
	b.startIMBridge()

	b.admin = b.authed.Group("/admin")
	b.admin.Use(middleware.RequireAdmin())
	b.registerAdminRoutes()
	b.registerAdminOpsRoutes()

	registerFrontendRoutes(r, opts.FrontendFS)
	return nil
}

// assembleRuntime builds the shared runtime and the IM bridge that observes it.
// Everything that can start a turn is wired here, before any route exists or
// any dispatcher worker runs.
func (b *routeBuilder) assembleRuntime(drainer *service.Drainer) {
	b.rt = buildRuntime(b.store, drainer, b.cfg)
	b.imManager = imbot.NewManager(nil)
	b.bots = b.store
	b.imBridge = &imbridge.IMBridge{
		Runtime:    b.rt,
		Bots:       b.bots,
		Mentions:   b.imManager,
		Responders: b.imManager,
	}
	b.imManager.Handler = b.imBridge
	b.rt.PromptObserver = b.imBridge
}

// registerPublicRoutes mounts everything reachable without a session cookie.
func (b *routeBuilder) registerPublicRoutes() {
	b.api.GET("/health", NewHealthCheck(b.store))

	b.auth = NewAuthHandler(b.store, b.cfg)
	b.api.GET("/auth/options", b.auth.Options)
	b.api.POST("/auth/login", b.auth.Login)
	b.api.POST("/auth/logout", b.auth.Logout)

	// Distinct instance from the authed conversation handler: the shared-link
	// read path needs none of the orchestration collaborators backfilled below.
	publicConv := NewConversationHandler(b.store)
	b.api.GET("/shared/conversations/:token", publicConv.GetShared)

	// First-run admin setup. Public on a brand-new install, self-disables
	// (409) the moment any login-capable user exists — so the public
	// surface only stays open until the operator finishes the setup form.
	bootstrap := NewBootstrapHandler(b.store, b.cfg)
	b.api.GET("/bootstrap/status", bootstrap.Status)
	b.api.POST("/bootstrap/admin", bootstrap.CreateAdmin)

	if b.opts.OIDC != nil {
		oidcH := NewOIDCHandler(b.store, b.cfg, b.opts.OIDC)
		b.api.GET("/auth/oidc/login", oidcH.Login)
		b.api.GET("/auth/oidc/callback", oidcH.Callback)
	}
}

// registerAccountRoutes mounts the signed-in user's own settings, the
// self-description endpoints, and the directory picker.
func (b *routeBuilder) registerAccountRoutes() {
	b.authed.GET("/auth/me", b.auth.Me)
	b.authed.POST("/auth/password", b.auth.ChangePassword)
	b.authed.GET("/auth/environment", b.auth.Environment)
	b.authed.PUT("/auth/environment", b.auth.UpdateEnvironment)
	b.authed.PUT("/auth/notifications", b.auth.UpdateNotifications)
	// Lightweight self-description (version, sandbox state) for any
	// authenticated user.
	srvInfo := NewServerInfoHandler(b.cfg, b.opts.CurrentVersion)
	b.authed.GET("/server-info", srvInfo.Get)
	// Single cold-start aggregate: bundles /auth/me + /users + /server-info
	// + the active agent's /conversations into one round-trip so the chat
	// surface reaches first paint without the previous serial waterfall.
	// The dedicated endpoints stay live for surfaces that update one slice
	// at a time (e.g. notification settings re-fetches /auth/me).
	appState := NewAppStateHandler(b.store, b.cfg, b.opts.CurrentVersion)
	b.authed.GET("/app-state", appState.Get)
	// Directory picker drives agent creation. The handler jails every
	// request to the caller's own work_dir, so any authenticated user can
	// browse and mkdir within their home — but never above it or into
	// another user's tree.
	browse := NewBrowseHandler(b.store)
	b.authed.GET("/browse-dirs", browse.BrowseDirs)
	b.authed.POST("/browse-dirs/mkdir", browse.MkdirBrowseDir)
}

// registerAgentRoutes mounts agent/user CRUD and the per-agent IM connector
// management.
func (b *routeBuilder) registerAgentRoutes() {
	user := NewUserHandler(b.store)
	user.Manager = b.imManager
	user.Cfg = b.cfg
	user.Bots = b.bots
	b.authed.GET("/users", user.List)
	b.authed.POST("/users", user.Create)
	// Routes that key off `:id` accept either the caller's own id or an
	// agent id (a User row with no username). Per-request access is enforced
	// inside each handler via canAccessOwner so that the human user can
	// manage their agents while still being blocked from poking at other
	// human users' rows.
	b.authed.PUT("/users/:id", user.Update)
	b.authed.GET("/users/:id/integrations/status", user.IntegrationStatus)
	bot := &BotHandler{Store: b.store, Bots: b.bots, Manager: b.imManager}
	b.authed.GET("/users/:id/bots", bot.List)
	b.authed.GET("/users/:id/bots/requirements", bot.Requirements)
	b.authed.POST("/users/:id/bots/test-connection", bot.Test)
	b.authed.POST("/users/:id/bots/wechat-pairing", bot.StartWeChatPairing)
	b.authed.GET("/users/:id/bots/wechat-pairing", bot.PollWeChatPairing)
	b.authed.POST("/users/:id/bots", bot.Create)
	b.authed.PUT("/users/:id/bots/:botId", bot.Update)
	b.authed.DELETE("/users/:id/bots/:botId", bot.Delete)
	b.authed.DELETE("/users/:id", user.Delete)
	b.authed.POST("/users/:id/duplicate", user.Duplicate)
	b.authed.POST("/users/:id/archive", user.Archive)
	b.authed.POST("/users/:id/unarchive", user.Unarchive)
	b.authed.GET("/users/:id/claude-md", user.GetClaudeMd)
	b.authed.PUT("/users/:id/claude-md", user.PutClaudeMd)
	// Collection-level (not :id-scoped) to avoid a static-vs-param sibling
	// clash with the /users/:id subtree — mirrors /conversation-pin-order.
	b.authed.PUT("/user-order", user.Reorder)
	b.authed.GET("/archived-agents", user.ListArchived)
}

// registerConversationRoutes mounts conversation CRUD plus the read-only
// metadata endpoints that back the slash-command surface.
func (b *routeBuilder) registerConversationRoutes() {
	conv := NewConversationHandler(b.store)
	conv.Cfg = b.cfg
	// Shared with the chat WebSocket so HTTP-side state resets (e.g. clearing
	// the context bar) and conversation lifecycle changes reach peer tabs.
	conv.Broadcaster = b.rt.Broadcaster
	conv.UserHub = b.rt.UserHub
	conv.Drainer = b.rt.Drainer
	b.authed.GET("/conversations", conv.List)
	b.authed.POST("/conversations", conv.Create)
	b.authed.DELETE("/stale-conversations", conv.DeleteStale)
	b.authed.GET("/conversations/:id", conv.Get)
	b.authed.DELETE("/conversations/:id", conv.Delete)
	b.authed.GET("/conversations/:id/messages", conv.GetMessages)
	b.authed.DELETE("/conversations/:id/messages", conv.ClearMessages)
	b.authed.POST("/conversations/:id/share", conv.Share)
	b.authed.DELETE("/conversations/:id/share", conv.Unshare)
	b.authed.PUT("/conversations/:id/work-dir", conv.UpdateWorkDir)
	b.authed.PUT("/conversations/:id/title", conv.UpdateTitle)
	b.authed.PUT("/conversations/:id/notifications", conv.UpdateNotifications)
	b.authed.PUT("/conversations/:id/pinned", conv.UpdatePinned)
	// Collection-level (not :id-scoped) to avoid a static-vs-param sibling
	// clash with the /conversations/:id subtree.
	b.authed.PUT("/conversation-pin-order", conv.ReorderPinned)
	b.authed.PUT("/conversations/:id/model", conv.UpdateModel)
	b.authed.POST("/conversations/:id/clear-context", conv.ClearContext)
	b.authed.POST("/conversations/:id/read", conv.MarkRead)
	b.authed.GET("/conversation-attention", conv.ListAttention)

	// Per-provider model registry consumed by the chat-header model picker.
	// Tiny static payload; gated by the same auth middleware as the rest of
	// the conversation routes for parity. Backends carries each provider's
	// Capabilities() so the frontend can feature-gate per backend.
	models := NewModelsHandler(b.cfg)
	models.Backends = b.rt.Backends
	b.authed.GET("/models", models.List)
	// /compact has to live on the terminal handler — it needs the runner,
	// pool, sandbox, and broadcaster to spawn a real (synchronous) claude
	// run against the existing session before rotating it. ConversationHandler
	// only owns the store-side bookkeeping.
	// Registered in registerTerminalRoutes.
}

func (b *routeBuilder) registerUploadRoutes() {
	upload := NewUploadHandler(b.store)
	b.authed.POST("/uploads", upload.Create)
}

// registerUsageRoutes mounts the per-user token report. The admin-wide view
// reuses the same handler under the admin group further down.
func (b *routeBuilder) registerUsageRoutes() {
	b.usage = NewUsageHandler(b.store)
	if b.cfg != nil {
		b.usage.Loc = b.cfg.UsageLocation()
		b.usage.Thresholds.ModelRequests = b.cfg.Usage.HighModelRequests
		b.usage.Thresholds.ToolCalls = b.cfg.Usage.HighToolCalls
		b.usage.Thresholds.ContextWarningRatio = b.cfg.Usage.ContextWarningRatio
		b.usage.Thresholds.ConversationCostUSD = b.cfg.Usage.HighConversationCost
	}
	b.authed.GET("/usage/me", b.usage.Mine)
	b.authed.GET("/usage/insights", b.usage.MineInsights)
}

// registerMarketplaceRoutes mounts the community-managed global app list.
// Authentication is the only gate: every signed-in user may list, submit, or
// remove any entry by product design.
func (b *routeBuilder) registerMarketplaceRoutes() {
	h := &MarketplaceHandler{Ops: &service.MarketplaceOps{Store: b.store}}
	b.authed.GET("/marketplace/apps", h.List)
	b.authed.POST("/marketplace/apps", h.Create)
	b.authed.PUT("/marketplace/apps/:id", h.Update)
	b.authed.DELETE("/marketplace/apps/:id", h.Delete)
}

// registerHelpRoutes mounts the read side of the admin-configured help
// document; every signed-in user can render the popup. The write side is
// mounted under the admin group further down.
func (b *routeBuilder) registerHelpRoutes() {
	b.help = NewHelpHandler(b.store)
	b.authed.GET("/help-doc", b.help.Get)
}

// registerFileRoutes mounts the workspace file browser. Per-request access is
// enforced inside FileHandler.getUserFileAccess via canAccessOwner, same model as
// the /users/:id routes above.
func (b *routeBuilder) registerFileRoutes() {
	file := NewFileHandler(b.store)
	// One watcher for the whole process, held for its lifetime — there is no
	// teardown path here because the only way out is process exit, which
	// releases the fd anyway. A failure to construct it must not stop the
	// server: /watch reports "unavailable" and browsers fall back to polling.
	if w, err := filewatch.New(); err != nil {
		log.Printf("file watch unavailable, clients will poll instead: %v", err)
	} else {
		file.Watcher = w
	}
	files := b.authed.Group("/users/:id/files")
	files.GET("", file.ListDir)
	files.GET("/inspect", file.InspectFile)
	files.GET("/watch", file.WatchFiles)
	files.GET("/search", file.Search)
	files.GET("/read", file.ReadFile)
	// Path-shaped sibling of /read: the rendered-HTML preview needs relative
	// asset references inside a page to resolve to the files next to it.
	files.GET("/preview/*path", file.PreviewFile)
	files.PUT("/write", file.WriteFile)
	files.GET("/download", file.DownloadFile)
	files.GET("/download-zip", file.DownloadZip)
	files.POST("/upload/check", file.CheckUploadConflicts)
	files.POST("/upload", file.Upload)
	files.PUT("/rename", file.Rename)
	files.DELETE("", file.Delete)
	files.POST("/mkdir", file.Mkdir)
	files.PUT("/move", file.Move)
	files.POST("/copy", file.Copy)
	files.POST("/extract", file.Extract)
	files.POST("/compress", file.Compress)
}

// recoverPendingPrompts recovers any prompts that were left "pending" or
// "processing" when the previous server instance exited (clean shutdown or
// crash). Logged once at startup so operators can spot a queue that didn't
// drain. Skipped in tests that pass s=nil — the dispatcher itself is also nil
// there and the WS handler refuses "input" cleanly.
func (b *routeBuilder) recoverPendingPrompts() {
	if b.rt.Dispatcher == nil {
		return
	}
	startCtx, startCancel := newStartupCtx()
	resetRows, abandoned, resumed, err := b.rt.Dispatcher.Start(startCtx)
	startCancel()
	if err != nil {
		log.Printf("dispatcher start: %v", err)
	} else if resetRows > 0 || abandoned > 0 || resumed > 0 {
		log.Printf("dispatcher recovery: reset %d processing→pending rows, abandoned %d exhausted prompts, resumed %d conversations", resetRows, abandoned, resumed)
	}
}

// registerCronRoutes starts the scheduler and mounts crontab CRUD.
//
// Scheduled Agent prompts deliberately enter through the same Dispatcher as
// WebSocket chat. That keeps provider bindings, account concurrency,
// environment layering, sandboxing, usage attribution, and transcript
// persistence identical across interactive and automatic runs.
func (b *routeBuilder) registerCronRoutes() {
	cronScheduler := service.NewCronScheduler(b.rt, b.store)
	startCtx, startCancel := newStartupCtx()
	if err := cronScheduler.ReloadAtStartup(startCtx); err != nil {
		log.Printf("crontab: start scheduler: %v", err)
	}
	startCancel()

	cronH := NewCronHandler(b.store, b.store, b.bots, cronScheduler)
	b.authed.GET("/cron-jobs", cronH.List)
	b.authed.POST("/cron-jobs", cronH.Create)
	b.authed.PUT("/cron-jobs/:id", cronH.Update)
	b.authed.DELETE("/cron-jobs/:id", cronH.Delete)
}

// registerTerminalRoutes mounts the chat WebSocket and /compact. /compact lives
// on the terminal handler (not ConversationHandler) because it needs the
// runtime's runner, Pool, Sandbox, and Broadcaster to spawn a real synchronous
// run against the existing session before rotating it.
func (b *routeBuilder) registerTerminalRoutes() {
	terminal := NewTerminalHandler(b.rt)
	b.authed.GET("/terminal", terminal.HandleTerminal)
	b.authed.POST("/conversations/:id/compact", terminal.Compact)
}

// startIMBridge brings the IM bot connectors (Slack Socket Mode / 飞书长连接 /
// ...) online. The bridge itself was built with the runtime, so IM-triggered
// runs share the chat and API surfaces' pool, sandbox, backends and drainer.
// Config lives in app_settings; startup applies whatever the admin last saved,
// and the admin PUT hot-reloads the connectors. Apply dials out, so it runs off
// the startup path.
func (b *routeBuilder) startIMBridge() {
	if b.store == nil || b.bots == nil || b.cfg == nil {
		return
	}
	go func() {
		startCtx, startCancel := newStartupCtx()
		defer startCancel()
		b.imBridge.ClearStaleQueuedPrompts(startCtx)
		imCfg, err := imbridge.LoadIMBotConfig(startCtx, b.store, b.bots)
		if err != nil {
			log.Printf("imbot: load config at startup: %v", err)
			return
		}
		b.imManager.Apply(imCfg)
	}()
}

// registerAdminRoutes mounts the admin-only surface: human user CRUD, password
// reset, enable/disable, a read-only view of the YAML config bits the admin UI
// needs, plus the write side of the shared usage/help/pricing/integration
// handlers.
func (b *routeBuilder) registerAdminRoutes() {
	adminH := NewAdminHandler(b.store, b.cfg)
	adminH.CurrentVersion = b.opts.CurrentVersion
	b.admin.GET("/users", adminH.ListHumans)
	b.admin.POST("/users", adminH.CreateHuman)
	b.admin.POST("/users/default-model", adminH.BatchSetDefaultModel)
	b.admin.POST("/users/sandbox-mode", adminH.BatchSetSandboxMode)
	b.admin.POST("/users/provider-binding", adminH.BatchSetProviderBinding)
	b.admin.PUT("/users/:id", adminH.UpdateHuman)
	b.admin.DELETE("/users/:id", adminH.Delete)
	b.admin.POST("/users/:id/password", adminH.SetPassword)
	b.admin.POST("/users/:id/disable", adminH.Disable)
	b.admin.POST("/users/:id/enable", adminH.Enable)
	b.admin.GET("/config/public", adminH.PublicConfig)
	b.admin.GET("/db/size", adminH.DatabaseSize)
	b.admin.POST("/db/optimize", adminH.OptimizeDatabase)
	b.admin.GET("/usage", b.usage.AdminAll)
	b.admin.GET("/usage/insights", b.usage.AdminInsights)
	b.admin.PUT("/help-doc", b.help.Put)
	pricingH := NewAdminPricingHandler(b.store, b.cfg)
	b.admin.GET("/pricing/:provider", pricingH.Get)
	b.admin.PUT("/pricing/:provider", pricingH.Put)

	modelsAdmin := NewAdminModelsHandler(b.cfg, b.store)
	b.admin.GET("/models", modelsAdmin.Get)
	b.admin.PUT("/models", modelsAdmin.Put)
	transportsAdmin := NewAdminTransportsHandler(b.store)
	b.admin.GET("/transports", transportsAdmin.Get)
	b.admin.PUT("/transports", transportsAdmin.Put)
	providersAdmin := NewAdminProvidersHandler(b.cfg, b.store, b.rt.Pool)
	b.admin.GET("/providers", providersAdmin.Get)
	b.admin.PUT("/providers", providersAdmin.Put)
	accountCheckH := NewAdminAccountCheckHandler(b.cfg, b.rt.Backends, b.rt.Pool)
	b.admin.POST("/providers/:name/check", accountCheckH.Check)
	b.admin.POST("/providers/:name/summary-check", accountCheckH.SummaryCheck)
	pauseH := NewAdminPauseHandler(b.rt.Pause, b.rt.Dispatcher)
	b.admin.GET("/pause", pauseH.Get)
	b.admin.PUT("/pause", pauseH.Put)
}

// registerAdminOpsRoutes mounts the operator-only surfaces that need a real
// config: self-upgrade, the codex device-code login WebSocket, and the browser
// terminal. Skipped only in unit tests that pass cfg=nil.
func (b *routeBuilder) registerAdminOpsRoutes() {
	if b.cfg == nil {
		return
	}
	// Self-upgrade: always mounted (manifest URL is hard-coded in service).
	upgrader := service.NewUpgrader(&b.cfg.Upgrade, b.opts.CurrentVersion, b.opts.ListenAddr)
	upgrader.ConfigPath = b.cfg.Path()
	uph := NewAdminUpgradeHandler(upgrader, b.rt.Drainer)
	b.admin.GET("/upgrade/check", uph.Check)
	b.admin.POST("/upgrade/apply", uph.Apply)
	b.admin.POST("/upgrade/restart", uph.Restart)
	b.admin.POST("/upgrade/rollback", uph.Rollback)
	b.admin.GET("/upgrade/status", uph.Status)
	b.admin.GET("/upgrade/busy", uph.Busy)

	// Admin browser-terminal (tty). A pure interactive PTY into the
	// selected account's CLI under a chosen working directory — no chat
	// history, no token accounting. Admin-only by the group gate above.
	// Shares the chat path's Pool so tty sessions are counted against the same
	// account (EnterLive's LiveMultiplier×MaxConcurrent cap) instead of forking
	// full agent processes outside every limit.
	terminalH := NewAdminTerminalHandler(b.cfg, b.rt.Pool)
	b.admin.GET("/terminal/ws", terminalH.HandleWS)

	// Session diagnostic bundle. Admin-only because it reads the account-level
	// config dir — codex's config.toml enumerates every project path on the
	// host, i.e. every other user's directories. See SessionBundleHandler.
	bundleH := NewSessionBundleHandler(b.store, b.cfg, b.rt.Pool, b.rt.Backends, b.opts.CurrentVersion)
	b.admin.GET("/conversations/:id/session-bundle", bundleH.Download)
}

// registerFrontendRoutes serves the embedded SPA, if one was provided.
func registerFrontendRoutes(r *gin.Engine, frontendFS fs.FS) {
	if frontendFS == nil {
		return
	}
	fileServer := http.FileServer(http.FS(frontendFS))
	// serveFrontend wraps fileServer with cache headers. Vite emits
	// content-hashed filenames under /assets/, so those can be cached
	// forever; everything else (most importantly index.html) must
	// revalidate on every load, otherwise a browser holding a stale
	// index.html keeps requesting old asset hashes after a redeploy.
	serveFrontend := func(c *gin.Context) {
		if immutableAsset(c.Request.URL.Path) {
			c.Header("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			c.Header("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(c.Writer, c.Request)
	}
	r.NoRoute(func(c *gin.Context) {
		urlPath := c.Request.URL.Path
		if strings.HasPrefix(urlPath, "/api/") {
			return
		}
		// Try to serve the exact file first
		if f, err := frontendFS.Open(strings.TrimPrefix(urlPath, "/")); err == nil {
			_ = f.Close()
			serveFrontend(c)
			return
		}
		// Release builds gzip-precompress JS/CSS at embed time and drop the
		// raw originals to keep the single binary small. If the raw asset
		// is gone but a .gz sibling is embedded, serve that instead.
		if immutableAsset(urlPath) &&
			serveGzipAsset(c, frontendFS, strings.TrimPrefix(urlPath, "/")) {
			return
		}
		// Asset-like requests (anything with a non-HTML file extension)
		// must 404 instead of falling back to index.html. A stale browser
		// holding an old /assets/index-<hash>.js URL after a redeploy
		// would otherwise receive index.html with text/html, which the
		// browser rejects under strict module MIME-type checking.
		if ext := path.Ext(urlPath); ext != "" && ext != ".html" {
			c.Status(http.StatusNotFound)
			return
		}
		// SPA fallback: serve index.html for client-side routes.
		c.Request.URL.Path = "/"
		serveFrontend(c)
	})
}

// officeAssetPrefix is where the two embedded office editors' runtimes live.
//
// They differ from Vite's output in two ways that matter here. Their filenames
// are fixed by upstream — the spreadsheet runtime starts its workers with
// new URL("./parser.worker.js", import.meta.url) — so they cannot carry a
// content hash; the frontend puts the package version in the path instead, which
// makes every URL's content equally immutable. And they are staged into the
// build already gzipped, with no raw sibling at all, because the ~28MB of raw
// JS would otherwise be embedded verbatim into the binary and re-sent on every
// cold load. Both properties line up with what /assets/ already gets, so the
// same two rules simply have to cover this prefix too.
const officeAssetPrefix = "/casual-office/"

// immutableAsset reports whether a URL path addresses content that can never
// change under that name, and so may be cached for a year.
func immutableAsset(urlPath string) bool {
	return strings.HasPrefix(urlPath, "/assets/") || strings.HasPrefix(urlPath, officeAssetPrefix)
}

// serveGzipAsset serves a gzip-precompressed embedded asset. Given the logical
// path (e.g. assets/index-<hash>.js) it looks for a ".gz" sibling that the
// release build emits in place of the raw file. Returns false when no such
// sibling exists, so the caller can fall through to its 404 / SPA logic.
//
// Almost every client advertises gzip, so the common path streams the stored
// bytes verbatim with Content-Encoding: gzip. The rare client that doesn't is
// served a decompressed copy so it never receives bytes it can't read.
func serveGzipAsset(c *gin.Context, fsys fs.FS, name string) bool {
	f, err := fsys.Open(name + ".gz")
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	// Content-hashed asset URLs are immutable; same caching as the raw path.
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("Content-Type", assetContentType(name))

	if !strings.Contains(c.Request.Header.Get("Accept-Encoding"), "gzip") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return true
		}
		defer func() { _ = gz.Close() }()
		c.Status(http.StatusOK)
		_, _ = io.Copy(c.Writer, gz)
		return true
	}

	c.Header("Content-Encoding", "gzip")
	c.Header("Vary", "Accept-Encoding")
	if info, err := f.Stat(); err == nil {
		c.Header("Content-Length", strconv.FormatInt(info.Size(), 10))
	}
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, f)
	return true
}

// assetContentType resolves the MIME type for an embedded asset by its logical
// name (the ".gz" suffix already stripped). JS/CSS are pinned explicitly so the
// browser's strict module MIME-type check always passes regardless of the host
// mime registry; anything else falls back to extension-based detection.
func assetContentType(name string) string {
	switch path.Ext(name) {
	// .mjs is the office document runtime's extension; some hosts have no
	// registry entry for it, and a module script served as anything but a
	// JavaScript type is rejected outright by the browser.
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	default:
		if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
			return ct
		}
		return "application/octet-stream"
	}
}
