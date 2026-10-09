# DayMug - Feature List

## Overview

DayMug is a web-based chat interface for code-CLI agents. Human users log in and manage one or more Agent personas; each Agent has its own persistent conversations backed by a pluggable provider (`claude`, `codex`, `claude-compatible`, `openai-compatible`), with configurable personas and permissions.

## Architecture

- **Backend**: Go + Gin + SQLite (modernc/sqlite, pure Go). Listens on `:8080` by default (`DAYMUG_ADDR` / config override).
- **Frontend**: Vue 3 + TypeScript + Vite + Tailwind CSS + shadcn-vue. Dev server on `:5174`.
- **Communication**: WebSocket for real-time streaming; REST API for CRUD.
- **AI backends**: Pluggable `agent.Backend` implementations under `internal/agent/`. Admins pick each provider type's transport (Settings → Admin → Models, `/api/admin/transports`):
  - `claude`: Claude Agent SDK (`claudeagentsdk/`, default) or the CLI (`claudecli/`, `claude -p --output-format=stream-json`); `claude-compatible`: CLI only.
  - `codex`: Codex app-server (`codexapp/`, default) or the CLI (`codexcli/`, `codex exec --json` / `codex exec resume`); `openai-compatible`: app-server only.
  - Each backend self-reports a `Capabilities` matrix (compaction, thinking stream, rate-limit events, cost reporting) that the handler and the frontend consume to gate features (e.g. `/compact` is hidden on backends that report `SupportsCompaction=false`).

## Features

### User / Agent Management

| Feature | Description | API |
|---------|-------------|-----|
| Create agent | Name, work_dir (directory picker), avatar, role, permissions | `POST /api/users` |
| List agents | The caller's agents, in user-defined order (drag to reorder) | `GET /api/users`, `PUT /api/user-order` |
| Update agent | Full config editing with sectioned UI | `PUT /api/users/:id` |
| Duplicate / archive agent | Copy an agent's config (not its login); archive hides it without deleting | `POST /api/users/:id/duplicate`, `/archive`, `/unarchive`, `GET /api/archived-agents` |
| Delete agent | Deletes the agent and its conversations, with confirmation dialog | `DELETE /api/users/:id` |
| Directory picker | Browse server filesystem to select work_dir | `GET /api/browse-dirs?path=` |

### Agent Persona Configuration

Each agent has a full configuration profile that controls Agent SDK / app-server behavior:

| Field | Injection Method | Description |
|-------|-----------------|-------------|
| Role Definition | SDK/app-server system instructions | The persona: identity, expertise and working style, e.g. "You are a frontend developer fluent in Vue 3 and TypeScript". Real skills (`SKILL.md`) live on disk — see the Skills guide on daymug.com |
| MCP Config | Structured SDK/app-server configuration | MCP server configuration (JSON) |
| CLAUDE.md | Written to `{work_dir}/CLAUDE.md` | Project-level persistent rules, optionally auto-synced |
| Thinking level | `/chat` account/model/effort selector → `--effort` (claude) / `model_reasoning_effort` (codex) | Stored per conversation. Unset omits the override and leaves the CLI/model default unchanged |
| Case-file mode | Per-turn system block + session rotation | Off by default. When on, this agent's IM bots keep thread state in a bounded case document instead of one ever-growing session — see below |

### CLAUDE.md Management

| Feature | Description | API |
|---------|-------------|-----|
| Read from disk | Load existing CLAUDE.md from agent's work_dir | `GET /api/users/:id/claude-md` |
| Write to disk | Manually write CLAUDE.md to agent's work_dir | `PUT /api/users/:id/claude-md` |
| Auto-sync | Optionally sync CLAUDE.md content to work_dir on every agent save | Toggle in agent edit UI |

### Conversation Management

