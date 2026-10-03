package service

import (
	"context"
	"log"
	"strings"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// UserOps owns the agent-row write use-cases behind /api/users — create,
// update, duplicate, archive/unarchive, reorder, delete — plus the ownership
// filter that decides which rows a caller may see at all.
//
// Like PromptRunner it is a bare struct with public fields and no constructor:
// the handler assembles one per call from its own fields, which routes.go wires
// *after* NewUserHandler returns. A snapshot taken at construction time would
// permanently capture the nil Cfg/Store of that moment.
//
// What deliberately stays outside this type: the IM-connector reload that
// follows most of these writes. That is side-effect orchestration over the
// imbot manager, not part of the user domain, so it remains in the handler.
type UserOps struct {
	Store store.Store
	// Cfg is the live server config, consulted only to validate
	// default_model against the configured providers. nil-safe: a nil Cfg
	// skips that validation, which is the pre-existing handler contract
	// (unit tests construct the handler without a config).
	Cfg *config.Config
}

// AgentParams is the mutable field set of an agent row as supplied by a
// create/update call. It is the service-side mirror of the handler's JSON
// request body: binding tags, and therefore the wire format, stay at the
// transport boundary and the handler maps field by field.
type AgentParams struct {
	Name            string
	WorkDir         string
	Avatar          string
	RoleDefinition  string
	McpConfig       string
	ClaudeMdContent string
	ManageClaudeMd  bool
	DefaultModel    string
	ThinkLevel      string
	CaseMode        bool
}

// CreateDefaultAgent provisions the first chattable persona for a newly
// created human. All human-provisioning entry points call this helper so an
// account is never born with an empty agent picker.
func CreateDefaultAgent(ctx context.Context, db store.Store, human store.User) (store.User, error) {
	return (&UserOps{Store: db}).CreateAgent(ctx, AgentParams{
		Name:    human.Username,
		WorkDir: human.WorkDir,
	}, human.ID)
}

// NormalizeThinkLevel validates the reasoning-effort vocabulary shared by
// Agent defaults and per-conversation overrides. Empty explicitly means the
// provider default.
func NormalizeThinkLevel(level string) (string, error) {
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case "", "low", "medium", "high", "max":
		return level, nil
	default:
		return "", BadRequest("think_level must be one of: low, medium, high, max")
	}
}

// NormalizeDefaultModel trims the requested default model and verifies it
// resolves to a configured provider. Returns the value to persist.
//
// A nil Cfg (unit tests, and any deployment without provider config) skips the
// check rather than rejecting every model — matching the handler behaviour this
// replaces.
func (o *UserOps) NormalizeDefaultModel(model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" || o.Cfg == nil {
		return model, nil
	}
	if ProviderForModelForConfig(o.Cfg, model) == "" {
		return "", BadRequest("unknown model " + model)
	}
	return model, nil
}

// EnsureWorkDirWithinCaller verifies workDir lies inside the authenticated
// caller's own work_dir. Agents created or moved through the /api/users surface
// inherit their owner's filesystem reach; without this check an admin could
// point a fresh agent at any host path.
//
// authedUID == "" means no auth middleware ran (unit tests exercising a handler
// in isolation) and skips the check — the same bypass convention CanAccessOwner
// uses.
func (o *UserOps) EnsureWorkDirWithinCaller(ctx context.Context, authedUID, workDir string) error {
	if authedUID == "" {
		return nil
	}
	if o.Store == nil {
		return Internal("store unavailable", nil)
	}
	caller, err := o.Store.GetUser(ctx, authedUID)
	if err != nil {
		// Deliberately not StoreError: a missing caller row here has always
		// surfaced as 500 with the raw store message, never as 404.
		return Internal(err.Error(), err)
	}
	if caller.WorkDir == "" {
		return BadRequest("your account has no work_dir; ask an admin to set one before creating agents")
	}
	within, err := PathWithin(caller.WorkDir, workDir)
	// Fails closed: an unresolvable path (dangling link, symlink loop, denied
	// parent) counts as outside.
	if err != nil || !within {
		return Forbidden("work_dir must be inside your own working directory")
	}
	return nil
}

