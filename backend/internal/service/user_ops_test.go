package service

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// wantUserOpsStatus asserts err is a *ServiceError carrying the expected status
// and message — the two halves the transport layer reproduces verbatim.
func wantUserOpsStatus(t *testing.T, err error, status int, msg string) {
	t.Helper()
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Fatalf("error = %v, want *ServiceError", err)
	}
	if svcErr.Status != status {
		t.Errorf("status = %d, want %d", svcErr.Status, status)
	}
	if msg != "" && svcErr.Message() != msg {
		t.Errorf("message = %q, want %q", svcErr.Message(), msg)
	}
}

func assertDefaultAgent(t *testing.T, users []store.User, human store.User) {
	t.Helper()
	var owned []store.User
	for _, user := range users {
		if user.OwnerID == human.ID {
			owned = append(owned, user)
		}
	}
	if len(owned) != 1 {
		t.Fatalf("owned agents = %+v, want exactly one default Agent", owned)
	}
	agent := owned[0]
	if agent.Username != "" || agent.Name != human.Username || agent.WorkDir != human.WorkDir {
		t.Errorf("default Agent = %+v, want name %q and work_dir %q", agent, human.Username, human.WorkDir)
	}
}

// The visibility fixture: one human caller with an owned agent, an archived
// agent, another human and that human's agent. Only the first agent is visible.
func visibilityFixture() *storetest.Fake {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-a", Name: "Alice", Username: "alice", WorkDir: "/home/alice"},
		{ID: "agent-a1", Name: "Researcher", OwnerID: "human-a"},
		{ID: "agent-a2", Name: "Archived", OwnerID: "human-a", Archived: true},
		{ID: "human-b", Name: "Bob", Username: "bob"},
		{ID: "agent-b1", Name: "Builder", OwnerID: "human-b"},
		{ID: "agent-orphan", Name: "Orphan"},
	}
	return ms
}

func TestUserOpsVisibleUsers(t *testing.T) {
	ops := &UserOps{Store: visibilityFixture()}

	users, err := ops.VisibleUsers(context.Background(), "human-a")
	if err != nil {
		t.Fatalf("VisibleUsers: %v", err)
	}
	if len(users) != 1 || users[0].ID != "agent-a1" {
		t.Fatalf("VisibleUsers = %+v, want only agent-a1", users)
	}
}

// The caller's own login row is never a selectable persona, and neither are
// archived agents, other humans, their agents, or ownerless strays.
func TestUserOpsVisibleUsersExcludes(t *testing.T) {
	ops := &UserOps{Store: visibilityFixture()}
	users, err := ops.VisibleUsers(context.Background(), "human-a")
	if err != nil {
		t.Fatalf("VisibleUsers: %v", err)
	}
	for _, u := range users {
		switch u.ID {
		case "human-a":
			t.Error("caller's own human row leaked into the picker")
		case "agent-a2":
			t.Error("archived agent leaked")
		case "human-b", "agent-b1":
			t.Error("another human's rows leaked")
		case "agent-orphan":
			t.Error("ownerless agent leaked")
		}
	}
}

// Both pre-merge call sites keyed the filter differently — /api/users on the
// authenticated id, /api/app-state on the loaded caller row's Owner(). For a
// login row (the only shape an auth path can produce) the two are the same
// value, which is what makes the single implementation safe.
func TestUserOpsVisibleUsersOwnerKeyMatchesAuthedID(t *testing.T) {
	ms := visibilityFixture()
	caller, err := ms.GetUser(context.Background(), "human-a")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if caller.Owner() != caller.ID {
		t.Fatalf("Owner() = %q, want the row's own id %q", caller.Owner(), caller.ID)
	}
}

// No auth middleware (unit-test bypass) returns the unfiltered list, and never
// a nil slice — the endpoint must render `[]`, not `null`.
func TestUserOpsVisibleUsersUnauthenticatedBypass(t *testing.T) {
	ops := &UserOps{Store: visibilityFixture()}
	users, err := ops.VisibleUsers(context.Background(), "")
	if err != nil {
		t.Fatalf("VisibleUsers: %v", err)
	}
	if len(users) != 6 {
		t.Fatalf("VisibleUsers = %d rows, want the unfiltered 6", len(users))
	}

	empty := &UserOps{Store: storetest.New()}
	users, err = empty.VisibleUsers(context.Background(), "")
	if err != nil {
		t.Fatalf("VisibleUsers: %v", err)
	}
	if users == nil {
		t.Error("VisibleUsers returned nil; the endpoint must render [] not null")
	}
}

