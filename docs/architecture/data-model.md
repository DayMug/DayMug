# Data model (SQLite)

Storage uses pure-Go `modernc/sqlite` (no CGO). The database file is hard-coded to `<binary_dir>/data/database.db` (`config.DefaultDBPath()`); there is no YAML/env switch. The schema is defined in `backend/internal/store/store_schema.go` (`baselineSchema`); later changes are appended to `migrationSteps` in `store_migrations.go`.

## Migration strategy

- **Baseline + appended steps**: every migration shipped through v1.5.109 (ledger versions 1–108) is folded into `baselineSchema`. Versions 1–108 keep their historical meaning in every existing `schema_migrations` table, so new steps are numbered from 109 and never reuse them.
- **What startup accepts**:
  - an empty database: created from `baselineSchema` in one transaction, with versions 1–108 all recorded (flagged destructive, so a binary older than v1.5.108 refuses it instead of misreading the trimmed schema; a rollback to v1.5.108/109 sees nothing to replay);
  - a database whose ledger records all of 1–108: only the post-baseline steps still pending run;
  - a v1.5.109 database whose only missing baseline step is 94 (`trim legacy user agent columns`): startup finishes that historical destructive migration in one transaction, then runs the pending post-baseline steps. It refuses while any legacy Agent rows (empty `users.username`, including archived or soft-deleted rows) remain in `users`, so their configuration is never discarded. Already-removed columns are skipped, and the historical name and version 94 are recorded only after the actual cleanup succeeds. No intermediate v0.0.1 install is needed;
  - any other baseline gap is refused with the missing versions and the fix; no cleanup or later migration runs. An older release or a pre-ledger database goes through `daymug upgrade --version v1.5.109` first and must start successfully. A failed start makes the upgrade watchdog roll the binary back.
- **One transaction per step**: a post-baseline step and its `schema_migrations` record commit in the same transaction, so a crash mid-way leaves the step either not applied at all or fully recorded, never replayed. Steps that rebuild a table with foreign keys disabled set `foreignKeysOff`; they turn foreign keys off on a dedicated pinned connection before opening the transaction. Released steps are locked by the `backend/internal/store/testdata/migration_steps.golden` snapshot; new steps may only be appended, and the snapshot is updated with `go test ./internal/store -run TestMigrationStepsSnapshot -update-migrations`.
- **Never edit `baselineSchema` to change the schema**: existing databases never re-read it. The upgrade tests load `testdata/v1.5.109-fresh.sql` (a v1.5.109 fresh install), both directly and after the historical v0.0.1 cleanup, and require the same tables, columns, constraints, indexes and triggers as a fresh database; column order is ignored because upgraded tables carry columns in the order their ALTERs ran. Direct-upgrade tests also cover preserved business data, partial cleanup, repeated startup, refusal with legacy Agent rows, and transaction rollback/retry.
- **Rollback protection**: when the database version is ahead of the current binary, an older binary may still start if the gap contains only additive migrations; if the gap includes a destructive migration (dropping columns, rebuilding tables), startup is refused outright, so you never get a passing health check while every request returns 500.
- **One-off rebuild migrations**: for primary/foreign-key changes, use "create a `*_new` table → copy data → rename".
- **Rename migrations**: when a provider type is retired, rewrite every row that names it (`conversations.provider`, `bot_threads.provider`, `user_provider_bindings.provider_type`, price-table keys, the JSON in `app_settings.providers.accounts`) rather than deleting them, as the folded `mimo` → `claude-compatible` step did. Leaving an unknown provider behind doesn't raise an error; it silently routes to the wrong backend: when `backendFor` finds no match it falls back to the default backend, and that conversation carries on under a different account and a different session log.
- **Post-baseline steps** (`migrationSteps`, run funcs in `store_migration_steps.go`):
  - 109 `convert legacy JSON env values to VAR=VAL lines` (additive): rewrites a `users.env` / `agents.env` JSON object into sorted `KEY=VAL` lines — the text the environment editor showed for it. A value the line format cannot carry (invalid key, NUL, line break) or malformed JSON is left unchanged and logged by row id; the runtime ignores it either way.
  - 110 `delete bot_threads rows with a bare platform` (additive): `bot_threads.platform` is always `<platform>@<agentID>@<botID>`; rows from before that qualifier were already invisible to inbound IM lookups and are dropped instead of being guessed at by the web-mirror / scheduled-task relay.
  - 111 `drop users.archived_at, conversations.archived, agents.permission_rules, agents.source` (destructive): columns nothing has read or written since agents moved out of `users`, the conversation soft-hide feature was removed, permission rules left the prompt, and IM bots became their own rows instead of provisioning agents. Being destructive, it also stops a pre-111 binary from starting on the database after a rollback.
  - 113 `drop the external HTTP API: api_tokens, api_session_costs, users.allow_api` (destructive): the bearer-token HTTP API was removed, taking its token table, its per-session cost checkpoints and the admin grant column with it. `DROP TABLE` takes `idx_api_tokens_user` along; `allow_api` was never indexed.
  - 114 `drop published sites: served_directories, users.serve_slug, agents.serve_slug` (destructive): the public `/serve/{slug}/` file server was removed. The partial unique indexes `idx_users_serve_slug` / `idx_agents_serve_slug` are dropped before the columns (DROP COLUMN refuses an indexed column); `DROP TABLE` takes `idx_served_directories_user` along.
