package service

import (
	"context"
	"log"
	"os"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// SandboxUnrestrictedFor reports whether runs owned by user may bypass the
// jail, applying the same rule the chat path uses: the tier is inherited from
// the owning human (a conversation/IM run executes under an agent row whose own
// sandbox_mode is always the jailed default), and admins always bypass because
// they already control the host the daemon runs on.
//
// Fail-safe in every direction — a nil store, an orphan agent, or a lookup
// error yields false, i.e. more isolation.
func SandboxUnrestrictedFor(ctx context.Context, st store.Store, user store.User) bool {
	if st == nil {
		return false
	}
	owner, err := st.GetOwner(ctx, user)
	if err != nil {
		// Orphan agent (empty/dangling owner_id) is the most common reason a
		// deployment "can't unjail": flipping the human to unrestricted has no
		// effect because the chain back to it is broken.
		log.Printf("[sandbox] owner lookup failed for user=%s (username=%q owner_id=%q): %v — keeping the jailed tier",
			user.ID, user.Username, user.OwnerID, err)
		return false
	}
	unrestricted := owner.IsAdmin || store.SandboxUnrestricted(owner.SandboxMode)
	log.Printf("[sandbox] user=%s (username=%q) owner=%q owner_admin=%t effective_mode=%q unrestricted=%t",
		user.ID, user.Username, owner.Username, owner.IsAdmin, owner.SandboxMode, unrestricted)
	return unrestricted
}

// runEnvFor is the self-service environment a run of user inherits: its
// owner's, because conversations and IM turns execute under an agent row whose
// stored copy may be stale or empty. A human is its own owner; a failed lookup
// falls back to the row's own value.
func runEnvFor(ctx context.Context, st store.Store, user store.User) string {
	if st == nil {
		return user.Env
	}
	if owner, err := st.GetOwner(ctx, user); err == nil {
		return owner.Env
	}
	return user.Env
}

// MintSessionID returns the session id a new session of backend should start
// with: a fresh one for a backend that takes the caller's id, "" for one that
// assigns its own (see agent.Capabilities.AssignsSessionID).
func MintSessionID(backend agent.Backend) string {
	if backend != nil && backend.Capabilities().AssignsSessionID {
		return ""
	}
	return uuid.New().String()
}

// PrepareRunSession records on opts which CLI session the turn continues and
// returns it. An empty sessionID starts a new session (MintSessionID). A resume
// is only claimed when the backend actually has the session's log on disk,
// probed under opts.ConfigDir — so opts must already carry the account.
func PrepareRunSession(backend agent.Backend, workDir, sessionID string, opts *agent.RunRequest) string {
	if sessionID == "" {
		sessionID = MintSessionID(backend)
	}
	opts.SessionID = sessionID
	opts.IsResume = sessionID != "" && backend != nil && backend.SessionExists(workDir, sessionID, opts.ConfigDir)
	return sessionID
}

// NewOneshotRunRequest assembles the account-, user- and sandbox-derived part
// of every agent RunRequest: browser chat and /compact (via
// PromptRunner.BuildRunOptions) and every IM turn. The run inherits the user's
// system prompt, tools and MCP config. Transport-specific parts — session, model, extra system prompts —
// are layered on by the caller (PrepareRunSession, AppendSystem).
//
// sandbox, jailRoot, unrestricted and the two runner watchdog timeouts are
// constructor inputs rather than fields the caller patches afterwards. That is
// deliberate: while they were optional, the IM bridge shipped without them, so
// `sandbox.enabled: true` only ever jailed the browser chat while every IM
// conversation ran bare in the user's real work_dir, and runner_max_silent_timeout was inert. Making them
// required arguments turns the same omission into a compile error for the next
// call site — an added parameter breaks every caller, an added struct field
// would not.
//
// The returned cleanup func removes the temporary MCP config file (if one was
// written) and must always be called by the caller, typically via defer.
func NewOneshotRunRequest(
	ctx context.Context,
	st store.Store,
	user store.User,
	resolved *config.Provider,
	cfg *config.Config,
	sandbox agent.Sandbox,
	jailRoot string,
	unrestricted bool,
) (agent.RunRequest, func()) {
	opts := agent.RunRequest{
		Sandbox:      sandbox,
		JailRoot:     jailRoot,
		Unrestricted: unrestricted,
	}
	if resolved != nil {
		opts.ConfigDir = resolved.ConfigDir
		opts.AccountEnv = resolved.Env
		applyProviderEnvToRequest(&opts, resolved)
	}
	mergeUserEnvIntoRequest(&opts, runEnvFor(ctx, st, user))
	opts.Spawner = agent.NewSpawner()
	if cfg != nil {
		// Zero would silently fall back to streamcommon.DefaultStallTimeout and
		// disable the max-silent check entirely (it is gated on > 0).
		opts.StallTimeout = cfg.RunnerStallTimeout.Duration
		opts.MaxSilentTimeout = cfg.RunnerMaxSilentTimeout.Duration
	}
	opts.SystemPrompt = BuildSystemPrompt(user)
	opts.ThinkLevel = user.ThinkLevel

	AttachAgentScopeBind(ctx, st, &opts, user)

	cleanup := func() {}
	if user.McpConfig != "" {
		if tmpPath, err := writeTempMcpConfig(user.McpConfig); err == nil {
			opts.McpConfigPath = tmpPath
			cleanup = func() { _ = os.Remove(tmpPath) }
		}
	}
	return opts, cleanup
}