// Filtering must not compact in place: the fake hands back the slice it stores,
// so an in-place filter would destroy rows for later reads on the same request.
func TestUserOpsVisibleUsersDoesNotMutateStoreSlice(t *testing.T) {
	ms := visibilityFixture()
	ops := &UserOps{Store: ms}
	if _, err := ops.VisibleUsers(context.Background(), "human-a"); err != nil {
		t.Fatalf("VisibleUsers: %v", err)
	}
	all, err := ms.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(all) != 6 || all[0].ID != "human-a" {
		t.Fatalf("store slice was mutated: %+v", all)
	}
}

func TestUserOpsNormalizeDefaultModel(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "mimo", Type: config.CLITypeClaudeCompatible},
	}}
	withAccountModels(t, map[string]AccountModels{
		"mimo": {Models: []string{"mimo-v2.5"}},
	})
	tests := []struct {
		name    string
		cfg     *config.Config
		model   string
		want    string
		wantErr string
	}{
		{name: "empty stays empty", cfg: cfg, model: "  "},
		{name: "nil config skips validation", model: "anything", want: "anything"},
		{name: "known model is trimmed", cfg: cfg, model: " mimo-v2.5 ", want: "mimo-v2.5"},
		{name: "unknown model rejected", cfg: cfg, model: "gpt-nope", wantErr: "unknown model gpt-nope"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ops := &UserOps{Store: storetest.New(), Cfg: tc.cfg}
			got, err := ops.NormalizeDefaultModel(tc.model)
			if tc.wantErr != "" {
				wantUserOpsStatus(t, err, http.StatusBadRequest, tc.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("NormalizeDefaultModel: %v", err)
			}
			if got != tc.want {
				t.Errorf("model = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUserOpsEnsureWorkDirWithinCaller(t *testing.T) {
	home := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-a", Username: "alice", WorkDir: home},
		{ID: "human-nohome", Username: "bob"},
	}
	ops := &UserOps{Store: ms}
	ctx := context.Background()

	if err := ops.EnsureWorkDirWithinCaller(ctx, "", "/anywhere"); err != nil {
		t.Errorf("no auth context should skip the jail check, got %v", err)
	}
	if err := ops.EnsureWorkDirWithinCaller(ctx, "human-a", filepath.Join(home, "proj")); err != nil {
		t.Errorf("path inside the caller's home rejected: %v", err)
	}
	err := ops.EnsureWorkDirWithinCaller(ctx, "human-a", filepath.Join(home, "..", "elsewhere"))
	wantUserOpsStatus(t, err, http.StatusForbidden, "work_dir must be inside your own working directory")

	// Fails closed on an unusable input rather than treating it as contained.
	err = ops.EnsureWorkDirWithinCaller(ctx, "human-a", "")
	wantUserOpsStatus(t, err, http.StatusForbidden, "work_dir must be inside your own working directory")

	err = ops.EnsureWorkDirWithinCaller(ctx, "human-nohome", home)
	wantUserOpsStatus(t, err, http.StatusBadRequest, "your account has no work_dir; ask an admin to set one before creating agents")

	err = ops.EnsureWorkDirWithinCaller(ctx, "ghost", home)
	wantUserOpsStatus(t, err, http.StatusInternalServerError, "")
}

func TestUserOpsCreateAgent(t *testing.T) {
	home := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "human-a", Username: "alice", WorkDir: home}}
	ops := &UserOps{Store: ms}
	ctx := context.Background()

	created, err := ops.CreateAgent(ctx, AgentParams{Name: "Scout", WorkDir: home}, "human-a")
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	// The new row is permanently bound to the creator's human owner; that
	// pointer is what every later visibility/access check keys on.
	if created.OwnerID != "human-a" {
		t.Errorf("owner_id = %q, want human-a", created.OwnerID)
	}
	if created.ID == "" || created.Name != "Scout" {
		t.Errorf("created = %+v, want a minted row named Scout", created)
	}
}

func TestUserOpsCreateAgentValidation(t *testing.T) {
	home := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "human-a", Username: "alice", WorkDir: home}}
	ops := &UserOps{Store: ms}
	ctx := context.Background()

	_, err := ops.CreateAgent(ctx, AgentParams{WorkDir: home}, "human-a")
	wantUserOpsStatus(t, err, http.StatusBadRequest, "name is required")

	_, err = ops.CreateAgent(ctx, AgentParams{Name: "Scout"}, "human-a")
	wantUserOpsStatus(t, err, http.StatusBadRequest, "work_dir is required")

	_, err = ops.CreateAgent(ctx, AgentParams{Name: "Scout", WorkDir: "/etc"}, "human-a")
	wantUserOpsStatus(t, err, http.StatusForbidden, "work_dir must be inside your own working directory")

	_, err = ops.CreateAgent(ctx, AgentParams{Name: "Scout", WorkDir: home}, "ghost")
	wantUserOpsStatus(t, err, http.StatusInternalServerError, "")
}