- Regular business data is only ever extended with new columns and soft-deleted (`deleted_at` / `archived_at`); historical data is kept wherever possible.

## Tables at a glance

| Table | Purpose |
|----|------|
| `users` | Human login users: auth, admin flag, notifications, permissions, sandbox mode, serve slug. Contains no Agent rows |
| `agents` | Core Agent config: role, prompt, MCP, CLAUDE.md, and sandbox |
| `bots` | IM platform connections attached to an Agent; one Agent can have several Bots, and a Bot holds no prompt of its own |
| `conversations` | Conversations: owner, title, work_dir, session_id, provider/model/think_level, pinned/shared/notification state |
| `messages` | Messages: role (user/assistant/error/thinking/tool), content, metadata, queue status |
| `bot_threads` | Mapping of IM thread `(platform, channel_id, thread_id)` (`platform` = `<platform>@<agentID>@<botID>`) ↔ DayMug conversation / CLI session / provider / model |
| `thread_cases` / `thread_case_history` | In case mode, the case file for each IM thread (keyed by `(channel_id, thread_id)`, not per Agent) and its full version history |
| `sessions` | Login session tokens (TTL set by `auth.session_ttl`) |
| `token_usage` | Per (user, model, day) token/cost aggregates (`/api/usage`) |
| `usage_events` | Event-level usage ledger: conversation / Agent / provider / source, request and tool counts, token breakdown, context fill level |
| `claude_usage_checkpoints` / `claude_usage_events` | Persistent baseline and dedup ledger for Claude CLI cumulative session usage; used only to atomically convert cumulative values into `token_usage` deltas |
| `user_provider_bindings` | User ↔ account bindings, PK `(user_id, provider_type, provider_name)`, exactly one row per type with `is_default=1` |
| `app_settings` | Runtime-editable global key/values (provider accounts, models, prices, help docs, etc.), so YAML doesn't need editing |
| `marketplace_apps` | Site-wide shared app-marketplace links; any logged-in user can add or delete them |
| `cron_jobs` | User-owned scheduled Agent tasks; each trigger creates a regular conversation and enqueues `prompt` as a user message |
| `schema_migrations` | Applied startup migrations: version, name, whether destructive, and when applied |

### `marketplace_apps`

```sql
id, name, description, url, icon_url, deploy_dir, created_by, created_at
```

The list is site-wide shared data with no permission isolation by `created_by`; creating and deleting only require a logged-in user. `deploy_dir`
is optional display info; the list query joins `created_by` to the user's name to show the publisher.
Per-app visit counts and last-visit times are not stored in the database; each browser keeps them in localStorage and the frontend sorts by them.

## Key table schemas

### `users` (many columns added by migrations)

Base columns `id / name / work_dir / avatar / created_at`; columns added by migrations:
`username`, `password_hash`, `email`, `is_admin`, `disabled`, `deleted_at`,
`default_model`, `sandbox_mode`, `env`, `bark_url`, `pushdeer_key`,
`notification_channel`, `sort_order`. `env` and `sandbox_mode` are account-level policies
that an Agent can inherit from its owner at runtime, so they still belong to human users.

> Older databases represented Agent personas as rows with an empty `username`; the startup migration `migrate legacy agents` moves them into `agents` and hard-deletes them. `users` now holds only human login users. See [multi-user.md](multi-user.md).

Migrations drop `skills`, `role_definition`, `permission_rules`, `mcp_config`,
`claude_md_content`, `manage_claude_md`, `owner_id`, `source`, `allow_cli_mode`
and `allow_tty_mode` from `users` (folded into the baseline), step 111 drops `archived_at`, step 113 drops `allow_api`, and step 114 drops `serve_slug` (from `agents` as well); these fields either belong only to Agents or have left the runtime permission model.

On `agents`, step 112 folds the free-text `skills` column into `role_definition` (under a `## Skills` heading) and drops it: the persona is the single prompt field, and real skills are `SKILL.md` directories the CLI discovers on disk.

### `agents`

