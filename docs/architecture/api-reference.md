# API Reference

All routes are registered in `backend/internal/handler/routes.go`. There are three access tiers:

- **Public** (`/api/*`, no login required)
- **Authenticated** (`authed` group, requires a session cookie)
- **Admin** (`admin` group, gated by `RequireAdmin()`)

Real-time conversation traffic goes over **WebSocket**; everything else is REST. Endpoints are grouped by function below, and paths omit the `/api` prefix.

## Public

| Method | Path | Purpose |
|------|------|------|
| GET | `/health` | Health check (polled by the self-upgrade watchdog) |
| GET | `/auth/options` | Login options (whether password / OIDC are available, SSO button label) |
| POST | `/auth/login` · `/auth/logout` | Password login / logout |
| GET | `/auth/oidc/login` · `/auth/oidc/callback` | OIDC authorization-code flow |
| GET | `/bootstrap/status` · POST `/bootstrap/admin` | Creates the first admin on an empty database. The emptiness check, the admin row and its default Agent commit in one transaction, so concurrent submissions yield exactly one admin (the rest get 409). The username (email local-part) must match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`; the password needs at least `min_password_length` (8) characters. `setup_mode` picks the form: `password`; `sso_email` (password login off, OIDC on — email only, the admin signs in later through SSO, which matches users by email); `sso` (OIDC `auto_provision` + `admin.bootstrap_usernames`: `setup_required=false`, POST 403, the first admin is created on SSO login); `unavailable` (no sign-in method enabled: the page shows the config fix, POST 403) |
| GET | `/shared/conversations/:token` | Read-only view of a conversation via a share token |

## Authenticated: account and info

| Method | Path | Purpose |
|------|------|------|
| GET | `/auth/me` | Current user |
| POST | `/auth/password` | Change password |
| GET · PUT | `/auth/environment` | Read / update personal environment variables (one `VAR=VAL` per line) |
| PUT | `/auth/notifications` | Update personal notification settings (Bark / PushDeer) |
| GET | `/server-info` · `/app-state` | Server info / frontend bootstrap state |
| GET | `/models` | Per-provider model list + `Capabilities` matrix |
| GET | `/help-doc` | Help document |
| GET/POST | `/marketplace/apps` | List site-wide apps / submit an app (name, description, link; icon link and deploy directory optional; response includes the publisher's name) |
| PUT | `/marketplace/apps/:id` | Edit any marketplace entry (same fields as POST; publisher unchanged) |
| DELETE | `/marketplace/apps/:id` | Delete any marketplace entry |

## Authenticated: Agent (persona) management

| Method | Path | Purpose |
|------|------|------|
| GET/POST | `/users` | List / create Agents (`default_model` optional) |
| PUT/DELETE | `/users/:id` | Update (including optional `default_model`) / delete (cascades to conversations and messages) |
| POST | `/users/:id/duplicate` · `/archive` · `/unarchive` | Duplicate / archive / unarchive |
| GET/PUT | `/users/:id/claude-md` | Read / write CLAUDE.md in the Agent's work_dir |
| GET/POST | `/users/:id/bots` | List / attach Bots; one Agent can have several, and an empty `model` inherits the Agent / server config |
| GET | `/users/:id/bots/requirements` | Required permission list for Slack / Feishu (Lark) Bots |
| GET | `/users/:id/integrations/status` | Live connection status of each Bot under this Agent (no credentials) |
| POST · GET | `/users/:id/bots/wechat-pairing` | WeChat iLink QR pairing: POST starts a QR code, GET checks scan status once (polling driven by the browser) |
| POST | `/users/:id/bots/test-connection` | Read-only test of the current Bot draft's credentials, connection, and per-item permissions (config not saved); with `bot_id`, blank fields are filled from stored credentials |
| PUT/DELETE | `/users/:id/bots/:botId` | Update / delete an attached Bot connection |
| PUT | `/user-order` | Sidebar drag-and-drop ordering |
| GET | `/archived-agents` | Archived Agent list |

Bot credentials are **write-only**. `bot_token` / `bot_app_token` / `bot_app_id` / `bot_app_secret`
can only be written; no response ever returns them in plaintext. Responses instead carry boolean flags
`bot_token_configured`, `bot_app_token_configured`, `bot_app_id_configured`,
`bot_app_secret_configured`, plus the aggregate `credentials_configured` (whether all credentials the
platform connection needs are present). On write, a blank (or whitespace-only) value means **keep the stored value**; only non-empty values replace it,
so editing channel rules doesn't require re-pasting the token.

Bot create/update requests and responses also include `model`, `unconfigured_reply`, and `unauthorized_reply`.
A non-empty `model` is used for new IM threads created by that Bot; an empty value dynamically inherits the Agent's default model and server config.
Editing never switches the provider/model of existing threads.
`unconfigured_reply` is used for conversations not covered by any rule, and `unauthorized_reply` when the sender isn't in `allowed_user_ids`. Both are sent only when
a human explicitly @-mentions the Bot or DMs it directly, and never trigger the Agent. Leaving them blank on create stores the system default text.

## Authenticated: conversations and messages

| Method | Path | Purpose |
|------|------|------|
| GET/POST | `/conversations` | List (`?user_id=`, optional `?limit=&offset=`) / create; create accepts `provider/model/account/think_level` |
| GET/DELETE | `/conversations/:id` | Get one / delete |
| DELETE | `/stale-conversations?user_id=` | Soft-delete the given Agent's conversations last updated more than 14 days ago; returns deleted IDs and count |
| GET | `/conversations/:id/messages` | Messages, oldest first: `?before_id=<id>&limit=` pages backwards (empty `before_id` = latest page), `?after_id=<id>&limit=` returns newer rows (empty = all); with neither, the whole conversation |
| DELETE | `/conversations/:id/messages` | Clear messages (deletes message rows only, leaves the session id); pushes `messages_cleared` to the conversation room so other tabs drop the chat body |
| POST | `/conversations/:id/clear-context` | Clear context |
| POST | `/conversations/:id/compact` | `/compact` (capability-gated, supported backends only) |
| PUT | `/conversations/:id/title` · `/model` | Change title / request config; `/model` accepts `provider/model` and optionally updates `account/think_level` (empty `think_level` clears the override). Changing the provider or account returns 409 once the model has replied (`Store.HasModelReply`); a first turn that failed before the model answered leaves them switchable |
| PUT | `/conversations/:id/pinned` · `/notifications` | Pin / notification toggle |
| PUT | `/conversation-pin-order` | Pinned ordering |
| GET | `/conversation-attention` | Conversations under the current user's Agents that "need a look" (completed / failed / awaiting input and not yet viewed), plus `titles`: titles of the user's own running conversations (the global activity snapshot carries no titles) |
| POST | `/conversations/:id/read` | The user is viewing this conversation: clears the attention flag and pushes `attention_changed` to the user's other tabs |
| POST/DELETE | `/conversations/:id/share` | Create / revoke a read-only share link |

### Conversation list pagination

`GET /app-state?user_id=` preloads only the **first page** (`store.ConversationPageDefault` = 50 rows) and returns a
`conversations_has_more` boolean. This endpoint is hit on every page load, while the sidebar only draws one screen no matter how many rows exist.

Further pages go through `GET /conversations?user_id=&limit=&offset=`. The response is a **bare array** (not an envelope) on every path.
Without `limit`/`offset` the behavior is unchanged and returns everything (capped at `ConversationListHardCap` = 5000), so older clients are unaffected.
"Has a next page" is inferred from "rows returned == requested limit".

Pagination is offset-based, not cursor-based, so **clients must dedupe by conversation id**: the sort key `updated_at` only increases, so a conversation
that receives a reply while paging moves up and reappears in a later window (duplicates only, never gaps). The frontend's `appendPage()` handles this.

## Authenticated: workspace files (`/users/:id/files`)

Path traversal is guarded by `safePath()`; everything is confined to the Agent's work_dir.

| Method | Path | Purpose |
|------|------|------|
| GET | `files` · `files/read` · `files/download` · `files/download-zip` | List directory / read / download / download as zip |
| GET | `files/inspect` | Metadata for a single file (`modified` / `size`, etc.), used by the `files/watch` polling fallback |
| GET | `files/preview/*path` | Returns a file inline in **path form**: the URL path segment is the workspace path, so relative references in previewed HTML (scripts / styles / wasm / assets in subdirectories) resolve to the files next to it. `Cache-Control: no-cache` + `nosniff` + `frame-ancestors 'self'`; directories fall back to their `index.html`; Content-Type for `.js` / `.mjs` / `.css` / `.wasm` is pinned by the server rather than taken from the host's mime table |
| GET | `files/search` | Workspace search (`?q=` required, `mode=name\|content`, `path=` to scope, `limit=` max 200) |
| GET | `files/watch` | **WebSocket**: subscribe to on-disk changes for a set of files (see below) |
| PUT | `files/write` · `files/rename` · `files/move` | Write / rename / move |
| POST | `files/upload/check` | Pre-upload check for target name conflicts (`files/upload` re-checks) |
| POST | `files/upload` · `files/mkdir` · `files/copy` · `files/compress` · `files/extract` | Upload / make directory / copy / compress / extract |
| DELETE | `files` | Delete (recursive) |
| GET | `/browse-dirs` · POST `/browse-dirs/mkdir` | Server directory browsing / make directory (directory picker) |
| POST | `/uploads` | Chat attachment upload. Stored in the conversation Agent's scope: `<owner home>/.daymug/agents/<agent id>/uploads/`. Returns `ref` (absolute path, inserted into the message body by the composer) and `path` (relative to the owner's home, used with the owner id in `url` via `files/read`) |

### `files/preview` vs. `files/read`

Both endpoints read the same files and differ only in URL shape and purpose: `files/read` takes `?path=` and suits the frontend
fetching content itself (editor, Markdown, PDF `<iframe>`); `files/preview` puts the path in the URL path and exists specifically
for "render HTML as a web page". Only this way do `./app.js` and `./assets/app.wasm` in the page resolve to the files
next to it on disk rather than to `/api/users/:id/files/`.

The pages it serves run on the app's own origin (the iframe has `allow-same-origin`, otherwise localStorage,
fetch, and `WebAssembly.instantiateStreaming` all break). The cost is that the previewed page gets the current session's
same-origin capabilities, so this endpoint is only open for "workspaces the caller can already read and write", with sandboxing identical to `files/read`.

### Optimistic locking for `files/write`

`files/read` returns an `ETag` response header (modtime nanoseconds + size). If `files/write` carries `If-Match`,
it becomes a compare-and-swap: if the file changed since it was read, it returns **412** with the current `etag` in the body,
and the on-disk content is untouched. Without `If-Match` it keeps the old last-write-wins behavior.

This exists because the agent and a human edit the same workspace concurrently: a buffer open in the browser goes stale
after the agent rewrites the file, and saving it directly would silently drop the agent's changes. A successful write also returns the new `etag`, so the client can save repeatedly
without re-reading each time.

### `files/search` limits

The work_dir may be a checked-out monorepo, so every limit exists to keep the worst case cheap:
`.git` / `node_modules` / `.venv` / `venv` / `__pycache__` / `.mypy_cache` /
`.pytest_cache` are skipped by default; content mode skips files >2 MiB and non-text files; at most 20 matching lines per file, each truncated to 400 bytes;
the walk is capped at 50000 entries. When matches are truncated the response has `truncated: true`.

It uses `WalkDir` (lstat, does not follow symlinks), so links pointing outside the workspace are not descended into,
the same constraint `safePath()` imposes on single-file endpoints.

### `files/watch` file change subscription

One WebSocket covers the set of files currently open in the browser. Authorization is decided **before the upgrade** by `getUserFileAccess`'s
`canAccessOwner`, and each path additionally passes the owning user's working-directory boundary check, so no new auth surface is introduced.

```
client → server: {"action":"watch","paths":["a.md","sub/b.txt"]}
server → client: {"type":"ready","watching":["a.md","sub/b.txt"]}
                 {"type":"changed","path":"a.md","mtime":"...","size":123}
                 {"type":"removed","path":"a.md"}
                 {"type":"error","code":"forbidden|ignored|capacity|unavailable|bad_request"}
```

The client **fully replaces** the watch set; the server does not union it. Only the client knows which files it still has open right now,
and a union would keep accumulating watches for files closed long ago. If any path in a batch is unauthorized, **the whole batch is rejected**: partial acceptance would leave
the client unable to tell whether to fall back.

The server watches only the **parent directories** of subscribed files, never recursively. Watching the parent rather than the file itself is required: agents and
editors commonly save atomically via "write temp file + rename", so watching the file's inode directly breaks after the first save.
Paths inside the ignore list (`node_modules` / `.git` / `dist` / `.next` / `vendor` / `__pycache__` /
`.venv` / `target`) are rejected outright. Limits: 16 paths per connection, 512 directories per process;
events are debounced 150ms per `(directory, filename)`. Implemented in `internal/service/filewatch`.

The client (`composables/useFileWatch.ts`) has two fallback paths, both reverting to 1.5s polling of
`files/inspect` comparing `modified` + `size`: receiving any `error` frame, or no
`ready` within 5s of subscribing. The latter covers NFS / some container overlay filesystems, where inotify accepts the subscription and then never reports
events, indistinguishable from "the file never changed".

## Authenticated: usage

| Method | Path | Purpose |
|------|------|------|
| GET | `/usage/me` | Personal token/cost usage |
| GET | `/usage/insights` | Personal event-level usage summary, token breakdown, rankings, anomalies, and filter facets (server-side pagination) |

## Authenticated: Crontab scheduled jobs

| Method | Path | Purpose |
|------|------|------|
| GET/POST | `/cron-jobs` | List / create the logged-in user's Agent scheduled jobs (`model` optional) |
| PUT/DELETE | `/cron-jobs/:id` | Update (including model and enable/disable) / delete a scheduled job |

Create and update requests include `agent_id / expression / timezone / prompt / enabled`. Each trigger creates a regular Chat conversation under the selected Agent, with the job body shown as the user message; execution follows the chat's `Dispatcher → PromptRunner` pipeline.

## WebSocket

| Path | Purpose |
|------|------|
| `GET /api/terminal` | Main conversation channel: init → input → streaming events → persistence. Regular input always goes into the FIFO; only an "insert" input carrying `steer: true` tries to write into the current Codex app-server turn or the Claude Agent SDK streaming-input, and falls back to the FIFO if either rejects it. Backends that support follow-up questions send `user_question` (`content` is a `{request_id,questions}` JSON string); the client answers with `question_response` + `request_id` + `answers: Record<questionId,string[]>`, and on success the room broadcasts `user_question_resolved`. |

Event alphabet (`Kind*` of `agent.StreamEvent`, defined in `internal/agent/event.go`): delta / result / tool_use_start / tool_input_delta / tool_result / thinking_delta / usage / context_usage / system_init / rate_limit / user_question / error, etc. The handler additionally sends room-state frames such as queue_status / prompt_started / input_ack / cancel_ack.

The server remembers the latest `rate_limit` reading per account in memory (`Pool.RecordRateLimit`); on `init` it replays the still-current windows for the conversation's account, each payload stamped with `observed_at`, so any browser shows the quota badge before its first turn. A restart forgets them until the next turn reports again.

## Admin (`/api/admin/*`)

| Method | Path | Purpose |
|------|------|------|
| GET/POST | `/users` | List / create human users |
| PUT/DELETE | `/users/:id` | Update / delete |
| POST | `/users/:id/password` · `/disable` · `/enable` | Set password / disable / enable |
| POST | `/users/default-model` · `/sandbox-mode` · `/provider-binding` | Bulk set: default model / sandbox mode / provider binding |
| GET | `/config/public` | Public config (consumed by the frontend) |
| GET/POST | `/db/size` · `/db/optimize` | Database size / VACUUM optimize; the optimize response includes counts of cleaned-up expired sessions and soft-deleted conversations / users / Agents past retention |
| GET/PUT | `/pause` | Read / write the "pause processing new tasks" switch (`{"paused": bool}`). Not persisted; resets to false on restart. While paused, chat and scheduled jobs stay at `queue_status='pending'` (survives restarts), and IM bots reject outright; the resume response includes `resumed_conversations` |
| GET | `/usage` | Site-wide usage |
| GET | `/usage/insights` | Site-wide event-level usage; supports user/Agent/provider/model/conversation/source filters and day/user/Agent/model/conversation grouping |
| PUT | `/help-doc` | Edit the help document |
| GET/PUT | `/pricing/:provider` | Read / write per-provider price tables (for cost calculation) |
| GET/PUT | `/models` | Read / write the per-account model registry (picker list + summary model). PUT replaces it wholesale; account names must already exist in the provider account registry (unknown names are rejected with 400); takes effect on save |
| GET/PUT | `/providers` | Read / write the provider account pool registry in the database; saving hot-swaps the runtime config cache and updates the Pool |
| POST | `/providers/:name/check` | Account health self-check: first asks the account's CLI for login status (`claude auth status` / `codex login status`), then runs a minimal read-only turn with its default model |
| POST | `/providers/:name/summary-check` | Summary model self-check: body `{model}` (may be an unsaved value); runs one title generation under that account with the same generator as auto-titling, returning the title or the model error |
| GET/PUT | `/transports` | Per provider type, choose whether chat uses the CLI or the structured transport (Agent SDK / app-server), effective next turn; PUT probes only the types that actually changed |
| GET/POST | `/upgrade/check` · `/apply` · `/restart` · `/rollback` | Self-upgrade check / apply / restart / rollback |
| GET | `/upgrade/status` · `/busy` | Upgrade status / whether busy |
| GET (WS) | `/terminal/ws` | Admin browser terminal: runs the account's CLI in a PTY; a `start` frame with `login: true` runs that account's login flow instead |
| GET | `/conversations/:id/session-bundle` | Download the conversation's diagnostic bundle (zip, see below) |

### Conversation diagnostic bundle `/conversations/:id/session-bundle`

Streams a conversation's **on-disk** evidence as a zip so an admin can hand it to an AI to diagnose bot failures. Messages in the DB are a lossy rendering artifact; tool inputs, raw tool outputs, thinking, and MCP attribution exist only in the JSONL the CLI writes itself.

Bundle layout: `meta.json` (conversation / user / account / server / path resolution), `MANIFEST.txt` (what was collected, what was skipped and why), `session/*.jsonl` (copied verbatim, not parsed), `workdir/*` (project-level config), `config/*` (account-level config).

Key semantics:

- **Why admin-only**: the bundle includes the account-level config dir, and codex's `config.toml` lists every project path on the machine, i.e. every other user's directory name. The endpoint is also designed to export **other users'** conversations, so it skips `canAccessOwner` and is authorized only by `RequireAdmin`.
- **Credential exclusion list**: `.credentials.json` (claude) / `auth.json` (codex) never enter the bundle, but each gets a "skipped" line in MANIFEST, since a silent drop would read as "this file doesn't exist on this machine".
- **400 is only for "the conversation never sent a message"**. When the log path can be computed but the file doesn't exist, the bundle is **still produced**, recorded via `meta.resolved.exists` and MANIFEST; that is exactly what a cwd-drift failure looks like (see `docs/architecture/working-directory.md`).
