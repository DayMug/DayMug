package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// countRows is a raw check on purge results: every store read path filters
// deleted_at, so the API-level view cannot distinguish "soft-deleted" from
// "actually gone" — which is exactly the bug (rows invisible but never
// reclaimed).
func countRows(t *testing.T, s *SQLiteStore, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count (%s): %v", query, err)
	}
	return n
}

func seedConversation(t *testing.T, s *SQLiteStore, id string) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateConversation(ctx, id, "T", "u1", "/tmp", "claude", ""); err != nil {
		t.Fatalf("create conversation %s: %v", id, err)
	}
	if err := s.SaveMessage(ctx, Message{ID: id + "-m", ConversationID: id, Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("save message for %s: %v", id, err)
	}
}

// softDelete stamps deleted_at at an explicit age so the test does not have to
// wait out the retention window.
func softDelete(t *testing.T, s *SQLiteStore, id string, age time.Duration) {
	t.Helper()
	if _, err := s.db.Exec("UPDATE conversations SET deleted_at = ? WHERE id = ?", time.Now().Add(-age), id); err != nil {
		t.Fatalf("soft-delete %s: %v", id, err)
	}
}

// The reclaim path must take the messages with it — they are the bulk of the
// bytes, and a conversation row without them is a rounding error.
func TestPurgeSoftDeletedConversations_RemovesAgedRowsAndMessages(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	seedConversation(t, s, "aged")
	softDelete(t, s, "aged", softDeleteRetention+time.Hour)

	n, err := s.purgeSoftDeletedConversations(context.Background(), time.Now().Add(-softDeleteRetention))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 1 {
		t.Errorf("purged conversations = %d, want 1", n)
	}

	if n := countRows(t, s, "SELECT COUNT(*) FROM conversations WHERE id = 'aged'"); n != 0 {
		t.Errorf("conversation rows = %d, want 0", n)
	}
	if n := countRows(t, s, "SELECT COUNT(*) FROM messages WHERE conversation_id = 'aged'"); n != 0 {
		t.Errorf("orphaned message rows = %d, want 0", n)
	}
}

// Inside the retention window the rows stay put — that window is the only
// recovery path an operator has, since there is no undelete in the UI.
func TestPurgeSoftDeletedConversations_KeepsRecentAndLiveRows(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	seedConversation(t, s, "recent")
	seedConversation(t, s, "live")
	softDelete(t, s, "recent", time.Hour)

	n, err := s.purgeSoftDeletedConversations(context.Background(), time.Now().Add(-softDeleteRetention))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 0 {
		t.Errorf("purged conversations = %d, want 0", n)
	}

	for _, id := range []string{"recent", "live"} {
		if n := countRows(t, s, "SELECT COUNT(*) FROM conversations WHERE id = ?", id); n != 1 {
			t.Errorf("conversation %s rows = %d, want 1", id, n)
		}
		if n := countRows(t, s, "SELECT COUNT(*) FROM messages WHERE conversation_id = ?", id); n != 1 {
			t.Errorf("messages for %s = %d, want 1", id, n)
		}
	}
}