```sql
id, owner_id, name, work_dir, avatar,
role_definition,                                 -- the persona, injected into the system prompt
env, mcp_config,                                -- extra env / --mcp-config
claude_md_content, manage_claude_md,            -- CLAUDE.md content + whether to auto-sync it into work_dir
sandbox_mode,                                    -- 'jailed' | 'unrestricted'
default_model,                                   -- optional; model for new web conversations / IM threads
think_level,                                     -- default reasoning level for new conversations, paired with default_model; no longer shown in the Agent form
case_mode,                                       -- 1 = IM bots under this Agent use case-file mode (thread_cases)
sort_order, archived_at, deleted_at, created_at
```

### `bots`

```sql
id, agent_id, name, platform, enabled, model,
max_conversation_duration,             -- default 12h; 0 = a platform thread reuses its conversation forever
bot_token, bot_app_token,            -- Slack Socket Mode
bot_app_id, bot_app_secret,          -- Feishu (Lark) long connection
channels,
unconfigured_reply, unauthorized_reply, -- configurable replies for "no rule configured" / "user not authorized"
created_at
```

A Bot is transport-layer config only. IM messages load the Agent via `agent_id`; the prompt, tools, working directory, account bindings, and the conversation's `user_id` all belong to the Agent. `model` optionally overrides the model used for this Bot's new threads; an empty value keeps inheriting the Agent's default model and server config. `max_conversation_duration` is a Go duration string; the default and an empty value both mean `12h`, and an explicit `0` means sticky forever. A positive duration is a fixed window measured from the conversation's `created_at`; the first message after it expires clears the old `conversation_id / session_id / provider / model` stickiness and starts a new conversation. Attaching a Bot does not change the Agent's principal type; legacy embedded Bot fields are split out automatically by the startup migration.

### `conversations`