| Feature | Description | API |
|---------|-------------|-----|
| Create / list / get / delete | Many conversations per agent, listed by last update | `POST/GET /api/conversations`, `GET/DELETE /api/conversations/:id` |
| Rename conversation | Update conversation title | `PUT /api/conversations/:id/title` |
| Pin conversation | Pin to the top of the list, with its own order | `PUT /api/conversations/:id/pinned`, `PUT /api/conversation-pin-order` |
| Update work_dir | Change conversation's working directory (locked once messages exist) | `PUT /api/conversations/:id/work-dir` |
| Switch model | Per-conversation account/model/effort | `PUT /api/conversations/:id/model` |
| Clear messages / context | Delete all messages, or keep them and rotate the backend session for a fresh context | `DELETE /api/conversations/:id/messages`, `POST /api/conversations/:id/clear-context` |
| Fetch messages | Paginated, ordered by time | `GET /api/conversations/:id/messages` |
| Share | Public read-only link to a conversation | `POST/DELETE /api/conversations/:id/share`, `GET /api/shared/conversations/:token` |
| Attention | Persisted per-conversation "finished / failed / waiting on a question, not yet opened" state; drives status dots and the mobile badge | `GET /api/conversation-attention`, `POST /api/conversations/:id/read` |
| Stale cleanup | Bulk-delete an agent's conversations idle for 14+ days | `DELETE /api/stale-conversations` |
| Auto-title | A title generator (same backend, under the owner's account) names the conversation from its first prompts | Automatic |
| Session bundle export (admin) | Zip of the CLI's own session log plus the work_dir / account config that produced it, for diagnosis; credential files are listed but never included | `GET /api/admin/conversations/:id/session-bundle` |

### Chat

| Feature | Description |
|---------|-------------|
| Real-time streaming | Backend responses stream via WebSocket (delta events), rendered as markdown |
| Context continuity | Resume is driven by the adapter's `SessionExists` probe against the on-disk session log, not by DB message state — see `docs/architecture/working-directory.md` |
| Multi-client broadcasting | Multiple browser tabs/clients on the same conversation receive updates in real-time |
| Tool use display | Shows tool calls with name, input parameters (streaming), results and elapsed time |
| Extended thinking | Displays the model's thinking, collapsible in UI; persisted to DB |
| Context usage | Shows token usage percentage bar (used vs. context window) |
| Usage & cost | Input/output tokens, cache stats, turns; the usage chip shows each turn's billed cost next to the conversation total |
| Prompt queue | Prompts sent while a turn runs are queued per conversation and can be recalled/edited before they start |
| Attachments | Upload files/images into the prompt (`POST /api/uploads`) |
| Slash text | Passed to the agent verbatim — no web-side commands or autocomplete; see `docs/guides/slash-commands.md` |
| Task guardrails | Per-turn soft thresholds for model calls, tool calls and cost (`guardrails` in config); an expensive turn finishes uninterrupted and the next user message gets one reminder |
| Cancel | Abort in-progress backend request via WebSocket cancel message |
| Error persistence | Backend CLI errors saved to DB and displayed in chat |

### File Management (Workspace)

| Feature | Description | API |
|---------|-------------|-----|
| Browse directory | List files/dirs in agent's work_dir with metadata (size, modified time) | `GET /api/users/:id/files?path=` |
| Read / inspect file | View file content with MIME type detection | `GET /api/users/:id/files/read?path=`, `/files/inspect` |
| Preview | Serve a file (e.g. HTML with its relative assets) for in-browser rendering | `GET /api/users/:id/files/preview/*path` |
| Edit file | Save text edits from the workbench editor | `PUT /api/users/:id/files/write` |
| Search | Quick-open file search within the work_dir | `GET /api/users/:id/files/search` |
| Watch | Live change notifications (falls back to polling if unavailable) | `GET /api/users/:id/files/watch` |
| Download file | Download single file with proper Content-Disposition | `GET /api/users/:id/files/download?path=` |
| Download as zip | Download directory as zip archive | `GET /api/users/:id/files/download-zip?path=` |
| Upload files | Upload files to a directory (supports folder uploads with relative paths, conflict check first) | `POST /api/users/:id/files/upload?path=`, `/files/upload/check` |
| Rename / move / copy | With conflict detection; copies get auto-dedup names ("file (copy).txt") | `PUT /files/rename`, `PUT /files/move`, `POST /files/copy` |
| Delete file/dir | Remove file or directory (recursive) | `DELETE /api/users/:id/files?path=` |
| Create directory | Create new directory (with intermediate dirs) | `POST /api/users/:id/files/mkdir` |
| Compress / extract | Create or unpack archives in place | `POST /files/compress`, `POST /files/extract` |
| Path traversal protection | All file operations validate paths stay within work_dir | Built-in |

### UI Layout

Desktop is a three-pane workspace: a slim agent rail, the conversation list, the chat panel, and a resizable workspace panel on the right. Phones get a dedicated single-column layout (agents screen, swipe-back, pull-to-refresh).

| Element | Description |
|---------|-------------|
| Agent rail | Agent avatars with status dots; left-click switches agent, right-click opens Edit / Clean up stale / Archive; "+" adds an agent |
| Account menu | Account, settings entry, running tasks |
| Conversation list | Per agent, with search, pin, rename, delete |
| Chat panel | Account/model/thinking-level picker plus message stream with markdown rendering, tool calls, thinking blocks |
| Workspace panel | File tree browser plus an inline file workbench: tabbed preview/editing (CodeMirror), HTML rendered as a page, office files, images |
| Resizable panels | Drag dividers between panes |

### Settings Pages

| Page | Description |
|------|-------------|
| System / Account / Advanced | Theme and language; password change; per-user environment variables passed to agent processes |
| Agent add / edit | Shared agent form (`UserForm.vue`) including attached IM bots |
| Notifications | Per-user Bark / PushDeer push for finished turns |
| Crontab | Scheduled prompts per agent (`/api/cron-jobs`) |
| Usage | Personal usage and insights (`/api/usage/me`, `/api/usage/insights`) |
| About | Version and the "Powered by DayMug" attribution |
| Admin | Users (incl. batch provider/model/sandbox), Global (upgrade, database, pricing, help doc, pause new tasks), Models (provider accounts, transports, model list), Bot (global IM settings), Terminal (admin-only shell for CLI login) |

### Theme & i18n

- Dark and light themes, follows system preference, togglable in settings
- Chinese and English UI (`frontend/src/i18n/locales/`), see `docs/development/i18n.md`

### Networking

| Feature | Description |
|---------|-------------|
| WebSocket auto-reconnect | Flat 3s retry with ±20% jitter; re-sends `init` on every reconnect |
| Origin checks | Cross-origin WebSocket handshakes are rejected |
| CORS middleware | Permissive CORS headers on the HTTP API |
| Dev proxy | Vite proxies `/api/*` to backend `127.0.0.1:8090` — the `init` default; override with `DAYMUG_DEV_BACKEND` (including WebSocket upgrade) |

### Auth & Multi-user

- Username/password login; optional OIDC single sign-on (`oidc` in config); first admin created via setup page or `daymug user add`
- Per-user provider bindings, per-account concurrency pool, optional sandbox (`sandbox` in config) — see `docs/architecture/multi-user.md`

### Data Persistence

| Storage | Description |
|---------|-------------|
| SQLite database | `<binary dir>/data/database.db` (e.g. `~/.daymug/data/database.db`); not configurable |
| Messages | Saved to DB on send/receive (user, assistant, thinking, error roles) |
| Backend session id | Persisted on `conversations.session_id`; claude pre-generates a UUID, codex picks its own and reports it via `system_init` |
| Schema migrations | Auto-applied on startup; each migration commits together with its ledger row |
| Retention | Opt-in automatic deletion via `retention` in config (inactive conversations, soft-deleted rows, old uploads); all default to 0 = never |

### IM bot integration (Slack / Feishu (Lark) / Telegram / WeChat)

| Aspect | Behaviour |
|--------|-----------|
| Connectivity | Outbound connections only — Slack Socket Mode, Feishu (Lark) event long connection, Telegram Bot API long polling, WeChat iLink Bot long polling; no public webhook URL needed |
| Config | Slack, Feishu and Telegram are configured per Agent on the shared New/Edit Agent form; WeChat is paired there by QR scan instead of typed credentials; one Agent can own multiple apps on any platform. Saves hot-reload only the changed connectors; failed or dropped connections reconnect automatically with backoff |
| Channel rules | Exact channel, `*` (all groups), and `dm` rules; exact rules take priority. Supports @-mention, auto-reply, extra system prompt, and explicit disable |
| Ownership | Manager injects stable agent + bot ids into every event; rules cannot redirect across Agents and overlapping workspace/channel ids remain isolated |
| Threading | Replies always land in the originating thread; each thread maps to one DayMug conversation and CLI session so web and IM share history/context |
| Progress | Slack updates one thread message with `chat.update` (Markdown converted to mrkdwn); Feishu updates one interactive markdown card and posts overflow chunks as cards too. WeChat cannot edit a sent message, so it shows a native typing indicator while working and posts only the finished reply. Thinking/tool/text/result events stream live to the linked web conversation |
| Runtime | Shares `service.Pool` accounts/concurrency with chat + HTTP API; per-thread serialization, app-scoped dedup, self-message filtering. No wall-clock run cap by default (`im_run_timeout` sets one); queue wait bounded at 30 min |
| Bot relay | An agent's reply ending in `[HANDOFF @AnotherBot] …` posts a real-mention handoff so that bot picks up the thread (e.g. coder → reviewer). Opt-in per channel (`allow_bot_mentions`), mention always required, sliding-window circuit breaker (default 3 runs / 30 min per thread, configurable) |
| Case-file mode | Opt-in per Agent, and only for conversations bound to an IM channel. Thread state lives in a `thread_cases` row keyed by `(channel_id, thread_id)` — shared by every agent on the thread — and is materialized to `<owner home>/.daymug/agents/<agent id>/case/<channel>-<thread>.md` for the turn to read and edit (the agent is given the absolute path; the scope hangs off the owner's home so a `.daymug/` folder does not appear in every project directory). The CLI session is rotated once `last_context_usage` passes 70% of the window (`case_rotate_context_ratio`); the case is capped at 8k/12k tokens with compression demanded in-prompt. The full case enters the system prompt only when a session opens — re-injecting a document the agent rewrites would change the cached prefix every turn — and a resumed turn over the cap gets a one-line reminder at the end of the prompt instead. Human edits to the file are adopted and promoted to a stored revision; every revision is kept in `thread_case_history` |

See `docs/guides/im-bots.md` for setup (required Slack scopes / Feishu (Lark) permissions) and the full rule schema, and `docs/guides/wechat.md` for WeChat.

## Key Files

### Backend

| File | Purpose |
|------|---------|
| `cmd/server/main.go` | Entrypoint, dispatches subcommands |
| `cmd/server/cmd_serve.go` | `daymug serve` — boots the HTTP server and validates configured Agent SDK / app-server runtimes |
| `internal/handler/routes.go` | Route registration |
| `internal/handler/runtime.go` | Assembles the shared `service.Runtime` and the per-provider `agent.Backend` map |
| `internal/handler/user.go` | Agent CRUD + CLAUDE.md endpoints |
| `internal/handler/conversation.go` | Conversation + message handlers |
| `internal/handler/terminal.go` | WebSocket handler scaffolding on the shared `service.Runtime` |
| `internal/service/runtime.go` / `backend_registry.go` | Shared runtime (dispatcher callback, runner/persister assembly) + per-provider backend routing |
| `internal/service/prompt_runner.go` | Per-prompt lifecycle: ticket → claim → run → persist |
| `internal/handler/compact.go` | `/compact` flow (capability-gated; rotates session id) |
| `internal/handler/file.go` | File browser, upload, download, rename, move, copy handlers |
| `internal/handler/browse.go` | Server filesystem directory browser (for directory picker) |
| `internal/handler/models.go` | `/api/models` — per-provider model list + `Capabilities` matrix |
| `internal/handler/health.go` | Health check endpoint |
| `internal/agent/agent.go` | `agent.Backend` interface + `Capabilities` + `RunRequest` |
| `internal/agent/event.go` | `StreamEvent` + `Kind*` constants (shared event alphabet) |
| `internal/agent/sandbox.go` / `spawner.go` / `binary.go` | Spawner + sandbox + binary resolver primitives |
| `internal/agent/claudeagentsdk/` | Claude Agent SDK chat adapter |
| `internal/agent/codexapp/` | Codex app-server chat adapter and process pool |
| `internal/agent/claudecli/` / `codexcli/` | CLI transports (selectable per provider type; `claudecli` is the only one for `claude-compatible`) |
| `internal/agent/sessionlog/` | Session-log probes (`SessionExists` / log path) shared by every transport |
| `internal/service/account_pool.go` | Per-account concurrency pool (FIFO queue, both backends) |
| `internal/service/prompt.go` | System prompt assembly from agent config |
| `internal/service/claudemd.go` | CLAUDE.md read/write to work_dir |
| `internal/service/prompt_dispatcher.go` | Per-conversation serialiser of inbound prompts |
| `internal/service/broadcaster.go` | Per-conversation multi-client WebSocket broadcaster |
| `internal/service/turn_admission.go` | Single turn admission shared by web, IM and `/compact` turns |
| `internal/service/task_guardrail.go` | Per-turn model-call / tool-call / cost tracking and next-message reminders |
| `internal/service/imbridge/` | IM bridge: rule gating, thread↔conversation mapping, progress, case-file mode |
| `internal/imbot/` | IM connectors (Slack / Feishu / Telegram / WeChat) |
| `internal/handler/session_bundle.go` | Admin session-bundle zip export |
| `internal/store/` | SQLite store (per-domain interfaces in `store_interface.go`, migrations, contract snapshot) |
| `internal/middleware/cors.go` | CORS middleware |

### Frontend

| File | Purpose |
|------|---------|
| `src/pages/ChatPage.vue` | Main chat interface with message display and input |
| `src/pages/PreviewPage.vue` | Bare-layout file preview page |
| `src/pages/settings/SettingsLayout.vue` | Settings page layout with sidebar navigation |
| `src/pages/settings/UserAdd.vue` | Add agent form with a user-authored role |
| `src/pages/settings/UserEdit.vue` | Edit agent form (3 tabs: basic, role & persona, integrations) |
| `src/pages/settings/SystemSettings.vue` | System-level settings (theme, language) |
| `src/components/SidebarPanel.vue` | Agent rail |
| `src/components/ConversationListPanel.vue` | Conversation list with create/delete/rename/pin |
| `src/components/chat/ChatInput.vue` | Message input textarea with send button |
| `src/components/chat/ChatMessageItem.vue` | Message renderer (markdown, tool calls, thinking) |
| `src/components/chat/ContextUsageBar.vue` | Token usage progress bar |
| `src/components/chat/ChatFooterControls.vue` | Model/account picker plus context and rate-limit readouts, inline on the composer's send row (`display: contents`, so the row can wrap them onto separate lines on a phone) |
| `src/components/WorkspacePanel.vue` | File browser for agent workspace |
| `src/components/FileWorkbench.vue` | Shared file view/edit surface for the inline panel and the standalone preview page |
| `src/components/FilePreview.vue` | File content viewer with syntax highlighting |
| `src/components/FileContextMenu.vue` | File right-click context menu |
| `src/components/FilePropertiesDialog.vue` | File metadata dialog |
| `src/components/FileIcon.vue` | Extension-based file icon renderer |
| `src/components/UserContextMenu.vue` | Right-click agent context menu |
| `src/components/DirPicker.vue` | Server directory browser/picker component |
| `src/components/UserForm.vue` | Shared agent configuration form |
| `src/composables/useApi.ts` | REST API functions + TypeScript types |
| `src/composables/appWiring.ts` | Cross-composable app-shell wiring, installed once from `main.ts` |
| `src/composables/useChat.ts` | Chat message management, WebSocket event handling |
| `src/composables/useConversations.ts` | Conversation CRUD and selection |
| `src/composables/useUsers.ts` | User management and selection |
| `src/composables/useWebSocket.ts` | WebSocket composable with auto-reconnect |
| `src/composables/useFileApi.ts` | File management API functions |
| `src/composables/useMarkdown.ts` | Markdown rendering (markdown-it) |
| `src/composables/useTheme.ts` | Dark/light theme management |

## Test Coverage

Each Go file has a `*_test.go` peer in the same package; each Vue / TS file has a co-located `*.test.ts`. Run `go test ./...` and `pnpm test` for the full lists — counts drift with every change, so this file no longer pins them.
