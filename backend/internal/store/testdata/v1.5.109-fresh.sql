CREATE TABLE schema_migrations (
    version      INTEGER PRIMARY KEY,
    name         TEXT NOT NULL DEFAULT '',
    destructive INTEGER NOT NULL DEFAULT 0,
    applied_at   DATETIME NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO schema_migrations VALUES(1,replace('\nCREATE TABLE IF NOT EXISTS users (\n    id         TEXT PRIMARY KEY,\n    name       TEXT NOT NULL,\n    work_dir   TEXT NOT NULL DEFAULT '''',\n    avatar     TEXT NOT NULL DEFAULT '''',\n    created_at DATETIME NOT NULL DEFAULT (datetime(''now''))\n);\n\nCREATE TABLE IF NOT EXISTS agents (\n    id                TEXT PRIMARY KEY,\n    owner_id          TEXT NOT NULL DEFAULT '''',\n    name              TEXT NOT NULL,\n    work_dir          TEXT NOT NULL DEFAULT '''',\n    avatar            TEXT NOT NULL DEFAULT '''',\n    skills            TEXT NOT NULL DEFAULT '''',\n    role_definition   TEXT NOT NULL DEFAULT '''',\n    permission_rules  TEXT NOT NULL DEFAULT '''',\n    env               TEXT NOT NULL DEFAULT '''',\n    mcp_config        TEXT NOT NULL DEFAULT '''',\n    claude_md_content TEXT NOT NULL DEFAULT '''',\n    manage_claude_md  INTEGER NOT NULL DEFAULT 0,\n    sandbox_mode      TEXT NOT NULL DEFAULT ''jailed'',\n    default_model     TEXT NOT NULL DEFAULT '''',\n	think_level       TEXT NOT NULL DEFAULT '''',\n	case_mode         INTEGER NOT NULL DEFAULT 0,\n    serve_slug        TEXT NOT NULL DEFAULT '''',\n    sort_order        INTEGER NOT NULL DEFAULT 0,\n    source            TEXT NOT NULL DEFAULT '''',\n    archived_at       DATETIME,\n    deleted_at        DATETIME,\n    created_at        DATETIME NOT NULL DEFAULT (datetime(''now''))\n);\n\nCREATE TABLE IF NOT EXISTS bots (\n    id             TEXT PRIMARY KEY,\n    agent_id       TEXT NOT NULL,\n    name           TEXT NOT NULL,\n    platform       TEXT NOT NULL,\n    enabled        INTEGER NOT NULL DEFAULT 0,\n    model          TEXT NOT NULL DEFAULT '''',\n    max_conversation_duration TEXT NOT NULL DEFAULT ''12h'',\n    bot_token      TEXT NOT NULL DEFAULT '''',\n    bot_app_token  TEXT NOT NULL DEFAULT '''',\n    bot_app_id     TEXT NOT NULL DEFAULT '''',\n    bot_app_secret TEXT NOT NULL DEFAULT '''',\n    channels       TEXT NOT NULL DEFAULT '''',\n    unconfigured_reply TEXT NOT NULL DEFAULT ''🙇 不好意思，我还没有在这个会话里开通，暂时不能回复你的消息。'',\n    unauthorized_reply TEXT NOT NULL DEFAULT ''🙇 不好意思， 暂时不能回复你的消息'',\n    created_at     DATETIME NOT NULL DEFAULT (datetime(''now''))\n);\n\nCREATE INDEX IF NOT EXISTS idx_bots_agent ON bots(agent_id, created_at);\n\nCREATE TABLE IF NOT EXISTS marketplace_apps (\n    id          TEXT PRIMARY KEY,\n    name        TEXT NOT NULL,\n    description TEXT NOT NULL,\n    url         TEXT NOT NULL,\n    icon_url    TEXT NOT NULL DEFAULT '''',\n    deploy_dir  TEXT NOT NULL DEFAULT '''',\n    created_by  TEXT NOT NULL DEFAULT '''',\n    created_at  DATETIME NOT NULL DEFAULT (datetime(''now''))\n);\n\nCREATE INDEX IF NOT EXISTS idx_marketplace_apps_created ON marketplace_apps(created_at, id);\n\nCREATE TABLE IF NOT EXISTS cron_jobs (\n    id                   TEXT PRIMARY KEY,\n    owner_id             TEXT NOT NULL,\n    agent_id             TEXT NOT NULL,\n    model                TEXT NOT NULL DEFAULT '''',\n    expression           TEXT NOT NULL,\n    timezone             TEXT NOT NULL DEFAULT ''UTC'',\n    description          TEXT NOT NULL DEFAULT '''',\n    prompt               TEXT NOT NULL,\n    enabled              INTEGER NOT NULL DEFAULT 1,\n    notifications_enabled INTEGER NOT NULL DEFAULT 1,\n    deliver_to_bot       INTEGER NOT NULL DEFAULT 0,\n    bot_id               TEXT NOT NULL DEFAULT '''',\n    disabled_reason      TEXT NOT NULL DEFAULT '''',\n    last_run_at          DATETIME,\n    last_conversation_id TEXT NOT NULL DEFAULT '''',\n    last_error           TEXT NOT NULL DEFAULT '''',\n    created_at           DATETIME NOT NULL DEFAULT (datetime(''now'')),\n    updated_at           DATETIME NOT NULL DEFAULT (datetime(''now''))\n);\n\nCREATE INDEX IF NOT EXISTS idx_cron_jobs_owner ON cron_jobs(owner_id, created_at);\nCREATE INDEX IF NOT EXISTS idx_cron_jobs_enabled ON cron_jobs(enabled, agent_id);\n\n-- Archiving or soft-deleting an Agent permanently pauses its scheduled work.\n-- Restoring the Agent does not silently restart old automation; the owner must\n-- explicitly review and re-enable each task.\nCREATE TRIGGER IF NOT EXISTS disable_cron_jobs_for_archived_agent\nAFTER UPDATE OF archived_at, deleted_at ON agents\nWHEN NEW.archived_at IS NOT NULL OR NEW.deleted_at IS NOT NULL\nBEGIN\n    UPDATE cron_jobs\n       SET enabled = 0,\n           disabled_reason = ''agent_unavailable'',\n           updated_at = datetime(''now'')\n     WHERE agent_id = NEW.id AND enabled = 1;\nEND;\n\n-- Kept for legacy Agent rows that have not yet been moved into agents.\nCREATE TRIGGER IF NOT EXISTS disable_cron_jobs_for_archived_legacy_agent\nAFTER UPDATE OF archived_at, deleted_at ON users\nWHEN NEW.username = '''' AND (NEW.archived_at IS NOT NULL OR NEW.deleted_at IS NOT NULL)\nBEGIN\n    UPDATE cron_jobs\n       SET enabled = 0,\n           disabled_reason = ''agent_unavailable'',\n           updated_at = datetime(''now'')\n     WHERE agent_id = NEW.id AND enabled = 1;\nEND;\n\n-- bot_threads maps one IM thread to its DayMug conversation and agent CLI\n-- session so web history and platform replies share one durable identity.\nCREATE TABLE IF NOT EXISTS bot_threads (\n    platform   TEXT NOT NULL,\n    channel_id TEXT NOT NULL,\n    thread_id  TEXT NOT NULL,\n    agent_id   TEXT NOT NULL DEFAULT '''',\n    conversation_id TEXT NOT NULL DEFAULT '''',\n    session_id TEXT NOT NULL DEFAULT '''',\n    provider   TEXT NOT NULL DEFAULT '''',\n    model      TEXT NOT NULL DEFAULT '''',\n    last_message_id TEXT NOT NULL DEFAULT '''',\n    created_at DATETIME NOT NULL DEFAULT (datetime(''now'')),\n    updated_at DATETIME NOT NULL DEFAULT (datetime(''now'')),\n    PRIMARY KEY (platform, channel_id, thread_id)\n);\n\n-- thread_cases holds the durable state of one IM thread''s work in case-file\n-- mode. Deliberately keyed by (channel_id, thread_id) and NOT by agent: the\n-- whole point is that every agent on the thread reads and writes one document,\n-- so a handoff transfers state instead of replaying a transcript. bot_threads\n-- carries the agent in its platform column, which is why it cannot be the key\n-- here.\nCREATE TABLE IF NOT EXISTS thread_cases (\n    channel_id TEXT NOT NULL,\n    thread_id  TEXT NOT NULL,\n    doc        TEXT NOT NULL DEFAULT '''',\n    version    INTEGER NOT NULL DEFAULT 0,\n    updated_by TEXT NOT NULL DEFAULT '''',\n    updated_at DATETIME NOT NULL DEFAULT (datetime(''now'')),\n    PRIMARY KEY (channel_id, thread_id)\n);\n\n-- Every case revision, so a case an agent corrupted can be read back and\n-- restored. A wrong line in a case is worse than a wrong line in a transcript:\n-- it is re-read as established fact on every later turn.\nCREATE TABLE IF NOT EXISTS thread_case_history (\n    channel_id TEXT NOT NULL,\n    thread_id  TEXT NOT NULL,\n    version    INTEGER NOT NULL,\n    doc        TEXT NOT NULL DEFAULT '''',\n    updated_by TEXT NOT NULL DEFAULT '''',\n    updated_at DATETIME NOT NULL DEFAULT (datetime(''now'')),\n    PRIMARY KEY (channel_id, thread_id, version)\n);\n\nCREATE TABLE IF NOT EXISTS conversations (\n    id          TEXT PRIMARY KEY,\n    user_id     TEXT NOT NULL DEFAULT '''',\n    title       TEXT NOT NULL DEFAULT '''',\n    think_level TEXT NOT NULL DEFAULT '''',\n    created_at  DATETIME NOT NULL DEFAULT (datetime(''now'')),\n    updated_at DATETIME NOT NULL DEFAULT (datetime(''now''))\n);\n\n	CREATE TABLE IF NOT EXISTS messages (\n	    id              TEXT PRIMARY KEY,\n	    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,\n	    role            TEXT NOT NULL CHECK (role IN (''user'', ''assistant'', ''error'', ''thinking'', ''tool'')),\n	    content         TEXT NOT NULL,\n	    content_json    TEXT NOT NULL DEFAULT '''',\n	    metadata        TEXT NOT NULL DEFAULT '''',\n	    created_at      DATETIME NOT NULL DEFAULT (datetime(''now''))\n	);\n	\n	CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id, created_at);\n	','\n',char(10)),0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(2,'migrate agent-owned bots',1,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(3,'ALTER TABLE bots ADD COLUMN unconfigured_reply TEXT NOT NULL DEFAULT ''🙇 不好意思，我还没有在这个会话里开通，暂时不能回复你的消息。''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(4,'ALTER TABLE bots ADD COLUMN unauthorized_reply TEXT NOT NULL DEFAULT ''🙇 不好意思， 暂时不能回复你的消息''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(5,'drop agent tool permission columns',1,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(6,'ALTER TABLE conversations ADD COLUMN user_id TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(7,'ALTER TABLE users ADD COLUMN skills TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(8,'ALTER TABLE users ADD COLUMN role_definition TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(9,'ALTER TABLE users ADD COLUMN permission_rules TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(10,'ALTER TABLE users ADD COLUMN env TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(11,'ALTER TABLE users ADD COLUMN mcp_config TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(12,'ALTER TABLE users ADD COLUMN claude_md_content TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(13,'ALTER TABLE users ADD COLUMN manage_claude_md INTEGER NOT NULL DEFAULT 0',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(14,'ALTER TABLE users ADD COLUMN bark_url TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(15,'ALTER TABLE users ADD COLUMN pushdeer_key TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(16,'ALTER TABLE users ADD COLUMN notification_channel TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(17,'ALTER TABLE users ADD COLUMN default_model TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(18,'ALTER TABLE users ADD COLUMN allow_cli_mode INTEGER NOT NULL DEFAULT 1',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(19,'ALTER TABLE users ADD COLUMN allow_tty_mode INTEGER NOT NULL DEFAULT 1',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(20,'ALTER TABLE users ADD COLUMN allow_api INTEGER NOT NULL DEFAULT 0',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(21,'ALTER TABLE users ADD COLUMN sandbox_mode TEXT NOT NULL DEFAULT ''jailed''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(22,'ALTER TABLE users ADD COLUMN source TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(23,'ALTER TABLE agents ADD COLUMN source TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(24,'ALTER TABLE agents ADD COLUMN default_model TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(25,'ALTER TABLE bot_threads ADD COLUMN conversation_id TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(26,'ALTER TABLE bot_threads ADD COLUMN last_message_id TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(27,'ALTER TABLE cron_jobs ADD COLUMN deliver_to_bot INTEGER NOT NULL DEFAULT 0',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(28,'ALTER TABLE cron_jobs ADD COLUMN bot_id TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(29,'ALTER TABLE conversations ADD COLUMN work_dir TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(30,'ALTER TABLE conversations ADD COLUMN notifications_enabled INTEGER NOT NULL DEFAULT 1',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(31,'ALTER TABLE conversations ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(32,'ALTER TABLE conversations ADD COLUMN pin_order INTEGER NOT NULL DEFAULT 0',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(33,'ALTER TABLE conversations ADD COLUMN share_token TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(34,'create conversation share token index',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(35,'ALTER TABLE conversations ADD COLUMN account_name TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(36,'UPDATE conversations SET pin_order = 0 WHERE pinned = 0 AND pin_order <> 0',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(37,'ALTER TABLE conversations ADD COLUMN archived INTEGER NOT NULL DEFAULT 0',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(38,'UPDATE conversations SET archived = 0 WHERE archived = 1',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(39,'ALTER TABLE conversations ADD COLUMN last_context_usage TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(40,'ALTER TABLE conversations ADD COLUMN provider TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(41,'ALTER TABLE conversations ADD COLUMN model TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(42,'UPDATE conversations SET provider = ''claude'' WHERE provider = ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(43,'rename session id column',1,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(44,'UPDATE conversations SET session_id = id WHERE session_id = ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(45,'drop pending_compact_summary',1,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(46,'migrate messages role',1,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(47,'ALTER TABLE messages ADD COLUMN metadata TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(48,'ALTER TABLE messages ADD COLUMN content_json TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(49,'ALTER TABLE messages ADD COLUMN queue_status TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(50,'create messages queue index',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(51,'ALTER TABLE messages ADD COLUMN recovery_count INTEGER NOT NULL DEFAULT 0',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(52,'ALTER TABLE messages ADD COLUMN source_id TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(53,'create messages source index',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(54,'ALTER TABLE users ADD COLUMN username TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(55,'ALTER TABLE users ADD COLUMN password_hash TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(56,'ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(57,'ALTER TABLE users ADD COLUMN is_admin INTEGER NOT NULL DEFAULT 0',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(58,'ALTER TABLE users ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(59,'ALTER TABLE users ADD COLUMN deleted_at DATETIME',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(60,'ALTER TABLE conversations ADD COLUMN deleted_at DATETIME',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(61,'create conversation user list index',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(62,'DROP INDEX IF EXISTS idx_users_username',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(63,'DROP INDEX IF EXISTS idx_users_email_human',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(64,'create username index',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(65,'ensure human email unique index',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(66,'ALTER TABLE users ADD COLUMN owner_id TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(67,'backfill owner_id',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(68,'create sessions table',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(69,'create api_tokens table',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(70,'create token_usage table',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(71,'ALTER TABLE users ADD COLUMN serve_slug TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(72,'create serve_slug index',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(73,'ALTER TABLE users ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(74,'ALTER TABLE users ADD COLUMN archived_at DATETIME',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(75,'create agents owner index',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(76,'create agents serve_slug index',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(77,'migrate legacy agents',1,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(78,'create served_directories table',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(79,'migrate served_directories foreign key',1,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(80,'create app_settings table',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(81,'create user_provider_bindings table',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(82,'migrate provider bindings to multi-account',1,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(83,'migrate claude_account column',1,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(84,'ALTER TABLE bots ADD COLUMN model TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(85,'ALTER TABLE cron_jobs ADD COLUMN model TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(86,'ALTER TABLE bots ADD COLUMN max_conversation_duration TEXT NOT NULL DEFAULT ''12h''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(87,'ALTER TABLE cron_jobs ADD COLUMN deliver_to_bot INTEGER NOT NULL DEFAULT 0',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(88,'ALTER TABLE cron_jobs ADD COLUMN bot_id TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(89,'ALTER TABLE cron_jobs ADD COLUMN notifications_enabled INTEGER NOT NULL DEFAULT 1',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(90,'ALTER TABLE cron_jobs ADD COLUMN description TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(91,'ALTER TABLE agents ADD COLUMN think_level TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(92,'ALTER TABLE agents ADD COLUMN case_mode INTEGER NOT NULL DEFAULT 0',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(93,'ALTER TABLE conversations ADD COLUMN think_level TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(95,'create thread case tables',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(96,'create marketplace apps table',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(97,'ALTER TABLE marketplace_apps ADD COLUMN deploy_dir TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(98,'rename mimo provider type to claude-compatible',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(99,'move claude-opus-5-5 onto the 1M context tier',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(100,'create claude usage checkpoints',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(101,'ALTER TABLE conversations ADD COLUMN source_type TEXT NOT NULL DEFAULT ''manual''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(102,'ALTER TABLE conversations ADD COLUMN cron_job_id TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(103,'create attributed usage events',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(104,'create api session cost checkpoints',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(105,'ALTER TABLE conversations ADD COLUMN attention TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(106,'ALTER TABLE conversations ADD COLUMN attention_at DATETIME',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(107,'ALTER TABLE conversations ADD COLUMN suspended_question TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
INSERT INTO schema_migrations VALUES(108,'ALTER TABLE conversations ADD COLUMN rotated_from TEXT NOT NULL DEFAULT ''''',0,'2026-10-02 00:00:00');
CREATE TABLE users (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    work_dir   TEXT NOT NULL DEFAULT '',
    avatar     TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
, skills TEXT NOT NULL DEFAULT '', role_definition TEXT NOT NULL DEFAULT '', permission_rules TEXT NOT NULL DEFAULT '', env TEXT NOT NULL DEFAULT '', mcp_config TEXT NOT NULL DEFAULT '', claude_md_content TEXT NOT NULL DEFAULT '', manage_claude_md INTEGER NOT NULL DEFAULT 0, bark_url TEXT NOT NULL DEFAULT '', pushdeer_key TEXT NOT NULL DEFAULT '', notification_channel TEXT NOT NULL DEFAULT '', default_model TEXT NOT NULL DEFAULT '', allow_cli_mode INTEGER NOT NULL DEFAULT 1, allow_tty_mode INTEGER NOT NULL DEFAULT 1, allow_api INTEGER NOT NULL DEFAULT 0, sandbox_mode TEXT NOT NULL DEFAULT 'jailed', source TEXT NOT NULL DEFAULT '', username TEXT NOT NULL DEFAULT '', password_hash TEXT NOT NULL DEFAULT '', email TEXT NOT NULL DEFAULT '', is_admin INTEGER NOT NULL DEFAULT 0, disabled INTEGER NOT NULL DEFAULT 0, deleted_at DATETIME, owner_id TEXT NOT NULL DEFAULT '', serve_slug TEXT NOT NULL DEFAULT '', sort_order INTEGER NOT NULL DEFAULT 0, archived_at DATETIME);
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
    source            TEXT NOT NULL DEFAULT '',
    archived_at       DATETIME,
    deleted_at        DATETIME,
    created_at        DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE bots (
    id             TEXT PRIMARY KEY,
    agent_id       TEXT NOT NULL,
    name           TEXT NOT NULL,
    platform       TEXT NOT NULL,
    enabled        INTEGER NOT NULL DEFAULT 0,
    model          TEXT NOT NULL DEFAULT '',
    max_conversation_duration TEXT NOT NULL DEFAULT '12h',
    bot_token      TEXT NOT NULL DEFAULT '',
    bot_app_token  TEXT NOT NULL DEFAULT '',
    bot_app_id     TEXT NOT NULL DEFAULT '',
    bot_app_secret TEXT NOT NULL DEFAULT '',
    channels       TEXT NOT NULL DEFAULT '',
    unconfigured_reply TEXT NOT NULL DEFAULT '🙇 不好意思，我还没有在这个会话里开通，暂时不能回复你的消息。',
    unauthorized_reply TEXT NOT NULL DEFAULT '🙇 不好意思， 暂时不能回复你的消息',
    created_at     DATETIME NOT NULL DEFAULT (datetime('now'))
);
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
    deliver_to_bot       INTEGER NOT NULL DEFAULT 0,
    bot_id               TEXT NOT NULL DEFAULT '',
    disabled_reason      TEXT NOT NULL DEFAULT '',
    last_run_at          DATETIME,
    last_conversation_id TEXT NOT NULL DEFAULT '',
    last_error           TEXT NOT NULL DEFAULT '',
    created_at           DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at           DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE bot_threads (
    platform   TEXT NOT NULL,
    channel_id TEXT NOT NULL,
    thread_id  TEXT NOT NULL,
    agent_id   TEXT NOT NULL DEFAULT '',
    conversation_id TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    provider   TEXT NOT NULL DEFAULT '',
    model      TEXT NOT NULL DEFAULT '',
    last_message_id TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (platform, channel_id, thread_id)
);
CREATE TABLE thread_cases (
    channel_id TEXT NOT NULL,
    thread_id  TEXT NOT NULL,
    doc        TEXT NOT NULL DEFAULT '',
    version    INTEGER NOT NULL DEFAULT 0,
    updated_by TEXT NOT NULL DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (channel_id, thread_id)
);
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
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL DEFAULT '',
    title       TEXT NOT NULL DEFAULT '',
    think_level TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
, work_dir TEXT NOT NULL DEFAULT '', notifications_enabled INTEGER NOT NULL DEFAULT 1, pinned INTEGER NOT NULL DEFAULT 0, pin_order INTEGER NOT NULL DEFAULT 0, share_token TEXT NOT NULL DEFAULT '', account_name TEXT NOT NULL DEFAULT '', archived INTEGER NOT NULL DEFAULT 0, last_context_usage TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '', session_id TEXT NOT NULL DEFAULT '', deleted_at DATETIME, source_type TEXT NOT NULL DEFAULT 'manual', cron_job_id TEXT NOT NULL DEFAULT '', attention TEXT NOT NULL DEFAULT '', attention_at DATETIME, suspended_question TEXT NOT NULL DEFAULT '', rotated_from TEXT NOT NULL DEFAULT '');
CREATE TABLE messages (
	    id              TEXT PRIMARY KEY,
	    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
	    role            TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'error', 'thinking', 'tool')),
	    content         TEXT NOT NULL,
	    content_json    TEXT NOT NULL DEFAULT '',
	    metadata        TEXT NOT NULL DEFAULT '',
	    created_at      DATETIME NOT NULL DEFAULT (datetime('now'))
	, queue_status TEXT NOT NULL DEFAULT '', recovery_count INTEGER NOT NULL DEFAULT 0, source_id TEXT NOT NULL DEFAULT '');
CREATE TABLE sessions (
    token      TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    expires_at DATETIME NOT NULL
);
CREATE TABLE api_tokens (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL,
    token        TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL DEFAULT '',
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    last_used_at DATETIME
);
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
CREATE TABLE served_directories (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    abs_path   TEXT NOT NULL UNIQUE,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE app_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE user_provider_bindings (
    user_id       TEXT     NOT NULL,
    provider_type TEXT     NOT NULL,
    provider_name TEXT     NOT NULL,
    is_default    INTEGER  NOT NULL DEFAULT 0,
    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (user_id, provider_type, provider_name)
);
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
CREATE TABLE usage_events (
    id                       TEXT PRIMARY KEY,
    event_id                 TEXT NOT NULL,
    conversation_id          TEXT NOT NULL DEFAULT '',
    agent_id                 TEXT NOT NULL DEFAULT '',
    provider                 TEXT NOT NULL DEFAULT '',
    model                    TEXT NOT NULL DEFAULT 'unknown',
    source_type              TEXT NOT NULL DEFAULT 'manual',
    cron_job_id              TEXT NOT NULL DEFAULT '',
    occurred_at              DATETIME NOT NULL DEFAULT (datetime('now')),
    day                      TEXT NOT NULL,
    user_instruction_count   INTEGER NOT NULL DEFAULT 0,
    model_request_count      INTEGER NOT NULL DEFAULT 0,
    tool_call_count          INTEGER NOT NULL DEFAULT 0,
    input_tokens             INTEGER NOT NULL DEFAULT 0,
    cache_read_input_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens            INTEGER NOT NULL DEFAULT 0,
    reasoning_output_tokens  INTEGER NOT NULL DEFAULT 0,
    cost_usd                 REAL NOT NULL DEFAULT 0,
    context_used_tokens      INTEGER NOT NULL DEFAULT 0,
    context_window_tokens    INTEGER NOT NULL DEFAULT 0,
    request_count_scope      TEXT NOT NULL DEFAULT 'billing_event',
    historical_estimate      INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE api_session_costs (
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id     TEXT NOT NULL,
    total_cost_usd REAL NOT NULL DEFAULT 0,
    updated_at     DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (user_id, session_id)
);
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
CREATE TRIGGER disable_cron_jobs_for_archived_legacy_agent
AFTER UPDATE OF archived_at, deleted_at ON users
WHEN NEW.username = '' AND (NEW.archived_at IS NOT NULL OR NEW.deleted_at IS NOT NULL)
BEGIN
    UPDATE cron_jobs
       SET enabled = 0,
           disabled_reason = 'agent_unavailable',
           updated_at = datetime('now')
     WHERE agent_id = NEW.id AND enabled = 1;
END;
CREATE INDEX idx_bots_agent ON bots(agent_id, created_at);
CREATE INDEX idx_marketplace_apps_created ON marketplace_apps(created_at, id);
CREATE INDEX idx_cron_jobs_owner ON cron_jobs(owner_id, created_at);
CREATE INDEX idx_cron_jobs_enabled ON cron_jobs(enabled, agent_id);
CREATE INDEX idx_messages_conversation ON messages(conversation_id, created_at);
CREATE UNIQUE INDEX idx_conversations_share_token ON conversations(share_token) WHERE share_token != '';
CREATE INDEX idx_messages_queue ON messages(conversation_id) WHERE queue_status != '';
CREATE UNIQUE INDEX idx_messages_source ON messages(conversation_id, source_id) WHERE source_id != '';
CREATE INDEX idx_conversations_user_list ON conversations(user_id, pinned DESC, updated_at DESC) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX idx_users_username ON users(username) WHERE username != '' AND deleted_at IS NULL;
CREATE UNIQUE INDEX idx_users_email_human ON users(email) WHERE username != '' AND email != '' AND deleted_at IS NULL;
CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);
CREATE INDEX idx_api_tokens_user ON api_tokens(user_id);
CREATE INDEX idx_token_usage_day ON token_usage(day);
CREATE INDEX idx_token_usage_user_day ON token_usage(user_id, day);
CREATE UNIQUE INDEX idx_users_serve_slug ON users(serve_slug) WHERE serve_slug != '';
CREATE INDEX idx_agents_owner ON agents(owner_id, deleted_at, archived_at, sort_order, created_at);
CREATE UNIQUE INDEX idx_agents_serve_slug ON agents(serve_slug) WHERE serve_slug != '';
CREATE INDEX idx_served_directories_user ON served_directories(user_id);
CREATE INDEX idx_user_provider_bindings_user ON user_provider_bindings(user_id);
CREATE INDEX idx_claude_usage_events_conversation
    ON claude_usage_events(conversation_id);
CREATE INDEX idx_usage_events_day ON usage_events(day);
CREATE INDEX idx_usage_events_agent_day ON usage_events(agent_id, day);
CREATE INDEX idx_usage_events_conversation_day ON usage_events(conversation_id, day);
CREATE INDEX idx_usage_events_provider_model_day ON usage_events(provider, model, day);
CREATE INDEX idx_usage_events_source_day ON usage_events(source_type, day);
CREATE INDEX idx_usage_events_event_id ON usage_events(event_id);