Base `id / user_id / title / created_at / updated_at`; columns added by migrations:
`work_dir` (locked by the first message; see [working-directory.md](working-directory.md)),
`session_id` (the backend CLI's session id), `provider` + `model` (selected backend and model),
`think_level` (`'' | low | medium | high | max`; empty means no override is sent to the provider),
`account_name` (pinned account; empty = the type's default),
`notifications_enabled`, `pinned` + `pin_order`, `share_token`, `deleted_at`, `last_context_usage`,
`source_type` (default `manual`) + `cron_job_id` (for conversations created by a scheduled task, records the source task for usage attribution),
`attention` + `attention_at` (`'' | done | error | waiting`: written by `TurnAdmission` when a turn started from the web or a scheduled task finishes, fails, or stops on a question;
cleared when a new turn starts, and cleared via `POST /conversations/:id/read` when the user opens the conversation. IM threads don't write it.
Changing this flag does not touch `updated_at`, so the sidebar doesn't reorder).

### `messages`

```sql
id, conversation_id (FK ON DELETE CASCADE),
role CHECK IN ('user','assistant','error','thinking','tool'),
content, content_json, metadata,
queue_status, recovery_count, source_id,
created_at
```
Index `idx_messages_conversation (conversation_id, created_at)`.

`queue_status` has four values: empty (a completed historical message), `'pending'` (a web prompt is enqueued but no worker has claimed it), `'processing'` (a worker is running it), and `'pool_queued'` (an IM message waiting for an account slot). The first three form the dispatcher's state machine; the last does not: **every dispatcher query matches exactly `'pending'` or `'processing'`**, so `'pool_queued'` is completely invisible to it. That is precisely why it can't reuse `'pending'`; otherwise the web dispatcher would treat the IM message as its own work and run it again. It exists for one reason only: so the chat UI still knows the message is queued after a refresh. See `docs/guides/im-bots.md`.

### `token_usage`

```sql
PRIMARY KEY (user_id, model, day)   -- day is a 'YYYY-MM-DD' string
input_tokens, output_tokens,
cache_read_input_tokens, cache_creation_input_tokens,
cost_usd (REAL), turns
```
`AddTokenUsage` folds each turn into the matching row with `ON CONFLICT`. The time zone for `day` comes from `usage.timezone` (default Asia/Shanghai); changing it does not migrate existing rows.

For Claude Code (the CLI and the Agent SDK are the same client), the `per_model` input/output/cost and
`total_cost_usd` are cumulative per session (after `--resume` they keep accumulating on top of the previous turn);
only the top-level `usage` is per-turn. The adapter declares this via `agent.UsageReport.Cumulative`, and
`PersistTokenUsage` looks only at that flag, not the provider name: before a cumulative report is written,
`RecordClaudeTokenUsage` diffs it against the previous snapshot in `claude_usage_checkpoints`, keyed by `(conversation_id, session_id)`, and updates the baseline and
`token_usage` in the same SQLite transaction. `claude_usage_events` makes exact-duplicate or concurrently arriving result frames idempotent.
A decreasing counter is treated as a new baseline period; compaction or a new session naturally starts from zero because the session id changes.
Top-level cache read/create still counts toward the main model at the result's per-turn value and is not part of the cumulative diff.

For a conversation that predates the checkpoint table, the baseline table has no row for it; in that case only the usage stored in the most recent assistant
message's metadata is read as the initial baseline, so its first turn doesn't count the whole history again.
`claude-compatible` runs the same Claude Code and takes the same cumulative-diff path: its cost is recomputed by
`claudecli` from the provider price table using each model's **cumulative** token and cumulative cache counts (modelUsage
doesn't distinguish 5m/1h, so all cumulative cache writes are priced at the 5m rate; if the endpoint actually writes 1h cache, this undercounts by
1h write volume × (1h rate − 5m rate)). A result without `per_model` can only be priced from the per-turn top-level counts;
such reports are marked non-cumulative and summed one by one. Codex does not use these two tables; it records the per-turn deltas produced by its adapter.
If the price table is changed mid-conversation, the cumulative cost is recomputed entirely at the new price, and the diff charges the price difference for historical tokens to the current turn;
lowering a price is also seen as a decreasing counter, re-charging the whole conversation once at the new price.

Everywhere that accumulates cost "per turn" uses the diffed value, not the raw value from the report:
`PersistTokenUsage` returns the cost actually booked for that report (the delta from `RecordClaudeTokenUsage`, 0 for a duplicate frame),
and the per-turn cost alert (`guardrails.max_cost_usd_per_turn`) sums exactly that. It falls back to the raw reported value when no baseline is available,
preferring over-reporting to under-reporting.

`turns` is not currently a strict "user conversation turn" count: it is the number of non-zero usage
writes a given model/day row received. A multi-model Claude result increments each model's row once, and the Usage page then sums across models;
the `num_turns` that Claude's payload carries (internal API call count) is not written to this column either. Codex's result
frame/usage merging rules also differ somewhat from Claude's. The column and API are kept as-is; if
the product later needs comparable turn counts, add a provider-neutral turn/event identifier and aggregate it separately instead of
reinterpreting historical `turns`.

### `usage_events`

The usage insights page computes comparable metrics from the event ledger only. Each row stores `event_id / conversation_id / agent_id /
provider / model / source_type / cron_job_id / occurred_at`, plus user instructions, model requests, tool calls,
input/cache read/cache creation/output/reasoning, cost, and context fill level. A multi-model result shares one `event_id`, and task-level
counts are stored on only one of its rows, so aggregation doesn't double-count.

On upgrade, existing `token_usage` is copied once into rows with `source_type='historical'` and
`historical_estimate=1`. These rows keep the old token/cost figures, but user instructions, model requests, tool calls,
conversation, and source are not guessed; the UI labels them "Historical estimate". New events continue to update
`token_usage` as well, keeping the old API backward compatible.

### `user_provider_bindings`

```sql
PRIMARY KEY (user_id, provider_type, provider_name)
is_default INTEGER   -- exactly one row per (user,type) is 1
```
`provider_name` has no foreign key; the provider registry is stored as JSON in `app_settings`, and at runtime the resolver validates (name, type) against the live config. After an account is deleted, leftover bindings resolve to a clean "no <type> provider configured" error. This table is what lets one user hold multiple accounts for the same CLI type.

### `cron_jobs`

```sql
id, owner_id, agent_id, model,
expression, timezone, description, prompt,
enabled, notifications_enabled, disabled_reason,
deliver_to_bot, bot_id,                  -- optional: deliver run results to a bot conversation bound to the Agent
last_run_at, last_conversation_id, last_error,
created_at, updated_at
```

`expression` is a five-field POSIX cron expression, `timezone` is an IANA time zone, and `description` is an optional user-supplied task description. A non-empty `model` is used for every new run; an empty one is inherited dynamically from the Agent's default model, then the server's default provider/model. The scheduler never invokes the CLI directly: it creates a `conversations` row and writes a regular user message via `Dispatcher.Enqueue`, so it fully reuses the chat pipeline's provider bindings, account-pool concurrency limits, environment variables, sandbox, usage accounting, and message persistence. When an Agent is archived or soft-deleted, a SQLite trigger disables its tasks; restoring the Agent does not automatically re-enable them.

After startup, the SQLite store purges expired login sessions every 6 hours (first run delayed 5 minutes). When `retention.inactive_conversations` is set, it soft-deletes unpinned conversations not updated within that period; only when `retention.deleted_conversations` is set does it also purge users, Agents, and conversations soft-deleted longer ago than that period (no purging by default). The background job does not run `VACUUM`; rewriting the database on disk still happens only when an admin explicitly triggers database optimization.
