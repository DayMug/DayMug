package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewSQLiteStore_RejectsEmptyPath(t *testing.T) {
	t.Parallel()
	if _, err := NewSQLiteStore(""); err == nil {
		t.Fatal("expected error for empty dbPath, got nil")
	}
}

func TestCreateAndListUsers(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateUser(ctx, User{ID: "u1", Name: "Alice", WorkDir: "/home/alice", Avatar: "A"}); err != nil {
		t.Fatalf("create u1: %v", err)
	}
	if err := s.CreateUser(ctx, User{ID: "u2", Name: "Bob", WorkDir: "/home/bob"}); err != nil {
		t.Fatalf("create u2: %v", err)
	}

	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2, got %d", len(users))
	}
	if users[0].Name != "Alice" || users[1].Name != "Bob" {
		t.Errorf("wrong names: %s, %s", users[0].Name, users[1].Name)
	}
}

func TestGetUser(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateUser(ctx, User{ID: "u1", Name: "Alice", WorkDir: "/home/alice"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	u, err := s.GetUser(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if u.Name != "Alice" || u.WorkDir != "/home/alice" {
		t.Errorf("unexpected user: %+v", u)
	}
}

func TestGetUser_NotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	// ErrNotFound, not sql.ErrNoRows: handlers map only the sentinel to 404,
	// so a raw ErrNoRows turned every missing-user lookup into a 500.
	_, err := s.GetUser(ctx, "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetUser(missing) err = %v, want ErrNotFound", err)
	}
}

// TestGetOwner_HumanIsOwnOwner — humans are their own owner row, no email
// lookup needed. The upload feature relies on this short-circuit so the
// authed-session user resolves immediately even when their email is empty
// (legacy rows that predate the username==email-prefix invariant).
func TestGetOwner_HumanIsOwnOwner(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	human := User{ID: "u1", Name: "Alice", Username: "alice", WorkDir: "/home/alice"}
	if err := s.CreateUser(ctx, human); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetOwner(ctx, human)
	if err != nil {
		t.Fatalf("GetOwner: %v", err)
	}
	if got.ID != "u1" {
		t.Errorf("expected human to be own owner, got %s", got.ID)
	}
}

// TestGetOwner_AgentResolvesByOwnerID covers the routing the upload + sandbox
// bind both depend on: an agent (a row with empty username) is owned by the
// human its owner_id points at, resolved by a direct id lookup.
func TestGetOwner_AgentResolvesByOwnerID(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	owner := User{ID: "owner-1", Name: "Alice", Username: "alice", Email: "alice@example.com", WorkDir: "/home/alice"}
	agent := User{ID: "agent-1", Name: "bot", OwnerID: "owner-1", WorkDir: "/home/alice/bot"}
	if err := s.CreateUser(ctx, owner); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := s.CreateUser(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	got, err := s.GetOwner(ctx, agent)
	if err != nil {
		t.Fatalf("GetOwner: %v", err)
	}
	if got.ID != "owner-1" {
		t.Errorf("expected owner-1, got %s", got.ID)
	}
}

// TestGetOwner_OrphanAgentReturnsNotFound — an agent with an unmatched
// email is a data-integrity bug (e.g. created before the invariant was
// enforced and never reconciled). The upload handler must surface it as a
// 500, not silently route writes into the wrong directory.
func TestGetOwner_OrphanAgentReturnsNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	agent := User{ID: "agent-1", Name: "bot", Email: "ghost@example.com"}
	if err := s.CreateUser(ctx, agent); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.GetOwner(ctx, agent); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for orphan agent, got %v", err)
	}
}

// TestGetOwner_IgnoresUsernameEmailMismatch pins the consequence of the
// owner_id migration for SSO auto-provisioning: an OIDC local-part that cannot
// round-trip through the username character set (alice+test → alice-test)
// leaves username != EmailLocalPart(email), and that is *harmless* to owner
// resolution. Ownership is a referential owner_id pointer, so the derived
// username is cosmetic here — no orphaning, and structurally no way to reach a
// different human's row.
//
// Regression value: if anyone reintroduces an email-keyed owner lookup, this is
// the test that catches it, because such a lookup is exactly what would turn a
// sanitised username into a cross-user resolution bug.
func TestGetOwner_IgnoresUsernameEmailMismatch(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	// The row SSO auto-provisioning produces for alice+test@example.com.
	human := User{ID: "human-1", Name: "Alice", Username: "alice-test", Email: "alice+test@example.com"}
	// A decoy whose email local-part *is* the sanitised username. An
	// email-keyed lookup could confuse the two; an owner_id lookup cannot.
	decoy := User{ID: "human-2", Name: "Other", Username: "alice-test-2", Email: "alice-test@example.com"}
	agent := User{ID: "agent-1", Name: "bot", OwnerID: "human-1"}
	for _, u := range []User{human, decoy, agent} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatalf("create %s: %v", u.ID, err)
		}
	}

	got, err := s.GetOwner(ctx, agent)
	if err != nil {
		t.Fatalf("GetOwner on a mismatched-local-part owner: %v", err)
	}
	if got.ID != "human-1" {
		t.Errorf("agent resolved to %q, want human-1 (owner_id target)", got.ID)
	}
	if got.Email != "alice+test@example.com" {
		t.Errorf("owner email = %q, want the verbatim provisioned address", got.Email)
	}
}

// TestCreateUser_DuplicateHumanEmailRejected verifies the partial unique
// index built in ensureHumanEmailUniqueIndex actually fires. Two humans
// with the same email would silently break GetOwner's "exactly one row"
// guarantee for any agent sharing that email.
func TestCreateUser_DuplicateHumanEmailRejected(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "u1", Username: "alice", Email: "alice@example.com"}); err != nil {
		t.Fatalf("create first: %v", err)
	}
	err := s.CreateUser(ctx, User{ID: "u2", Username: "alice2", Email: "alice@example.com"})
	if err == nil {
		t.Fatal("expected duplicate-email insert to fail, got nil")
	}
	// Don't pin to a specific SQLite error message — modernc/sqlite's
	// wording is stable but not part of any documented contract.
	if !strings.Contains(err.Error(), "UNIQUE") && !strings.Contains(err.Error(), "unique") {
		t.Errorf("expected unique-constraint error, got %v", err)
	}
}

