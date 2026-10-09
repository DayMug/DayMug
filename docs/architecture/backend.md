# Backend architecture (Go)

Module: `github.com/DayMug/DayMug/backend`. Code style: tabs, `gofmt`/`goimports`, local import prefix `github.com/DayMug/DayMug`. Tests sit next to the code: `foo.go` is covered by a same-package `foo_test.go`.

## Layering and dependency direction

```
cmd/server  ──►  handler  ──►  service  ──►  store
                    │            │
                    └──►  agent ◄┘        (service depends only on the agent.Backend interface)
                          │
     claudeagentsdk / claudecli / codexapp / codexcli  (depend only on agent/**, config, prompts)
```

**Layering rules**:

- `handler` depends on `agent` (for the `Backend` interface) and `service` (business use cases + cross-adapter utilities).
- Adapter subpackages (`agent/claudeagentsdk`, `agent/claudecli`, `agent/codexapp`, `agent/codexcli`) depend only on `agent` and its shared subpackages (`streamcommon` / `sessionlog` / `residentpool` / `codexcommon` / `pricing`). The only inter-adapter dependency is `claudeagentsdk` reusing `claudecli`'s stream-json parser (`StreamProcessor`) and default title model — both run the same Claude Code.
- `service` **never** imports a concrete adapter directly; when it needs a backend it receives the `agent.Backend` interface, and `buildBackends` in `handler/runtime.go` wires the concrete implementations into `service.BackendRegistry`.
- `middleware` depends on `store.Store` directly (`RequireAuth` looks up sessions). This is a cross-cutting concern, not a business use case, so it bypasses `service`.

These rules are enforced by tests in `internal/archtest`, not by reviewers' memory: it parses the imports of every
non-test file under `internal/` and checks each layer against its allow-list (`agent` may only touch `agent/**`, `config`, `prompts`; `store`
is the foundation and may only touch itself; `service` may not import `handler` / `middleware`; `config` / `prompts` /
`userenv` are leaves with no internal dependencies). Test files are out of scope — store tests using service fixtures is
fine, because what matters is coupling in production code, not test code. Adding a new `internal/<layer>` turns `TestRulesCoverEveryLayer`
red, forcing a decision about its layer at creation time rather than untangling it a year later.

### handler ↔ service boundary

handler does only three things: **parse parameters, draw the permission boundary, render the response**; business orchestration lives in `service`.

- Service use cases are **bare structs with exported fields and no constructor** (modeled on `service.PromptRunner`); the handler side assembles them on the spot via **lazy factories** (`AdminHandler.ops()`, `AuthHandler.authOps()`, `ConversationHandler.ops()`, etc.). They are lazy rather than snapshotted in the constructor because `routes.go` calls `NewXxxHandler(...)` first and assigns fields afterwards — a construction-time snapshot would permanently capture a nil `*config.Config` and silently skip provider/model validation.
- Shared dependencies for running agent turns (Store / Cfg / `BackendRegistry` / Broadcaster / UserHub / Drainer / Pause / Pool / Sandbox / Dispatcher / title generation / push / PromptObserver) are centralized in `service.Runtime`: `buildRuntime` in `handler/runtime.go` assembles it once before any route is mounted, and `RegisterRoutes` calls `Runtime.Validate()` when starting with a config — it uses reflection to check every field not tagged `runtime:"optional"`, so a newly added dependency that was never wired refuses startup instead of receiving nil and being swallowed by a nil-safe branch. `TerminalHandler`, `CronScheduler`, and `imbridge.IMBridge` all embed the same `*service.Runtime`; `PromptRunner` / `MessagePersister` / `TurnAdmission` / `PromptIntake` are assembled only by the same-named methods on `Runtime`. The Dispatcher's callback is `Runtime.ProcessPrompt`; both web chat and cron enter the service layer through it, not through handler.
- The two layers **pass `store` models directly, with no DTOs**. Inputs use service-side Params structs; `json` / `binding` tags stay on handler request structs.
- Errors: service returns `*service.ServiceError` (HTTP status + client-facing message + preserved cause), and handler renders it uniformly via `respondServiceError` in `respond.go`; errors that are not `ServiceError` fall back to the legacy store mapping (404 / 500).

**When to push logic down**: only when a handler function performs **≥2 store writes**, or **"a write + non-trivial decision logic"**. For single-read or single-write pass-throughs, wrapping them in a service just adds indirection.

So handler keeps an explicit **thin-read allow-list** — reading store directly is intentional, not legacy debt, and the gate blocks only writes, not reads. Typical cases:

