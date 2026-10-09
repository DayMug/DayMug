package store

import (
	"fmt"
	"strings"
	"testing"
)

func TestInit_UpgradesBaselineReleaseDirectly(t *testing.T) {
	t.Parallel()
	for name, prepare := range map[string][]string{
		"deferred cleanup": nil,
		"partial cleanup":  {"ALTER TABLE users DROP COLUMN skills"},
		"completed cleanup without ledger": append(append([]string{}, ledgerCompleteFinish[:len(ledgerCompleteFinish)-1]...),
			"DROP TRIGGER IF EXISTS disable_cron_jobs_for_archived_legacy_agent"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := openBaselineReleaseDB(t, append(prepare,
				`INSERT INTO users (id, name, username, password_hash, email, env)
				 VALUES ('u1', 'Alice', 'alice', 'password-hash', 'alice@example.com', '{"KEY":"value"}')`,
				`INSERT INTO agents (id, owner_id, name, role_definition, skills)
				 VALUES ('a1', 'u1', 'Assistant', 'Persona', 'Remember')`,
				`INSERT INTO conversations (id, user_id, title, session_id, provider, model, account_name)
				 VALUES ('c1', 'u1', 'Old chat', 'session-1', 'codex', 'model-1', 'account-1')`,
				`INSERT INTO messages (id, conversation_id, role, content) VALUES ('m1', 'c1', 'user', 'hello')`,
				`INSERT INTO sessions (token, user_id, expires_at) VALUES ('login-token', 'u1', '2099-01-01')`,
				`INSERT INTO user_provider_bindings (user_id, provider_type, provider_name, is_default)
				 VALUES ('u1', 'codex', 'account-1', 1)`,
			)...)
			if err := s.Init(); err != nil {
				t.Fatalf("upgrade from %s: %v", baselineRelease, err)
			}
			wantSchema := strings.Join(normalizedSchema(t, newTestStore(t)), "\n")
			if got := strings.Join(normalizedSchema(t, s), "\n"); got != wantSchema {
				t.Fatalf("upgraded schema differs from a fresh install\n got:\n%s\nwant:\n%s", got, wantSchema)
			}
			applied := ledger(t, s)
			for version := 1; version <= baselineVersion+len(migrationSteps); version++ {
				if _, ok := applied[version]; !ok {
					t.Errorf("missing migration %d after upgrade", version)
				}
			}
			cleanup := applied[legacyUserCleanupVersion]
			if cleanup.name != "trim legacy user agent columns" || !cleanup.destructive {
				t.Fatalf("cleanup ledger = %+v, want the historical destructive migration", cleanup)
			}
			assertCount(t, s, `SELECT COUNT(*) FROM users WHERE id = 'u1' AND username = 'alice'
				AND password_hash = 'password-hash' AND email = 'alice@example.com' AND env = 'KEY=value'`, 1)
			assertCount(t, s, `SELECT COUNT(*) FROM agents WHERE id = 'a1' AND owner_id = 'u1'
				AND role_definition = 'Persona' || char(10, 10) || '## Skills' || char(10) || 'Remember'`, 1)
			assertCount(t, s, `SELECT COUNT(*) FROM conversations WHERE id = 'c1' AND user_id = 'u1'
				AND title = 'Old chat' AND session_id = 'session-1' AND provider = 'codex'
				AND model = 'model-1' AND account_name = 'account-1'`, 1)
			assertCount(t, s, `SELECT COUNT(*) FROM messages WHERE id = 'm1' AND conversation_id = 'c1' AND content = 'hello'`, 1)
			assertCount(t, s, `SELECT COUNT(*) FROM sessions WHERE token = 'login-token' AND user_id = 'u1'`, 1)
			assertCount(t, s, `SELECT COUNT(*) FROM user_provider_bindings WHERE user_id = 'u1'
				AND provider_type = 'codex' AND provider_name = 'account-1' AND is_default = 1`, 1)
			firstSchema := strings.Join(schemaSnapshot(t, s), "\n")
			if err := s.Init(); err != nil {
				t.Fatalf("restart after upgrade: %v", err)
			}
			if got := strings.Join(schemaSnapshot(t, s), "\n"); got != firstSchema {
				t.Fatal("schema changed on the second startup")
			}
			if got := ledger(t, s)[legacyUserCleanupVersion]; got != cleanup {
				t.Fatalf("cleanup ledger changed on restart: %+v -> %+v", cleanup, got)
			}
		})
	}
}

func TestInit_LegacyCleanupRefusesRemainingAgentRows(t *testing.T) {
	t.Parallel()
	for name, extra := range map[string]string{
		"active":       "",
		"archived":     ", archived_at = datetime('now')",
		"soft deleted": ", deleted_at = datetime('now')",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := openBaselineReleaseDB(t,
				`INSERT INTO users (id, name, role_definition) VALUES ('legacy-agent', 'Agent', 'Keep this persona')`,
				`UPDATE users SET name = 'Agent'`+extra+` WHERE id = 'legacy-agent'`,
			)
			before := strings.Join(schemaSnapshot(t, s), "\n")
			if err := s.Init(); err == nil || !strings.Contains(err.Error(), "legacy Agent row(s) remain") {
				t.Fatalf("Init error = %v, want refusal to discard a legacy Agent", err)
			}
			if got := strings.Join(schemaSnapshot(t, s), "\n"); got != before {
				t.Fatal("schema changed despite remaining legacy Agent rows")
			}
			assertCount(t, s, `SELECT COUNT(*) FROM users WHERE id = 'legacy-agent' AND role_definition = 'Keep this persona'`, 1)
			assertCount(t, s, fmt.Sprintf(`SELECT COUNT(*) FROM schema_migrations WHERE version = %d OR version > %d`,
				legacyUserCleanupVersion, baselineVersion), 0)
		})
	}
}

func TestInit_LegacyCleanupRollsBackAndRetries(t *testing.T) {
	t.Parallel()
	s := openBaselineReleaseDB(t, `CREATE TRIGGER fail_cleanup_ledger BEFORE INSERT ON schema_migrations
		WHEN NEW.version = 94 BEGIN SELECT RAISE(ABORT, 'injected ledger failure'); END`)
	before := strings.Join(schemaSnapshot(t, s), "\n")
	if err := s.Init(); err == nil || !strings.Contains(err.Error(), "injected ledger failure") {
		t.Fatalf("Init error = %v, want injected ledger failure", err)
	}
	if got := strings.Join(schemaSnapshot(t, s), "\n"); got != before {
		t.Fatal("column or trigger changes escaped the failed migration transaction")
	}
	assertCount(t, s, "SELECT COUNT(*) FROM schema_migrations WHERE version = 94 OR version > 108", 0)
	if _, err := s.db.Exec("DROP TRIGGER fail_cleanup_ledger"); err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatalf("retry failed cleanup: %v", err)
	}
	assertCount(t, s, "SELECT COUNT(*) FROM schema_migrations WHERE version = 94 AND destructive = 1", 1)
}