func TestPurgeSoftDeletedConversations_KeepsPinnedForLiveOwner(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if err := s.CreateUser(context.Background(), User{ID: "u1", Name: "Live User", Username: "live-user"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	seedConversation(t, s, "pinned-user")
	if _, err := s.db.Exec("INSERT INTO agents (id, owner_id, name) VALUES ('live-agent', 'u1', 'Live Agent')"); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := s.CreateConversation(context.Background(), "pinned-agent", "T", "live-agent", "/tmp", "claude", ""); err != nil {
		t.Fatalf("create agent conversation: %v", err)
	}
	if err := s.SaveMessage(context.Background(), Message{ID: "pinned-agent-m", ConversationID: "pinned-agent", Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("save agent message: %v", err)
	}
	for _, id := range []string{"pinned-user", "pinned-agent"} {
		if _, err := s.db.Exec("UPDATE conversations SET pinned = 1 WHERE id = ?", id); err != nil {
			t.Fatalf("pin %s: %v", id, err)
		}
		softDelete(t, s, id, softDeleteRetention+time.Hour)
	}

	n, err := s.purgeSoftDeletedConversations(context.Background(), time.Now().Add(-softDeleteRetention))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 0 {
		t.Fatalf("purged conversations = %d, want 0", n)
	}
	for _, id := range []string{"pinned-user", "pinned-agent"} {
		if n := countRows(t, s, "SELECT COUNT(*) FROM conversations WHERE id = ?", id); n != 1 {
			t.Errorf("conversation %s rows = %d, want 1", id, n)
		}
		if n := countRows(t, s, "SELECT COUNT(*) FROM messages WHERE conversation_id = ?", id); n != 1 {
			t.Errorf("messages for %s = %d, want 1", id, n)
		}
	}
}

func TestPurgeSoftDeletedConversations_RemovesPinnedWithoutLiveOwner(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if err := s.CreateUser(context.Background(), User{ID: "u1", Name: "Deleted User", Username: "deleted-user"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	seedConversation(t, s, "pinned")
	if _, err := s.db.Exec("UPDATE conversations SET pinned = 1 WHERE id = 'pinned'"); err != nil {
		t.Fatalf("pin conversation: %v", err)
	}
	softDelete(t, s, "pinned", softDeleteRetention+time.Hour)
	if _, err := s.db.Exec("UPDATE users SET deleted_at = ? WHERE id = 'u1'", time.Now()); err != nil {
		t.Fatalf("delete owner: %v", err)
	}

	n, err := s.purgeSoftDeletedConversations(context.Background(), time.Now().Add(-softDeleteRetention))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 1 {
		t.Fatalf("purged conversations = %d, want 1", n)
	}
	if n := countRows(t, s, "SELECT COUNT(*) FROM conversations WHERE id = 'pinned'"); n != 0 {
		t.Errorf("conversation rows = %d, want 0", n)
	}
	if n := countRows(t, s, "SELECT COUNT(*) FROM messages WHERE conversation_id = 'pinned'"); n != 0 {
		t.Errorf("message rows = %d, want 0", n)
	}
}

func TestPurgeSoftDeletedUsers_RemovesOwnedData(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	aged := time.Now().Add(-softDeleteRetention - time.Hour)
	recent := time.Now().Add(-time.Hour)

	statements := []string{
		`INSERT INTO users (id, name, username) VALUES ('aged-user', 'Aged', 'aged')`,
		`INSERT INTO users (id, name, username) VALUES ('recent-user', 'Recent', 'recent')`,
		`INSERT INTO agents (id, owner_id, name) VALUES ('aged-agent', 'aged-user', 'Agent')`,
		`INSERT INTO agents (id, owner_id, name) VALUES ('deleted-agent', 'recent-user', 'Deleted Agent')`,
		`INSERT INTO conversations (id, user_id) VALUES ('aged-user-conv', 'aged-user'), ('aged-agent-conv', 'aged-agent'), ('deleted-agent-conv', 'deleted-agent'), ('recent-user-conv', 'recent-user')`,
		`UPDATE conversations SET pinned = 1 WHERE id IN ('aged-user-conv', 'aged-agent-conv', 'deleted-agent-conv')`,
		`INSERT INTO messages (id, conversation_id, role, content) VALUES ('m1', 'aged-user-conv', 'user', 'x'), ('m2', 'aged-agent-conv', 'user', 'x'), ('m3', 'deleted-agent-conv', 'user', 'x'), ('m4', 'recent-user-conv', 'user', 'x')`,
		`INSERT INTO bots (id, agent_id, name, platform) VALUES ('bot1', 'aged-agent', 'Bot', 'slack'), ('bot2', 'deleted-agent', 'Bot', 'slack')`,
		`INSERT INTO bot_threads (platform, channel_id, thread_id, agent_id, conversation_id) VALUES ('slack', 'c1', 't1', 'aged-agent', 'aged-agent-conv'), ('slack', 'c2', 't2', 'deleted-agent', 'deleted-agent-conv')`,
		`INSERT INTO cron_jobs (id, owner_id, agent_id, expression, prompt) VALUES ('cron1', 'aged-user', 'aged-agent', '* * * * *', 'x'), ('cron2', 'recent-user', 'deleted-agent', '* * * * *', 'x')`,
		`INSERT INTO sessions (token, user_id, expires_at) VALUES ('session', 'aged-user', datetime('now', '+1 day'))`,
		`INSERT INTO token_usage (user_id, model, day) VALUES ('aged-agent', 'm', '2026-01-01')`,
		`INSERT INTO user_provider_bindings (user_id, provider_type, provider_name) VALUES ('aged-user', 'claude', 'default')`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed purge data: %v\n%s", err, statement)
		}
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE users SET deleted_at = ? WHERE id = 'aged-user'", aged); err != nil {
		t.Fatalf("age user: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE users SET deleted_at = ? WHERE id = 'recent-user'", recent); err != nil {
		t.Fatalf("soft-delete recent user: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE agents SET deleted_at = ? WHERE id = 'deleted-agent'", aged); err != nil {
		t.Fatalf("age agent: %v", err)
	}

	users, agents, conversations, err := s.purgeSoftDeletedUsers(ctx, time.Now().Add(-softDeleteRetention))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if users != 1 || agents != 2 || conversations != 3 {
		t.Fatalf("purged (%d users, %d agents, %d conversations), want (1, 2, 3)", users, agents, conversations)
	}

	for _, table := range []string{"bots", "bot_threads", "cron_jobs", "sessions", "token_usage", "user_provider_bindings", "agents"} {
		if n := countRows(t, s, "SELECT COUNT(*) FROM "+table); n != 0 {
			t.Errorf("%s rows = %d, want 0", table, n)
		}
	}
	if n := countRows(t, s, "SELECT COUNT(*) FROM users WHERE id = 'recent-user'"); n != 1 {
		t.Errorf("recent user rows = %d, want 1", n)
	}
	if n := countRows(t, s, "SELECT COUNT(*) FROM conversations WHERE id = 'recent-user-conv'"); n != 1 {
		t.Errorf("recent user's conversation rows = %d, want 1", n)
	}
	if n := countRows(t, s, "SELECT COUNT(*) FROM messages WHERE id = 'm4'"); n != 1 {
		t.Errorf("recent user's message rows = %d, want 1", n)
	}
}

func TestPurgeSoftDeletedUsers_PreservesLiveAgentWithLegacyUserID(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	aged := time.Now().Add(-softDeleteRetention - time.Hour)

	statements := []string{
		`INSERT INTO users (id, name, username, deleted_at) VALUES ('shared-id', 'Legacy Agent', '', ?)`,
		`INSERT INTO agents (id, owner_id, name) VALUES ('shared-id', 'owner', 'Live Agent')`,
		`INSERT INTO conversations (id, user_id) VALUES ('live-conversation', 'shared-id')`,
		`INSERT INTO messages (id, conversation_id, role, content) VALUES ('live-message', 'live-conversation', 'user', 'hi')`,
		`INSERT INTO token_usage (user_id, model, day) VALUES ('shared-id', 'model', '2026-08-12')`,
	}
	for i, statement := range statements {
		var args []any
		if i == 0 {
			args = []any{aged}
		}
		if _, err := s.db.ExecContext(ctx, statement, args...); err != nil {
			t.Fatalf("seed legacy collision: %v\n%s", err, statement)
		}
	}

	users, agents, conversations, err := s.purgeSoftDeletedUsers(ctx, time.Now().Add(-softDeleteRetention))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if users != 1 || agents != 0 || conversations != 0 {
		t.Fatalf("purged (%d users, %d agents, %d conversations), want (1, 0, 0)", users, agents, conversations)
	}
	for table, query := range map[string]string{
		"agent":        "SELECT COUNT(*) FROM agents WHERE id = 'shared-id'",
		"conversation": "SELECT COUNT(*) FROM conversations WHERE id = 'live-conversation'",
		"message":      "SELECT COUNT(*) FROM messages WHERE id = 'live-message'",
		"token usage":  "SELECT COUNT(*) FROM token_usage WHERE user_id = 'shared-id'",
	} {
		if n := countRows(t, s, query); n != 1 {
			t.Errorf("%s rows = %d, want 1", table, n)
		}
	}
	if n := countRows(t, s, "SELECT COUNT(*) FROM users WHERE id = 'shared-id'"); n != 0 {
		t.Errorf("legacy user rows = %d, want 0", n)
	}
}

// The "optimize" button is the only place either reclaim runs, so both have to
// be wired into it — a purge that VACUUM never sees frees no disk.
func TestOptimizeDatabase_ReclaimsSessionsAndAgedConversations(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "opt.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	ctx := context.Background()

	if err := s.CreateSession(ctx, Session{Token: "stale", UserID: "u1", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	seedConversation(t, s, "aged")
	seedConversation(t, s, "recent")
	softDelete(t, s, "aged", softDeleteRetention+time.Hour)
	softDelete(t, s, "recent", time.Hour)
	if _, err := s.db.ExecContext(ctx,
		"INSERT INTO users (id, name, username, deleted_at) VALUES ('deleted-user', 'Deleted', 'deleted', ?)",
		time.Now().Add(-softDeleteRetention-time.Hour)); err != nil {
		t.Fatalf("seed deleted user: %v", err)
	}
	if _, err := s.db.ExecContext(ctx,
		"INSERT INTO agents (id, owner_id, name) VALUES ('owned-agent', 'deleted-user', 'Owned')"); err != nil {
		t.Fatalf("seed owned agent: %v", err)
	}

	res, err := s.OptimizeDatabase(ctx)
	if err != nil {
		t.Fatalf("optimize: %v", err)
	}
	if res.ExpiredSessionsDeleted != 1 {
		t.Errorf("ExpiredSessionsDeleted = %d, want 1", res.ExpiredSessionsDeleted)
	}
	if res.PurgedConversations != 1 {
		t.Errorf("PurgedConversations = %d, want 1", res.PurgedConversations)
	}
	if res.PurgedUsers != 1 || res.PurgedAgents != 1 {
		t.Errorf("purged (%d users, %d agents), want (1, 1)", res.PurgedUsers, res.PurgedAgents)
	}
	if n := countRows(t, s, "SELECT COUNT(*) FROM conversations"); n != 1 {
		t.Errorf("conversations left = %d, want 1 (only the recent soft-delete)", n)
	}
	if n := countRows(t, s, "SELECT COUNT(*) FROM messages"); n != 1 {
		t.Errorf("messages left = %d, want 1", n)
	}
}

func TestRunMaintenance_ReclaimsWithoutVacuum(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateSession(ctx, Session{Token: "stale", UserID: "u1", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	seedConversation(t, s, "aged")
	softDelete(t, s, "aged", softDeleteRetention+time.Hour)
	s.SetPurgeRetention(softDeleteRetention)

	sessions, conversations, err := s.RunMaintenance(ctx)
	if err != nil {
		t.Fatalf("maintenance: %v", err)
	}
	if sessions != 1 || conversations != 1 {
		t.Fatalf("maintenance removed (%d sessions, %d conversations), want (1, 1)", sessions, conversations)
	}
}

// Without retention.deleted_conversations the sweep still reaps login sessions
// but never erases deleted conversations, however old.
func TestRunMaintenance_KeepsDeletedConversationsByDefault(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateSession(ctx, Session{Token: "stale", UserID: "u1", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	seedConversation(t, s, "ancient")
	softDelete(t, s, "ancient", 365*24*time.Hour)

	sessions, conversations, err := s.RunMaintenance(ctx)
	if err != nil {
		t.Fatalf("maintenance: %v", err)
	}
	if sessions != 1 || conversations != 0 {
		t.Fatalf("maintenance removed (%d sessions, %d conversations), want (1, 0)", sessions, conversations)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM conversations WHERE id = 'ancient'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted conversation rows = %d, want 1", n)
	}
}

func TestRunMaintenance_DeletesInactiveUnpinnedConversations(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	for _, id := range []string{"stale", "stale-pinned", "fresh"} {
		seedConversation(t, s, id)
	}
	old := time.Now().Add(-15 * 24 * time.Hour)
	if _, err := s.db.ExecContext(ctx, "UPDATE conversations SET updated_at = ? WHERE id IN ('stale', 'stale-pinned')", old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE conversations SET pinned = 1 WHERE id = 'stale-pinned'"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.RunMaintenance(ctx); err != nil {
		t.Fatalf("maintenance: %v", err)
	}
	var live int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM conversations WHERE deleted_at IS NULL").Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 3 {
		t.Fatalf("live conversations without retention = %d, want 3", live)
	}

	s.SetInactiveRetention(14 * 24 * time.Hour)
	if _, _, err := s.RunMaintenance(ctx); err != nil {
		t.Fatalf("maintenance: %v", err)
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM conversations WHERE deleted_at IS NOT NULL")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var deleted []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		deleted = append(deleted, id)
	}
	if len(deleted) != 1 || deleted[0] != "stale" {
		t.Fatalf("deleted = %v, want only the unpinned stale conversation", deleted)
	}
}

func TestRunMaintenance_InactiveDeletedHookReceivesDeletedConversations(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	for _, id := range []string{"stale", "fresh"} {
		seedConversation(t, s, id)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE conversations SET updated_at = ? WHERE id = 'stale'", time.Now().Add(-15*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var got []ConversationRef
	s.SetInactiveDeletedHook(func(_ context.Context, refs []ConversationRef) { got = append(got, refs...) })
	s.SetInactiveRetention(14 * 24 * time.Hour)

	if _, _, err := s.RunMaintenance(ctx); err != nil {
		t.Fatalf("maintenance: %v", err)
	}
	if len(got) != 1 || got[0] != (ConversationRef{ID: "stale", UserID: "u1"}) {
		t.Fatalf("hook got %v, want the one stale conversation", got)
	}
}