| Location | Shape |
|------|------|
| `file.go` / `browse.go` | 1 `GetUser` each to get `WorkDir`; everything else is filesystem operations |
| `app_state.go` | BFF read-model; the shape read is the shape of the HTTP response |
| `usage.go` | Thin reads (`ListUsers` / `AggregateTokenUsage` / `ListTokenUsageModels`) + pure computation |
| `terminal_ws.go` | On-demand reads inside the WS frame loop |

`handler/layering_test.go` is the static gate for this rule: it uses `go/ast` to scan the handler package's production files (skipping `_test.go`) and fails on any call to a store **write method** (`Create*` `Update*` `Set*` `Delete*` `Reset*` `Reorder*` `Archive*` `Unarchive*` `Ensure*` `Regenerate*` `Save*` `Clear*`). The criterion is the **call's receiver**, not the method name — only fields, variables, and type assertions declared as `store.Store` or one of its domain sub-interfaces (`store.UserStore` / `store.BotStore` / `store.CronStore` …, list in `layeringStoreInterfaces`) count, so `h.Store.CreateUser(...)` is a violation while `h.ops().CreateHuman(...)` is legal. Exceptions live in `layeringWaivers` (file or file+method granularity, each with a required reason), and `TestLayeringWaiversAreLive` fails as soon as the corresponding write is moved out. `go/ast` was chosen over `depguard` because the target is a "method call pattern", not a "package import", and depguard cannot exempt per file.

`layeringWaivers` is split into two categories, and every new waiver must state which one it belongs to: **deliberate** entries (e.g. `DeleteSession` in `auth.go`, `UpdateConversationWorkDir` in `upload.go`) must explain why the write inherently belongs in handler; **debt** entries must be something a later change can move into a `service` use case.

## `cmd/server/` — entry point and subcommands

`main.go` only dispatches; each subcommand lives in its own file.

| Subcommand | File | Purpose |
|--------|------|------|
| `serve` | `cmd_serve.go` + `app.go` | Start the HTTP server (default subcommand). `buildApp(cfg, opts) (*App, error)` in `app.go` assembles everything (open DB → read app_settings → probe agent runtimes → mount routes); all errors are returned upward and only `cmdServe` calls `log.Fatal`. `loadAppSettings` reads the DB in the order providers → transports → pricing → models and returns `appSettings`; runtime probing and `registerRoutes` must both start from that return value, so "load before use" is guaranteed by code structure, and the read order is asserted by a test |
| `bootstrap` | `cmd_bootstrap.go` + `cmd_install.go` / `cmd_install_service.go` | Preflight + create the `~/.daymug/` layout + register a user-level system service (systemd --user / launchd). Idempotent |
| `doctor` | `cmd_doctor.go` | Read-only diagnosis: runtimes, config, service state, linger (Linux), health endpoint; exits non-zero and prints a fix for each failure |
| `uninstall` | `cmd_uninstall.go` | Stop and remove the user-level service; keeps `~/.daymug` unless `--purge` |
| `stop` | `cmd_stop.go` | Gracefully stop the server started by this binary, via `data/daymug.pid` (`--timeout`) |
| `check-config` | `cmd_check_config.go` | Only load and validate `config.yaml`; self-upgrade runs it with the new version as a preflight before swapping the binary |
| `init` | `cmd_init.go` | Generate `config.yaml` + `data/` + `users/` in the current directory (for non-standard layouts) |
| `upgrade` | `cmd_upgrade.go` | Apply a release from the hard-coded manifest; `--version` selects a tag, `--rollback` rolls back |
| `upgrade-watchdog` | `cmd_upgrade.go` | Self-upgrade watchdog (restarts and health-checks after the binary swap, rolls back automatically on failure) |
| `user` | `cmd_user.go` | `user add` / `user passwd`: create a login user, set/change password |
| `version` / `status` | `cmd_version.go` | Print version / show systemd service status |

Frontend embedding is controlled by a build tag: `frontend_embed.go` (`//go:build embed_frontend`) vs `frontend_dev.go`.

## `internal/config/` — configuration