// CallerOwnerID resolves the human-owner id of the authenticated caller. Empty
// when no auth middleware ran (unit tests).
func (o *UserOps) CallerOwnerID(ctx context.Context, authedUID string) (string, error) {
	if authedUID == "" {
		return "", nil
	}
	authed, err := o.Store.GetUser(ctx, authedUID)
	if err != nil {
		return "", StoreError(err, "user not found")
	}
	return authed.Owner(), nil
}

// VisibleUsers returns the rows the caller may act on and which the sidebar
// renders: every non-archived agent they own. The caller's own human self-row
// is deliberately excluded — a login account is not a chattable persona, so it
// does not appear in the agent picker (see the account/agent split). Archived
// agents are excluded too; the settings page lists those via ListArchivedAgents.
// Never nil.
//
// authedUID == "" returns the unfiltered list, the same auth-bypass convention
// used across this package for handler unit tests.
//
// This is the single implementation of the visibility rule. It previously
// existed twice — UserHandler.ownedUsers keyed the filter on the authenticated
// id, the /app-state aggregate keyed it on the loaded caller row's Owner(). The
// two agree for every reachable input: an authenticated principal is always a
// row with a username (sessions are minted by password login/OIDC), and for
// such a row Owner() == ID == authedUID.
// They could only diverge for a principal that is itself a username-less agent
// row, which no auth path can produce.
func (o *UserOps) VisibleUsers(ctx context.Context, authedUID string) ([]store.User, error) {
	users, err := o.Store.ListUsers(ctx)
	if err != nil {
		return nil, Internal(err.Error(), err)
	}
	if authedUID != "" {
		// Allocate a fresh slice rather than filtering in place (users[:0]):
		// some Store implementations hand back a slice that aliases internal
		// state, and an in-place filter would corrupt it for later reads on
		// this same request.
		filtered := make([]store.User, 0, len(users))
		for _, u := range users {
			// Keep only the caller's own agents: agent rows (empty username)
			// whose owner_id points back at the caller. Other humans and
			// orphan agents resolve elsewhere / to "" and drop out.
			if u.Username == "" && u.Owner() == authedUID && !u.Archived {
				filtered = append(filtered, u)
			}
		}
		users = filtered
	}
	if users == nil {
		users = []store.User{}
	}
	return users, nil
}

// CreateAgent mints a new agent row owned by the creator's human owner and
// returns it as persisted.
//
// creatorUID is the caller resolved at the transport boundary; "" means "no
// auth context" and leaves the row unowned, which only happens in unit tests
// that exercise the handler without middleware — production routes are all
// behind RequireAuth.
func (o *UserOps) CreateAgent(ctx context.Context, p AgentParams, creatorUID string) (store.User, error) {
	if p.Name == "" {
		return store.User{}, BadRequest("name is required")
	}
	if p.WorkDir == "" {
		return store.User{}, BadRequest("work_dir is required")
	}
	defaultModel, err := o.NormalizeDefaultModel(p.DefaultModel)
	if err != nil {
		return store.User{}, err
	}
	thinkLevel, err := NormalizeThinkLevel(p.ThinkLevel)
	if err != nil {
		return store.User{}, err
	}
	// Agents inherit the human owner's filesystem reach. Refuse to create one
	// whose work_dir escapes the caller's own home, otherwise the resulting
	// agent process would have access we never granted.
	if err := o.EnsureWorkDirWithinCaller(ctx, creatorUID, p.WorkDir); err != nil {
		return store.User{}, err
	}

	// Stamp the new agent with the creator's owner id so it's permanently
	// bound to that human (CanAccessOwner / VisibleUsers both gate on the
	// owner_id pointer). The creator is always a human login row, so its
	// Owner() is its own id.
	ownerID := ""
	if creatorUID != "" {
		authed, err := o.Store.GetUser(ctx, creatorUID)
		if err != nil {
			return store.User{}, StoreError(err, "creator not found")
		}
		ownerID = authed.Owner()
	}

	user := store.User{
		ID:              uuid.New().String(),
		Name:            p.Name,
		OwnerID:         ownerID,
		WorkDir:         p.WorkDir,
		Avatar:          p.Avatar,
		RoleDefinition:  p.RoleDefinition,
		McpConfig:       p.McpConfig,
		ClaudeMdContent: p.ClaudeMdContent,
		ManageClaudeMd:  p.ManageClaudeMd,
		DefaultModel:    defaultModel,
		ThinkLevel:      thinkLevel,
		CaseMode:        p.CaseMode,
	}
	if err := o.Store.CreateUser(ctx, user); err != nil {
		return store.User{}, Internal(err.Error(), err)
	}
	created, err := o.Store.GetUser(ctx, user.ID)
	if err != nil {
		return store.User{}, Internal(err.Error(), err)
	}
	return created, nil
}