func TestUserOpsValidatesThinkLevel(t *testing.T) {
	home := t.TempDir()
	ms := &storetest.Fake{Users: []store.User{{ID: "human-a", Username: "alice", WorkDir: home}}}
	ops := &UserOps{Store: ms}

	created, err := ops.CreateAgent(context.Background(), AgentParams{
		Name: "Scout", WorkDir: home, ThinkLevel: " HIGH ",
	}, "human-a")
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if created.ThinkLevel != "high" {
		t.Fatalf("think_level = %q, want high", created.ThinkLevel)
	}
	if _, err := ops.UpdateAgent(context.Background(), created, AgentParams{
		Name: "Scout", WorkDir: home, ThinkLevel: "extreme",
	}, "human-a"); err == nil {
		t.Fatal("UpdateAgent accepted an unsupported think level")
	}
}

func TestUserOpsUpdateAgentMovesWorkDir(t *testing.T) {
	home := t.TempDir()
	moved := filepath.Join(home, "moved")
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-a", Username: "alice", WorkDir: home},
		{ID: "agent-a1", Name: "Scout", OwnerID: "human-a", WorkDir: home},
	}
	ops := &UserOps{Store: ms}
	target := ms.Users[1]

	updated, err := ops.UpdateAgent(context.Background(), target, AgentParams{Name: "Scout II", WorkDir: moved}, "human-a")
	if err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}
	if updated.Name != "Scout II" {
		t.Errorf("name = %q, want Scout II", updated.Name)
	}
	// UpdateUser doesn't carry work_dir; the move is a second write, and
	// skipping it would silently drop the new path.
	if updated.WorkDir != moved {
		t.Errorf("work_dir = %q, want %q", updated.WorkDir, moved)
	}
}

// The CLAUDE.md disk sync must trail the row update. It used to run first, so a
// rejected store write still left the on-disk file rewritten — the file is what
// the agent child process reads, so it would then advertise config the DB never
// accepted.
func TestUserOpsUpdateAgentSkipsClaudeMdSyncWhenStoreWriteFails(t *testing.T) {
	home := t.TempDir()
	claudeMd := filepath.Join(home, "CLAUDE.md")
	if err := os.WriteFile(claudeMd, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed CLAUDE.md: %v", err)
	}
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-a", Username: "alice", WorkDir: home},
		{ID: "agent-a1", Name: "Scout", OwnerID: "human-a", WorkDir: home},
	}
	ms.UpdateErr = errors.New("db is locked")
	ops := &UserOps{Store: ms}

	_, err := ops.UpdateAgent(context.Background(), ms.Users[1], AgentParams{
		Name: "Scout", WorkDir: home, ManageClaudeMd: true, ClaudeMdContent: "rewritten",
	}, "human-a")
	if err == nil {
		t.Fatal("UpdateAgent must surface the store failure")
	}

	got, readErr := os.ReadFile(claudeMd)
	if readErr != nil {
		t.Fatalf("read CLAUDE.md: %v", readErr)
	}
	if string(got) != "original" {
		t.Errorf("CLAUDE.md = %q, want %q — disk must not change when the row update fails", got, "original")
	}
}

