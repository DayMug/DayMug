# Agent backend abstraction

DayMug abstracts "how to run a code-CLI agent" into the `agent.Backend` interface (`backend/internal/agent/agent.go`). Handlers program only against the interface; which adapter is used is decided by the conversation's provider type and wired up in `buildBackends` in `handler/runtime.go`.

## Core contract (`internal/agent/`)

| Type | Purpose |
|------|------|
| `agent.Backend` | Unified interface: run a turn, probe whether a session exists, oneshot, etc. |
| `agent.RunRequest` | Neutral input contract (prompt, work_dir, session, model, tool allow/deny lists, MCP config…) |
| `agent.StreamEvent` + `Kind*` | Shared event alphabet: delta / tool_use / thinking / usage / error / system_init / queue_status … |
| `agent.Capabilities` | Self-reported capability matrix: whether the backend supports compaction, thinking streams, rate-limit events, cost reporting, and whether the backend assigns new session ids itself (`AssignsSessionID`, Codex family; callers decide whether to pre-generate an id via `service.PrepareRunSession` instead of checking the provider type) |
| `agent.UsageReport` | Typed payload of `KindUsage` (built with `NewUsageEvent`, read with `UsageOf`; on the wire `Content` is plain JSON). `Cumulative` is declared by the adapter: "are per_model / cost session-cumulative values". Claude Code (claude, claude-compatible, Agent SDK) is true, Codex is false; billing looks only at this flag, never at the provider name |

The capability matrix drives **capability gating** on both ends: `/api/models` passes the matrix per provider to the frontend, and the frontend's `useModelRegistry()` gates features on it (e.g. whether a mid-turn send can be inserted into the running task); the backend `/compact` endpoint rejects backends with `SupportsCompaction=false`. Frontend and backend fields are kept in sync by `internal/agent/agent_test.go` and `internal/handler/models_test.go`.

Besides the **hand-written** `Capabilities` matrix, there is a set of **derived** capability bits: `agent.OptionalCapabilitiesOf(b)`
answers "can this backend accept steering / answer questions". The answer is derived directly from whether the
optional `TurnSteeringBackend` / `UserQuestionBackend` interfaces are satisfied, so there is no second table that
must be maintained by hand and can drift from reality. The companion `SteererOf` / `QuestionResponderOf`
replace scattered bare type assertions. `internal/agent/optional_ext_test.go` pins the capability matrix of the four production adapters:
renaming `SteerTurn` causes no compile error; it just makes the interface silently stop being satisfied and steering disappears in production,
and this table is what catches it.

Shared primitives (not owned by any single adapter): `spawner.go` (spawning child processes), `sandbox.go` (isolation-layer seam), `binary.go` (CLI binary resolution), `probe.go` (startup probing), `streamcommon/emitter.go` (event-channel send contract), `residentpool` (the over-capacity LRU eviction rule shared by resident process pools: skip keys in use and busy instances; the rest of the mechanics in `codexapp/pool.go` and `claudeagentsdk/residency.go` (shared vs. exclusive, spawn, expiry policy, reclamation) are deliberately kept separate), `sessionlog` (session-log probing for Claude `projects/*.jsonl` and Codex rollouts; the log layout belongs to the provider client, not the transport, so Agent SDK / app-server use it directly to answer `SessionExists` / `SessionLogPath` instead of each holding a CLI Backend for that). bwrap identity binds first go through unified mount planning: ordinary subpaths already covered by a parent mount with the same permissions are not mounted again; a symlink visible in the parent tree is skipped only if its resolved target is also covered by a mount with the same permissions, otherwise a clear error is raised before the process starts. `/etc/resolv.conf` is resolved separately and its actual target file is mounted, which supports layouts such as systemd-resolved and NetworkManager and avoids mounting onto a symlink target again.

**Event self-consistency checks**: `StreamEvent.Validate()` (`internal/agent/validate.go`) checks that the Kind belongs to the closed set,
that structured payloads are valid JSON, and that `user_question_resolved` carries a request id. `Content` is an opaque string on this side,
so a tool_result mangled upstream would travel all the way to the browser and the messages table, and the user would just see a blank tool card, with no error and
no log pointing at the culprit adapter. The Emitter logs at most one warning per turn **but still lets the event through** (`ServerMessage` already degrades invalid JSON
to a plain string; dropping frames is worse than ugly rendering). `agenttest.LoadFrames` instead **validates on load**, so that a fixture recorded from a buggy
version does not end up defining what "correct" means. `TestKnownKindsCoversEveryConstant` parses `event.go` to catch new
Kind constants that were never registered.

