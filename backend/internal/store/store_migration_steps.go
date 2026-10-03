package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
)

// The run funcs of migrationSteps. Each is frozen once released: it must keep
// producing the same result on a database that has not run it yet, so it
// carries its own copy of any logic it needs instead of calling code that may
// change later.

// convertLegacyJSONEnv rewrites users.env / agents.env values still stored as
// the JSON object the old admin UI wrote into the VAR=VAL lines the runtime
// reads. The text is what the self-service editor has been showing for such a
// row (keys sorted, one assignment per line), so saving it back from the UI
// would have stored the same thing. A value the line format cannot carry — a
// key that is not a valid variable name, a NUL, a line break — or JSON that
// does not decode leaves the row as it is and is logged by id: the runtime
// already ignored a malformed environment, and keeping the original keeps the
// data an admin needs to fix it by hand.
func convertLegacyJSONEnv(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"users", "agents"} {
		type row struct{ id, env string }
		var pending []row
		rows, err := tx.QueryContext(ctx, "SELECT id, env FROM "+table+" WHERE env != ''")
		if err != nil {
			return fmt.Errorf("read %s.env: %w", table, err)
		}
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.env); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan %s.env: %w", table, err)
			}
			if strings.HasPrefix(strings.TrimSpace(r.env), "{") {
				pending = append(pending, r)
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("read %s.env: %w", table, err)
		}
		for _, r := range pending {
			text, ok := legacyJSONEnvToText(r.env)
			if !ok {
				log.Printf("[store] %s %s: env is a JSON object that cannot be written as VAR=VAL lines; left unchanged, fix it in the environment editor", table, r.id)
				continue
			}
			if _, err := tx.ExecContext(ctx, "UPDATE "+table+" SET env = ? WHERE id = ?", text, r.id); err != nil {
				return fmt.Errorf("rewrite %s.env for %s: %w", table, r.id, err)
			}
		}
	}
	return nil
}

func legacyJSONEnvToText(raw string) (string, bool) {
	env := map[string]string{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &env); err != nil {
		return "", false
	}
	keys := make([]string, 0, len(env))
	for key, value := range env {
		if !legacyEnvKeyValid(key) || strings.ContainsAny(value, "\x00\r\n") {
			return "", false
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+env[key])
	}
	return strings.Join(lines, "\n"), true
}

func legacyEnvKeyValid(key string) bool {
	if key == "" {
		return false
	}
	for index, r := range key {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' || (index > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

// deleteBareBotThreads removes thread bindings written before the platform
// column was qualified as "<platform>@<agentID>@<botID>". The inbound IM path
// only ever looks threads up by the qualified key, so such a row was already
// invisible to it; what still read it was the web-mirror and scheduled-task
// relay, which guessed the bot from the Agent's bots. Dropping the row loses
// only that guess: the next IM message in the thread already started a fresh
// binding of its own.
func deleteBareBotThreads(ctx context.Context, tx *sql.Tx) error {
	res, err := tx.ExecContext(ctx, "DELETE FROM bot_threads WHERE platform NOT LIKE '%@%@%'")
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		log.Printf("[store] deleted %d bot_threads row(s) without an agent@bot qualifier", n)
	}
	return nil
}

// deadColumns are columns nothing reads or writes any more: users.archived_at
// (agents moved to their own table, which has its own), conversations.archived
// (a removed soft-hide feature), agents.permission_rules (no longer exposed or
// injected into prompts) and agents.source (provenance of IM-provisioned
// agents, which nothing has written since bots became separate rows). None is
// indexed or named by a trigger, which DROP COLUMN would refuse.
var deadColumns = []struct{ table, column string }{
	{"users", "archived_at"},
	{"conversations", "archived"},
	{"agents", "permission_rules"},
	{"agents", "source"},
}

func dropDeadColumns(ctx context.Context, tx *sql.Tx) error {
	for _, c := range deadColumns {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE "+c.table+" DROP COLUMN "+c.column); err != nil {
			return fmt.Errorf("drop %s.%s: %w", c.table, c.column, err)
		}
	}
	return nil
}

// foldAgentSkills retires agents.skills. The column held free text that the
// prompt rendered as a "## Your Skills" section right after the role; it was
// never a real skill (SKILL.md) and duplicated what the persona already says.
// A non-empty value is appended to role_definition under a "## Skills"
// heading, so the text keeps reaching the system prompt, and then the column
// is dropped.
func foldAgentSkills(ctx context.Context, tx *sql.Tx) error {
	res, err := tx.ExecContext(ctx, `UPDATE agents SET role_definition = CASE
		WHEN trim(role_definition) = '' THEN '## Skills' || char(10) || trim(skills)
		ELSE rtrim(role_definition, char(9, 10, 13, 32)) || char(10, 10) || '## Skills' || char(10) || trim(skills)
	END WHERE trim(skills) != ''`)
	if err != nil {
		return fmt.Errorf("fold agents.skills into role_definition: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		log.Printf("[store] folded skills into the role definition of %d agent(s)", n)
	}
	if _, err := tx.ExecContext(ctx, "ALTER TABLE agents DROP COLUMN skills"); err != nil {
		return fmt.Errorf("drop agents.skills: %w", err)
	}
	return nil
}

// dropExternalAPI retires the external HTTP API (/api/v1) and its bearer
// tokens. api_tokens held the tokens, api_session_costs the per-session cost
// checkpoints of API calls, and users.allow_api the admin grant; nothing reads
// or writes any of them any more. Usage rows the API recorded stay in the
// usage ledger untouched. DROP TABLE takes idx_api_tokens_user with it;
// allow_api is neither indexed nor named by a trigger, which DROP COLUMN would
// refuse.
func dropExternalAPI(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS api_tokens",
		"DROP TABLE IF EXISTS api_session_costs",
		"ALTER TABLE users DROP COLUMN allow_api",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

// dropPublishedSites retires the public /serve/{slug}/ file server.
// served_directories held the directories users had published and the
// serve_slug columns the per-user and per-agent URL slugs. Both slug columns
// carry a partial unique index, which DROP COLUMN refuses, so the indexes go
// first; DROP TABLE takes idx_served_directories_user with it.
func dropPublishedSites(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS served_directories",
		"DROP INDEX IF EXISTS idx_users_serve_slug",
		"DROP INDEX IF EXISTS idx_agents_serve_slug",
		"ALTER TABLE users DROP COLUMN serve_slug",
		"ALTER TABLE agents DROP COLUMN serve_slug",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}
