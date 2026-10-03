package store

import (
	"context"
	"errors"
	"testing"
)

func TestCreateUser_AgentWithOwnerUsesAgentsTable(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateUser(ctx, User{ID: "owner", Username: "alice", Name: "Alice", Email: "alice@example.com"}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := s.CreateUser(ctx, User{
		ID: "agent-1", OwnerID: "owner", Name: "Worker", WorkDir: "/tmp/worker",
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	var userRows int
	if err := s.db.QueryRow("SELECT count(*) FROM users WHERE id = 'agent-1'").Scan(&userRows); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if userRows != 0 {
		t.Fatalf("agent row should not be inserted into users, got %d rows", userRows)
	}
	var agentRows int
	if err := s.db.QueryRow("SELECT count(*) FROM agents WHERE id = 'agent-1'").Scan(&agentRows); err != nil {
		t.Fatalf("count agents: %v", err)
	}
	if agentRows != 1 {
		t.Fatalf("agent row should be inserted into agents, got %d rows", agentRows)
	}

	got, err := s.GetUser(ctx, "agent-1")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if got.OwnerID != "owner" || got.Username != "" || got.WorkDir != "/tmp/worker" {
		t.Fatalf("unexpected agent projection: %+v", got)
	}
}

// TestUpdateUser_MigratedAgentRoutesToAgentsTable guards the rename bug: once an
// agent has been migrated (its legacy users row deleted, live row in the
// agents table), UpdateUser must route the write to the agents table. That
// routing keys on OwnerID via isAgentRow — so the caller MUST populate OwnerID.
// Dropping it (the pre-fix handler behaviour) makes the update fall straight to
// the users table, match zero rows, and fail with ErrNotFound, i.e.
// a rename that silently does nothing.
func TestUpdateUser_MigratedAgentRoutesToAgentsTable(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateUser(ctx, User{ID: "owner", Username: "alice", Name: "Alice", Email: "alice@example.com"}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	// CreateUser routes an owned agent into the agents table, mirroring the
	// post-migration state where the legacy users row is gone.
	if err := s.CreateUser(ctx, User{ID: "agent-1", OwnerID: "owner", Name: "Worker", WorkDir: "/tmp/worker"}); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// Rename with OwnerID set (the fixed handler contract) must succeed and
	// persist to the agents table.
	if err := s.UpdateUser(ctx, User{
		ID: "agent-1", OwnerID: "owner", Name: "Renamed",
	}); err != nil {
		t.Fatalf("rename migrated agent: %v", err)
	}
	got, err := s.GetUser(ctx, "agent-1")
	if err != nil {
		t.Fatalf("get renamed agent: %v", err)
	}
	if got.Name != "Renamed" {
		t.Fatalf("name = %q, want %q", got.Name, "Renamed")
	}
	// Without OwnerID, isAgentRow is false: the update targets only the users
	// table, where the agent has no live row, so it must report ErrNotFound.
	if err := s.UpdateUser(ctx, User{ID: "agent-1", Name: "Ignored"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rename without OwnerID: err = %v, want ErrNotFound", err)
	}
}

func TestAgentModelSettingsRoundTrip(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateUser(ctx, User{ID: "owner", Username: "alice", Name: "Alice"}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := s.CreateUser(ctx, User{
		ID: "agent-1", OwnerID: "owner", Name: "Worker", DefaultModel: "claude-opus-4-8", ThinkLevel: "high",
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	got, err := s.GetUser(ctx, "agent-1")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if got.DefaultModel != "claude-opus-4-8" {
		t.Fatalf("default_model = %q, want claude-opus-4-8", got.DefaultModel)
	}
	if got.ThinkLevel != "high" {
		t.Fatalf("think_level = %q, want high", got.ThinkLevel)
	}

	got.DefaultModel = ""
	got.ThinkLevel = "low"
	if err := s.UpdateUser(ctx, got); err != nil {
		t.Fatalf("clear default model: %v", err)
	}
	cleared, err := s.GetUser(ctx, "agent-1")
	if err != nil {
		t.Fatalf("get cleared agent: %v", err)
	}
	if cleared.DefaultModel != "" {
		t.Fatalf("default_model = %q, want empty", cleared.DefaultModel)
	}
	if cleared.ThinkLevel != "low" {
		t.Fatalf("think_level = %q, want low", cleared.ThinkLevel)
	}
}

// TestSetUserWorkDir_MigratedAgentUpdatesAgentsTable guards the "saving the
// agent work_dir has no effect" bug. A migrated agent keeps a soft-deleted
// legacy users row alongside its live agents row. SetUserWorkDir must skip the
// dead users row and land the write on the agents table; otherwise the UPDATE
// hits the soft-deleted row, reports success, and the real work_dir never
// changes.
func TestSetUserWorkDir_MigratedAgentUpdatesAgentsTable(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateUser(ctx, User{ID: "owner", Username: "alice", Name: "Alice", Email: "alice@example.com"}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	// Seed the exact post-migration shape: a soft-deleted legacy users row and a
	// live agents row sharing the same id.
	if err := s.CreateUser(ctx, User{ID: "legacy-agent", OwnerID: "owner", Name: "Legacy", WorkDir: "/tmp/legacy"}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if _, err := s.db.Exec("INSERT INTO users (id, name, work_dir, deleted_at) VALUES ('legacy-agent', 'Legacy', '/tmp/legacy', datetime('now'))"); err != nil {
		t.Fatalf("seed soft-deleted legacy row: %v", err)
	}

	if err := s.SetUserWorkDir(ctx, "legacy-agent", "/tmp/moved"); err != nil {
		t.Fatalf("SetUserWorkDir: %v", err)
	}

	got, err := s.GetUser(ctx, "legacy-agent")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if got.WorkDir != "/tmp/moved" {
		t.Fatalf("work_dir = %q, want %q", got.WorkDir, "/tmp/moved")
	}
	// The write must land on the agents table, not the dead users row.
	var agentWorkDir string
	if err := s.db.QueryRow("SELECT work_dir FROM agents WHERE id = 'legacy-agent'").Scan(&agentWorkDir); err != nil {
		t.Fatalf("read agent row: %v", err)
	}
	if agentWorkDir != "/tmp/moved" {
		t.Fatalf("agents.work_dir = %q, want %q", agentWorkDir, "/tmp/moved")
	}
}

func assertCount(t *testing.T, s *SQLiteStore, query string, want int) {
	t.Helper()
	var got int
	if err := s.db.QueryRow(query).Scan(&got); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	if got != want {
		t.Fatalf("count %q = %d, want %d", query, got, want)
	}
}

// The users half of ListUsers projects archived_at as a bare NULL, which has
// no declared type. Were it the first SELECT of the UNION, an archived agent's
// timestamp would arrive as a string and fail to scan into a time.
func TestListUsers_ScansArchivedAgent(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "owner", Username: "alice", Name: "Alice"}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := s.CreateUser(ctx, User{ID: "agent-1", OwnerID: "owner", Name: "Worker"}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := s.ArchiveUser(ctx, "agent-1"); err != nil {
		t.Fatalf("archive agent: %v", err)
	}

	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	var archived bool
	for _, u := range users {
		if u.ID == "agent-1" {
			archived = u.Archived
		}
	}
	if !archived {
		t.Fatalf("archived agent missing or not archived in %+v", users)
	}
}
