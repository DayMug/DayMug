package store

const (
	DefaultBotUnconfiguredReply       = "🙇 不好意思，我还没有在这个会话里开通，暂时不能回复你的消息。"
	DefaultBotUnauthorizedReply       = "🙇 不好意思， 暂时不能回复你的消息"
	DefaultBotMaxConversationDuration = "12h"
)

// baselineSchema is the schema produced by every migration DayMug shipped
// through v1.5.109 (ledger versions 1..baselineVersion), including the
// once-deferred legacy users column trim. A fresh database is created from it
// in one step; an existing one must already be at that point (see Init).
//
// Column order is not significant: upgraded databases carry the same columns
// in the order their ALTERs ran, and no query depends on position. Changing
// the schema means appending a migrationStep, never editing this text —
// existing databases never re-read it. Some columns below are dropped again by
// later steps (see store_migration_steps.go), so this is not the current schema.
const baselineSchema = `
-- Humans only. Agents moved to their own table; username/email are unique
-- among live rows so a soft-deleted account frees its slot.
CREATE TABLE users (
    id                   TEXT PRIMARY KEY,
    name                 TEXT NOT NULL,
    username             TEXT NOT NULL DEFAULT '',
    password_hash        TEXT NOT NULL DEFAULT '',
    email                TEXT NOT NULL DEFAULT '',
    is_admin             INTEGER NOT NULL DEFAULT 0,
    disabled             INTEGER NOT NULL DEFAULT 0,
    work_dir             TEXT NOT NULL DEFAULT '',
    avatar               TEXT NOT NULL DEFAULT '',
    env                  TEXT NOT NULL DEFAULT '',
    bark_url             TEXT NOT NULL DEFAULT '',
    pushdeer_key         TEXT NOT NULL DEFAULT '',
    notification_channel TEXT NOT NULL DEFAULT '',
    default_model        TEXT NOT NULL DEFAULT '',
    allow_api            INTEGER NOT NULL DEFAULT 0,
    sandbox_mode         TEXT NOT NULL DEFAULT 'jailed',
    serve_slug           TEXT NOT NULL DEFAULT '',
    sort_order           INTEGER NOT NULL DEFAULT 0,
    -- Unused; kept so a rollback to v1.5.109 reads it (see legacyUserAgentColumns).
    archived_at          DATETIME,
    deleted_at           DATETIME,
    created_at           DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE UNIQUE INDEX idx_users_username ON users(username) WHERE username != '' AND deleted_at IS NULL;
CREATE UNIQUE INDEX idx_users_email_human ON users(email) WHERE username != '' AND email != '' AND deleted_at IS NULL;
CREATE UNIQUE INDEX idx_users_serve_slug ON users(serve_slug) WHERE serve_slug != '';

CREATE TABLE agents (
    id                TEXT PRIMARY KEY,
    owner_id          TEXT NOT NULL DEFAULT '',
    name              TEXT NOT NULL,
    work_dir          TEXT NOT NULL DEFAULT '',
    avatar            TEXT NOT NULL DEFAULT '',
    skills            TEXT NOT NULL DEFAULT '',
    role_definition   TEXT NOT NULL DEFAULT '',
    permission_rules  TEXT NOT NULL DEFAULT '',
    env               TEXT NOT NULL DEFAULT '',
    mcp_config        TEXT NOT NULL DEFAULT '',
    claude_md_content TEXT NOT NULL DEFAULT '',
    manage_claude_md  INTEGER NOT NULL DEFAULT 0,
    sandbox_mode      TEXT NOT NULL DEFAULT 'jailed',
    default_model     TEXT NOT NULL DEFAULT '',
    think_level       TEXT NOT NULL DEFAULT '',
    case_mode         INTEGER NOT NULL DEFAULT 0,
    serve_slug        TEXT NOT NULL DEFAULT '',
    sort_order        INTEGER NOT NULL DEFAULT 0,
    -- '' = human-configured; 'slack' / 'feishu' / … = auto-provisioned by an IM bot.
    source            TEXT NOT NULL DEFAULT '',
    archived_at       DATETIME,
    deleted_at        DATETIME,
    created_at        DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_agents_owner ON agents(owner_id, deleted_at, archived_at, sort_order, created_at);
CREATE UNIQUE INDEX idx_agents_serve_slug ON agents(serve_slug) WHERE serve_slug != '';

CREATE TABLE bots (
    id             TEXT PRIMARY KEY,
    agent_id       TEXT NOT NULL,
    name           TEXT NOT NULL,
    platform       TEXT NOT NULL,
    enabled        INTEGER NOT NULL DEFAULT 0,
    model          TEXT NOT NULL DEFAULT '',
    -- Fixed window for one IM thread's conversation; '0' means no limit.
    max_conversation_duration TEXT NOT NULL DEFAULT '` + DefaultBotMaxConversationDuration + `',
    bot_token      TEXT NOT NULL DEFAULT '',
    bot_app_token  TEXT NOT NULL DEFAULT '',
    bot_app_id     TEXT NOT NULL DEFAULT '',
    bot_app_secret TEXT NOT NULL DEFAULT '',
    channels       TEXT NOT NULL DEFAULT '',
    unconfigured_reply TEXT NOT NULL DEFAULT '` + DefaultBotUnconfiguredReply + `',
    unauthorized_reply TEXT NOT NULL DEFAULT '` + DefaultBotUnauthorizedReply + `',
    created_at     DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_bots_agent ON bots(agent_id, created_at);

-- Global application marketplace: entries are deliberately not owner-scoped.
CREATE TABLE marketplace_apps (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL,
    url         TEXT NOT NULL,
    icon_url    TEXT NOT NULL DEFAULT '',
    deploy_dir  TEXT NOT NULL DEFAULT '',
    created_by  TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_marketplace_apps_created ON marketplace_apps(created_at, id);

CREATE TABLE cron_jobs (
    id                   TEXT PRIMARY KEY,
    owner_id             TEXT NOT NULL,
    agent_id             TEXT NOT NULL,
    model                TEXT NOT NULL DEFAULT '',
    expression           TEXT NOT NULL,
    timezone             TEXT NOT NULL DEFAULT 'UTC',
    description          TEXT NOT NULL DEFAULT '',
    prompt               TEXT NOT NULL,
    enabled              INTEGER NOT NULL DEFAULT 1,
    notifications_enabled INTEGER NOT NULL DEFAULT 1,
    -- Relay the answer into the Agent's IM thread; empty bot_id = the most
    -- recently used thread.
    deliver_to_bot       INTEGER NOT NULL DEFAULT 0,
    bot_id               TEXT NOT NULL DEFAULT '',
    disabled_reason      TEXT NOT NULL DEFAULT '',
    last_run_at          DATETIME,
    last_conversation_id TEXT NOT NULL DEFAULT '',
    last_error           TEXT NOT NULL DEFAULT '',
    created_at           DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at           DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_cron_jobs_owner ON cron_jobs(owner_id, created_at);
CREATE INDEX idx_cron_jobs_enabled ON cron_jobs(enabled, agent_id);

-- Archiving or soft-deleting an Agent permanently pauses its scheduled work.
-- Restoring the Agent does not silently restart old automation; the owner must
-- explicitly review and re-enable each task.
CREATE TRIGGER disable_cron_jobs_for_archived_agent
AFTER UPDATE OF archived_at, deleted_at ON agents
WHEN NEW.archived_at IS NOT NULL OR NEW.deleted_at IS NOT NULL
BEGIN
    UPDATE cron_jobs
       SET enabled = 0,
           disabled_reason = 'agent_unavailable',
           updated_at = datetime('now')
     WHERE agent_id = NEW.id AND enabled = 1;
END;

-- bot_threads maps one IM thread to its DayMug conversation and agent CLI
-- session so web history and platform replies share one durable identity.
CREATE TABLE bot_threads (
    platform        TEXT NOT NULL,
    channel_id      TEXT NOT NULL,
    thread_id       TEXT NOT NULL,
    agent_id        TEXT NOT NULL DEFAULT '',
    conversation_id TEXT NOT NULL DEFAULT '',
    session_id      TEXT NOT NULL DEFAULT '',
    provider        TEXT NOT NULL DEFAULT '',
    model           TEXT NOT NULL DEFAULT '',
    -- Last platform message mirrored into the thread, so a later mention can
    -- backfill messages that did not trigger the bot.
    last_message_id TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (platform, channel_id, thread_id)
);

-- thread_cases holds the durable state of one IM thread's work in case-file
-- mode. Deliberately keyed by (channel_id, thread_id) and NOT by agent: every
-- agent on the thread reads and writes one document, so a handoff transfers
-- state instead of replaying a transcript.
CREATE TABLE thread_cases (
    channel_id TEXT NOT NULL,
    thread_id  TEXT NOT NULL,
    doc        TEXT NOT NULL DEFAULT '',
    version    INTEGER NOT NULL DEFAULT 0,
    updated_by TEXT NOT NULL DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (channel_id, thread_id)
);

-- Every case revision, so a case an agent corrupted can be read back and
-- restored.
CREATE TABLE thread_case_history (
    channel_id TEXT NOT NULL,
    thread_id  TEXT NOT NULL,
    version    INTEGER NOT NULL,
    doc        TEXT NOT NULL DEFAULT '',
    updated_by TEXT NOT NULL DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (channel_id, thread_id, version)
);

CREATE TABLE conversations (
    id                    TEXT PRIMARY KEY,
    user_id              TEXT NOT NULL DEFAULT '',
    title                 TEXT NOT NULL DEFAULT '',
    -- Agent CLI session id; rotates on /compact.
    session_id            TEXT NOT NULL DEFAULT '',
    provider              TEXT NOT NULL DEFAULT '',
    model                 TEXT NOT NULL DEFAULT '',
    -- Pins one of the owner's accounts for the provider; '' = their default.
    account_name          TEXT NOT NULL DEFAULT '',
    -- '' lets the provider pick its own reasoning effort.
    think_level           TEXT NOT NULL DEFAULT '',
    work_dir              TEXT NOT NULL DEFAULT '',
    notifications_enabled INTEGER NOT NULL DEFAULT 1,
    pinned                INTEGER NOT NULL DEFAULT 0,
    pin_order             INTEGER NOT NULL DEFAULT 0,
    -- Non-empty = publicly readable at /share/conversation/:token.
    share_token           TEXT NOT NULL DEFAULT '',
    -- Dead column from a removed soft-hide feature; kept so fresh and
    -- upgraded databases stay identical.
    archived              INTEGER NOT NULL DEFAULT 0,
    last_context_usage    TEXT NOT NULL DEFAULT '',
    source_type           TEXT NOT NULL DEFAULT 'manual',
    cron_job_id           TEXT NOT NULL DEFAULT '',
    -- Unseen finish / failure / question, so the "needs you" badge survives a
    -- closed tab.
    attention             TEXT NOT NULL DEFAULT '',
    attention_at          DATETIME,
    -- A question still pending when a restart ended the turn.
    suspended_question    TEXT NOT NULL DEFAULT '',
    -- The conversation a case-mode IM thread rotated away from to create this one.
    rotated_from          TEXT NOT NULL DEFAULT '',
    deleted_at            DATETIME,
    created_at            DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at            DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE UNIQUE INDEX idx_conversations_share_token ON conversations(share_token) WHERE share_token != '';
-- Serves the sidebar list on every cold start (GET /api/app-state).
CREATE INDEX idx_conversations_user_list ON conversations(user_id, pinned DESC, updated_at DESC) WHERE deleted_at IS NULL;

CREATE TABLE messages (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    role            TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'error', 'thinking', 'tool')),
    content         TEXT NOT NULL,
    -- Oversized tool payloads, kept apart from the short plain-text content.
    content_json    TEXT NOT NULL DEFAULT '',
    -- Per-turn usage / cost / model JSON on assistant rows.
    metadata        TEXT NOT NULL DEFAULT '',
    -- '' = finished, 'pending' / 'processing' = queued for the dispatcher.
    queue_status    TEXT NOT NULL DEFAULT '',
    -- Startup recoveries of a 'processing' prompt; caps crash loops.
    recovery_count  INTEGER NOT NULL DEFAULT 0,
    -- Dedup key for rows imported from a CLI transcript; '' for live rows.
    source_id       TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_messages_conversation ON messages(conversation_id, created_at);
CREATE INDEX idx_messages_queue ON messages(conversation_id) WHERE queue_status != '';
CREATE UNIQUE INDEX idx_messages_source ON messages(conversation_id, source_id) WHERE source_id != '';

CREATE TABLE sessions (
    token      TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    expires_at DATETIME NOT NULL
);
CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);

-- External-API bearer tokens, hard-deleted on revoke.
CREATE TABLE api_tokens (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL,
    token        TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL DEFAULT '',
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    last_used_at DATETIME
);
CREATE INDEX idx_api_tokens_user ON api_tokens(user_id);

-- Per-day usage rollup, one row per (user_id, model, UTC day). Kept as the
-- compatibility aggregate beside usage_events.
CREATE TABLE token_usage (
    user_id                     TEXT NOT NULL,
    model                       TEXT NOT NULL,
    day                         TEXT NOT NULL,
    input_tokens                INTEGER NOT NULL DEFAULT 0,
    output_tokens               INTEGER NOT NULL DEFAULT 0,
    cache_read_input_tokens     INTEGER NOT NULL DEFAULT 0,
    cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd                    REAL NOT NULL DEFAULT 0,
    turns                       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, model, day)
);
CREATE INDEX idx_token_usage_day ON token_usage(day);
CREATE INDEX idx_token_usage_user_day ON token_usage(user_id, day);

-- Explicit publish opt-ins. The unique abs_path keeps two records from
-- claiming the same bytes; deliberately no FK to users.
CREATE TABLE served_directories (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    abs_path   TEXT NOT NULL UNIQUE,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_served_directories_user ON served_directories(user_id);

-- Runtime-editable global settings the admin UI owns.
CREATE TABLE app_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- The set of accounts each user may use per provider type; exactly one row
-- per (user, type) carries is_default=1.
CREATE TABLE user_provider_bindings (
    user_id       TEXT     NOT NULL,
    provider_type TEXT     NOT NULL,
    provider_name TEXT     NOT NULL,
    is_default    INTEGER  NOT NULL DEFAULT 0,
    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (user_id, provider_type, provider_name)
);
CREATE INDEX idx_user_provider_bindings_user ON user_provider_bindings(user_id);

-- Claude reports session-cumulative usage; the last accepted snapshot plus an
-- idempotency ledger turn it into exactly one token_usage delta per event.
CREATE TABLE claude_usage_checkpoints (
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    session_id      TEXT NOT NULL,
    snapshot        TEXT NOT NULL DEFAULT '',
    updated_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (conversation_id, session_id)
);
CREATE TABLE claude_usage_events (
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    session_id      TEXT NOT NULL,
    event_key       TEXT NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (conversation_id, session_id, event_key)
);
CREATE INDEX idx_claude_usage_events_conversation ON claude_usage_events(conversation_id);

-- Attributed usage, one row per billing event.
CREATE TABLE usage_events (
    id                          TEXT PRIMARY KEY,
    event_id                    TEXT NOT NULL,
    conversation_id             TEXT NOT NULL DEFAULT '',
    agent_id                    TEXT NOT NULL DEFAULT '',
    provider                    TEXT NOT NULL DEFAULT '',
    model                       TEXT NOT NULL DEFAULT 'unknown',
    source_type                 TEXT NOT NULL DEFAULT 'manual',
    cron_job_id                 TEXT NOT NULL DEFAULT '',
    occurred_at                 DATETIME NOT NULL DEFAULT (datetime('now')),
    day                         TEXT NOT NULL,
    user_instruction_count      INTEGER NOT NULL DEFAULT 0,
    model_request_count         INTEGER NOT NULL DEFAULT 0,
    tool_call_count             INTEGER NOT NULL DEFAULT 0,
    input_tokens                INTEGER NOT NULL DEFAULT 0,
    cache_read_input_tokens     INTEGER NOT NULL DEFAULT 0,
    cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens               INTEGER NOT NULL DEFAULT 0,
    reasoning_output_tokens     INTEGER NOT NULL DEFAULT 0,
    cost_usd                    REAL NOT NULL DEFAULT 0,
    context_used_tokens         INTEGER NOT NULL DEFAULT 0,
    context_window_tokens       INTEGER NOT NULL DEFAULT 0,
    request_count_scope         TEXT NOT NULL DEFAULT 'billing_event',
    -- 1 = copied from the pre-attribution token_usage rollup.
    historical_estimate         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_usage_events_day ON usage_events(day);
CREATE INDEX idx_usage_events_agent_day ON usage_events(agent_id, day);
CREATE INDEX idx_usage_events_conversation_day ON usage_events(conversation_id, day);
CREATE INDEX idx_usage_events_provider_model_day ON usage_events(provider, model, day);
CREATE INDEX idx_usage_events_source_day ON usage_events(source_type, day);
CREATE INDEX idx_usage_events_event_id ON usage_events(event_id);

-- Last-seen session-cumulative cost per (user, session) for /api/v1 calls,
-- which have no conversation to hang a checkpoint on.
CREATE TABLE api_session_costs (
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id     TEXT NOT NULL,
    total_cost_usd REAL NOT NULL DEFAULT 0,
    updated_at     DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (user_id, session_id)
);
`
