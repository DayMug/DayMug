# Working directory mechanism

This document explains how the working directory (cwd) works in DayMug and how it interacts with each backend's session storage.

## Backend session storage conventions

Each `agent.Backend` implements its own on-disk session log layout; the handler is unaware of the details:

| Backend | Session log path | Encoding rule |
| --- | --- | --- |
| Claude (same layout for both the Agent SDK and CLI transports) | `<CLAUDE_CONFIG_DIR>/projects/<encoded-cwd>/<session-uuid>.jsonl` | `sessionlog.ClaudeProjectsEncoder` (`internal/agent/sessionlog`): replaces every `[^A-Za-z0-9-]` character in the absolute path with `-` |
| Codex (same layout for both the app-server and `codex exec` transports) | `<CODEX_HOME>/sessions/YYYY/MM/DD/rollout-*-<session-uuid>.jsonl` | Independent of cwd; organized by date, with the session id as the filename suffix |

claude's `--resume` fails when it cannot find the jsonl under the encoded directory, so **once a conversation's cwd is locked it must never drift**. codex looks sessions up by date + session id regardless of cwd, so the same session can be resumed from any cwd.

## The working-directory chain in DayMug

Each `conversation` has a `work_dir` field (`backend/internal/store/models.go`). It ultimately determines `cmd.Dir` when the backend spawns the child process.

### Three sources (by priority)

1. **`conversations.work_dir`** (highest; locked once set)
2. **`users.work_dir`** (per-user default)
3. **The backend process's cwd** (`os.Getwd()`, last resort)

### On conversation creation

`POST /api/conversations` (`service.ConversationOps.Create`): the owner's current `users.work_dir` is always copied into `conversations.work_dir`. An Agent's working directory is fixed: the create request takes no `work_dir`, and there is no API for repointing a conversation at another directory. If the owner cannot be read, creation is rejected outright rather than persisting a conversation with an empty `work_dir`. If the owner has no working directory set, `conversations.work_dir` is still an empty string.

### On sending a message

WS `input` first enters a FIFO; when this prompt's turn comes, in `backend/internal/service/prompt_runner.go`:

1. Call `PromptRunner.LockConversationWorkDir(ctx, convID, user.WorkDir)` (`/compact` goes through `service/compact.go`, same function):
   - If `conv.WorkDir` is already non-empty → use it as the cwd for this and all future messages
   - If `conv.WorkDir` is still empty → **persist** the fallback (the `user.WorkDir` read from the DB right now, or `os.Getwd()` if that is empty too) to the DB, locking the conversation to that cwd
2. `applySessionToRequest` decides between resume and a new session (see below)
3. Call `agent.Backend.RunWithSession(ctx, prompt, workDir, req, ch)` with the locked workdir

### `--session-id` or `--resume`?

`applySessionToRequest` (`service/prompt_runner.go`) → `PrepareRunSession` (`service/oneshot.go`) decides:

- Call `SessionExists(workDir, sessionID, configDir)` on the backend running this turn to probe whether the current account's session directory holds a log for this session
- Yes → `IsResume=true` → claude resumes (CLI `--resume` / Agent SDK `resume`), codex uses `exec resume` or the app-server's `thread/resume`
- No → treated as the first run; claude starts a new session with the session id on the conversation row; backends with `Capabilities.AssignsSessionID` (codex) get an empty id from `MintSessionID`, let the CLI assign one, and report it back via `system_init`

Messages with the `error` role do not count as "successful responses", but the probe is based on **files on disk**, not DB message state: claude writes the jsonl the moment it receives stdin, before the model replies, so a failed first request still leaves a log and the next one must use `--resume`, or it hits "Session ID is already in use".

## Why the cwd is locked: drift breaks --resume

### Failure scenario without the lock (claude only — codex does not depend on cwd)

1. The user creates a conversation **before** configuring `users.work_dir` → `conversations.work_dir = ""`
2. On the first message, `state.userWorkDir = user.WorkDir` (still empty), so `claude` runs in the backend process's cwd and the session is written there
3. The user **later** sets `users.work_dir = "/some/path"`
4. Browser refresh or reconnect → the WebSocket re-runs `init`, and this time `state.userWorkDir = "/some/path"`
5. Second message → `claude --resume <UUID>` runs in `/some/path` → session not found → error

### The lock

`LockConversationWorkDir` checks `conv.WorkDir` before every message:

- On the first message it writes the cwd actually used back to `conv.WorkDir`
- Every later message reads `conv.WorkDir` straight from the DB instead of the `state.userWorkDir` cached on the WS connection

So however the user later changes `users.work_dir` or reconnects the WS, the conversation's cwd stays pinned to the moment of the first message.

Test coverage:

- `TestTerminalHandler_LocksConversationWorkDirOnFirstMessage`
- `TestTerminalHandler_PreservesLockedWorkDirAcrossUserChange`

