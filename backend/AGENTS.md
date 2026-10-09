# backend/AGENTS.md

Go backend of DayMug. Module `github.com/DayMug/DayMug/backend`, import prefix for `goimports` is `github.com/DayMug/DayMug`. Full architecture: `../docs/architecture/backend.md`.

## Rules that bite

- **Dependency direction**: `cmd/server → handler → service → store`. `service` never imports a concrete adapter (`agent/claudeagentsdk`, `agent/claudecli`, `agent/codexapp`, `agent/codexcli`) — it takes the `agent.Backend` interface; concrete backends are wired in `buildBackends` in `internal/handler/runtime.go`. Adapters may import only `agent/**`, `config` and `prompts` (enforced by `internal/archtest`); shared logic goes in `agent/streamcommon`, `sessionlog`, `codexcommon`, etc. — the one adapter-to-adapter edge is `claudeagentsdk` reusing `claudecli`'s stream parser.
- **Prompts**: all LLM-facing prompt text and marker strings live in `internal/prompts` — never inline a prompt or a marker literal (`[DAYMUG_ARTIFACT`, `[HANDOFF @…]`, …) elsewhere; the golden test in `internal/prompts` locks the assembled output. Text injected into the transcript as a synthetic message (history-truncated, attachments-skipped) counts as LLM-facing and belongs there too.
- **IM copy**: user-visible strings a bot posts into a thread go in a `messages.go`, never inline. Connector-level copy (empty reply, typing status, chunk truncation) → `internal/imbot/messages.go`; bridge orchestration copy (ack, progress phases, superseded, queue, relay) → `internal/service/imbridge/messages.go`. `imbot.EmptyReplyText` is the one string both layers use — reference it, don't re-declare it. Go `error` strings stay at their `errors.New`/`fmt.Errorf` call site.
- **Schema changes**: migrations run in `internal/store` at open time; add a migration alongside the schema change, see `../docs/architecture/data-model.md`.
- **Tests**: every `foo.go` has a same-package `foo_test.go` (stdlib `testing` + `httptest`); no host filesystem outside `t.TempDir()`, no network beyond `httptest.Server`. Table-driven when inputs vary.
- **Verify** from `backend/`: `go build ./... && go test ./... && golangci-lint run`.