// The mirror of the test above: once the row update lands, the file is written.
func TestUserOpsUpdateAgentSyncsClaudeMdAfterStoreWrite(t *testing.T) {
	home := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-a", Username: "alice", WorkDir: home},
		{ID: "agent-a1", Name: "Scout", OwnerID: "human-a", WorkDir: home},
	}
	ops := &UserOps{Store: ms}

	if _, err := ops.UpdateAgent(context.Background(), ms.Users[1], AgentParams{
		Name: "Scout", WorkDir: home, ManageClaudeMd: true, ClaudeMdContent: "rewritten",
	}, "human-a"); err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(home, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if string(got) != "rewritten" {
		t.Errorf("CLAUDE.md = %q, want %q", got, "rewritten")
	}
}

// An unchanged work_dir skips the home-jail check, so rows created under an
// older, laxer regime can still have their name/persona edited.
func TestUserOpsUpdateAgentKeepsOutOfJailWorkDirWhenUnchanged(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-a", Username: "alice", WorkDir: t.TempDir()},
		{ID: "agent-a1", Name: "Legacy", OwnerID: "human-a", WorkDir: "/somewhere/else"},
	}
	ops := &UserOps{Store: ms}
	target := ms.Users[1]

	if _, err := ops.UpdateAgent(context.Background(), target, AgentParams{Name: "Renamed", WorkDir: "/somewhere/else"}, "human-a"); err != nil {
		t.Fatalf("rename of a legacy out-of-jail agent was refused: %v", err)
	}
}

// A human row has no agent work_dir of its own, so the "work_dir is required"
// rule must not fire for it.
func TestUserOpsUpdateAgentHumanTargetNeedsNoWorkDir(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "human-a", Name: "Alice", Username: "alice", WorkDir: "/home/alice"}}
	ops := &UserOps{Store: ms}
	target := ms.Users[0]

	if _, err := ops.UpdateAgent(context.Background(), target, AgentParams{Name: "Alice Renamed"}, "human-a"); err != nil {
		t.Fatalf("UpdateAgent on a human row: %v", err)
	}
}

func TestUserOpsUpdateAgentValidation(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-a", Username: "alice", WorkDir: "/home/alice"},
		{ID: "agent-a1", Name: "Scout", OwnerID: "human-a", WorkDir: "/home/alice/p"},
	}
	ops := &UserOps{Store: ms}
	target := ms.Users[1]
	ctx := context.Background()

	_, err := ops.UpdateAgent(ctx, target, AgentParams{WorkDir: "/home/alice/p"}, "human-a")
	wantUserOpsStatus(t, err, http.StatusBadRequest, "name is required")

	_, err = ops.UpdateAgent(ctx, target, AgentParams{Name: "Scout"}, "human-a")
	wantUserOpsStatus(t, err, http.StatusBadRequest, "work_dir is required")

	_, err = ops.UpdateAgent(ctx, store.User{ID: "ghost", OwnerID: "human-a"}, AgentParams{Name: "Scout", WorkDir: "/home/alice/p"}, "")
	wantUserOpsStatus(t, err, http.StatusNotFound, "user not found")
}

func TestUserOpsDuplicateAgent(t *testing.T) {
	home := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-a", Username: "alice", WorkDir: home},
		{ID: "agent-a1", Name: "Scout", OwnerID: "human-a", WorkDir: home, RoleDefinition: "recon", Username: ""},
	}
	ops := &UserOps{Store: ms}

	dup, err := ops.DuplicateAgent(context.Background(), ms.Users[1], "human-a")
	if err != nil {
		t.Fatalf("DuplicateAgent: %v", err)
	}
	if dup.Name != "Scout (copy)" {
		t.Errorf("name = %q, want %q", dup.Name, "Scout (copy)")
	}
	if dup.ID == "agent-a1" || dup.ID == "" {
		t.Errorf("id = %q, want a fresh uuid", dup.ID)
	}
	// The copy stays under the source's owner even when an admin duplicates
	// somebody else's agent, and it never inherits a login.
	if dup.OwnerID != "human-a" || dup.Username != "" {
		t.Errorf("dup = %+v, want owner human-a and no username", dup)
	}
	if dup.RoleDefinition != "recon" {
		t.Errorf("role_definition = %q, want the source's", dup.RoleDefinition)
	}
}