**Record and replay**: `Record` / `LoadFrames` / `ReplayBackend` in `internal/agent/agenttest` (jsonl, one
`{at_ms, event}` per line). A hand-written fixture encodes "what someone thinks the provider will emit", which is exactly what gets
disproved when the provider upgrades; recording one real session replaces it with "what actually happened". Only `Realtime` sleeps according to `at_ms`; by default nothing sleeps.

**Stale-frame gate for Codex usage** (`codexapp/usage.go`): `tokenUsage.last` is the cost of a **single model request**,
and `tokenUsage.total` is the thread cumulative total. Measured: a turn with three requests reports three entries whose `last` values sum exactly to the increase in `total`,
so every entry must be emitted and summed downstream. The only thing to block is "this turn spent nothing and Codex re-reported last turn's numbers",
via two gates: the notification's own `turnId` does not match this turn, or the thread cumulative total has not moved since the turn started. The cumulative baseline lives on
`accountServer` and disappears when the pool reclaims the process, so it shares the thread's lifetime.

**Event channel contract**: adapters always write events to a turn's `outputCh` through `streamcommon.Emitter`, never with bare sends.
A send waits for whichever comes first: the consumer, the caller's ctx, or `DefaultSendTimeout` (30s). Errors are sticky
(later frames return the same error immediately instead of waiting out the timeout again). This constraint comes from codexapp: its pump loop
both forwards events and feeds the stall watchdog and `MaxSilentTimeout`, so if a bare send blocked, the very mechanism meant to detect the hang
would be blocked by that hang. **No frames are currently dropped**: `turnState.absorb` in `service.AgentStreamer` accumulates `KindDelta` into `reply` and
`KindThinkingDelta` into `thinking`, and both are persisted (a cancelled turn, or a turn with an empty result, stores
the accumulated `reply`), so dropping deltas would silently truncate history, not just the display. Enabling a "drop deltas under backpressure" mode requires
each message to end with an authoritative full-text frame for reconciliation; do not add it before then.

`think_level` is a conversation-level choice (default, low, medium, high, max) passed to the current backend on every turn via
`RunRequest.ThinkLevel`. If the Agent has a default model bound, a new web conversation copies that model's
matching `think_level` into the conversation; after that it can be adjusted independently in `/chat` and does not drift with the Agent
config. Claude uses `--effort`; Codex CLI uses `model_reasoning_effort` (max maps to
xhigh); Codex app-server and Claude Agent SDK use their own `effort` fields. An empty value passes no parameter,
preserving the model's or provider's own default, rather than DayMug forcing high.

## Choosing the transport

Admins choose the transport per **provider type** in "Settings → Providers & Models". It is stored in `agent.transports` in `app_settings` and takes effect on save (from the next turn), with no restart:

| type | Available transports (first is default) |
|------|------------------------------|
| `claude` | `agent-sdk`, `cli` (`claude -p`) |
| `codex` | `app-server`, `cli` (`codex exec --json`) |
| `claude-compatible` | `cli` only |
| `openai-compatible` | `app-server` only |

The choice is per type rather than per account because a session may land on any authorized account of that type. In the implementation, `buildBackends` registers one `agent.TransportSwitch` each for `claude` / `codex`, which delegates every call to the concrete adapter via `service.TransportFor`. Both transports write the same session log (Claude's `projects/*.jsonl`, Codex's rollout JSONL), so existing sessions can be resumed directly after switching. `AgentStreamer.runOnce` calls `agent.Resolve` once at the start of each turn, guaranteeing that execution, steering hooks, and log rollback within a turn all use the same adapter; optional-capability accessors such as `agent.SteererOf` also unwrap the switch first, so "can insert" is determined by the currently selected transport.

`/api/models` sends `transport` and `capabilities.supports_steering` for each type. The chat input uses this to decide the send button: when steering is available (Agent SDK / app-server) the default is "Insert into current task", with "Send after it finishes" in the dropdown menu; the CLI transport cannot accept input while running, so the send button only queues the message to run after the current task completes, and the insert option is not shown.

Before saving, `PUT /api/admin/transports` probes the target transport's runtime (SDK needs node + the npm package, Claude CLI needs `claude`, both Codex transports probe `codex app-server`) and refuses to save if it is missing; the startup preflight checks likewise follow the currently selected transport.

## Account check and login

Every account card in "Settings → Providers & Models" has "Check account" and "Log in" buttons, so account configuration, login, and verification all happen on one page. Both buttons are disabled while the row has unsaved changes, because they act on the settings saved on the server.

