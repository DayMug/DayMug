# Multi-user model

## Human users

Authenticate with `username` + `password_hash` (or via OIDC if enabled). At every startup the server reads `admin.bootstrap_usernames` from YAML and flips `is_admin=true` on matching rows; this is the recovery hatch if every admin gets demoted.

## Agents

Rows in `agents` with no `username`; they are the only personas/conversation owners. Each Agent may own zero or more rows in `bots`. Bot rows only hold the IM connection (Slack / Feishu (Lark) / Telegram / WeChat) and channel rules; every attached Bot shares the Agent's prompt, permissions, working directory, model, and web-conversation identity via `agent_id`.

## Path sandbox

Every file-API handler resolves paths relative to the selected Agent's `work_dir`. For an Agent, reads and writes may follow links or `..` paths anywhere inside its owning human user's `work_dir`; paths outside that owner boundary are rejected after symlink resolution. Human-user file routes use their own `work_dir` as both the relative base and the boundary. The directory picker (`/api/browse-dirs` and its mkdir) is confined to the caller's own `work_dir` for non-admin users. Admins open at their own `work_dir` but may browse and create folders anywhere on the host, because they assign other users' `work_dir`s (which can live anywhere) and already hold a host shell via the admin terminal. Paths reported by an Agent are displayed verbatim in chat.

## Per-user provider bindings

A user is granted a *set* of accounts per provider type via `user_provider_bindings` (PK `(user_id, provider_type, provider_name)`), with exactly one row per type flagged `is_default=1`. So a user can hold several accounts of one type (e.g. two separate `claude` logins) side by side; a third-party Anthropic- or OpenAI-compatible endpoint is its own `claude-compatible` / `openai-compatible` type with its own bindings.

A conversation may **pin** any account from its type's allowed set via `conversations.account_name`; unpinned conversations, the stateless API, and auto-title runs resolve to the type's default. The dispatcher (`service.ResolveConversationAccount`) re-validates a pinned account against the user's *current* allowed set before each turn, so revoking a binding takes effect even on conversations that already pinned it — there is no implicit fallback to a shared default, which would funnel every unbound user onto one account and trip its rate limit.

**Round-robin account assignment for IM Bots.** A Bot pins one default model. If every turn went to the single `is_default` account, the Agent's other accounts offering the same model would sit idle while the default account queued up and then got rate-limited. So the IM path (`imbridge.IMBridge.resolveRunAccount`) round-robins **new threads** across accounts that are in the authorized set and whose `models` actually include this turn's model (`service.AccountRotator` + `service.EligibleAccountsForModel`), skipping accounts whose rate-limit cooldown for that model is active.

Rotation happens only when a new session starts: the chosen account is written to `conversations.account_name` immediately, and every later turn in that thread returns to the same account — the CLI session lives in that account's config dir, so switching accounts mid-thread would lose the resumable session. Split points such as session-window expiry, `/new`, and case-mode rotation clear the thread binding, so the next turn joins the rotation again. When the authorized set is empty or no account offers the model, it falls back to the default binding.

## Account pool

Per-account concurrency is enforced by `service.Pool` (file: `service/account_pool.go`). When a user's account is at capacity the WebSocket sees `queue_status` events containing the task's one-based waiting position, the total tasks ahead, and how many of those are currently running. Active tasks are excluded from the waiting position, so the fifth task on a four-slot account reports position 1, four tasks ahead, and four running. At each handoff, the runnable user with the least cumulative execution time during the previous hour goes first; active tasks count up to the handoff instant and enqueue order breaks ties. Cancelling removes the ticket cleanly.

A turn that stops to ask the user something (AskUserQuestion) hands its slot back for the duration of the question — see `service/turn_slot_gate.go`. The human may take minutes or hours, and holding a slot through that starves everyone else on the account. The drainer job moves to `JobStatusWaiting`, so operator views and the conversation-activity snapshot stop counting it as running work (it still counts toward `InFlight`: the child process is alive and a drain must wait for it). Submitting the answer re-enters the queue exactly like a fresh prompt, so it resumes behind whoever started while the question was on screen. The websocket acknowledges the answer immediately and delivers it once the slot is granted; if the queue never frees one, the turn is cancelled with the pool's error rather than left blocked.

The same pool gates all four provider types (`claude` / `codex` / `claude-compatible` / `openai-compatible`). Config validation requires account names to be globally unique across all provider types, while `Pool.AccountForType(name, providerType)` still carries the type explicitly as a defensive routing boundary.

## Per-user environment

Each human configures newline-delimited `VAR=VAL` entries from Settings → Advanced. The values are stored on the human row, inherited by every owned agent, and override same-name provider-account env values at process launch. Admin user-management endpoints neither accept nor expose this field.

## Sandbox

`agent.Sandbox` (file: `agent/sandbox.go`) is the seam that wraps each backend invocation in an isolation layer. Two implementations ship: the no-op pass-through (default) and `bwrap` (`sandbox.type: bwrap`, see [Configuration](../guides/configuration.md)). With the no-op the spawned CLI inherits whatever filesystem reach the server user has — `safePath()` only governs the HTTP API — so treat the server as single-tenant (or front it with container-level isolation) unless bwrap is enabled.

The sandbox applies to both entry points that spawn an agent: browser chat and every IM (Slack / Feishu (Lark) / Telegram / WeChat) turn. The jail tier is inherited from the human who owns the agent, and admins always run unwrapped — the same rule on every surface, so an IM turn is never stricter or looser than the same person's browser turn. If `bwrap` is not on PATH the server does **not** refuse to start; it logs a `SECURITY:` line and runs with no isolation at all.