// Duplicating re-runs the home-jail: a source path the caller could no longer
// choose directly must not be resurrected through a copy.
func TestUserOpsDuplicateAgentRefusesOutOfJailSource(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-a", Username: "alice", WorkDir: t.TempDir()},
		{ID: "agent-a1", Name: "Legacy", OwnerID: "human-a", WorkDir: "/somewhere/else"},
	}
	ops := &UserOps{Store: ms}
	_, err := ops.DuplicateAgent(context.Background(), ms.Users[1], "human-a")
	wantUserOpsStatus(t, err, http.StatusForbidden, "work_dir must be inside your own working directory")
}

func TestUserOpsArchiveUnarchive(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-a", Name: "Alice", Username: "alice"},
		{ID: "agent-a1", Name: "Scout", OwnerID: "human-a"},
	}
	ops := &UserOps{Store: ms}
	ctx := context.Background()
	agentRow := ms.Users[1]

	if err := ops.Archive(ctx, agentRow); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if !ms.Users[1].Archived {
		t.Fatal("agent was not archived")
	}
	restored, err := ops.Unarchive(ctx, agentRow)
	if err != nil {
		t.Fatalf("Unarchive: %v", err)
	}
	if restored.Archived {
		t.Error("restored row still marked archived")
	}

	// Login rows are managed through the admin surface, not this one.
	wantUserOpsStatus(t, ops.Archive(ctx, ms.Users[0]), http.StatusBadRequest, "login users cannot be archived")
	// Archiving twice is a no-op in the store, which reports it as missing.
	if err := ops.Archive(ctx, agentRow); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	wantUserOpsStatus(t, ops.Archive(ctx, agentRow), http.StatusNotFound, "user not found")
}

func TestUserOpsDelete(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-a", Name: "Alice", Username: "alice"},
		{ID: "agent-a1", Name: "Scout", OwnerID: "human-a"},
	}
	ops := &UserOps{Store: ms}
	ctx := context.Background()

	wantUserOpsStatus(t, ops.Delete(ctx, ms.Users[0]), http.StatusBadRequest, "login users cannot be deleted from the agent API")
	if err := ops.Delete(ctx, ms.Users[1]); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	wantUserOpsStatus(t, ops.Delete(ctx, store.User{ID: "agent-a1"}), http.StatusNotFound, "user not found")
}

func TestUserOpsReorderReturnsVisibleUsers(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-a", Name: "Alice", Username: "alice"},
		{ID: "agent-a1", Name: "One", OwnerID: "human-a"},
		{ID: "agent-a2", Name: "Two", OwnerID: "human-a"},
		{ID: "agent-b1", Name: "Foreign", OwnerID: "human-b"},
	}
	ops := &UserOps{Store: ms}

	// The foreign id is part of the payload a malicious client could send; the
	// store must skip it rather than reordering somebody else's sidebar.
	users, err := ops.Reorder(context.Background(), "human-a", []string{"agent-a2", "agent-a1", "agent-b1"})
	if err != nil {
		t.Fatalf("Reorder: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("Reorder returned %+v, want the caller's two agents", users)
	}
	if ms.Users[3].SortOrder != 0 {
		t.Errorf("foreign agent was reordered: sort_order = %d", ms.Users[3].SortOrder)
	}
}

func TestUserOpsCallerOwnerID(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-a", Username: "alice"},
		{ID: "agent-a1", OwnerID: "human-a"},
	}
	ops := &UserOps{Store: ms}
	ctx := context.Background()

	if got, err := ops.CallerOwnerID(ctx, ""); err != nil || got != "" {
		t.Errorf("CallerOwnerID(unauthenticated) = %q, %v; want \"\", nil", got, err)
	}
	if got, err := ops.CallerOwnerID(ctx, "human-a"); err != nil || got != "human-a" {
		t.Errorf("CallerOwnerID(human) = %q, %v; want human-a, nil", got, err)
	}
	// An agent row resolves to the human behind it, not to itself.
	if got, err := ops.CallerOwnerID(ctx, "agent-a1"); err != nil || got != "human-a" {
		t.Errorf("CallerOwnerID(agent) = %q, %v; want human-a, nil", got, err)
	}
	_, err := ops.CallerOwnerID(ctx, "ghost")
	wantUserOpsStatus(t, err, http.StatusNotFound, "user not found")
}