- **Check** (`POST /api/admin/providers/:name/check`, `handler/admin_account_check.go`) has two steps: first it queries login status with the account's own CLI (`claude auth status --json` / `codex login status`, with that account's config dir and env); if logged in (or a compatible type, which relies on an API key in env and has no status command), it then runs a read-only oneshot through the current transport's backend using the account's default model (first in the list), executed in a temporary directory, deleting the resulting session log afterwards. The second step catches problems the first cannot, such as expired tokens, models the account cannot run, or a missing transport runtime. Only one check per account runs at a time, and the run phase occupies one of that account's live slots (`Pool.EnterLive`).
- **Log in** reuses the admin terminal WebSocket (`login: true`); frontend logic is in `useAdminTerminalSession`, shared by the terminal page and the models page. Both use headless flows: Claude uses `claude auth login` (prints an OAuth URL; paste the code back), Codex uses `codex login --device-auth` (device code; the default `codex login` requires a browser that can reach the server's localhost callback). Compatible types offer no login. Closing the login terminal triggers an automatic re-check.

## Claude Agent SDK adapter (`agent/claudeagentsdk/`)

Default transport for `type: claude`. `agent/claudecli/` implements the `cli` transport and is also the chat transport for `claude-compatible`. Key points:

- **Module resolution happens in Go** (`module.go`): static candidate directories first, `npm root -g` as fallback. The result is passed to the bridge via `DAYMUG_CLAUDE_AGENT_SDK_MODULE`, and the containing `node_modules` is bind-mounted read-only into the sandbox. Global packages are not in any default bind, so without the mount there is no SDK inside the jail.
- **The bridge is written to disk content-addressed**: `/tmp/daymug-claude-agent-sdk/bridge-<hash>.mjs`. Each turn checks it exists and atomically rewrites it, so neither `/tmp` cleanup nor multiple versions on one host break it.
- **Title generation uses the same bridge** (`title.go`) in `minimal` mode: no settings / MCP / CLAUDE.md / tools loaded and no session file written, matching the token-saving flags of the CLI generator.
- **Live input while running**: while a task is running, the send button's default "Insert" (`steer: true`) uses the Agent SDK's recommended streaming-input mode; "Send after it finishes" in the dropdown goes to the persisted FIFO. The bridge keeps a stdin control channel, receives the user message while the task runs, and sends an ACK after writing it to the SDK's async input stream; DayMug clears the message's pending flag only after receiving the ACK. If the response has already ended, the input stream is closed, or the bridge write fails, the message stays in the existing FIFO queue. This capability is the SDK's dynamic message queueing, not an in-turn change like Codex `turn/steer`.
- **Structured questions**: web turns install a `canUseTool` callback for `AskUserQuestion`. The bridge normalizes the SDK's question into `KindUserQuestion`, waits for the WebSocket `question_response`, and writes the answers back into `updatedInput.answers` keyed by the original question text; multi-select answers are joined with commas per SDK convention. IM, oneshot, and other shared streams without an answering UI explicitly disable this capability and keep `AskUserQuestion` in disallowedTools, so turns don't hang forever waiting for a response nobody will give.
- **Resident processes are kept only for real background tasks**: an ordinary turn closes the SDK query immediately after `result`; only sessions that still have a background task stay resident, subject to a global count cap, auto-termination after 30 minutes of background-event silence, reclamation when no tasks remain, and a two-hour hard TTL. Silence termination persists a notice in the conversation so the task doesn't vanish unexplained. One SDK query is bound to one session and cwd and cannot be shared across sessions like Codex app-server; forcing that would mix session state, project instructions, and background tasks.
- The browser interactive terminal is a separate admin feature (`handler/admin_terminal.go` builds its own argv and launches bare `claude` / `codex` directly); it goes through no chat adapter and does not affect the chat transport.
- The actual executor is the Claude Code bundled with the npm package, versioned independently of the host's `claude` binary; the startup log prints which one is used.

## Codex app-server adapter (`agent/codexapp/`)

Default transport for `type: codex`: stdio JSON-RPC against a resident `codex app-server --listen stdio://`. `agent/codexcli/` (`codex exec --json`) implements the `cli` transport and also provides transcript import and title helper code.

- **The two sandbox naming schemes must not be mixed** (`sandbox.go`): the `sandbox` of `thread/start` / `thread/resume` is a kebab-case `SandboxMode` (`read-only` / `workspace-write` / `danger-full-access`), while `sandboxPolicy.type` of `turn/start` is a camelCase tagged union (`readOnly` / `externalSandbox` / `dangerFullAccess`). Passing the wrong one doesn't degrade; it returns `-32600` and the whole turn fails. When an outer bwrap is present: thread-level `danger-full-access` (so Codex doesn't nest its own sandbox) + turn-level `externalSandbox`. The enum values are generated by `codex app-server generate-json-schema`, and `sandbox_test.go` keeps a copy as a guard.
- **Process pool** (`pool.go`): when bwrap is not enabled (or the current user is unrestricted), key = account + account env + fixed pipe spawner, so different cwds of the same account share one app-server; only when bwrap actually applies are the sandbox type and jail root added to the key, preserving directory isolation. Idle instances are reclaimed after 3 minutes, idle instances beyond 16 are evicted LRU, and `agent.RegisterCloser` ensures they are closed on normal shutdown too.
- **Per-turn liveness**: `pumpTurn` shares the same `streamcommon.Watchdog` (stall + liveness probe + absolute cap) with the pipe-based adapters' `streamcommon.StreamLines`; on timeout it sends `turn/interrupt` and reports an error.
- **Steering while running**: after `turn/start` succeeds, an ordinary turn is registered as the active turn under the conversation id. Ordinary WebSocket input only enters the persisted FIFO; only "Insert" input with `steer: true` calls `turn/steer` after persisting (with `expectedTurnId` and the message id). On success the message goes straight into the current turn; if there is no active turn, the turn is not steerable (review / manual compact, etc.), or app-server rejects steering, the message falls back to the FIFO.
- **Structured questions**: initialization declares the experimental API, and dispatch allows only one server request, `item/tool/requestUserInput`. Web turns convert it into the unified `KindUserQuestion`; after the user submits, the reply `{answers:{questionId:{answers:[...]}}}` is sent with the original JSON-RPC id, and Codex resumes the same turn. Request ids are valid only within the current active turn; duplicate or expired answers are rejected. Other server requests still return `-32601`.
- **Event mapping** (`events.go`): `commandExecution` / `fileChange` / `mcpToolCall` / `webSearch` all become tool events (MCP keeps the `mcp__server__tool` naming), and `reasoning` maps to thinking. Translation of command cards (Bash) and usage frames (`usage` / `context_usage`) is not written here but shared with `codexcli` via `agent/codexcommon`: each transport only decodes its own wire format (camelCase JSON-RPC / snake_case exec stream) into `codexcommon.Command` / `codexcommon.TokenUsage`, so the same input yields byte-identical frames on both sides (same-named cases in `codexcli/testdata/stream.golden.json` and `codexapp/testdata/events.golden.json` cross-check each other). Command output delta frames (`item/commandExecution/outputDelta`) are not forwarded: the shared event alphabet has no corresponding frame, matching cli mode, and the full output arrives in `item/completed`.
- **Subscription backpressure**: each thread has a queue of depth 256; when full, only that turn fails (`errEventBacklogOverflow`), never blocking the shared dispatch loop.
- **Context usage**: `thread/tokenUsage/updated` carries `modelContextWindow`, from which `usageEvents` emits a real `context_usage`; the frontend still decides whether to display it based on the capability sent by `/api/models`.