// UpdateAgent applies an edit to an existing row and returns it as persisted.
// target is the row the transport layer already loaded and access-checked, so
// this method never re-reads it just to learn whether it is a human or an agent.
//
// TODO(tx): the row update and the work_dir move are two separate store writes
// with no transaction around them, exactly as before this moved out of the
// handler. A failure between them leaves the row updated and the work_dir stale.
func (o *UserOps) UpdateAgent(ctx context.Context, target store.User, p AgentParams, authedUID string) (store.User, error) {
	if p.Name == "" {
		return store.User{}, BadRequest("name is required")
	}
	// Agents (User rows with no username) carry their own work_dir field;
	// humans don't change theirs through this endpoint (admin-only).
	isAgent := target.Username == ""
	if isAgent && p.WorkDir == "" {
		return store.User{}, BadRequest("work_dir is required")
	}
	defaultModel, err := o.NormalizeDefaultModel(p.DefaultModel)
	if err != nil {
		return store.User{}, err
	}
	thinkLevel, err := NormalizeThinkLevel(p.ThinkLevel)
	if err != nil {
		return store.User{}, err
	}
	// Only enforce the home-jail when the agent's work_dir actually changes.
	// Older rows created before this check landed may sit outside the caller's
	// home; we shouldn't block name/persona edits on those just because the
	// saved path is now out-of-bounds. New values must always pass.
	workDirChanged := isAgent && p.WorkDir != target.WorkDir
	if workDirChanged {
		if err := o.EnsureWorkDirWithinCaller(ctx, authedUID, p.WorkDir); err != nil {
			return store.User{}, err
		}
	}

	user := store.User{
		ID: target.ID,
		// Carry the owner pointer so store.UpdateUser's isAgentRow() routes
		// the write to the agents table. Without it a migrated agent (whose
		// legacy users row is soft-deleted) matches zero rows and the update
		// fails with ErrNotFound — i.e. renames silently do nothing.
		OwnerID:         target.OwnerID,
		Name:            p.Name,
		Avatar:          p.Avatar,
		RoleDefinition:  p.RoleDefinition,
		McpConfig:       p.McpConfig,
		ClaudeMdContent: p.ClaudeMdContent,
		ManageClaudeMd:  p.ManageClaudeMd,
		DefaultModel:    defaultModel,
		ThinkLevel:      thinkLevel,
		CaseMode:        p.CaseMode,
	}

	if err := o.Store.UpdateUser(ctx, user); err != nil {
		return store.User{}, StoreError(err, "user not found")
	}
	if workDirChanged {
		if err := o.Store.SetUserWorkDir(ctx, target.ID, p.WorkDir); err != nil {
			return store.User{}, Internal(err.Error(), err)
		}
	}

	// The disk sync trails the store writes on purpose. Running it first meant a
	// rejected save still left a rewritten CLAUDE.md on disk, advertising config
	// the row never received — the on-disk file is what the agent process
	// actually reads, so that skew is worse than a stale file.
	//
	// The reverse failure stays best-effort: the row is the source of truth for
	// claude_md_content and there is no transaction to roll it back, so a 500
	// here would tell the caller "nothing was saved" about a committed write.
	// Log it instead; re-saving (or PUT /claude-md) retries.
	if isAgent && p.ManageClaudeMd && p.ClaudeMdContent != "" && p.WorkDir != "" {
		if err := SyncClaudeMd(p.WorkDir, p.ClaudeMdContent); err != nil {
			log.Printf("[user] sync CLAUDE.md agent=%s dir=%s: %v", target.ID, p.WorkDir, err)
		}
	}

	updated, err := o.Store.GetUser(ctx, target.ID)
	if err != nil {
		return store.User{}, Internal(err.Error(), err)
	}
	return updated, nil
}

