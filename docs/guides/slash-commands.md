# Slash commands

The web composer has no slash-command layer. Text starting with `/` is ordinary input: there is no autocomplete popup and nothing is intercepted, so `/help`, `/compact`, or a project's `.claude/commands/<name>.md` name reaches the agent exactly as typed and the agent CLI decides what it means.

IM bots keep their own small command set (e.g. WeChat's `/new`) — see `integrations-im.md` / `integrations-wechat.md`.

## `/compact` endpoint

`POST /api/conversations/:id/compact` still exists for API callers, gated on `Capabilities.SupportsCompaction`. The orchestration lives in `service.PromptRunner.Compact` (`backend/internal/service/compact.go`); `backend/internal/handler/compact.go` is only the HTTP adapter. It resumes the existing session, captures the summary through `agent.Backend.RunOneshot`, persists it as a normal assistant message, and rotates the conversation's `session_id` so the next turn starts a fresh CLI session. The summary is **not** auto-injected into the next prompt.
