package store

import (
	"context"
	"testing"
)

func columnValue(t *testing.T, s *SQLiteStore, query string, args ...any) string {
	t.Helper()
	var v string
	if err := s.db.QueryRowContext(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("%s %v: %v", query, args, err)
	}
	return v
}

// The JSON env the old admin UI stored becomes exactly the text the
// environment editor showed for it: keys sorted, values verbatim — including
// '=', quotes, spaces and non-ASCII. Anything the line format cannot carry is
// left untouched rather than corrupted.
func TestInit_ConvertsLegacyJSONEnv(t *testing.T) {
	t.Parallel()
	const quoted = `{"Q":"\"double\" 'single'","B":"two  words ","A":"x=y=z","EMPTY":"","U":"é ü 中","_lower1":"v"}`
	for _, tc := range []struct {
		name, table, env, want string
	}{
		{"edge cases sorted", "users", quoted,
			"A=x=y=z\nB=two  words \nEMPTY=\nQ=\"double\" 'single'\nU=é ü 中\n_lower1=v"},
		{"surrounding whitespace", "users", " \n{\"Z\":\"1\",\"Y\":\"2\"}\t", "Y=2\nZ=1"},
		{"empty object", "users", "{}", ""},
		{"agent row", "agents", `{"TOKEN":"abc","HOME_DIR":"/srv/a b"}`, "HOME_DIR=/srv/a b\nTOKEN=abc"},
		{"already text", "users", "B=1\nA=2", "B=1\nA=2"},
		{"line break in value", "users", `{"KEY":"line1\nline2"}`, `{"KEY":"line1\nline2"}`},
		{"carriage return in value", "agents", `{"KEY":"a\rb"}`, `{"KEY":"a\rb"}`},
		{"invalid key", "users", `{"1BAD":"v"}`, `{"1BAD":"v"}`},
		{"non-string value", "users", `{"N":1}`, `{"N":1}`},
		{"malformed JSON", "users", `{"A":`, `{"A":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			seed := "INSERT INTO users (id, name, username, env) VALUES ('r1', 'Row', 'row', ?)"
			if tc.table == "agents" {
				seed = "INSERT INTO agents (id, owner_id, name, env) VALUES ('r1', 'h1', 'Row', ?)"
			}
			s := openLedgerCompleteDB(t)
			if _, err := s.db.Exec(seed, tc.env); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if err := s.Init(); err != nil {
				t.Fatalf("init: %v", err)
			}
			if got := columnValue(t, s, "SELECT env FROM "+tc.table+" WHERE id = 'r1'"); got != tc.want {
				t.Errorf("env = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInit_DeletesBareBotThreads(t *testing.T) {
	t.Parallel()
	s := openLedgerCompleteDB(t,
		`INSERT INTO bot_threads (platform, channel_id, thread_id, conversation_id) VALUES
			('slack', 'C1', 'T1', 'bare'),
			('feishu@a1', 'C1', 'T1', 'half'),
			('slack@a1@b1', 'C1', 'T1', 'kept')`,
	)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if got := columnValue(t, s, "SELECT group_concat(conversation_id) FROM bot_threads"); got != "kept" {
		t.Errorf("remaining bot_threads = %q, want only the qualified row", got)
	}
}

// Both populations lose the dead columns (the schema comparison in
// TestInit_FreshDatabaseMatchesUpgradedRelease covers that they agree); the
// rows around them survive, and archiving an agent still pauses its jobs
// through the trigger that sits next to the dropped columns.
func TestInit_DropsDeadColumns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	upgraded := openLedgerCompleteDB(t,
		`INSERT INTO users (id, name, username) VALUES ('h1', 'Alice', 'alice')`,
		`INSERT INTO agents (id, owner_id, name, permission_rules) VALUES ('a1', 'h1', 'Worker', 'no force-push')`,
		`INSERT INTO conversations (id, user_id, title, archived) VALUES ('c1', 'a1', 'kept', 1)`,
		`INSERT INTO cron_jobs (id, owner_id, agent_id, expression, prompt) VALUES ('j1', 'h1', 'a1', '* * * * *', 'p')`,
	)
	if err := upgraded.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	for name, s := range map[string]*SQLiteStore{"fresh": newTestStore(t), "upgraded": upgraded} {
		for _, c := range deadColumns {
			columns, err := tableColumns(ctx, s.db, c.table)
			if err != nil {
				t.Fatalf("%s columns: %v", c.table, err)
			}
			if columns[c.column] {
				t.Errorf("%s: %s.%s survived", name, c.table, c.column)
			}
		}
	}

	if conv, err := upgraded.GetConversation(ctx, "c1"); err != nil || conv.Title != "kept" {
		t.Fatalf("conversation after drop = %+v, %v", conv, err)
	}
	if err := upgraded.ArchiveUser(ctx, "a1"); err != nil {
		t.Fatalf("archive agent: %v", err)
	}
	if got := columnValue(t, upgraded, "SELECT disabled_reason FROM cron_jobs WHERE id = 'j1'"); got != "agent_unavailable" {
		t.Errorf("cron job disabled_reason = %q, want the archive trigger to pause it", got)
	}
	users, err := upgraded.ListUsers(ctx)
	if err != nil || len(users) != 2 {
		t.Fatalf("ListUsers = %+v, %v", users, err)
	}
	for _, u := range users {
		if want := u.ID == "a1"; u.Archived != want {
			t.Errorf("%s archived = %v, want %v", u.ID, u.Archived, want)
		}
	}
	// The released binary must refuse the trimmed schema rather than query
	// columns that are gone.
	if err := assertBinaryCanReadSchema(ledger(t, upgraded), baselineVersion+2); err == nil {
		t.Error("a binary without the column drop accepted the database")
	}
}

// The free-text skills of an upgraded agent survive inside its persona, under
// their own heading, so the system prompt keeps carrying them; both
// populations end up without the column.
func TestInit_FoldsAgentSkillsIntoRoleDefinition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	upgraded := openLedgerCompleteDB(t,
		`INSERT INTO users (id, name, username) VALUES ('h1', 'Alice', 'alice')`,
		`INSERT INTO agents (id, owner_id, name, role_definition, skills) VALUES
			('both', 'h1', 'Both', 'Backend engineer'||char(10), ' Go, Python '),
			('skills', 'h1', 'Skills', '', 'Vue'),
			('role', 'h1', 'Role', 'Reviewer', '  ')`,
	)
	if err := upgraded.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	for id, want := range map[string]string{
		"both":   "Backend engineer\n\n## Skills\nGo, Python",
		"skills": "## Skills\nVue",
		"role":   "Reviewer",
	} {
		if got := columnValue(t, upgraded, "SELECT role_definition FROM agents WHERE id = '"+id+"'"); got != want {
			t.Errorf("%s role_definition = %q, want %q", id, got, want)
		}
	}
	for name, s := range map[string]*SQLiteStore{"fresh": newTestStore(t), "upgraded": upgraded} {
		columns, err := tableColumns(ctx, s.db, "agents")
		if err != nil {
			t.Fatalf("%s agents columns: %v", name, err)
		}
		if columns["skills"] {
			t.Errorf("%s: agents.skills survived", name)
		}
	}
}

// Both populations lose the API tables and the grant column; the user row and
// its other columns survive the column drop.
func TestInit_DropsExternalAPI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	upgraded := openLedgerCompleteDB(t,
		`INSERT INTO users (id, name, username, allow_api) VALUES ('h1', 'Alice', 'alice', 1)`,
		`INSERT INTO api_tokens (id, user_id, token, name) VALUES ('tok1', 'h1', 'dmug_secret', 'laptop')`,
		`INSERT INTO api_session_costs (user_id, session_id, total_cost_usd) VALUES ('h1', 's1', 1.5)`,
	)
	if err := upgraded.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	for name, s := range map[string]*SQLiteStore{"fresh": newTestStore(t), "upgraded": upgraded} {
		for _, table := range []string{"api_tokens", "api_session_costs"} {
			if n := columnValue(t, s, "SELECT COUNT(*) FROM sqlite_master WHERE name = ?", table); n != "0" {
				t.Errorf("%s: table %s survived", name, table)
			}
		}
		if n := columnValue(t, s, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'idx_api_tokens_user'"); n != "0" {
			t.Errorf("%s: idx_api_tokens_user survived", name)
		}
		columns, err := tableColumns(ctx, s.db, "users")
		if err != nil {
			t.Fatalf("%s users columns: %v", name, err)
		}
		if columns["allow_api"] {
			t.Errorf("%s: users.allow_api survived", name)
		}
	}
	if u, err := upgraded.GetUser(ctx, "h1"); err != nil || u.Username != "alice" {
		t.Fatalf("user after drop = %+v, %v", u, err)
	}
}

// Both populations lose the published-sites table, the slug columns and their
// unique indexes; the user and agent rows survive the column drops.
func TestInit_DropsPublishedSites(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	upgraded := openLedgerCompleteDB(t,
		`INSERT INTO users (id, name, username, serve_slug) VALUES ('h1', 'Alice', 'alice', 'slug-h1')`,
		`INSERT INTO agents (id, owner_id, name, serve_slug) VALUES ('a1', 'h1', 'Worker', 'slug-a1')`,
		`INSERT INTO served_directories (id, user_id, abs_path) VALUES ('site', 'a1', '/srv/site')`,
	)
	if err := upgraded.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	for name, s := range map[string]*SQLiteStore{"fresh": newTestStore(t), "upgraded": upgraded} {
		for _, object := range []string{"served_directories", "idx_served_directories_user", "idx_users_serve_slug", "idx_agents_serve_slug"} {
			if n := columnValue(t, s, "SELECT COUNT(*) FROM sqlite_master WHERE name = ?", object); n != "0" {
				t.Errorf("%s: %s survived", name, object)
			}
		}
		for _, table := range []string{"users", "agents"} {
			columns, err := tableColumns(ctx, s.db, table)
			if err != nil {
				t.Fatalf("%s %s columns: %v", name, table, err)
			}
			if columns["serve_slug"] {
				t.Errorf("%s: %s.serve_slug survived", name, table)
			}
		}
	}
	if u, err := upgraded.GetUser(ctx, "h1"); err != nil || u.Username != "alice" {
		t.Fatalf("user after drop = %+v, %v", u, err)
	}
	if a, err := upgraded.GetUser(ctx, "a1"); err != nil || a.Name != "Worker" {
		t.Fatalf("agent after drop = %+v, %v", a, err)
	}
}