## Compatible endpoints: `claude-compatible` / `openai-compatible`

These two types **add no new transport**; they point the two transports above at third-party API-compatible endpoints:
`claude-compatible` reuses **claudecli** (not the Agent SDK: the SDK bridge resolves the globally installed
npm package and offers less direct control over a custom base URL than the CLI), and `openai-compatible` reuses **codexapp**.
They therefore get exactly the same session / tool / steering pipeline as the first-party types, and stay separate in only three places:
user bindings, the model picker, and the **price table**.

A separate price table is central to this design, not fastidiousness. The `total_cost_usd` reported by Claude Code is computed from Anthropic's
catalog, and Codex reports no cost at all; third-party unit prices can differ from first-party by integer multiples, and model ids can collide
(a self-hosted gateway can perfectly well call its model `gpt-5.6-sol`). So the first parameter of both `ComputeCodexCost` /
`ComputeClaudeCost` is the provider: **tables are keyed by provider, rows by model id**;
missing either would bill someone else's usage to the wrong account. `claudecli.NewBackendForProvider` and
`codexapp.NewBackendForProvider` are where that provider is bound to the Backend instance. Adapters also derive their own name and `Capabilities` from the bound provider (`claude-compatible-cli` reports no
rate-limit events: those come from Anthropic subscription response headers, which third-party endpoints don't send), so wiring code no longer hand-writes capability matrices.

No type **ships a built-in model list** (see the model registry below). A compatible type's catalog depends on which endpoint the operator points it at, so stuffing in a few ids
would just be wrong. Therefore, after creating an account you must first fill in models under "Settings → Providers & Models", then fill in rates on the "Pricing" page;
until rates are filled in, every turn against that endpoint is billed at 0 (rather than guessing with first-party prices). The model rows on the pricing page are
read from the model registry precisely because otherwise a provider with no default prices could never get its first row edited.

Typical env (`claude-compatible`, using the Xiaomi MiMo endpoint as an example):

```yaml
env:
  ANTHROPIC_BASE_URL: https://llm-gateway.example.com/anthropic
  ANTHROPIC_AUTH_TOKEN: <API_KEY>
  ANTHROPIC_MODEL: mimo-v2.5-pro
  ANTHROPIC_DEFAULT_SONNET_MODEL: mimo-v2.5-pro
  ANTHROPIC_DEFAULT_OPUS_MODEL: mimo-v2.5-pro
  ANTHROPIC_DEFAULT_HAIKU_MODEL: mimo-v2.5
```

Typical env (`openai-compatible`):

```yaml
config_dir: /srv/daymug/codex-qwen   # Required for the Codex family; maps to CODEX_HOME
env:
  OPENAI_BASE_URL: https://dashscope.aliyuncs.com/compatible-mode/v1
  OPENAI_API_KEY: <API_KEY>
```

**Retired `mimo` type**: it was "claude-compatible with the base URL hard-coded into the type name" and is no longer a supported type;
`config.NormalizeProviders` rejects it like any unknown type. A startup migration (now folded into the baseline schema) rewrote
`conversations` / `bot_threads` / `user_provider_bindings` / price-table keys / `providers.accounts` to `claude-compatible`.
Rewriting rather than discarding was mandatory: once the provider becomes an unknown value, `backendFor` falls back to the default backend,
and that conversation continues on a different account with a different session log, looking as if a different model were answering.

**Don't hand-write string comparisons for family checks**: `config.IsClaudeFamily` / `config.IsCodexFamily`
(the frontend's `src/lib/providerTypes.ts` is the same table) answer questions like "which config dir to read, is there a `.claude`
config surface"; "can it assign its own session id" is answered by the backend's self-reported `Capabilities.AssignsSessionID`. Adding a bare `type == CLITypeCodex` comparison would quietly
send `openai-compatible` down the Claude branch.

The chat model picker and the summary model (auto titles, `/compact`) are not configured in YAML: they are stored per account in the model registry in `app_settings`, edited on the "Settings → Providers & Models" page, and take effect on save. DayMug ships no built-in model ids (`service.ProviderModels` keeps only the set of types, with empty lists): an account with nothing configured has no selectable models; list order is picker order, the first entry is the default model for new conversations, and the order can be adjusted on the page. Accounts with models configured must have a summary model filled in manually; `PUT /api/admin/models` rejects accounts missing one. The page only suggests reference model names as text (`MODEL_SUGGESTIONS` in `frontend/src/pages/settings/AdminProvidersEditor.vue`). Tests write fixed fixtures into `ProviderModels` via `service/modeltest`.

## Child process layout

Plain CLI adapters always use a PTY so that behavior depending on `isatty()` matches the interactive terminal. The Claude Agent SDK NDJSON bridge and the Codex app-server JSON-RPC always use separate pipes at their protocol boundaries, so terminal echo and merged stderr can't corrupt structured frames. This is not configurable; a legacy `runner_mode` key in config is ignored.

## Adding a backend adapter

1. Implement `agent.Backend` in `agent/<name>cli/` (`runner.go` + `stream.go`). Titles default to the backend's
   `RunOneshot`; if a full oneshot is too expensive (the Claude family loads all tools and MCP schemas), implement
   `agent.TitleBackend` to provide a dedicated lightweight call (see `claudecli/title.go`).
2. Fill in the self-reported `Capabilities` matrix and align frontend/backend fields in `agent_test.go` / `models_test.go`.
3. Wire it up in `buildBackends` in `handler/runtime.go`; it goes into `service.BackendRegistry`, and chat,
   /compact, cron, IM, and title generation all get their backend from this one registry.
4. Accept the new `type` in `SupportedCLITypes` and `NormalizeProviders()` in `internal/config/config.go`, and decide which family it belongs to (or neither).
5. If its cost is computed locally, register a key in `pricing.defaultPrices` (leave the table empty if there are no trustworthy defaults:
   a missing key makes `/api/admin/pricing/<type>` return 400 and operators can never enter prices).
6. The coverage test in `routes_codex_backend_test.go` requires `buildBackends` to register a backend for every `SupportedCLITypes`
   entry: missing wiring raises no error, it just makes that kind of conversation silently fall back to the default backend.