func TestDeleteUser_CascadeConversations(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateUser(ctx, User{ID: "u1", Name: "Alice", WorkDir: "/tmp"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := s.CreateConversation(ctx, "c1", "Chat", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create conv: %v", err)
	}
	if err := s.SaveMessage(ctx, Message{ID: "m1", ConversationID: "c1", Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("save msg: %v", err)
	}

	if err := s.DeleteUser(ctx, "u1"); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	convs, _ := s.ListConversations(ctx, "u1")
	if len(convs) != 0 {
		t.Errorf("expected 0 conversations after user delete, got %d", len(convs))
	}

	// User-facing soft-delete contract: GetUser hides the row.
	if _, err := s.GetUser(ctx, "u1"); err == nil {
		t.Error("expected GetUser to fail after soft-delete")
	}

	// But the underlying data is preserved on disk so an operator can
	// still recover it. Soft-deleted user, conversation, and the
	// conversation's messages all survive a raw lookup.
	var userDeletedAt sql.NullTime
	if err := s.db.QueryRowContext(ctx,
		"SELECT deleted_at FROM users WHERE id = 'u1'").Scan(&userDeletedAt); err != nil {
		t.Fatalf("raw user lookup: %v", err)
	}
	if !userDeletedAt.Valid {
		t.Error("expected users.deleted_at to be non-NULL after soft-delete")
	}
	var convDeletedAt sql.NullTime
	if err := s.db.QueryRowContext(ctx,
		"SELECT deleted_at FROM conversations WHERE id = 'c1'").Scan(&convDeletedAt); err != nil {
		t.Fatalf("raw conv lookup: %v", err)
	}
	if !convDeletedAt.Valid {
		t.Error("expected conversations.deleted_at to be non-NULL after soft-delete")
	}
	var messageCount int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM messages WHERE conversation_id = 'c1'").Scan(&messageCount); err != nil {
		t.Fatalf("raw message count: %v", err)
	}
	if messageCount != 1 {
		t.Errorf("expected message to survive on disk, got %d rows", messageCount)
	}
}

// TestDeleteUser_FreesUsernameSlot verifies the partial unique index on
// username/email excludes soft-deleted rows so an admin can re-create a
// user with the same credentials after deleting the previous one.
func TestDeleteUser_FreesUsernameSlot(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	original := User{ID: "u1", Name: "Alice", Username: "alice", Email: "alice@example.com"}
	if err := s.CreateUser(ctx, original); err != nil {
		t.Fatalf("create original: %v", err)
	}
	if err := s.DeleteUser(ctx, "u1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	replacement := User{ID: "u2", Name: "Alice", Username: "alice", Email: "alice@example.com"}
	if err := s.CreateUser(ctx, replacement); err != nil {
		t.Fatalf("expected re-create with same username/email to succeed, got %v", err)
	}
}

func TestUpdateUser(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateUser(ctx, User{
		ID:      "u1",
		Name:    "Alice",
		WorkDir: "/home/alice",
		Avatar:  "A",
		Env:     `{"GH_TOKEN":"user-token"}`,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// UpdateUser only touches fields a regular user can change. WorkDir is
	// admin-only and must NOT move via this path.
	err := s.UpdateUser(ctx, User{ID: "u1", Name: "Alice Updated", WorkDir: "/should-be-ignored", Avatar: "B"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	u, err := s.GetUser(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if u.Name != "Alice Updated" {
		t.Errorf("expected name 'Alice Updated', got '%s'", u.Name)
	}
	if u.WorkDir != "/home/alice" {
		t.Errorf("UpdateUser must not change work_dir, got '%s'", u.WorkDir)
	}
	if u.Avatar != "B" {
		t.Errorf("expected avatar 'B', got '%s'", u.Avatar)
	}
	if u.Env != `{"GH_TOKEN":"user-token"}` {
		t.Errorf("UpdateUser must not change env, got %q", u.Env)
	}

	// Admin-only setter is the supported path for changing work_dir.
	if err := s.SetUserWorkDir(ctx, "u1", "/new/dir"); err != nil {
		t.Fatalf("SetUserWorkDir: %v", err)
	}
	u, err = s.GetUser(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if u.WorkDir != "/new/dir" {
		t.Errorf("SetUserWorkDir should change work_dir, got '%s'", u.WorkDir)
	}
	if err := s.SetUserEnv(ctx, "u1", `{"GLAB_CONFIG_DIR":"/tmp/glab"}`); err != nil {
		t.Fatalf("SetUserEnv: %v", err)
	}
	u, err = s.GetUser(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if u.Env != `{"GLAB_CONFIG_DIR":"/tmp/glab"}` {
		t.Errorf("expected env to update, got %q", u.Env)
	}
}

func TestSetUserAdminAndDisabled(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "u1", Name: "Alice", Username: "alice"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetUserAdmin(ctx, "u1", true); err != nil {
		t.Fatalf("SetUserAdmin: %v", err)
	}
	if err := s.SetUserDisabled(ctx, "u1", true); err != nil {
		t.Fatalf("SetUserDisabled: %v", err)
	}
	u, err := s.GetUser(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !u.IsAdmin || !u.Disabled {
		t.Errorf("expected admin=true disabled=true, got admin=%v disabled=%v", u.IsAdmin, u.Disabled)
	}
	if err := s.SetUserAdmin(ctx, "missing", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing user, got %v", err)
	}
}

func TestSetUserClaudeAccountAndEmail(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	// Username is required on human rows: an empty username marks the row
	// as an agent, whose bindings are read from the human owner rather than
	// its own user_provider_bindings entries.
	if err := s.CreateUser(ctx, User{ID: "u1", Name: "Alice", Username: "alice"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetUserProviderBinding(ctx, "u1", "claude", "alt"); err != nil {
		t.Fatalf("SetUserProviderBinding: %v", err)
	}
	if err := s.SetUserEmail(ctx, "u1", "alice@example.com"); err != nil {
		t.Fatalf("SetUserEmail: %v", err)
	}
	u, err := s.GetUser(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if u.Email != "alice@example.com" {
		t.Errorf("email = %q", u.Email)
	}
	if got := u.ProviderBindings["claude"]; got != "alt" {
		t.Errorf("ProviderBindings[claude] = %q, want %q", got, "alt")
	}

	// Empty account = clear binding.
	if err := s.DeleteUserProviderBinding(ctx, "u1", "claude"); err != nil {
		t.Fatalf("DeleteUserProviderBinding: %v", err)
	}
	u, err = s.GetUser(ctx, "u1")
	if err != nil {
		t.Fatalf("get after clear: %v", err)
	}
	if len(u.ProviderBindings) != 0 {
		t.Errorf("expected cleared binding, got %v", u.ProviderBindings)
	}
}

// Multi-type bindings: a single user can be bound to one provider per CLI
// type. SetUserProviderBinding is idempotent (upsert) and DeleteUserProviderBinding
// only touches the targeted slot.
func TestUserProviderBindings_MultiType(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "u1", Name: "Alice"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetUserProviderBinding(ctx, "u1", "claude", "default"); err != nil {
		t.Fatalf("set claude: %v", err)
	}
	if err := s.SetUserProviderBinding(ctx, "u1", "codex", "default-codex"); err != nil {
		t.Fatalf("set codex: %v", err)
	}

	got, err := s.GetUserProviderBindings(ctx, "u1")
	if err != nil {
		t.Fatalf("get bindings: %v", err)
	}
	if got["claude"] != "default" || got["codex"] != "default-codex" {
		t.Errorf("bindings = %v, want {claude:default, codex:default-codex}", got)
	}

	// Idempotent upsert overwrites without conflict.
	if err := s.SetUserProviderBinding(ctx, "u1", "claude", "team-paid"); err != nil {
		t.Fatalf("update claude: %v", err)
	}
	got, _ = s.GetUserProviderBindings(ctx, "u1")
	if got["claude"] != "team-paid" {
		t.Errorf("after update bindings[claude] = %q, want team-paid", got["claude"])
	}
	if got["codex"] != "default-codex" {
		t.Errorf("codex binding clobbered by claude update: %q", got["codex"])
	}

	// Deleting one type leaves the other intact.
	if err := s.DeleteUserProviderBinding(ctx, "u1", "claude"); err != nil {
		t.Fatalf("delete claude: %v", err)
	}
	got, _ = s.GetUserProviderBindings(ctx, "u1")
	if _, ok := got["claude"]; ok {
		t.Errorf("claude binding not deleted: %v", got)
	}
	if got["codex"] != "default-codex" {
		t.Errorf("codex binding lost after claude delete: %v", got)
	}
}

// A user may hold several accounts of the same provider type; the set is
// stored default-first, GetUserProviderBindings returns only the default, and
// a conversation can pin any account from the set.
func TestUserProviderAccounts_MultiAccountPerType(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "u1", Name: "Alice", Username: "alice"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Grant two claude accounts, ollama-claude as the default.
	if err := s.SetUserProviderAccounts(ctx, "u1", "claude", []string{"default", "ollama"}, "ollama"); err != nil {
		t.Fatalf("set accounts: %v", err)
	}

	accounts, err := s.GetUserProviderAccounts(ctx, "u1")
	if err != nil {
		t.Fatalf("get accounts: %v", err)
	}
	if len(accounts["claude"]) != 2 || accounts["claude"][0] != "ollama" {
		t.Errorf("accounts[claude] = %v, want default-first [ollama default]", accounts["claude"])
	}

	// The default-only view picks the chosen default.
	bindings, _ := s.GetUserProviderBindings(ctx, "u1")
	if bindings["claude"] != "ollama" {
		t.Errorf("default binding = %q, want ollama", bindings["claude"])
	}

	// Attached on user reads so the dispatcher can re-validate pins.
	u, err := s.GetUser(ctx, "u1")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if len(u.ProviderAccounts["claude"]) != 2 {
		t.Errorf("user.ProviderAccounts[claude] = %v, want 2 entries", u.ProviderAccounts["claude"])
	}
	if u.ProviderBindings["claude"] != "ollama" {
		t.Errorf("user.ProviderBindings[claude] = %q, want ollama", u.ProviderBindings["claude"])
	}

	// A conversation can pin the non-default account.
	if err := s.CreateConversation(ctx, "c1", "Chat", "u1", "/tmp", "claude", ""); err != nil {
		t.Fatalf("create conv: %v", err)
	}
	if err := s.UpdateConversationAccount(ctx, "c1", "default"); err != nil {
		t.Fatalf("pin account: %v", err)
	}
	conv, err := s.GetConversation(ctx, "c1")
	if err != nil {
		t.Fatalf("get conv: %v", err)
	}
	if conv.AccountName != "default" {
		t.Errorf("conv.AccountName = %q, want default", conv.AccountName)
	}

	// Replacing the set with a single account clears the extras.
	if err := s.SetUserProviderAccounts(ctx, "u1", "claude", []string{"default"}, ""); err != nil {
		t.Fatalf("shrink set: %v", err)
	}
	accounts, _ = s.GetUserProviderAccounts(ctx, "u1")
	if len(accounts["claude"]) != 1 || accounts["claude"][0] != "default" {
		t.Errorf("after shrink accounts[claude] = %v, want [default]", accounts["claude"])
	}
}

// SetUserProviderBinding against a missing user returns ErrNotFound so
// admins get a clean 404 instead of silently writing an orphaned row.
func TestSetUserProviderBinding_UnknownUser(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	err := s.SetUserProviderBinding(context.Background(), "no-such", "claude", "default")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing user, got %v", err)
	}
}

// TestAttachUserBindings_AgentInheritsOwner — an agent (Username=="") has no
// independent provider config; GetUser must surface the human owner's
// bindings so the terminal handler's binding check passes for agent-owned
// conversations. Same owner_id invariant GetOwner uses.
func TestAttachUserBindings_AgentInheritsOwner(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "owner-1", Name: "Alice", Username: "alice", Email: "alice@example.com"}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := s.SetUserProviderBinding(ctx, "owner-1", "claude", "default"); err != nil {
		t.Fatalf("set owner claude: %v", err)
	}
	if err := s.SetUserProviderBinding(ctx, "owner-1", "codex", "codex"); err != nil {
		t.Fatalf("set owner codex: %v", err)
	}
	if err := s.CreateUser(ctx, User{ID: "agent-1", Name: "bot", OwnerID: "owner-1"}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	agent, err := s.GetUser(ctx, "agent-1")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if agent.ProviderBindings["claude"] != "default" || agent.ProviderBindings["codex"] != "codex" {
		t.Errorf("agent did not inherit owner bindings: %v", agent.ProviderBindings)
	}
}

// TestAttachUserBindings_AgentBindingRowsIgnored — even when somebody (the
// old admin UI, a stale migration) wrote rows keyed to the agent's id,
// reads must ignore them and use the owner's. The point of going through
// the owner is to keep agent state consistent with the human who controls
// it; honouring stale agent rows would defeat that.
func TestAttachUserBindings_AgentBindingRowsIgnored(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "owner-1", Username: "alice", Email: "alice@example.com"}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := s.SetUserProviderBinding(ctx, "owner-1", "claude", "owner-claude"); err != nil {
		t.Fatalf("set owner claude: %v", err)
	}
	if err := s.CreateUser(ctx, User{ID: "agent-1", Name: "bot", OwnerID: "owner-1"}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	// SetUserProviderBinding refuses to write against a row with no
	// username? It doesn't — agent rows are valid user rows. Write a
	// stale binding directly so the test reflects an existing DB state.
	if err := s.SetUserProviderBinding(ctx, "agent-1", "claude", "stale-on-agent"); err != nil {
		t.Fatalf("seed stale agent binding: %v", err)
	}
	agent, err := s.GetUser(ctx, "agent-1")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if agent.ProviderBindings["claude"] != "owner-claude" {
		t.Errorf("agent read stale own row instead of owner's: %v", agent.ProviderBindings)
	}
}

// TestAttachUserBindings_OrphanAgentHasNoBindings — an agent whose email
// matches no live human stays empty. The terminal handler will then refuse
// the prompt with "no account bound", which is the right answer: the
// orphan is a data-integrity bug an operator must fix, not something to
// silently paper over.
func TestAttachUserBindings_OrphanAgentHasNoBindings(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "agent-1", Name: "bot", Email: "ghost@example.com"}); err != nil {
		t.Fatalf("create orphan agent: %v", err)
	}
	agent, err := s.GetUser(ctx, "agent-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(agent.ProviderBindings) != 0 {
		t.Errorf("orphan agent should have no bindings, got %v", agent.ProviderBindings)
	}
}

// TestListUsers_AgentInheritsOwnerBindings — the batch loader must apply
// the same agent → owner inheritance as the per-row loader, otherwise the
// admin UI would show agents as unbound and the user list returned to the
// frontend would disagree with what the terminal handler sees.
func TestListUsers_AgentInheritsOwnerBindings(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "owner-1", Username: "alice", Email: "alice@example.com"}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := s.SetUserProviderBinding(ctx, "owner-1", "claude", "default"); err != nil {
		t.Fatalf("set owner claude: %v", err)
	}
	if err := s.SetUserProviderBinding(ctx, "owner-1", "codex", "codex"); err != nil {
		t.Fatalf("set owner codex: %v", err)
	}
	if err := s.CreateUser(ctx, User{ID: "agent-1", Name: "bot", OwnerID: "owner-1"}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	// Add an orphan agent (empty owner_id) to make sure it stays empty rather
	// than picking up bindings from an unrelated human.
	if err := s.CreateUser(ctx, User{ID: "orphan-1", Name: "ghost"}); err != nil {
		t.Fatalf("create orphan: %v", err)
	}
	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byID := map[string]User{}
	for _, u := range users {
		byID[u.ID] = u
	}
	if got := byID["agent-1"].ProviderBindings; got["claude"] != "default" || got["codex"] != "codex" {
		t.Errorf("agent-1 bindings = %v, want owner's {claude:default, codex:codex}", got)
	}
	if len(byID["orphan-1"].ProviderBindings) != 0 {
		t.Errorf("orphan-1 should stay empty, got %v", byID["orphan-1"].ProviderBindings)
	}
	if got := byID["owner-1"].ProviderBindings; got["claude"] != "default" || got["codex"] != "codex" {
		t.Errorf("owner-1 bindings = %v, want {claude:default, codex:codex}", got)
	}
}

func TestAgentRoleDefinition(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateUser(ctx, User{
		ID: "u1", OwnerID: "owner", Name: "Alice", WorkDir: "/home/alice",
		RoleDefinition: "Backend engineer",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	u, err := s.GetUser(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if u.RoleDefinition != "Backend engineer" {
		t.Errorf("expected role_definition 'Backend engineer', got '%s'", u.RoleDefinition)
	}

	// Update
	err = s.UpdateUser(ctx, User{
		ID: "u1", OwnerID: "owner", Name: "Alice", WorkDir: "/home/alice",
		RoleDefinition: "Senior engineer",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	u, err = s.GetUser(ctx, "u1")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if u.RoleDefinition != "Senior engineer" {
		t.Errorf("expected role_definition 'Senior engineer', got '%s'", u.RoleDefinition)
	}

	// Verify ListUsers also returns new fields
	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(users) != 1 || users[0].RoleDefinition != "Senior engineer" {
		t.Errorf("ListUsers did not return the updated role definition")
	}
}

func TestUpdateUser_NotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	err := s.UpdateUser(ctx, User{ID: "nonexistent", Name: "X", WorkDir: "/tmp"})
	if err == nil {
		t.Fatal("expected error for nonexistent user")
	}
}

func TestClearMessages(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "Test", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create conv: %v", err)
	}
	if err := s.SaveMessage(ctx, Message{ID: "m1", ConversationID: "c1", Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("save msg: %v", err)
	}
	if err := s.SaveMessage(ctx, Message{ID: "m2", ConversationID: "c1", Role: "assistant", Content: "hello"}); err != nil {
		t.Fatalf("save msg: %v", err)
	}

	if err := s.ClearMessages(ctx, "c1"); err != nil {
		t.Fatalf("clear: %v", err)
	}

	msgs, err := s.ListMessages(ctx, "c1", 100, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages after clear, got %d", len(msgs))
	}

	// Conversation should still exist
	convs, err := s.ListConversations(ctx, "u1")
	if err != nil {
		t.Fatalf("list convs: %v", err)
	}
	if len(convs) != 1 {
		t.Errorf("expected conversation to still exist, got %d", len(convs))
	}
}

func TestCreateConversation_GeneratesClaudeSessionID(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "Test", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}

	sessionID, err := s.GetSessionID(ctx, "c1")
	if err != nil {
		t.Fatalf("get session id: %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected non-empty claude_session_id")
	}
	if sessionID == "c1" {
		t.Error("claude_session_id should differ from conversation id")
	}
}

func TestConversationThinkLevelRoundTrip(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "Test", "u1", "/tmp", "claude", "model-1"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.UpdateConversationThinkLevel(ctx, "c1", "medium"); err != nil {
		t.Fatalf("update think level: %v", err)
	}
	got, err := s.GetConversation(ctx, "c1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ThinkLevel != "medium" {
		t.Fatalf("think_level = %q, want medium", got.ThinkLevel)
	}
	listed, err := s.ListConversations(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 1 || listed[0].ThinkLevel != "medium" {
		t.Fatalf("listed conversations = %+v", listed)
	}
}

func TestClearMessages_RotatesClaudeSessionID(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "Test", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SaveMessage(ctx, Message{ID: "m1", ConversationID: "c1", Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	oldSessionID, err := s.GetSessionID(ctx, "c1")
	if err != nil {
		t.Fatalf("get old session id: %v", err)
	}

	if err := s.ClearMessages(ctx, "c1"); err != nil {
		t.Fatalf("clear: %v", err)
	}

	newSessionID, err := s.GetSessionID(ctx, "c1")
	if err != nil {
		t.Fatalf("get new session id: %v", err)
	}

	if newSessionID == oldSessionID {
		t.Error("expected claude_session_id to change after ClearMessages")
	}

	msgs, _ := s.ListMessages(ctx, "c1", 100, 0)
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestGetClaudeSessionID_NotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetSessionID(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent conversation")
	}
}

func TestInit_Idempotent(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if err := s.Init(); err != nil {
		t.Fatalf("second init: %v", err)
	}
}

func TestCreateAndListConversations(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	// Manually set different timestamps to avoid relying on clock resolution
	if err := s.CreateConversation(ctx, "c1", "First", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create c1: %v", err)
	}
	// Set c1 to an older timestamp
	if _, err := s.db.ExecContext(ctx, "UPDATE conversations SET updated_at = datetime('now', '-1 minute') WHERE id = 'c1'"); err != nil {
		t.Fatalf("update c1 timestamp: %v", err)
	}
	if err := s.CreateConversation(ctx, "c2", "Second", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create c2: %v", err)
	}

	convs, err := s.ListConversations(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(convs) != 2 {
		t.Fatalf("expected 2, got %d", len(convs))
	}
	// Most recent first
	if convs[0].ID != "c2" {
		t.Errorf("expected c2 first, got %s", convs[0].ID)
	}
	if convs[1].ID != "c1" {
		t.Errorf("expected c1 second, got %s", convs[1].ID)
	}
}

func TestSaveAndListMessages(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "Test", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}

	msgs := []Message{
		{ID: "m1", ConversationID: "c1", Role: "user", Content: "hello"},
		{ID: "m2", ConversationID: "c1", Role: "assistant", Content: "hi there"},
	}
	for _, m := range msgs {
		if err := s.SaveMessage(ctx, m); err != nil {
			t.Fatalf("save %s: %v", m.ID, err)
		}
	}

	got, err := s.ListMessages(ctx, "c1", 100, 0)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].ID != "m1" || got[1].ID != "m2" {
		t.Errorf("wrong order: %s, %s", got[0].ID, got[1].ID)
	}
	if got[0].Role != "user" || got[1].Role != "assistant" {
		t.Errorf("wrong roles: %s, %s", got[0].Role, got[1].Role)
	}
}

// Regression: a prompt queued while the prior turn is still running used
// to sort ahead of that turn's reply, because EnqueuePrompt persists the
// row the instant the user hits send (before the assistant message is
// saved at end-of-turn). Claiming the prompt now re-inserts it so history
// reads user → reply → user → reply, not user → user → reply → reply.
func TestClaimPendingPromptByID_ReordersAfterPriorReply(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "T", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Turn 1: user sends, and BEFORE the reply is persisted the user
	// queues a second prompt — exactly the race that mis-ordered history.
	if err := s.SaveMessage(ctx, Message{ID: "u1", ConversationID: "c1", Role: "user", Content: "first"}); err != nil {
		t.Fatalf("save u1: %v", err)
	}
	queued, err := s.EnqueuePrompt(ctx, "c1", "second")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := s.SaveMessage(ctx, Message{ID: "a1", ConversationID: "c1", Role: "assistant", Content: "reply one"}); err != nil {
		t.Fatalf("save a1: %v", err)
	}

	// Turn 2 begins: the dispatcher claims the queued prompt (this is the
	// moment that re-inserts it), then the second reply is persisted.
	claimed, err := s.ClaimPendingPromptByID(ctx, queued.ID)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.ID != queued.ID || claimed.Content != "second" {
		t.Fatalf("claimed = %+v, want id=%s content=second", claimed, queued.ID)
	}
	if claimed.QueueStatus != "processing" {
		t.Errorf("claimed queue_status = %q, want processing", claimed.QueueStatus)
	}
	if !claimed.CreatedAt.Equal(queued.CreatedAt) {
		t.Errorf("claim must preserve the send-time created_at: got %v, want %v", claimed.CreatedAt, queued.CreatedAt)
	}
	if err := s.SaveMessage(ctx, Message{ID: "a2", ConversationID: "c1", Role: "assistant", Content: "reply two"}); err != nil {
		t.Fatalf("save a2: %v", err)
	}

	got, err := s.ListMessages(ctx, "c1", 100, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var order []string
	for _, m := range got {
		order = append(order, m.ID)
	}
	want := []string{"u1", "a1", queued.ID, "a2"}
	if !slices.Equal(order, want) {
		t.Errorf("history order = %v, want %v", order, want)
	}
}

func TestSaveMessage_UpdatesConversationTimestamp(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "Old", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create c1: %v", err)
	}
	if err := s.CreateConversation(ctx, "c2", "New", "u1", "/tmp2", "", ""); err != nil {
		t.Fatalf("create c2: %v", err)
	}
	// Set c1 to an older timestamp, c2 to a newer one
	if _, err := s.db.ExecContext(ctx, "UPDATE conversations SET updated_at = datetime('now', '-2 minutes') WHERE id = 'c1'"); err != nil {
		t.Fatalf("update c1: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE conversations SET updated_at = datetime('now', '-1 minute') WHERE id = 'c2'"); err != nil {
		t.Fatalf("update c2: %v", err)
	}

	// c2 should be first (more recent)
	convs, _ := s.ListConversations(ctx, "u1")
	if convs[0].ID != "c2" {
		t.Fatalf("expected c2 first before update")
	}

	// Save message to c1 — its updated_at becomes now
	if err := s.SaveMessage(ctx, Message{ID: "m1", ConversationID: "c1", Role: "user", Content: "bump"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	convs, _ = s.ListConversations(ctx, "u1")
	if convs[0].ID != "c1" {
		t.Errorf("expected c1 first after message, got %s", convs[0].ID)
	}
}

// updated_at is second-resolution (datetime('now')), so conversations created
// in the same second tie on the primary sort key. The ORDER BY breaks the tie
// by created_at DESC, id DESC so the result is deterministic across calls
// instead of leaving same-second rows in SQLite's undefined order.
func TestListConversations_StableTiebreakOnEqualUpdatedAt(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	for _, id := range []string{"c1", "c2", "c3"} {
		if err := s.CreateConversation(ctx, id, id, "u1", "/tmp", "", ""); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	// Pin every row to the same second so updated_at and created_at can't
	// disambiguate them; the id tiebreak must.
	if _, err := s.db.ExecContext(ctx,
		"UPDATE conversations SET created_at = datetime('now'), updated_at = datetime('now') WHERE user_id = 'u1'"); err != nil {
		t.Fatalf("flatten timestamps: %v", err)
	}

	first, _ := s.ListConversations(ctx, "u1")
	second, _ := s.ListConversations(ctx, "u1")

	want := []string{"c3", "c2", "c1"} // id DESC
	for i, c := range first {
		if c.ID != want[i] {
			t.Fatalf("order[%d] = %s, want %s", i, c.ID, want[i])
		}
		if second[i].ID != c.ID {
			t.Errorf("order not stable at %d: %s vs %s", i, c.ID, second[i].ID)
		}
	}
}

func TestDeleteConversation_CascadeMessages(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "ToDelete", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SaveMessage(ctx, Message{ID: "m1", ConversationID: "c1", Role: "user", Content: "bye"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := s.DeleteConversation(ctx, "c1"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Soft-delete: from the API's point of view the conversation and its
	// messages are gone — every public read filters them out.
	convs, _ := s.ListConversations(ctx, "u1")
	if len(convs) != 0 {
		t.Errorf("expected 0 conversations, got %d", len(convs))
	}

	msgs, _ := s.ListMessages(ctx, "c1", 100, 0)
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages after cascade, got %d", len(msgs))
	}

	// On disk, however, the messages survive so the data isn't lost. The
	// previous hard-delete path would have cascade-deleted this row.
	var messageCount int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM messages WHERE conversation_id = 'c1'").Scan(&messageCount); err != nil {
		t.Fatalf("raw message count: %v", err)
	}
	if messageCount != 1 {
		t.Errorf("expected message to survive on disk, got %d rows", messageCount)
	}
}

// TestDeleteConversation_Idempotent verifies a second delete on an
// already-soft-deleted conversation is treated as ErrNotFound rather than
// silently re-stamping the deletion timestamp.
func TestDeleteConversation_Idempotent(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "Once", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.DeleteConversation(ctx, "c1"); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if err := s.DeleteConversation(ctx, "c1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound on second delete, got %v", err)
	}
}

func TestDeleteConversation_NotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	err := s.DeleteConversation(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent conversation")
	}
}

func TestDeleteConversationsUpdatedBefore(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	for _, tc := range []struct {
		id     string
		userID string
	}{
		{id: "old-target", userID: "agent-1"},
		{id: "new-target", userID: "agent-1"},
		{id: "old-other-agent", userID: "agent-2"},
	} {
		if err := s.CreateConversation(ctx, tc.id, tc.id, tc.userID, "", "claude", "model"); err != nil {
			t.Fatalf("create %s: %v", tc.id, err)
		}
	}
	cutoff := time.Now().Add(-14 * 24 * time.Hour)
	if _, err := s.db.ExecContext(ctx, "UPDATE conversations SET updated_at = ? WHERE id IN (?, ?)", cutoff.Add(-time.Hour), "old-target", "old-other-agent"); err != nil {
		t.Fatalf("age conversations: %v", err)
	}

	ids, err := s.DeleteConversationsUpdatedBefore(ctx, "agent-1", cutoff)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if !slices.Equal(ids, []string{"old-target"}) {
		t.Fatalf("deleted ids = %v, want [old-target]", ids)
	}
	if _, err := s.GetConversation(ctx, "old-target"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old target should be hidden, got %v", err)
	}
	for _, id := range []string{"new-target", "old-other-agent"} {
		if _, err := s.GetConversation(ctx, id); err != nil {
			t.Fatalf("%s should remain: %v", id, err)
		}
	}
}

func TestSQLiteStore_Backend(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if got := s.Backend(); got != "sqlite" {
		t.Errorf("Backend() = %q, want %q", got, "sqlite")
	}
}

// TestOptimizeDatabase_ReclaimsSpace writes a non-trivial amount of data,
// deletes it, then VACUUMs. The post-optimize file size should be smaller
// than the pre-optimize size — proving the VACUUM actually returned pages
// to the OS rather than leaving them as free pages inside the file.
func TestOptimizeDatabase_ReclaimsSpace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "u1", Name: "owner", WorkDir: "/tmp"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := s.CreateConversation(ctx, "c1", "Bulk", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create conv: %v", err)
	}
	// Write enough rows that the database file noticeably grows.
	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = 'x'
	}
	for i := 0; i < 500; i++ {
		if err := s.SaveMessage(ctx, Message{
			ID:             "m" + itoa(i),
			ConversationID: "c1",
			Role:           "user",
			Content:        string(payload),
		}); err != nil {
			t.Fatalf("save msg %d: %v", i, err)
		}
	}
	// Insert an expired session — OptimizeDatabase should reap it.
	if err := s.CreateSession(ctx, Session{
		Token:     "stale",
		UserID:    "u1",
		CreatedAt: time.Now().Add(-2 * time.Hour),
		ExpiresAt: time.Now().Add(-1 * time.Hour),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Delete everything so VACUUM has free pages to reclaim.
	if err := s.DeleteConversation(ctx, "c1"); err != nil {
		t.Fatalf("delete conv: %v", err)
	}

	res, err := s.OptimizeDatabase(ctx)
	if err != nil {
		t.Fatalf("optimize: %v", err)
	}
	if res.BeforeBytes <= 0 {
		t.Fatalf("expected positive before bytes, got %d", res.BeforeBytes)
	}
	if res.AfterBytes >= res.BeforeBytes {
		t.Errorf("expected after < before, got before=%d after=%d", res.BeforeBytes, res.AfterBytes)
	}
	if res.ExpiredSessionsDeleted != 1 {
		t.Errorf("expected 1 expired session reaped, got %d", res.ExpiredSessionsDeleted)
	}

	// Confirm the file on disk really shrank.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() >= res.BeforeBytes {
		t.Errorf("on-disk size %d should be < before %d", fi.Size(), res.BeforeBytes)
	}
}

func TestOptimizeDatabase_InMemoryReportsZero(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	res, err := s.OptimizeDatabase(context.Background())
	if err != nil {
		t.Fatalf("optimize: %v", err)
	}
	if res.BeforeBytes != 0 || res.AfterBytes != 0 {
		t.Errorf("in-memory db should report zero sizes, got %+v", res)
	}
}

// itoa is a tiny inlined int→string helper so this test file doesn't need
// to pull in strconv just for one usage.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[n:])
}

func TestSaveMessage_ThinkingRole(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "Test", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create conv: %v", err)
	}

	// Save thinking, then assistant
	if err := s.SaveMessage(ctx, Message{ID: "m1", ConversationID: "c1", Role: "thinking", Content: "let me think..."}); err != nil {
		t.Fatalf("save thinking: %v", err)
	}
	if err := s.SaveMessage(ctx, Message{ID: "m2", ConversationID: "c1", Role: "assistant", Content: "here is my answer"}); err != nil {
		t.Fatalf("save assistant: %v", err)
	}

	msgs, err := s.ListMessages(ctx, "c1", 100, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "thinking" || msgs[0].Content != "let me think..." {
		t.Errorf("unexpected thinking msg: %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "here is my answer" {
		t.Errorf("unexpected assistant msg: %+v", msgs[1])
	}
}

func TestListMessages_Pagination(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "Paged", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := range 5 {
		if err := s.SaveMessage(ctx, Message{
			ConversationID: "c1",
			Role:           "user",
			Content:        string(rune('a' + i)),
		}); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	got, _ := s.ListMessages(ctx, "c1", 2, 0)
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}

	got, _ = s.ListMessages(ctx, "c1", 2, 3)
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
}

func TestListMessagesAfter(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "Backfill", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create conv: %v", err)
	}
	if err := s.CreateConversation(ctx, "c2", "Other", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create other conv: %v", err)
	}

	saved := make([]Message, 0, 4)
	for i, m := range []Message{
		{ID: "m1", ConversationID: "c1", Role: "user", Content: "hi"},
		{ID: "m2", ConversationID: "c1", Role: "thinking", Content: "..."},
		{ID: "m3", ConversationID: "c1", Role: "assistant", Content: "hello"},
		{ID: "m4", ConversationID: "c1", Role: "user", Content: "follow-up"},
	} {
		if err := s.SaveMessage(ctx, m); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
		// Bump created_at so the cursor query has a stable ordering.
		// SQLite's default datetime('now') has 1s resolution, so without
		// nudging individual rows the test relies on the (created_at, id)
		// tiebreaker — but we exercise both branches by spreading some
		// rows over multiple seconds.
		if i == 1 {
			if _, err := s.db.ExecContext(ctx,
				"UPDATE messages SET created_at = datetime(created_at, '+1 second') WHERE id IN (?, ?)",
				"m3", "m4"); err != nil {
				t.Fatalf("bump created_at: %v", err)
			}
		}
		saved = append(saved, m)
	}

	// Foreign-conversation row that must never be returned.
	if err := s.SaveMessage(ctx, Message{ID: "x1", ConversationID: "c2", Role: "user", Content: "leak?"}); err != nil {
		t.Fatalf("save c2: %v", err)
	}

	t.Run("after first message returns the rest", func(t *testing.T) {
		got, err := s.ListMessagesAfter(ctx, "c1", "m1", 100)
		if err != nil {
			t.Fatalf("after m1: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("expected 3 messages after m1, got %d", len(got))
		}
		if got[0].ID != "m2" || got[1].ID != "m3" || got[2].ID != "m4" {
			t.Errorf("wrong order: %s, %s, %s", got[0].ID, got[1].ID, got[2].ID)
		}
		for _, m := range got {
			if m.ConversationID != "c1" {
				t.Errorf("leaked message from %s", m.ConversationID)
			}
		}
	})

	t.Run("tiebreaker on identical created_at", func(t *testing.T) {
		// m1 and m2 share the original second; m2 must not include itself.
		got, err := s.ListMessagesAfter(ctx, "c1", "m2", 100)
		if err != nil {
			t.Fatalf("after m2: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 after m2, got %d", len(got))
		}
		if got[0].ID != "m3" || got[1].ID != "m4" {
			t.Errorf("wrong order: %v", got)
		}
	})

	t.Run("after most-recent returns nothing", func(t *testing.T) {
		got, err := s.ListMessagesAfter(ctx, "c1", "m4", 100)
		if err != nil {
			t.Fatalf("after m4: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected 0, got %d", len(got))
		}
	})

	t.Run("unknown cursor returns the full conversation", func(t *testing.T) {
		// Treat a stale / never-seen cursor the same as the empty-cursor
		// recovery path: hand back everything so the client can resync
		// from scratch instead of being stuck with the ambiguous tail.
		got, err := s.ListMessagesAfter(ctx, "c1", "does-not-exist", 100)
		if err != nil {
			t.Fatalf("unknown cursor: %v", err)
		}
		if len(got) != 4 {
			t.Fatalf("expected 4 messages on unknown cursor, got %d", len(got))
		}
		if got[0].ID != "m1" || got[3].ID != "m4" {
			t.Errorf("wrong slice on unknown cursor: %v", got)
		}
	})

	t.Run("blank cursor returns the full conversation", func(t *testing.T) {
		// New-conversation recovery path: a client that never anchored
		// a cursor (e.g. its REST snapshot was empty) sends "" and
		// expects the server to push everything that has since landed.
		got, err := s.ListMessagesAfter(ctx, "c1", "", 100)
		if err != nil {
			t.Fatalf("blank cursor: %v", err)
		}
		if len(got) != 4 {
			t.Fatalf("expected 4 messages on blank cursor, got %d", len(got))
		}
	})

	t.Run("blank conversation returns empty without error", func(t *testing.T) {
		if got, err := s.ListMessagesAfter(ctx, "", "m1", 100); err != nil || len(got) != 0 {
			t.Errorf("blank conv: got %d, err %v", len(got), err)
		}
	})

	t.Run("limit caps the result", func(t *testing.T) {
		got, err := s.ListMessagesAfter(ctx, "c1", "m1", 2)
		if err != nil {
			t.Fatalf("limit: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2, got %d", len(got))
		}
		if got[0].ID != "m2" || got[1].ID != "m3" {
			t.Errorf("wrong slice under limit: %v", got)
		}
	})

	_ = saved
}

func TestListMessagesBefore(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "Page", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create conv: %v", err)
	}
	if err := s.CreateConversation(ctx, "c2", "Other", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create other conv: %v", err)
	}

	for _, m := range []Message{
		{ID: "m1", ConversationID: "c1", Role: "user", Content: "1"},
		{ID: "m2", ConversationID: "c1", Role: "assistant", Content: "2"},
		{ID: "m3", ConversationID: "c1", Role: "user", Content: "3"},
		{ID: "m4", ConversationID: "c1", Role: "assistant", Content: "4"},
		{ID: "m5", ConversationID: "c1", Role: "user", Content: "5"},
	} {
		if err := s.SaveMessage(ctx, m); err != nil {
			t.Fatalf("save %s: %v", m.ID, err)
		}
	}
	if err := s.SaveMessage(ctx, Message{ID: "x1", ConversationID: "c2", Role: "user", Content: "leak?"}); err != nil {
		t.Fatalf("save c2: %v", err)
	}

	t.Run("empty cursor returns the latest page", func(t *testing.T) {
		got, err := s.ListMessagesBefore(ctx, "c1", "", 3)
		if err != nil {
			t.Fatalf("before \"\": %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("expected 3 latest, got %d", len(got))
		}
		if got[0].ID != "m3" || got[1].ID != "m4" || got[2].ID != "m5" {
			t.Errorf("expected ascending [m3,m4,m5], got %s,%s,%s",
				got[0].ID, got[1].ID, got[2].ID)
		}
	})

	t.Run("cursor returns the page strictly before it", func(t *testing.T) {
		got, err := s.ListMessagesBefore(ctx, "c1", "m4", 2)
		if err != nil {
			t.Fatalf("before m4: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2, got %d", len(got))
		}
		if got[0].ID != "m2" || got[1].ID != "m3" {
			t.Errorf("expected [m2,m3], got [%s,%s]", got[0].ID, got[1].ID)
		}
	})

	t.Run("cursor at head returns empty", func(t *testing.T) {
		got, err := s.ListMessagesBefore(ctx, "c1", "m1", 100)
		if err != nil {
			t.Fatalf("before m1: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected 0, got %d", len(got))
		}
	})

	t.Run("limit larger than remaining returns all", func(t *testing.T) {
		got, err := s.ListMessagesBefore(ctx, "c1", "m3", 100)
		if err != nil {
			t.Fatalf("before m3: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2, got %d", len(got))
		}
		if got[0].ID != "m1" || got[1].ID != "m2" {
			t.Errorf("expected [m1,m2], got [%s,%s]", got[0].ID, got[1].ID)
		}
	})

	t.Run("unknown cursor returns the latest page", func(t *testing.T) {
		// Same recovery contract as ListMessagesAfter: an unknown cursor
		// is indistinguishable from "no cursor", so hand back the latest
		// page so the client can resync from scratch.
		got, err := s.ListMessagesBefore(ctx, "c1", "does-not-exist", 2)
		if err != nil {
			t.Fatalf("unknown cursor: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2, got %d", len(got))
		}
		if got[0].ID != "m4" || got[1].ID != "m5" {
			t.Errorf("expected latest page, got [%s,%s]", got[0].ID, got[1].ID)
		}
	})

	t.Run("blank conversation returns empty without error", func(t *testing.T) {
		if got, err := s.ListMessagesBefore(ctx, "", "m1", 100); err != nil || len(got) != 0 {
			t.Errorf("blank conv: got %d, err %v", len(got), err)
		}
	})

	t.Run("zero limit applies the default", func(t *testing.T) {
		got, err := s.ListMessagesBefore(ctx, "c1", "", 0)
		if err != nil {
			t.Fatalf("zero limit: %v", err)
		}
		if len(got) != 5 {
			t.Fatalf("expected 5 (default cap > total), got %d", len(got))
		}
	})

	t.Run("does not leak across conversations", func(t *testing.T) {
		got, err := s.ListMessagesBefore(ctx, "c1", "", 100)
		if err != nil {
			t.Fatalf("no leak: %v", err)
		}
		for _, m := range got {
			if m.ConversationID != "c1" {
				t.Errorf("leaked message from %s", m.ConversationID)
			}
		}
	})
}

func TestListMessagesAfter_SameSecondRandomIDsOrderByInsertion(t *testing.T) {
	t.Parallel()
	// Regression: the previous (created_at, id) ordering tie-broke on
	// random V4 UUIDs, so when user/thinking/assistant all landed in the
	// same wall-clock second a reconnecting client could be served the
	// thinking row but not the assistant (or vice versa) depending on
	// which UUID happened to sort larger than the cursor's UUID. rowid
	// is monotonic per insert, so the order is now deterministic.
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "Same-sec", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create conv: %v", err)
	}

	// IDs handcrafted so that lexicographic order disagrees with insertion
	// order: zzz_user (largest) is saved first, aaa_assistant (smallest)
	// is saved last. Old (created_at, id) tie-break with cursor=zzz_user
	// would have returned NOTHING (id > zzz_user is false for both).
	for _, m := range []Message{
		{ID: "zzz_user", ConversationID: "c1", Role: "user", Content: "hi"},
		{ID: "mmm_thinking", ConversationID: "c1", Role: "thinking", Content: "..."},
		{ID: "aaa_assistant", ConversationID: "c1", Role: "assistant", Content: "hello"},
	} {
		if err := s.SaveMessage(ctx, m); err != nil {
			t.Fatalf("save %s: %v", m.ID, err)
		}
	}

	got, err := s.ListMessagesAfter(ctx, "c1", "zzz_user", 100)
	if err != nil {
		t.Fatalf("after zzz_user: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 after zzz_user, got %d (%+v)", len(got), got)
	}
	if got[0].ID != "mmm_thinking" || got[1].ID != "aaa_assistant" {
		t.Errorf("wrong order, expected [mmm_thinking, aaa_assistant], got [%s, %s]",
			got[0].ID, got[1].ID)
	}
}

func TestGetConversation(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "Test", "u1", "/home/test", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}

	conv, err := s.GetConversation(ctx, "c1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if conv.WorkDir != "/home/test" {
		t.Errorf("expected work_dir '/home/test', got '%s'", conv.WorkDir)
	}
	if conv.Title != "Test" {
		t.Errorf("expected title 'Test', got '%s'", conv.Title)
	}
}

func TestUpdateConversationWorkDir(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "Test", "u1", "/old", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := s.UpdateConversationWorkDir(ctx, "c1", "/new/dir"); err != nil {
		t.Fatalf("update: %v", err)
	}

	conv, err := s.GetConversation(ctx, "c1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if conv.WorkDir != "/new/dir" {
		t.Errorf("expected '/new/dir', got '%s'", conv.WorkDir)
	}
}

func TestUpdateConversationContextUsage(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "Test", "u1", "", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Fresh row: empty payload until the first usage event lands.
	conv, err := s.GetConversation(ctx, "c1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if conv.LastContextUsage != "" {
		t.Errorf("expected empty initial context usage, got %q", conv.LastContextUsage)
	}

	payload := `{"used":12345,"total":200000,"input_tokens":1000,"cache_read":11000,"cache_creation":345}`
	if err := s.UpdateConversationContextUsage(ctx, "c1", payload); err != nil {
		t.Fatalf("update: %v", err)
	}
	conv, err = s.GetConversation(ctx, "c1")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if conv.LastContextUsage != payload {
		t.Errorf("payload not persisted: got %q", conv.LastContextUsage)
	}

	// Updating a nonexistent conversation surfaces ErrNotFound so callers
	// can decide between log-and-ignore (live persist path) and surface
	// (admin actions).
	if err := s.UpdateConversationContextUsage(ctx, "missing", payload); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing row, got %v", err)
	}
}

func TestResetConversationClaudeSession(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "Test", "u1", "", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Seed a context_usage payload so we can verify it gets cleared as
	// part of the reset (the bar must drop to zero on the next render).
	if err := s.UpdateConversationContextUsage(ctx, "c1", `{"used":42,"total":200000}`); err != nil {
		t.Fatalf("seed usage: %v", err)
	}

	before, err := s.GetConversation(ctx, "c1")
	if err != nil {
		t.Fatalf("get before: %v", err)
	}
	if before.SessionID == "" {
		t.Fatal("expected initial claude_session_id to be non-empty")
	}
	if before.LastContextUsage == "" {
		t.Fatal("expected seeded context usage to be present")
	}

	newID, err := s.ResetConversationSession(ctx, "c1")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if newID == before.SessionID {
		t.Errorf("expected a new claude_session_id, got the same %q", newID)
	}

	after, err := s.GetConversation(ctx, "c1")
	if err != nil {
		t.Fatalf("get after: %v", err)
	}
	if after.SessionID != newID {
		t.Errorf("returned id %q does not match persisted id %q", newID, after.SessionID)
	}
	if after.LastContextUsage != "" {
		t.Errorf("expected last_context_usage to be cleared, got %q", after.LastContextUsage)
	}

	if _, err := s.ResetConversationSession(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing row, got %v", err)
	}
}

func TestSetClaudeSessionID(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "Test", "u1", "", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := s.SetSessionID(ctx, "c1", "codex-sess-1"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.GetSessionID(ctx, "c1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "codex-sess-1" {
		t.Errorf("session id: got %q want codex-sess-1", got)
	}

	if err := s.SetSessionID(ctx, "missing", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing conversation should return ErrNotFound, got %v", err)
	}
	if err := s.SetSessionID(ctx, "c1", ""); err == nil {
		t.Errorf("empty session id should be rejected")
	}
}

func TestHasUserMessages(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "Test", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}

	has, err := s.HasUserMessages(ctx, "c1")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if has {
		t.Error("expected no user messages")
	}

	// Add an error message — should not count
	if err := s.SaveMessage(ctx, Message{ID: "m1", ConversationID: "c1", Role: "error", Content: "oops"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	has, _ = s.HasUserMessages(ctx, "c1")
	if has {
		t.Error("error messages should not count")
	}

	// Add a user message
	if err := s.SaveMessage(ctx, Message{ID: "m2", ConversationID: "c1", Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	has, _ = s.HasUserMessages(ctx, "c1")
	if !has {
		t.Error("expected user messages after adding one")
	}
}

func TestCreateConversation_WithWorkDir(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateConversation(ctx, "c1", "Test", "u1", "/workspace/project", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}

	convs, err := s.ListConversations(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(convs) != 1 {
		t.Fatalf("expected 1, got %d", len(convs))
	}
	if convs[0].WorkDir != "/workspace/project" {
		t.Errorf("expected work_dir '/workspace/project', got '%s'", convs[0].WorkDir)
	}
}

func TestDeletePendingPrompt_DropsRowAndPreservesSiblings(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "", "u1", "", "", ""); err != nil {
		t.Fatalf("create conv: %v", err)
	}

	a, err := s.EnqueuePrompt(ctx, "c1", "first")
	if err != nil {
		t.Fatalf("enqueue a: %v", err)
	}
	b, err := s.EnqueuePrompt(ctx, "c1", "second")
	if err != nil {
		t.Fatalf("enqueue b: %v", err)
	}

	dropped, err := s.DeletePendingPrompt(ctx, a.ID)
	if err != nil {
		t.Fatalf("delete one: %v", err)
	}
	if dropped.ID != a.ID || dropped.Content != "first" {
		t.Errorf("returned wrong row: %+v", dropped)
	}

	msgs, err := s.ListMessages(ctx, "c1", 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ID != b.ID {
		t.Errorf("expected only sibling b to survive, got %+v", msgs)
	}
}

func TestDeletePendingPrompt_ProtectsProcessingRow(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "", "u1", "", "", ""); err != nil {
		t.Fatalf("create conv: %v", err)
	}
	a, err := s.EnqueuePrompt(ctx, "c1", "first")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	peeked, err := s.PeekNextPendingPrompt(ctx, "c1")
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if _, err := s.ClaimPendingPromptByID(ctx, peeked.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if _, err := s.DeletePendingPrompt(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for processing row, got %v", err)
	}
}

func TestDeletePendingPrompt_UnknownIDReturnsNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if _, err := s.DeletePendingPrompt(context.Background(), "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestPeekDoesNotMutateRow guards the staging-area invariant: a peek
// must leave the row at queue_status='pending' so a worker that's
// parked behind a pool slot doesn't prematurely promote the prompt out
// of the staging area.
func TestPeekDoesNotMutateRow(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "", "u1", "", "", ""); err != nil {
		t.Fatalf("create conv: %v", err)
	}
	a, err := s.EnqueuePrompt(ctx, "c1", "hello")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	peeked, err := s.PeekNextPendingPrompt(ctx, "c1")
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if peeked.ID != a.ID || peeked.QueueStatus != "pending" {
		t.Errorf("peek returned wrong row: %+v", peeked)
	}

	// Re-peek must still return the same row — nothing changed in DB.
	again, err := s.PeekNextPendingPrompt(ctx, "c1")
	if err != nil {
		t.Fatalf("re-peek: %v", err)
	}
	if again.ID != a.ID {
		t.Errorf("re-peek returned different row: %+v", again)
	}

	msgs, err := s.ListMessages(ctx, "c1", 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(msgs) != 1 || msgs[0].QueueStatus != "pending" {
		t.Errorf("expected row to stay pending, got %+v", msgs)
	}
}

func TestPeekReturnsNotFoundOnEmpty(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "", "u1", "", "", ""); err != nil {
		t.Fatalf("create conv: %v", err)
	}
	if _, err := s.PeekNextPendingPrompt(ctx, "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestClaimPendingPromptByID_TransitionsRow(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "", "u1", "", "", ""); err != nil {
		t.Fatalf("create conv: %v", err)
	}
	a, err := s.EnqueuePrompt(ctx, "c1", "hi")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	claimed, err := s.ClaimPendingPromptByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.QueueStatus != "processing" {
		t.Errorf("expected processing, got %q", claimed.QueueStatus)
	}
	if claimed.Content != "hi" {
		t.Errorf("content mismatch: %q", claimed.Content)
	}

	// Second claim of the same id must fail — the row is no longer pending.
	if _, err := s.ClaimPendingPromptByID(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on second claim, got %v", err)
	}
}

// TestClaimPendingPromptByID_AfterDeleteReturnsNotFound mirrors the
// staging-area cancel race: a user clicks recall on a row that's
// already been peeked by the worker but isn't yet 'processing'. The
// DELETE wins, the subsequent claim must return ErrNotFound so the
// worker releases its pool slot and moves on.
func TestClaimPendingPromptByID_AfterDeleteReturnsNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "", "u1", "", "", ""); err != nil {
		t.Fatalf("create conv: %v", err)
	}
	a, err := s.EnqueuePrompt(ctx, "c1", "hi")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := s.DeletePendingPrompt(ctx, a.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.ClaimPendingPromptByID(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

// TestSQLiteStore_BusyTimeoutAbsorbsConcurrentWrites is the regression
// for the "third tab stuck on queued forever" report. Three browser
// tabs each enqueueing a prompt + the dispatcher claiming + the
// streaming context_usage persist all race for the SQLite write lock
// from independent pool connections. Pre-fix the contended writers
// failed instantly with SQLITE_BUSY; processPrompt's claim path
// returned an error, the dispatcher's MarkPromptDone sweep cleared the
// still-pending row, and the user's prompt vanished without ever
// reaching claude. The DSN-level busy_timeout(5000) tells SQLite to
// wait up to 5s for the lock instead, which is plenty for any
// realistic burst.
//
// Uses a real on-disk file (not :memory:) because each :memory:
// connection in the modernc driver is a separate database — there is
// no contention to time out on.
func TestSQLiteStore_BusyTimeoutAbsorbsConcurrentWrites(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewSQLiteStore(filepath.Join(dir, "busy.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "u1", Name: "owner", WorkDir: "/tmp"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	const numConvs = 8
	const writesPerConv = 4
	convIDs := make([]string, numConvs)
	for i := 0; i < numConvs; i++ {
		cid := fmt.Sprintf("c%d", i)
		convIDs[i] = cid
		if err := s.CreateConversation(ctx, cid, "T", "u1", "/tmp", "", ""); err != nil {
			t.Fatalf("create conv %s: %v", cid, err)
		}
	}

	// Release every goroutine at the same instant so the pool actually
	// experiences contention. Without the gate the first goroutines
	// finish before the rest start writing.
	gate := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, numConvs*writesPerConv)
	for _, cid := range convIDs {
		cid := cid
		for k := 0; k < writesPerConv; k++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-gate
				if _, err := s.EnqueuePrompt(ctx, cid, "p"); err != nil {
					errCh <- err
				}
			}()
		}
	}
	close(gate)
	wg.Wait()
	close(errCh)

	for e := range errCh {
		t.Errorf("concurrent EnqueuePrompt failed: %v", e)
	}
}

func TestReorderPinnedConversations(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	for _, id := range []string{"c1", "c2", "c3"} {
		if err := s.CreateConversation(ctx, id, id, "u1", "/tmp", "", ""); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	// Pin c1 and c3 in an explicit order: c3 first, then c1.
	if err := s.ReorderPinnedConversations(ctx, "u1", []string{"c3", "c1"}); err != nil {
		t.Fatalf("reorder: %v", err)
	}

	got, err := s.ListConversations(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// Pinned block first (c3, c1 by pin_order), then the unpinned c2.
	want := []struct {
		id     string
		pinned bool
	}{{"c3", true}, {"c1", true}, {"c2", false}}
	if len(got) != len(want) {
		t.Fatalf("expected %d rows, got %d", len(want), len(got))
	}
	for i, w := range want {
		if got[i].ID != w.id || got[i].Pinned != w.pinned {
			t.Errorf("row %d = (%s, pinned=%v), want (%s, pinned=%v)", i, got[i].ID, got[i].Pinned, w.id, w.pinned)
		}
	}

	// Re-pin with a different order that drops c3 — it must become unpinned.
	if err := s.ReorderPinnedConversations(ctx, "u1", []string{"c1"}); err != nil {
		t.Fatalf("reorder 2: %v", err)
	}
	got, err = s.ListConversations(ctx, "u1")
	if err != nil {
		t.Fatalf("list 2: %v", err)
	}
	if got[0].ID != "c1" || !got[0].Pinned {
		t.Errorf("expected c1 pinned on top, got (%s, pinned=%v)", got[0].ID, got[0].Pinned)
	}
	for _, c := range got[1:] {
		if c.Pinned {
			t.Errorf("expected %s unpinned after drop, but it's pinned", c.ID)
		}
	}
}

func TestUpdateConversationPinnedPlacesNewPinOnTop(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	for _, id := range []string{"a", "b"} {
		if err := s.CreateConversation(ctx, id, id, "u1", "/tmp", "", ""); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	// Pin a, then b. The most-recently pinned (b) should sort above a.
	if err := s.UpdateConversationPinned(ctx, "a", true); err != nil {
		t.Fatalf("pin a: %v", err)
	}
	if err := s.UpdateConversationPinned(ctx, "b", true); err != nil {
		t.Fatalf("pin b: %v", err)
	}
	got, err := s.ListConversations(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got[0].ID != "b" || got[1].ID != "a" {
		t.Errorf("expected newest pin (b) on top, got order %s, %s", got[0].ID, got[1].ID)
	}
}

// TestUpdateConversationPinned_UnpinResetsPinOrder guards the fix for a sidebar
// ordering bug: unpinning used to leave the row's negative pin_order behind, so
// a once-pinned row sorted above genuinely newer ones in the unpinned block.
func TestUpdateConversationPinned_UnpinResetsPinOrder(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	for _, id := range []string{"old", "new"} {
		if err := s.CreateConversation(ctx, id, id, "u1", "/tmp", "", ""); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	// "old" is the stale conversation, "new" the recently active one.
	if _, err := s.db.ExecContext(ctx, "UPDATE conversations SET updated_at = ? WHERE id = ?", "2026-05-01 00:00:00", "old"); err != nil {
		t.Fatalf("stamp old: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE conversations SET updated_at = ? WHERE id = ?", "2026-05-31 00:00:00", "new"); err != nil {
		t.Fatalf("stamp new: %v", err)
	}

	// Pin then unpin "old": this is the gesture that used to orphan a negative
	// pin_order on the now-unpinned row.
	if err := s.UpdateConversationPinned(ctx, "old", true); err != nil {
		t.Fatalf("pin old: %v", err)
	}
	if err := s.UpdateConversationPinned(ctx, "old", false); err != nil {
		t.Fatalf("unpin old: %v", err)
	}

	var pinOrder int
	if err := s.db.QueryRowContext(ctx, "SELECT pin_order FROM conversations WHERE id = ?", "old").Scan(&pinOrder); err != nil {
		t.Fatalf("read pin_order: %v", err)
	}
	if pinOrder != 0 {
		t.Errorf("expected pin_order reset to 0 on unpin, got %d", pinOrder)
	}

	got, err := s.ListConversations(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got[0].ID != "new" || got[1].ID != "old" {
		t.Errorf("expected unpinned block ordered by updated_at (new, old), got %s, %s", got[0].ID, got[1].ID)
	}
}

func TestEnableConversationShare(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "u1", Name: "User", WorkDir: "/tmp"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := s.CreateConversation(ctx, "c1", "Shared", "u1", "/tmp", "claude", "sonnet"); err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	shared, err := s.EnableConversationShare(ctx, "c1")
	if err != nil {
		t.Fatalf("enable share: %v", err)
	}
	if shared.ShareToken == "" {
		t.Fatal("expected share token")
	}

	again, err := s.EnableConversationShare(ctx, "c1")
	if err != nil {
		t.Fatalf("enable share again: %v", err)
	}
	if again.ShareToken != shared.ShareToken {
		t.Fatalf("expected stable token, got %q then %q", shared.ShareToken, again.ShareToken)
	}

	got, err := s.GetSharedConversation(ctx, shared.ShareToken)
	if err != nil {
		t.Fatalf("get shared conversation: %v", err)
	}
	if got.ID != "c1" || got.Title != "Shared" || !got.Shared {
		t.Fatalf("unexpected shared conversation: %+v", got)
	}

	unshared, err := s.DisableConversationShare(ctx, "c1")
	if err != nil {
		t.Fatalf("disable share: %v", err)
	}
	if unshared.Shared {
		t.Fatal("expected shared=false after disabling share")
	}
	if _, err := s.GetSharedConversation(ctx, shared.ShareToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected old token to be hidden, got %v", err)
	}
}

func TestToolMessageContentIsTruncatedWhenSaved(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "Tools", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	content := strings.Repeat("x", 700)
	if err := s.SaveMessage(ctx, Message{
		ID:             "tool1",
		ConversationID: "c1",
		Role:           "tool",
		Content:        content,
	}); err != nil {
		t.Fatalf("save tool: %v", err)
	}

	msgs, err := s.ListMessagesBefore(ctx, "c1", "", 50)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	var got string
	if err := json.Unmarshal([]byte(msgs[0].Content), &got); err != nil {
		t.Fatalf("stored content_json is not a JSON string: %v (%s)", err, msgs[0].Content)
	}
	if len(got) != toolContentJSONValueLimitBytes {
		t.Fatalf("truncated length = %d, want %d", len(got), toolContentJSONValueLimitBytes)
	}
	if got != content[:toolContentJSONValueLimitBytes] {
		t.Fatal("truncated content does not match prefix")
	}
	var contentText, contentJSON string
	if err := s.db.QueryRowContext(ctx,
		"SELECT content, content_json FROM messages WHERE id = 'tool1'",
	).Scan(&contentText, &contentJSON); err != nil {
		t.Fatalf("read raw message: %v", err)
	}
	if contentText != "" || contentJSON == "" {
		t.Fatalf("raw content/content_json = %q/%q, want only content_json", contentText, contentJSON)
	}
}

func TestToolMessageJSONContentFieldIsTruncatedWhenSaved(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "Tools", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	longContent := strings.Repeat("中", 100)
	payload := fmt.Sprintf(`{"name":"Bash","id":"call1","content":%q,"exit_code":0}`, longContent)
	if err := s.SaveMessage(ctx, Message{
		ID:             "tool1",
		ConversationID: "c1",
		Role:           "tool",
		Content:        payload,
	}); err != nil {
		t.Fatalf("save tool: %v", err)
	}

	msgs, err := s.ListMessagesBefore(ctx, "c1", "", 50)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	var got struct {
		Name     string `json:"name"`
		ID       string `json:"id"`
		Content  string `json:"content"`
		ExitCode int    `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(msgs[0].Content), &got); err != nil {
		t.Fatalf("summary is not valid JSON: %v (%s)", err, msgs[0].Content)
	}
	if got.Name != "Bash" || got.ID != "call1" || got.ExitCode != 0 {
		t.Fatalf("summary lost JSON fields: %+v", got)
	}
	if len(got.Content) != 48 {
		t.Fatalf("content bytes = %d, want 48", len(got.Content))
	}
	if got.Content != strings.Repeat("中", 16) {
		t.Fatalf("content = %q, want 16 Chinese chars", got.Content)
	}
	var contentText, contentJSON string
	if err := s.db.QueryRowContext(ctx,
		"SELECT content, content_json FROM messages WHERE id = 'tool1'",
	).Scan(&contentText, &contentJSON); err != nil {
		t.Fatalf("read raw message: %v", err)
	}
	if contentText != "" || contentJSON == "" {
		t.Fatalf("raw content/content_json = %q/%q, want only content_json", contentText, contentJSON)
	}
}

func TestToolMessageNestedJSONContentFieldIsTruncatedWhenSaved(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "Tools", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	uuidValue := "8e6aa2d1-e61e-4d00-a99e-a3b63b8fe449"
	payload := fmt.Sprintf(
		`{"name":"Bash","uuid":%q,"id":"call1","input":{"command":%q,"output":%q,"metadata":{"short":"kept","body":%q}},"exit_code":0}`,
		uuidValue,
		strings.Repeat("x", 260),
		strings.Repeat("y", 260),
		strings.Repeat("z", 260),
	)
	if err := s.SaveMessage(ctx, Message{
		ID:             "tool1",
		ConversationID: "c1",
		Role:           "tool",
		Content:        payload,
	}); err != nil {
		t.Fatalf("save tool: %v", err)
	}

	msgs, err := s.ListMessagesBefore(ctx, "c1", "", 50)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	var outer struct {
		Name  string `json:"name"`
		UUID  string `json:"uuid"`
		ID    string `json:"id"`
		Input struct {
			Command  string         `json:"command"`
			Output   string         `json:"output"`
			Metadata map[string]any `json:"metadata"`
		} `json:"input"`
		ExitCode int `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(msgs[0].Content), &outer); err != nil {
		t.Fatalf("summary is not valid JSON: %v (%s)", err, msgs[0].Content)
	}
	if outer.Name != "Bash" || outer.ID != "call1" || outer.ExitCode != 0 {
		t.Fatalf("summary lost JSON fields: %+v", outer)
	}
	if outer.UUID != uuidValue {
		t.Fatalf("uuid = %q, want preserved", outer.UUID)
	}
	if outer.Input.Command != strings.Repeat("x", toolContentJSONValueLimitBytes) {
		t.Fatalf("command length/value = %d/%q", len(outer.Input.Command), outer.Input.Command)
	}
	if outer.Input.Output != strings.Repeat("y", toolContentJSONValueLimitBytes) {
		t.Fatalf("output length/value = %d/%q", len(outer.Input.Output), outer.Input.Output)
	}
	if outer.Input.Metadata["short"] != "kept" {
		t.Fatalf("short metadata = %q, want kept", outer.Input.Metadata["short"])
	}
	if got := outer.Input.Metadata["body"].(string); got != strings.Repeat("z", toolContentJSONValueLimitBytes) {
		t.Fatalf("metadata body length/value = %d/%q", len(got), got)
	}
}

func TestToolMessageJSONContentMarshalsAsJSONObject(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "Tools", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	payload := fmt.Sprintf(
		`{"name":"Bash","input":{"command":%q},"content":%q}`,
		strings.Repeat("x", 260),
		strings.Repeat("y", 260),
	)
	if err := s.SaveMessage(ctx, Message{
		ID:             "tool1",
		ConversationID: "c1",
		Role:           "tool",
		Content:        payload,
	}); err != nil {
		t.Fatalf("save tool: %v", err)
	}
	msgs, err := s.ListMessagesBefore(ctx, "c1", "", 50)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	encoded, err := json.Marshal(msgs)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	var wire []struct {
		Content struct {
			Name  string `json:"name"`
			Input struct {
				Command string `json:"command"`
			} `json:"input"`
			Content string `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("content did not marshal as object: %v (%s)", err, encoded)
	}
	if wire[0].Content.Name != "Bash" {
		t.Fatalf("name = %q, want Bash", wire[0].Content.Name)
	}
	if wire[0].Content.Input.Command != strings.Repeat("x", toolContentJSONValueLimitBytes) {
		t.Fatalf("command = %q", wire[0].Content.Input.Command)
	}
	if wire[0].Content.Content != strings.Repeat("y", toolContentJSONValueLimitBytes) {
		t.Fatalf("content = %q", wire[0].Content.Content)
	}
	if bytes.Contains(encoded, []byte("[omitted]")) {
		t.Fatalf("encoded content still contains omitted marker: %s", encoded)
	}
}

func TestToolMessageContentKeepsShortContent(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "Tools", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := s.SaveMessage(ctx, Message{
		ID:             "tool1",
		ConversationID: "c1",
		Role:           "tool",
		Content:        "short",
	}); err != nil {
		t.Fatalf("save tool: %v", err)
	}

	msgs, err := s.ListMessagesBefore(ctx, "c1", "", 50)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Content != "short" {
		t.Fatalf("content = %q, want short", msgs[0].Content)
	}
	var contentText, contentJSON string
	if err := s.db.QueryRowContext(ctx,
		"SELECT content, content_json FROM messages WHERE id = 'tool1'",
	).Scan(&contentText, &contentJSON); err != nil {
		t.Fatalf("read raw message: %v", err)
	}
	if contentText != "short" || contentJSON != "" {
		t.Fatalf("raw content/content_json = %q/%q, want only content", contentText, contentJSON)
	}
}

// TestListConversations_IgnoresStalePinOrderOnUnpinned guards the ORDER BY: even
// if a row carries a leftover pin_order (legacy data written before the unpin
// reset), an unpinned row must order by updated_at, not float to the top.
func TestListConversations_IgnoresStalePinOrderOnUnpinned(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	for _, id := range []string{"stale", "fresh"} {
		if err := s.CreateConversation(ctx, id, id, "u1", "/tmp", "", ""); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	// Simulate legacy data: "stale" is unpinned yet carries a negative
	// pin_order, and is older than "fresh".
	if _, err := s.db.ExecContext(ctx,
		"UPDATE conversations SET pinned = 0, pin_order = -3, updated_at = ? WHERE id = ?",
		"2026-05-01 00:00:00", "stale"); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	if _, err := s.db.ExecContext(ctx,
		"UPDATE conversations SET updated_at = ? WHERE id = ?", "2026-05-31 00:00:00", "fresh"); err != nil {
		t.Fatalf("stamp fresh: %v", err)
	}

	got, err := s.ListConversations(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got[0].ID != "fresh" || got[1].ID != "stale" {
		t.Errorf("expected updated_at order (fresh, stale) despite stale pin_order, got %s, %s", got[0].ID, got[1].ID)
	}
}

// An unbounded pool is the failure mode here: each SQLite connection costs
// three fds and they all contend for the same WAL write lock.
func TestNewSQLiteStore_BoundsConnectionPool(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if got := s.db.Stats().MaxOpenConnections; got != maxOpenConns {
		t.Fatalf("MaxOpenConnections = %d, want %d", got, maxOpenConns)
	}
}