### What about conversations already broken?

The lock only applies to **newly sent messages**. If a conversation's session log is ever stranded under a different cwd:

- Clear context (`POST /api/conversations/:id/clear-context`, `ResetSession`) → rotates `session_id`; the next new message starts a new session and locks the cwd. Note that "clear messages" (`DELETE …/messages`) only deletes message rows and does **not** rotate the session id, so it cannot rescue such a conversation
- Or delete the conversation and recreate it

## Files DayMug manages are not kept in the working directory

The working directory is **the user's project**; DayMug does not put anything in it. Web uploads, inbound IM attachments, and case files all land in a per-agent scope under the
**owner's home directory**:

```
<owner.work_dir>/.daymug/agents/<agent id>/
    uploads/           # web composer uploads
    slack/ feishu/ im/ # inbound IM attachments, split by source platform
    case/              # case files <channel>-<thread>.md + .version
```

Two trade-offs:

- **Under the home directory, not the conversation cwd.** Otherwise every project the user opens a conversation in would gain a `.daymug/` —
  a few screenshots, plus permanent noise in `git status`. The owner's home directory is already the auth boundary of the file API
  (`handler.getUserFileAccess`), and `conversations.work_dir` is forced to stay inside it, so moving the files up
  grants no extra permissions.
- **One directory per agent id.** All Agents owned by the same person share one home directory, so the id is the only layer
  separating them (this also keeps Agents that share a `work_dir` apart). The id is used rather than the name because a rename would leave paths already recorded in past messages pointing at an old directory
  nobody writes to anymore.

Two hard constraints follow:

- **Paths handed to the Agent must be absolute.** The Agent's cwd is some project directory and the scope sits beside it, so no relative
  form works. `POST /api/uploads` therefore returns two paths: `ref` (absolute, inserted into the message body by the composer) and
  `path` (relative to the home directory, used by the file-read API); the user id in the read URL is the **owner**, not the agent.
  `ref` goes into the body so the Agent can open the file, but it is **not shown in the bubble**: when `ChatMessageItem` renders a user message
  it strips refs off the end one by one using that message's own `attachments[].path` (`ref` always ends with `/<path>`, so this is an
  exact match, not a shape match; if it doesn't match, the whole text is kept as-is). The attachment strip already shows the name and thumbnail, so printing the host's absolute path again
  is just noise. The stored content is unchanged — that is the prompt the Agent actually received.
- **Jail mode needs an explicit bind.** `opts.JailRoot` is the session cwd and the scope is outside it, so inside the bwrap namespace
  it is ENOENT. `service.AttachAgentScopeBind` adds a writable mapping to `ExtraBinds` that maps the path
  to itself, so absolute paths in the prompt resolve both inside and outside the jail.

Related code: `backend/internal/service/upload_paths.go` (paths and binds), `handler/upload.go` (web uploads),
`service/imbridge/imbot_attachments.go` (IM attachments), `service/imbridge/imbot_case.go` (case files).

## Related code

- `backend/internal/service/prompt_runner.go:LockConversationWorkDir` — locking logic
- `backend/internal/service/prompt_runner.go:applySessionToRequest` / `service/oneshot.go:PrepareRunSession` — calls `backend.SessionExists` to decide resume / first run
- `backend/internal/service/conversation_ops.go:Create` — copies user.WorkDir on creation
- `backend/internal/service/conversation_ops.go:UpdateWorkDir` — change via the API (allowed only when there are no user messages, and must stay inside the owner's home directory)
- `backend/internal/agent/sessionlog/sessionlog.go` — session log paths and `SessionExists` probes for both providers
- `backend/internal/agent/claudeagentsdk/backend.go` / `claudecli/runner.go` — claude's two transports
- `backend/internal/agent/codexapp/backend.go` / `codexcli/runner.go` — codex's two transports
- `backend/internal/store/store_conversation.go:UpdateConversationWorkDir` — DB persistence

## File paths in output

Absolute paths in Agent output are left untouched: the backend does not rewrite event content, so storage, WebSocket broadcasts, push notifications, and the IM bridge all see the same raw text. `prompts.AbsolutePaths` also explicitly tells the Agent to use absolute paths when stating file locations, avoiding ambiguity between web and IM clients with different cwds.

Absolute paths are displayed only as **plain text** and do not grant file access: the sandbox and `safePath()` still block out-of-bounds reads and writes, and file downloads still go through the authenticated `/api/users/:id/files` endpoint. The link fallback in the frontend's `useMarkdown.ts` also still applies — only `http(s)://`, `mailto:`, and in-page anchors stay clickable; href/src values shaped like filesystem paths are removed so the browser doesn't mistake `/home/u/proj/out.html` for a same-site URL.