// DuplicateAgent copies an existing agent (all config fields) into a new row
// with a fresh UUID. The display name gets a " (copy)" suffix; everything else —
// work_dir, role, MCP config, CLAUDE.md content — is mirrored verbatim.
// Username/password are intentionally NOT copied: a duplicated agent has no
// login until an admin sets credentials. BarkURL is not copied either —
// notification config lives on the human owner now.
//
// Returns the row as constructed, not as re-read: the caller has always been
// answered with the local value.
func (o *UserOps) DuplicateAgent(ctx context.Context, src store.User, authedUID string) (store.User, error) {
	// Duplicating mints a fresh row, so the same home-jail that gates create
	// applies. If the source's saved work_dir is no longer within the caller's
	// home (e.g. set under a previous lax regime) the copy is refused —
	// duplicating shouldn't quietly resurrect a path the caller can't choose
	// directly.
	if err := o.EnsureWorkDirWithinCaller(ctx, authedUID, src.WorkDir); err != nil {
		return store.User{}, err
	}

	dup := store.User{
		ID:   uuid.New().String(),
		Name: src.Name + " (copy)",
		// owner_id carries the agent's owner pointer (the human creator's id).
		// Preserve it verbatim so an admin duplicating another user's agent
		// doesn't silently re-home the copy under the admin — the duplicate
		// stays owned by the same human as the source.
		OwnerID:         src.OwnerID,
		WorkDir:         src.WorkDir,
		Avatar:          src.Avatar,
		RoleDefinition:  src.RoleDefinition,
		McpConfig:       src.McpConfig,
		ClaudeMdContent: src.ClaudeMdContent,
		ManageClaudeMd:  src.ManageClaudeMd,
		DefaultModel:    src.DefaultModel,
		ThinkLevel:      src.ThinkLevel,
		CaseMode:        src.CaseMode,
	}
	if err := o.Store.CreateUser(ctx, dup); err != nil {
		return store.User{}, Internal(err.Error(), err)
	}
	return dup, nil
}

// Archive hides one of the caller's agents from the sidebar. Login users
// (humans) cannot be archived through this use case.
func (o *UserOps) Archive(ctx context.Context, target store.User) error {
	if target.Username != "" {
		return BadRequest("login users cannot be archived")
	}
	if err := o.Store.ArchiveUser(ctx, target.ID); err != nil {
		return StoreError(err, "user not found")
	}
	return nil
}

// Unarchive restores a previously archived agent to the sidebar and returns the
// refreshed row.
func (o *UserOps) Unarchive(ctx context.Context, target store.User) (store.User, error) {
	if err := o.Store.UnarchiveUser(ctx, target.ID); err != nil {
		return store.User{}, StoreError(err, "user not found")
	}
	updated, err := o.Store.GetUser(ctx, target.ID)
	if err != nil {
		return store.User{}, Internal(err.Error(), err)
	}
	return updated, nil
}

// Reorder persists the manual sidebar order of the caller's rows from a
// drag-to-reorder gesture. ids is the full ordered list — the caller's own human
// row plus its agents, freely interleaved; the store treats it as authoritative
// for the caller's rows and skips any id the caller doesn't own. Returns the
// refreshed visible list so the client can re-render from one response.
//
// TODO(tx): the reorder write and the read-back are not one transaction; a
// concurrent edit can make the returned list disagree with what was just
// written. Pre-existing behaviour, unchanged by the move into this package.
func (o *UserOps) Reorder(ctx context.Context, authedUID string, ids []string) ([]store.User, error) {
	ownerID, err := o.CallerOwnerID(ctx, authedUID)
	if err != nil {
		return nil, err
	}
	if err := o.Store.ReorderAgents(ctx, ownerID, ids); err != nil {
		return nil, Internal(err.Error(), err)
	}
	users, err := o.VisibleUsers(ctx, authedUID)
	if err != nil {
		return nil, err
	}
	return users, nil
}

// Delete removes an agent row for good. Login users (humans) are refused: they
// are managed through the admin surface, not the agent API.
func (o *UserOps) Delete(ctx context.Context, target store.User) error {
	if target.Username != "" {
		return BadRequest("login users cannot be deleted from the agent API")
	}
	if err := o.Store.DeleteUser(ctx, target.ID); err != nil {
		return StoreError(err, "user not found")
	}
	return nil
}
