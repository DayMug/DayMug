# Configuration Reference

DayMug's service-level runtime configuration comes from YAML; provider accounts, credentials, models, and prices are stored in SQLite and maintained through the admin panel. A copyable YAML template is in [config.sample.yaml](config.sample.yaml).

## Loading and precedence

At startup the following are resolved in order, and the first readable file wins; **if none is readable, startup is refused** (`ErrNoConfigPath`):

1. `--config <path>` command-line flag
2. `DAYMUG_CONFIG` environment variable
3. `config.yaml` in the same directory as the binary

**Read once at startup, no hot reload.** Restart after editing: Linux `systemctl --user restart daymug.service`; macOS `launchctl kickstart -k "gui/$(id -u)/com.daymug.daymug"`; in development, restart the `air` / `serve` process.

Environment variable overrides (no YAML edit needed): `DAYMUG_CONFIG` (config path), `DAYMUG_ADDR` (listen address).

Other operational switches that are read only from the process environment, never from YAML: `DAYMUG_HONOR_SIGTERM=1` (makes a bare SIGTERM take effect, for supervisors that can only send SIGTERM; see [Service management](installation.md#13-service-management)), `DAYMUG_CLAUDE_AGENT_SDK_MODULE` (manually point at the Agent SDK's `sdk.mjs`; see below), `DAYMUG_AGENT_SDK_MAX_PARKED` (cap on resident Agent SDK sessions, default 4, `0` disables residency), `DAYMUG_WECHAT_WIRE_DEBUG=1` (log raw WeChat protocol messages).

## Hard-coded, not in YAML

- **SQLite path**: fixed at `<binary_dir>/data/database.db`, no YAML/env switch.
- **Self-upgrade manifest URL**: hard-coded in `service.DefaultManifestURL`; YAML only exposes watchdog-related runtime switches.
- **User↔account bindings**: stored in the `user_provider_bindings` table and managed through the admin UI; **there is no YAML hook**.

---

## `server` — Listening and external address

```yaml
server:
  addr: "127.0.0.1:8090"
  public_url: "https://daymug.example.com"   # optional
```

| Field | Default | Description |
|------|------|------|
| `addr` | `:8080` | Listen address. Use `127.0.0.1:<port>` behind a reverse proxy on the same host; use `:<port>` or `0.0.0.0:<port>` to expose directly (for HTTPS see `auth.cookie_secure`) |
| `public_url` | empty | External base URL users reach the app at. **Strongly recommended**: push notifications (Bark) only carry a clickable deep link when it is set; without it notifications are still sent but tapping them does nothing. Trailing slash optional |

---

## `auth` — Sessions and login

```yaml
auth:
  session_ttl: 720h
  cookie_secure: false
  password_login_enabled: true
```

| Field | Default | Description |
|------|------|------|
| `session_ttl` | `720h` (30 days) | Login session lifetime, Go duration syntax; cookie max-age follows this value |
| `cookie_secure` | `false` | Forces the `Secure` flag on the session cookie. Leaving it `false` does not mean the flag is never set: it is added automatically when the request itself is HTTPS (direct TLS, or a reverse proxy forwarding `X-Forwarded-Proto: https`). It only needs to be set to `true` explicitly when a reverse proxy terminates TLS without forwarding that header. Defaults to `false` because browsers drop `Secure` cookies over plain HTTP, which would make LAN/local deployments unable to log in |
| `password_login_enabled` | `true` | Username + password login toggle; set `false` to use OIDC only |

---

## `admin` — Admin fallback

```yaml
admin:
  bootstrap_usernames:
    - admin
```

Usernames listed here are set to `is_admin=true` **on every startup**. This is the recovery path after accidentally demoting everyone: add your own username and restart to regain admin.

---

## `users` — User directories and concurrency limit

```yaml
users:
  default_home_root: ~/.daymug/users
  max_concurrent: 4
```

A new human user's default work_dir is `<default_home_root>/<username>`. A leading `~/` expands to the service user's home; the resolved path **must be absolute**, otherwise startup fails. Defaults to `~/.daymug/users` when empty. The root itself can only be changed in config and is shown read-only in the admin UI; admins can override an individual user's work_dir on the user edit page.

`max_concurrent` caps how many tasks a single user can run at once across all provider accounts, Web/API/IM entry points, and `/compact`. Excess tasks stay queued on their original account; binding a user to multiple accounts does not bypass the limit. Defaults to **4** when omitted or set below 1.

---

## `retention` — Automatic cleanup of old data

```yaml
retention:
  inactive_conversations: 0
  deleted_conversations: 0
  uploads: 0
```

By default no conversation data is deleted automatically.

| Field | Default | Description |
|------|------|------|
| `inactive_conversations` | `0` (off) | Conversations with no update (`updated_at`) for longer than this are deleted by a background maintenance job, same as a manual user delete (soft delete); pinned conversations are kept. E.g. `336h` = 14 days |
| `deleted_conversations` | `0` (off) | How long soft-deleted users, Agents, and conversations are kept before a background maintenance job (every 6 hours) purges them permanently, including messages. E.g. `168h` = 7 days |
| `uploads` | `0` (off) | Uploaded attachments (web and IM) older than this are deleted the next time their directory is written to; files referenced in old conversations will break. E.g. `2160h` = 90 days |

Whenever a conversation is deleted — manually, via "clean up stale conversations", or by `inactive_conversations` — the attachment files its messages carry (web uploads and IM attachments under `.daymug/agents/<agent id>/`) are removed from disk right away, independent of `uploads`. A file another live conversation still references is kept, and thread case files are never touched. The conversation row itself stays soft-deleted as before, so a conversation restored by hand comes back without its attachments.

Clicking "Optimize database" in the admin panel still purges data deleted more than 7 days ago; that is an explicit action and is not affected by this setting.

---

## `guardrails` — Per-task usage guardrails

```yaml
guardrails:
  max_model_calls_per_turn: 50
  max_tool_calls_per_turn: 100
  max_cost_usd_per_turn: 0
```

A "task" is the full execution triggered by one top-level user input, not the whole conversation. Every new Web or IM user message resets the count. A running task only has its usage tallied; guardrails never pause it, cancel it, or inject a confirmation prompt. If any threshold is reached, a one-time notice is shown when the user sends the next message, and that new message still runs normally.

| Field | Default | Description |
|------|------|------|
| `max_model_calls_per_turn` | `50` | Notice threshold for actual model requests by the main Agent (excluding subagents); `0` disables |
| `max_tool_calls_per_turn` | `100` | Notice threshold for real tool invocations by the main Agent (excluding subagents); tool results are not double-counted; `0` disables |
| `max_cost_usd_per_turn` | `0` | USD cost incurred or estimated from provider usage; `0` (default) disables |

If a task finishes over a threshold, DayMug records a pending notice for that conversation in process memory. When the next user message actually starts, the web UI and IM show the previous task's model request count, tool call count, recorded cost, and which thresholds were hit, then continue with the current message. Each notice is consumed once; a service restart clears notices not yet shown. Cron and provider-initiated wake-ups neither trigger nor consume notices.

**Case mode exemption**: for Agents with [case mode](im-bots.md#case-mode) enabled, IM thread turns are not counted toward this notice (model requests, tool calls, and cost are all excluded), controlled by `AgentStreamRequest.Unbounded`. Web sessions of the same Agent are still subject to guardrail notices.

There is one observability limit at the provider boundary: Claude exposes each request at `message_start`; Codex app-server currently only exposes underlying requests in token-usage notifications. So Codex counts update after a request completes, but both are only used for post-task soft notices and never interrupt the provider.

All values must be non-negative. The startup log prints the effective values, without credentials or other sensitive configuration.

---

## Provider accounts, models, and prices (SQLite)

These are not configured in YAML; they are managed under "Settings → Admin panel → Providers & models":

- Provider accounts: name, type (`claude` / `codex` / `claude-compatible` / `openai-compatible`), credentials directory, environment variables, concurrency limit; can be added, removed, and reordered.
- Models: each account's available models, default model order, and summary model.
- Token prices: input, cache, and output prices for `codex` / `openai-compatible` / `claude-compatible` (`claude` self-reports cost that matches the Anthropic bill, so no override is needed).
- Transport: chosen per provider type, see the two sections below; before saving, the host is probed for the required runtime, and the change takes effect from the next turn.

The first item in the list is the default provider for new conversations. Users without a bound account of the matching type are still rejected and never silently share another account. Changes saved to SQLite apply to new tasks immediately, with no file edit or restart.

Providers are never read from YAML; a `providers` or `claude_accounts` block left in an old config file is ignored.

### Adding an account: Agent framework × access method

"Providers & models → Add account" is a two-step choice; the four provider types are the combinations of the two:

| | Official account login | API Key |
|---|---|---|
| **Claude Code** | `claude` (`claude auth login`, cost self-reported by Claude Code) | `claude-compatible` (any Anthropic Messages–compatible endpoint) |
| **Codex** | `codex` (`codex login`, estimated from built-in OpenAI prices) | `openai-compatible` (any OpenAI **Responses**–compatible endpoint) |

After choosing API Key, pick a vendor preset: Anthropic API, OpenAI API, DeepSeek (via Claude Code,
prefilled with `https://api.deepseek.com/anthropic` and the Sonnet/Opus/Haiku/subagent model mapping; see
[DeepSeek's Claude Code integration guide](https://api-docs.deepseek.com/quick_start/agent_integrations/claude_code/)),
or "Other (custom compatible endpoint)". A preset only creates a draft on the page; fill in the key, save, then click "Check account" to verify a real call.
If a vendor offers both the Anthropic and OpenAI Responses protocols, the same API key can be used to create
separate Claude Code and Codex accounts. Endpoints compatible only with `/chat/completions` work with neither framework.

- `claude-compatible`: the page's URL and key map to `ANTHROPIC_BASE_URL` / `ANTHROPIC_AUTH_TOKEN` (or `ANTHROPIC_API_KEY`).
- `openai-compatible`: the page's URL and key map to `OPENAI_BASE_URL` / `OPENAI_API_KEY`. When both are set, DayMug
  starts app-server with `-c model_provider=…` to define a Codex provider with `wire_api = "responses"` for that account,
  so **no hand-written `config.toml` is needed** (the key is passed only via environment variables, never on the command line). If either is left empty, the provider
  declared in that account's own `CODEX_HOME/config.toml` is used instead (e.g. old configs using a different `env_key`). Each Codex account still needs
  its own `CODEX_HOME` directory.

URL and key share the same account settings as "Environment variables"; API keys are shown in password fields. Costs for API Key accounts are computed from the "Token prices" page,
so after connecting, fill in the vendor's rates and grant the account to the users who need it in user management.

### Model specs: context window and image input

Each model can declare two specs (stored in `specs` within `models.accounts`):

- **Context window**: accepts `128K`, `1M`, or a token count; empty = use the value the Agent reports. When Claude Code / Codex talk to third-party endpoints
  they only estimate the window for official models they recognize. Once set, the context progress bar and `auto_compact_ratio` use the declared value; Codex
  (CLI and app-server) also receives `model_context_window`, so its own auto-compaction uses this limit too. Claude Code has no equivalent parameter,
  so its internal auto-compaction still follows its own judgment.
- **Images**: unchecking means the model cannot read images. Attachments are still passed to the Agent as file paths, but the chat input warns the user when an image is attached.

For the Claude family (`claude` / `claude-compatible`), `config_dir` can be left empty to use the service user's `~/.claude`; the Codex family (`codex` / `openai-compatible`) requires a `CODEX_HOME` directory. To log in, click "Check account → Log in" for the account on that page; the admin terminal runs `claude auth login` (Claude) or `codex login --device-auth` (Codex).

---

## Claude programmatic protocol — Agent SDK

`type: claude` conversations run the official TypeScript Claude Agent SDK's `query()` by default; admins can switch this type's transport to CLI on the "Providers & models" page (one `claude -p` child process per turn, where messages sent mid-run can only queue until the turn ends). This is a database setting, not a YAML key.

SDK messages map to DayMug's existing streaming events. New messages sent during a run are fed into the current session dynamically via SDK streaming input; if the bridge does not acknowledge receipt, they fall back to regular FIFO queueing. Session resume, model, appended system prompt, MCP, read-only mode, account environment, sandbox, cancellation, and watchdog all keep their existing semantics.

**Prerequisites: Node.js 18+ and the global SDK package.** This is a hard dependency of the default transport. The one-line installer offers to install it (see [Installation](installation.md#11-install)); otherwise install it yourself:

```bash
npm install -g @anthropic-ai/claude-agent-sdk
```

The `daymug bootstrap` pre-flight report checks whether node and the package are present and warns if missing; at `serve` startup, if a `claude` account exists with the Agent SDK transport, a missing dependency refuses startup.

After startup DayMug resolves the SDK location on the Go side (`NODE_PATH` → node prefix's `lib/node_modules` → `~/.local`, `/usr/local`, `/usr/lib`, Homebrew → `npm root -g` as a last resort), passes the absolute path to the bridge via `DAYMUG_CLAUDE_AGENT_SDK_MODULE`, and, when the sandbox is enabled, bind-mounts that `node_modules` directory read-only into the jail. **This step is required**: the global `node_modules` is not in any default bind and `npm` is not visible either, so without it the bridge inside the jail can neither import the SDK nor fall back to `npm root -g`. For unusual install paths (vendored / pnpm store / container image), set `DAYMUG_CLAUDE_AGENT_SDK_MODULE` manually to the absolute path of `sdk.mjs`; the mount follows it.

The startup log prints the SDK version and path actually used. Note that **the executable is the Claude Code bundled in the npm package, not the host's `claude` binary**: upgrading this path means upgrading the npm package, and `claude --version` does not reflect the version agent-sdk mode runs.

In this mode auto-titling goes through the same bridge (a minimal session: no settings / MCP / CLAUDE.md / tools loaded, and no session file written) and does not depend on the `claude` CLI. This mode only affects `type: claude`; `claude-compatible` still uses the more compatible CLI adapter — the Agent SDK bridge resolves the globally installed npm package, and its support for a custom `ANTHROPIC_BASE_URL` is less direct than the CLI's.

---

## Codex programmatic protocol — app-server

`type: codex` conversations by default keep a resident `codex app-server --listen stdio://` per account; admins can switch the `codex` type to CLI on the "Providers & models" page (one `codex exec --json` child process per turn). `openai-compatible` only supports app-server. The default transport needs a Codex version with app-server:

```bash
npm install -g @openai/codex
codex app-server --help
```

It uses structured thread, turn, item, and token usage events. One account can run multiple conversations concurrently in a single process; if the process exits abnormally, a new instance is created on the next turn. At the end of each turn loaded threads are unsubscribed, while persistent history is still kept by Codex. The DayMug backend handles protocol translation; the browser never sees or connects to app-server directly. This path reports real context window usage (`tokenUsage` events carry `modelContextWindow`), used by the token progress bar, `auto_compact_ratio`, and `case_rotate_context_ratio`.

Codex login still uses Codex's own device authorization command; the two `*-compatible` types do not use OAuth, and credentials are stored as an API key in the account's `env`.

The resident pool uses `CODEX_HOME` as the account identity and folds the account environment, outer sandbox type, and jail root into an isolation fingerprint. If two users are bound to the same account but run in different jails, DayMug keeps two isolated instances rather than breaking the filesystem boundary to reuse one process. One-shot Codex calls such as auto-titling also reuse the selected app-server backend; one-shot flows with a different isolation config may use a separate instance under that account.

Because the jail root is the session's working directory, the fingerprint granularity is effectively "account × working directory". The pool self-limits: instances idle for over 3 minutes are reclaimed on the next lookup, idle instances are evicted LRU when more than 16 are resident (instances with a running turn are never reclaimed), and processes are shut down on a clean shutdown, so restarts/self-upgrades do not leave orphans.

app-server always uses pipes at the protocol boundary: JSON-RPC over a pseudo-terminal gets corrupted by terminal echo. The plain CLI adapter always uses a PTY; there is no run-mode switch. The stall watchdog (`runner_stall_timeout` / `runner_max_silent_timeout`) applies to both transports; a turn silent for too long is aborted with `turn/interrupt` and reported as an error, instead of holding an account pool slot indefinitely.

---

## runner watchdog — Liveness probing for long tasks

```yaml
runner_stall_timeout: 10m
runner_max_silent_timeout: 2h
im_run_timeout: 0s
auto_compact_ratio: 0
case_rotate_context_ratio: 0.70
```

- `runner_stall_timeout`: once the agent CLI's stdout has been silent this long, DayMug does not kill the process outright; it first checks whether the process group's CPU time and the CLI session log are still growing.
- `runner_max_silent_timeout`: absolute cap on a single stdout silence; past this, the task is judged stuck even if probes still show activity.
- `im_run_timeout`: total wall-clock cap for a single IM (Slack / Feishu (Lark) / Telegram / WeChat) agent turn. The default `0s` means no limit, so long tasks still running tools normally are not force-SIGTERMed after a fixed time; set e.g. `2h` or `6h` if you need a hard cap on slot occupancy.
- `auto_compact_ratio`: after a turn ends, if session context usage (`used / total`) reaches this ratio, `/compact` runs automatically once. The default `0` keeps it manual. Must be in `[0, 1)`; `>= 1` or negative values error at startup — a threshold that never fires is harder to debug than one that is simply off.
- `case_rotate_context_ratio`: the watermark at which IM threads rotate sessions in case mode; see [case mode](im-bots.md#case-mode). Empty means the built-in `0.70`. Must be in `(0, 1)`; here `0` means "unset", not "off" — not rotating in case mode is exactly the failure this switch fixes, so to turn it off, disable case mode on the Agent. Lower values save money (the longer the session, the more each turn's cache_read costs), but too low cuts the Agent off before it has finished writing the case file, and the case file is all the next session gets.

### Auto-compact behavior and limits

It triggers **after a turn ends and the task lock is released**, wired into both the web and IM paths; IM needs it most, because a thread's session is never naturally reset by "new conversation" the way the web UI is.

- A ratio rather than an absolute token count: backend windows differ by an order of magnitude (200k for claude vs. 1M for codex), so compaction should happen at the same *relative* pressure.
- Backends that don't support compaction are skipped instead of returning a 400 every turn.
- Only one auto-compact runs per session at a time; if the user sends a new message meanwhile, this run is abandoned (re-evaluated next turn) rather than interrupting the user.
- Compact is itself a full CLI child process and takes an account concurrency slot like a normal conversation, so it cannot bypass the pool limit.
- The summary stays in history as an assistant message but is **not** automatically injected into the next turn's prompt; reference it yourself when the model needs to know.

The problem being solved is `cache_read` cost: every tool step in a turn rereads the entire resident context, so long sessions scale cost linearly with the number of tool steps. In long-running agent sessions cache_read typically dominates total cost, and rotating sessions is the main lever that reduces it. `0.6` ~ `0.7` is a reasonably safe starting point.

The watchdog protects long-running tools / subagents whose stdout is silent, while keeping a fallback exit for when the gateway hangs. Even with `im_run_timeout: 0s`, tasks can still be cancelled from the web UI and are bounded by the account concurrency pool, graceful service restarts, and the watchdog.

---

## `sandbox` — Per-process isolation

```yaml
sandbox:
  enabled: false
  type: noop           # noop | bwrap
  network: true
  extra_ro_binds: []
  extra_rw_binds: []
```

| type | Description |
|------|------|
| `noop` | No isolation; the Agent inherits the service user's full filesystem reach. `enabled: true` + `type: noop` is normalized by Validate back to "effectively unisolated" |
| `bwrap` | Linux bubblewrap jail. Users with `sandbox_mode: unrestricted` are not jailed; everyone else is confined to their own work_dir — other users' files, the central DB, and host secrets are not mapped into the namespace. Requires `bwrap` on PATH; **its absence does not fail startup** — it only logs a `SECURITY:` warning and keeps running with no isolation at all (so an optional binary can't take down the live service), so after enabling it you must check the startup log to confirm |

`network` only matters for bwrap: true (the default, also when the key is omitted) shares the host network, false starts jailed Agents offline. `extra_ro_binds` / `extra_rw_binds` are absolute host paths additionally mounted into every jailed Agent (e.g. a shared assets directory).

Inside the jail the system tree (`/usr`, `/bin`, `/lib*`, `/etc`, `/opt`) is read-only, and so is every non-system directory on the **service's** `PATH` (nvm, pnpm, bun, cargo, `~/.local/bin`, virtualenvs, …). DayMug follows each command's symlinks, scans launcher scripts for the program they exec (pnpm's `$basedir/../global/…` shim, a wrapper into a virtualenv), and mounts what they point at, so a command that works in the service's shell works in the jail too. A tool missing from the service's `PATH` (check `systemctl --user show daymug -p Environment`; Go's `/usr/local/go/bin` is a common omission) is not found by name inside the jail either. Home directories, ancestors of the jail, and ancestors of the account `config_dir` are never mounted this way. Not covered: shim-style managers that resolve the real program at runtime (volta, asdf, mise) — add their data dir to `extra_ro_binds` — and `pip install --user` packages, which python looks up under `$HOME`, i.e. inside the jail. Users install packages into their own work_dir (`npm i`, `pnpm add`, a venv, `go mod download`); with `network: false` anything that downloads fails.

The sandbox treats both entry points the same: browser chat and IM conversations (Slack / Feishu (Lark) / Telegram / WeChat). The jail level is decided by "who owns the Agent" (admins are never jailed), the same rules as the web UI.

> ⚠️ For now treat DayMug as single-tenant (or wrap it in container-level isolation), unless the bwrap sandbox is enabled. The HTTP layer's `safePath()` only constrains the API, not the spawned CLI. See [Multi-user model](../architecture/multi-user.md).

---

## `oidc` — Casdoor / generic OIDC SSO (optional)

With `enabled: false` (default) all other fields are ignored. Once enabled, `issuer` / `client_id` / `client_secret` / `redirect_uri` are **required and validated at startup**; failing to discover the issuer aborts startup.

```yaml
oidc:
  enabled: false
  issuer: https://idp.example.com
  client_id: ""
  client_secret: ""
  redirect_uri: https://daymug.example.com/api/auth/oidc/callback
  scopes: ["openid", "profile", "email"]
  group_claim: groups
  role_claim: roles
  permission_claim: permissions
  required_groups: []
  required_roles: []
  required_permissions: []
  auto_provision: true
  button_label: "Sign in with Casdoor"
```

| Field | Description |
|------|------|
| `issuer` | Endpoints are learned via discovery from `<issuer>/.well-known/openid-configuration`; don't hard-code paths |
| `redirect_uri` | Must equal "the public URL the SPA uses to reach the service + `/api/auth/oidc/callback`", matching the IdP config character for character |
| `scopes` | `profile` is required (Casdoor uses it to put groups/roles/permissions on userinfo) |
| `*_claim` | Claim names; only change them if your values live under non-standard keys |
| `required_*` | Access policy: each non-empty list forms a dimension, and the user must satisfy "≥1 item in ≥1 dimension" (OR across dimensions). All three empty = any authenticated IdP user can log in (subject only to `auto_provision`) |
| `auto_provision` | true = auto-create an account on first OIDC login when no local user matches (matched by email); false = closed allowlist, admins must pre-create users |
| `button_label` | SSO button text on the login page; empty means "Sign in with SSO" |

Casdoor application essentials: token format JWT-Standard; grants `authorization_code` + `refresh_token`; Redirect URL matching character for character; scope `openid profile email`.

---

## `upgrade` — Self-upgrade watchdog

```yaml
upgrade:
  service: daymug          # systemd unit name
  service_mode: user       # user | system
  health_path: "/api/health"
  health_timeout: 60s
```

The manifest URL is hard-coded server-side and needs no configuration; only deployment-related switches live here. After swapping the binary, the watchdog uses these to restart the service and poll `health_path` (host:port is derived from the listen address); if no 200 comes back within `health_timeout`, it rolls back to the previous binary. `service_mode: user` runs `systemctl --user restart`; `system` drops `--user`.

---

## `usage` — Usage statistics time zone

```yaml
usage:
  timezone: "Asia/Shanghai"
  high_model_requests: 20
  high_tool_calls: 30
  high_conversation_cost_usd: 10
  context_warning_ratio: 0.8
```

Per (user, model, day) token/cost aggregation uses this time zone to decide "which day". An IANA name / `"UTC"` / `"Local"`; empty defaults to `Asia/Shanghai`. An unparseable name errors at startup. **Changing the time zone does not migrate old rows**: on the day of the switch, the two bucketings overlap for one day.

The other four fields control the explainable anomaly hints on the usage page: per-task model requests, tool calls, per-conversation cost, and context watermark.
The cache-read share threshold is fixed at 95%. `context_warning_ratio` must be between 0 and 1; hints only suggest compacting
or starting a new conversation and never modify the conversation. Actual auto-compaction is still controlled by the top-level `auto_compact_ratio`.

---

## Minimal working config

```yaml
server:
  addr: "127.0.0.1:8090"
admin:
  bootstrap_usernames: [admin]
```

All other fields use their defaults. After the first login, add provider accounts under "Settings → Admin panel → Providers & models". The full annotated template is [config.sample.yaml](config.sample.yaml).
