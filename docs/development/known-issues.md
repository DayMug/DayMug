# Known issues

Open bugs and engineering debt that contributors should know about. Locations are given by file and symbol name; verify against current code before acting.

## Open issues

### Backend / ops

| Severity | Problem | Location |
|---|---|---|
| MED | Deleting a conversation/user does not stop a running agent: `ConversationOps.Delete` only soft-deletes + broadcasts, never calls `Broadcaster.CancelJob`, so the child runs to completion while holding an account slot | `service/conversation_ops.go` `Delete` |
| MED | Shutdown cannot wake tasks still waiting for a ticket in the pool queue (the process-group kill only covers tasks that already hold a ticket) | `cmd/server/cmd_serve.go` `forceStopAgents` comment |
| MED | A scheduled upgrade cannot be cancelled and waits indefinitely for an idle window, so it may restart hours after the admin has left | `handler/upgrade_admin.go` `waitForIdleThenDrain` |
| MED | Prompts abandoned after exceeding `maxPromptRecoveries` during crash recovery get neither an error row nor a broadcast; whether this causes duplicate replies is unverified | `store/store_message.go` `ResetProcessingToPending` |
| MED | Runners read `stderrBuf.String()` before `stderrWait()`: a data race, and the stderr tail/truncation marker never makes it into the error message | `claudecli/runner.go`, `codexcli/runner.go`, `streamcommon/exec.go` `DrainStderr` |
| LOW | The public shared-conversation endpoint (`GET /api/shared/conversations/:token`) loads the whole conversation and has no rate limiting | `handler/routes.go`, `ConversationHandler.GetShared` |
| LOW | Saves of model overrides and pricing are not serialized (transports already are); concurrent saves have a small chance of DB/memory inconsistency | `service/model_overrides.go` `SaveModelOverrides`, `agent/pricing` `Save` |
| LOW | With `server.addr: ""`, serve listens on :8080 but the `daymug upgrade` health check probes :8090 | `cmd/server/app.go`, `config.go`, `service/upgrader.go` |
| LOW | A wake-up turn retrying on a transient error attaches again (no current adapter has a trigger path) | `service/agent_stream_retry.go` `Run` |
| LOW | Relayed bot sessions run on `context.Background()`, so bot removal/hot reload cannot stop them | `imbridge/imbot_relay.go` |
| LOW | Child processes have zero resource limits (no rlimit/cgroup/`NODE_OPTIONS`), so the OOM killer does not prefer them | `agent/spawner.go` |
| LOW | `IMBridge.threadLocks` only grows, never shrinks (fine over months, not over years) | `imbridge/imbot_bridge.go` |
| LOW | Subagent frames enter replay but never trigger `ResetReplay` (a byte cap already bounds it) | `service/agent_stream_turn.go` |
| LOW | `/compact` registers a no-op cancel, so pressing stop during compaction reports success while the room stays busy (up to 5 minutes) | `service/compact.go` (`Admit` is not passed `Cancel`) |

### Frontend

| Severity | Problem | Location |
|---|---|---|
| MED | Large workspace directories are not virtualized; a `node_modules`-sized directory freezes the panel (chat history is already mitigated with `content-visibility`) | `components/workspace/WorkspaceFileGrid.vue`, `WorkspaceFileTree.vue` |
| LOW | While an upgrade is in `waiting_for_conversations`, it falsely reports "Upgrade timed out" after 3 minutes | `pages/settings/AdminGlobalUpgrade.vue` `startPolling` |
| LOW | After `ResetSession`, peer tabs already open keep a stale token bar until the next turn (needs a new "bar cleared" push) | `service/conversation_ops.go` `ResetSession` comment |

### Engineering debt

- **gocritic runs with default checks only.** Enabling the `style` and `performance` tags is the next step: disable `hugeParam` / `rangeValCopy` / `unnamedResult` with documented reasons, fix the rest, and keep `whyNoLint`. When measuring, set `max-issues-per-linter` / `max-same-issues` to 0, or the counts get truncated.
- **`UserOps.UpdateAgent` builds a new struct**: adding a column to the agent table but forgetting to add it to `AgentParams` silently drops that column on every update, with no guard.
- **The fake's `ListConversations` does not filter by userID**, so privilege-escalation regressions go undetected in handler tests (adding the filter breaks tests whose fixtures don't set UserID).
- **Store state is not reset on logout.** It is currently clean only because `LoginPage` does a hard reload; `resetAllStores()` is never called in production, so making login an in-SPA navigation would leak the previous user's state.
- Consolidating the responsibilities of `FilePreview.vue` and `workspace/WorkspacePreview.vue` has not been evaluated (the latter's `probeRun` pattern is a useful reference).

## Memory sizing

The `daymug serve` process itself is small; memory is dominated by agent child processes. Each concurrent agent session (the CLI or SDK runtime plus its MCP servers) can take roughly 1 GB. Size the sum of `providers[*].max_concurrent` to the host's RAM, since child processes currently run without resource limits (see above).