`Config` in `config.go` holds server-level YAML switches. `Load()` resolves `--config` → `DAYMUG_CONFIG` → `<binary_dir>/config.yaml` in order, and refuses to start with `ErrNoConfigPath` if none is found. Loading has four stages: parse YAML (layered over `defaultConfig()`) → `applyEnv` (DayMug's own env-var overrides are read only here; `DAYMUG_ADDR` is consumed via `Config.ListenAddr()`) → `Normalize()` (fill defaults, canonicalize spelling, never fails) → `Validate()` (read-only, reports the first error in a fixed order). A hand-built `Config` must also call `Normalize()` before `Validate()`. Provider accounts never come from YAML: `service/provider_settings.go` loads them from SQLite into a thread-safe runtime snapshot. Field details: [configuration guide](../guides/configuration.md).

## `internal/middleware/` — cross-cutting concerns

CORS (`cors.go`), session auth, and the `RequireAdmin()` admin gate. Convention: public routes mount under `/api`, authenticated routes in a nested group, and admin routes wrapped in one more `RequireAdmin()`.

## `internal/handler/` — HTTP / WS handlers

Routes are registered centrally in `routes.go`; concrete `agent.Backend`s are wired in `buildBackends` in `runtime.go`. One file per resource + `*_test.go`. Business orchestration lives in `service`; handlers keep only parameter parsing, permission boundaries, and response rendering — see [handler ↔ service boundary](#handler--service-boundary) above for the boundary and its gate. Main files:

| File | Responsibility |
|------|------|
| `routes.go` | Route registration (`routeBuilder` mounts groups per resource) |
| `auth.go` / `oidc.go` | Password login, sessions, OIDC/Casdoor SSO |
| `bootstrap.go` | First admin creation (empty-DB bootstrap) |
| `user.go` | Agent (persona) CRUD + CLAUDE.md read/write + sort/archive/duplicate |
| `admin.go` | Human user management, bulk settings (default model / permissions / sandbox mode / provider binding), DB size/optimize, public config |
| `conversation.go` | Conversations and messages: create/delete, clear, change title/work dir/model, share, pin, notifications, clear context |
| `runtime.go` | Production `service.Runtime` assembly: `buildBackends` (per-provider backend + transport switching), title generation, Pool, Sandbox |
| `terminal.go` / `terminal_ws.go` | Main WebSocket chat path (enqueue, steer, cancel, replay); running a turn is the job of `service.Runtime`'s Dispatcher |
| `compact.go` | `/compact` flow (capability-gated, rotates session id) |
| `file.go` / `file_upload.go` / `file_archive.go` | Workspace file browse, upload, download, compress/extract; path-traversal protection `safePath()` |
| `file_preview.go` / `file_search.go` / `file_watch.go` | File workbench: per-path preview rendering, bounded workspace search, WS change push for open files (`service/filewatch`) |
| `upload.go` | Chat attachment upload |
| `bot.go` / `bot_wechat.go` | Agent IM bot CRUD, connection test, WeChat QR pairing |
| `cron.go` | Crontab job CRUD |
| `session_bundle.go` | Admin export of one conversation's CLI session logs + runtime environment as a zip |
| `browse.go` | Directory picker (choosing work_dir for a new Agent), restricted to the caller's own work_dir |
| `models.go` | `/api/models`: per-provider model list + `Capabilities` matrix |
| `admin_terminal.go` / `account_env.go` | Admin browser terminal WS (runs the account's `claude`/`codex` in a PTY; with `login: true` runs `claude auth login` / `codex login --device-auth`), plus account env assembly |
| `admin_pricing.go` | Per-provider price table read/write (for cost calculation) |
| `admin_providers.go` / `admin_models.go` / `admin_transports.go` / `admin_account_check.go` / `admin_pause.go` | Provider accounts, model registry, transport selection, account check, pause-intake switch |
| `upgrade_admin.go` | Self-upgrade check/apply/restart/rollback/status |
| `app_state.go` / `server_info.go` / `access.go` / `health.go` / `help.go` | Frontend boot state, server info, access control, health check, help docs |
| `respond.go` | `respondServiceError`: the single translation point from `*service.ServiceError` to an HTTP response |
| `layering_test.go` | Static layering gate: forbids handler from calling store write methods directly |

## `internal/agent/` — agent backend abstraction

`agent.Backend` is the uniform interface every model adapter implements; `agent.RunRequest` is the neutral input contract; `agent.StreamEvent` + `Kind*` constants are the shared event alphabet consumed by handler; `agent.Capabilities` is the self-reported capability matrix. Shared spawner / sandbox / binary-resolver / probe primitives also live here (`spawner.go`, `sandbox.go`, `binary.go`, `probe.go`). Adapter details: [agent-backends.md](agent-backends.md).

## `internal/service/` — cross-adapter business logic

Business components decoupled from HTTP. `*_ops.go` files are business use cases moved down from handler, each a bare struct with exported fields:

| File | Responsibility |
|------|------|
| `errors.go` | `ServiceError`: status + client message + cause; the single contract for "how a failure becomes an HTTP response" |
| `access.go` | Ownership checks such as `CanAccessOwner` / `CanAccessChatOwner` (strict ownership, no admin shortcut) |
| `auth_ops.go` | Login, session creation, password change, notification channel and env-var settings |
| `conversation_ops.go` | Conversation create/delete/update, title/work dir/model, pin, share, clear context |
| `user_ops.go` / `admin_user_ops.go` | Agent (persona) use cases / human accounts and bulk settings (default model, sandbox mode, provider binding) |
| `bot_ops.go` / `cron_ops.go` | IM bot CRUD (including the `MergeStoredCredentials` credential-merge decision) / Crontab job CRUD (cron expression and timezone validation + `Scheduler.Reload`) |
| `bootstrap_ops.go` / `oidc_ops.go` | First-run admin bootstrap / OIDC auto-provisioning. Deliberately separate from `CreateHuman` in `admin_user_ops.go`: the paths take opposite values for username sanitization, admin-flag source, password, work_dir/provider inputs, and name-collision handling, so merging would only yield a function full of mode branches |
| `compact.go` | Full `/compact` flow: capability gate, busy guard, one-shot summary run, persistence, session id rotation |
| `app_setting.go` | `app_settings` key-value (`AppSettingStore` narrow interface) |
| `turn_admission.go` | The single admission flow for a turn: pause → conversation room → drainer (queued) → account pool ticket → bounded wait → running; the returned `TurnLease` releases in reverse order. Web / IM / `/compact` all go through it, with platform-specific copy injected via hooks such as `Describe` / `OnRefused` |
| `account_pool.go` | Per-account concurrency pool (fair scheduling across users by cumulative task run time over the last hour, exposing queue position to WS); account names are globally unique in config, yet `Pool.AccountForType(name, type)` still carries the provider type explicitly as a routing boundary |
| `cron_scheduler.go` | Loads users' Crontab jobs; on trigger creates a normal conversation and hands it to `Dispatcher`, thereby reusing account concurrency, env vars, sandbox, and message persistence |
| `conversation_seed.go` | `ResolveConversationSeed` / `CreateSeededConversation`: one set of rules for a new conversation's provider / model / think level, shared by Web creation, cron, and IM (no auth) |
| `prompt.go` | Builds the system prompt from Agent config |
| `prompt_dispatcher.go` | Per-conversation serialization of inbound prompts + `ResolveConversationAccount` |
| `prompt_intake.go` | Entry point for one Web chat prompt: resolve conversation and attachment ownership, persist as pending, pause/drain decision, steer or KickWorker; the WS handler only receives echo / ack / stale Insert errors via callbacks |
| `broadcaster.go` | Per-conversation multi-client WebSocket broadcast |
| `task_guardrail.go` | Soft per-turn consumption reminders (`guardrails.*`: model request / tool call / cost thresholds); the current task is not interrupted, the next user message gets one reminder, and only the parent Agent's requests and tool calls count |
| `imbridge/` | IM bridge: runs messages received by `imbot` connectors as agent turns (queueing, progress, case mode, web mirror), embeds `*service.Runtime` |
| `claudemd.go` | Read/write sync between CLAUDE.md and work_dir |
| `upgrader.go` / `upgrade_*.go` | Self-upgrade: manifest fetch, sha256-verified download, atomic binary swap, detached watchdog + automatic rollback on failure; manifest URL hard-coded in `DefaultManifestURL` |
| `oidc.go` | Casdoor / generic OIDC SSO |

## `internal/store/` — SQLite

Pure-Go `modernc/sqlite`, with append-only automatic migrations at startup driven by the `schema_migrations` version table. `Store` (`store_interface.go`) is the persistence interface upper layers depend on, composed of small per-domain interfaces (`UserStore`, `ConversationStore`, `MessageQueueStore`, `UsageStore`, `BotStore`, `CronStore`, `MarketplaceStore`, etc.); callers that use only one domain can depend on just that interface. Production binds to `*SQLiteStore` and tests to the in-memory `storetest.Fake`; `storetest/conformance_test.go` cross-checks the two domain by domain, and `conformance_coverage_test.go` requires every `Store` method to be covered by a conformance case or explicitly waived. Domain types are in `models.go`. Capabilities production always has go into `Store`, never probed via runtime type assertions — a missing implementation should fail to compile, not silently leave a route unmounted. **Writes** are orchestrated exclusively by `service`; `handler` / `middleware` only do the thin reads listed above (gate: `handler/layering_test.go`). Schema and migrations: [data-model.md](data-model.md).
