# AGENTS.md

Thin index for coding agents and contributors. Per-topic detail lives in `docs/` — load on demand.

## Project

DayMug is a multi-user web chat interface for code-CLI agents. A Go backend (Gin + SQLite, single static binary) spawns and supervises agent child processes; a Vue 3 SPA drives them over WebSockets. Each conversation is keyed by a persistent session id and bound to one backend for its lifetime.

Two provider families ship today — Anthropic Claude (`claude`, `claude-compatible`) and OpenAI Codex (`codex`, `openai-compatible`) — each run over a structured transport (Claude Agent SDK / Codex app-server) or the CLI, all implementing `agent.Backend` (`backend/internal/agent/`). Shared runtime collaborators (backends, pool, sandbox, dispatcher, …) live in `service.Runtime`, assembled once in `handler/runtime.go`; per-conversation provider routing is `service.BackendRegistry.For`; per-account credentials and concurrency in `service.Pool`. Product context: `FEATURES.md`, `README.md`.

Layout: `backend/` (Go), `frontend/` (Vue 3 + TS + Vite + Tailwind + shadcn-vue, pnpm), `docs/` (deep dives).

## Installing frontend dependencies

Run `pnpm install` **from `frontend/`, never from the repo root**:

```bash
cd frontend && pnpm install
```

`pnpm` is usually a corepack shim that picks its version from the nearest `package.json`. Outside `frontend/` there is none, so `pnpm install --dir frontend` runs corepack's bundled pnpm 7, which cannot read `lockfileVersion: 9` and would re-resolve every dependency into a rewritten lockfile. `engines.pnpm` rejects that pnpm, and `frontend/.npmrc` sets `frozen-lockfile=true` so no plain install can mutate the lockfile. Adding a dependency goes through `pnpm add` (it ignores the flag) or an explicit `pnpm install --no-frozen-lockfile`.

## Verification commands

Inner loop while iterating: `./scripts/test.sh fast` (repo root) runs the backend suite and the fast frontend projects in parallel. Before reporting completion or opening a pull request, run the full per-layer pipelines below.

Backend (from `backend/`):

```bash
go build ./...
go test ./...
golangci-lint run
```

Frontend (from `frontend/`):

```bash
pnpm build               # vue-tsc + production build
pnpm test                # Vitest
pnpm lint                # ESLint --fix
pnpm api-contract:check  # TS interfaces vs the Go contract snapshot
```

`.github/workflows/ci.yml` runs the same pipelines on every pull request, every push to `main` (merges included) and every release tag.

A failure that looks like a tooling bug but isn't:

- **`pnpm api-contract:check`** is the frontend half of the anti-drift pair. It reads `backend/internal/store/contract.golden.json` (written by the Go snapshot test) and compares the JSON field names and optionality the Go structs emit against the hand-written TS interfaces — never types, because the frontend narrows them on purpose. When you intentionally change a Go response struct, regenerate the golden **and** update the matching TS interface; doing only the first just moves the failure from the Go test to the TS check.

  ```bash
  cd backend && go test ./internal/store -run TestAPIContractSnapshot -update-contract
  ```

## Workflow rules

- Verify the touched layer(s) after every change — backend → backend pipeline, frontend → frontend pipeline, full-stack → both. Don't report completion until green.
- Write a test alongside the change. Go: `foo_test.go` next to `foo.go`, same package (stdlib `testing` + `httptest`). Frontend: `Foo.test.ts` next to `Foo.vue` (vitest + `@vue/test-utils` + `happy-dom`). One behaviour per test; table-driven in Go when inputs naturally vary.
- On failure: read the error, fix the root cause, re-run the full pipeline. Never suppress, skip, or comment out a failing check.
- Only commit when the pipeline is green. Messages describe what + why, not how.
- Releases are cut by maintainers (tag pushes publish a GitHub Release and move the self-upgrade pointer); see `RELEASING.md`. Don't push tags.

## Code style

- **Go**: tabs, `gofmt`/`goimports` with local prefix `github.com/DayMug/DayMug`. Module: `github.com/DayMug/DayMug/backend`.
- **Frontend**: 2-space indent, ESLint + Prettier, strict TypeScript (`noUnusedLocals`, `noUnusedParameters`).
- Comments explain *why*, not *what*. Don't restate what well-named identifiers say.
- Tests must not depend on the host filesystem outside `t.TempDir()` or on network endpoints other than `httptest.Server`.

## Detail docs — read on demand

Start from `docs/README.md` (it indexes everything below); load a row only when the task touches that area.

| Working on… | Read |
|-------------|------|
| Docs index / navigation | `docs/README.md` |
| Global architecture, request lifecycle, top-level layout | `docs/architecture/overview.md` |
| Backend module layout (cmd / config / handler / service / store / agent) | `docs/architecture/backend.md` |
| Frontend structure (pages / components / composables) | `docs/architecture/frontend.md` |
| Agent backend abstraction, claudeagentsdk / codexapp / claudecli / codexcli adapters, the two `*-compatible` provider types | `docs/architecture/agent-backends.md` |
| SQLite schema & migrations | `docs/architecture/data-model.md` |
| REST / WebSocket / admin API reference | `docs/architecture/api-reference.md` |
| YAML config resolution, env-var overrides, providers + per-user bindings | `docs/guides/configuration.md` (+ `docs/guides/config.sample.yaml`) |
| Linux install (one-line / from-source / public access) | `docs/guides/installation.md` |
| Slash text pass-through, `/compact` endpoint | `docs/guides/slash-commands.md` |
| IM bots (Slack Socket Mode / Feishu long connection / Telegram long polling), channel rules, thread↔session mapping, case-file mode | `docs/guides/im-bots.md` |
| WeChat iLink Bot (QR-code pairing, long polling, `/new` session splitting, progress fallback without message edits) | `docs/guides/wechat.md` |
| Auth, agents, sandbox, per-user provider bindings, account pool, `safePath()` | `docs/architecture/multi-user.md` |
| Release tagging, GitHub Releases publish, self-upgrade, watchdog rollback | `RELEASING.md` (steps), `docs/development/releases.md` (mechanism) |
| Dev server (`air`, `pnpm dev`, root `Makefile`), bootstrap, installer | `docs/development/local-development.md` |
| Container image, compose, state volume | `docs/guides/docker.md` |
| Working-directory semantics (per-conversation cwd), per-backend session log layout | `docs/architecture/working-directory.md` |
| i18n locales, `t()` usage, locale parity check, pre-commit translation guard | `docs/development/i18n.md` |
| Known open bugs and tech debt | `docs/development/known-issues.md` |

## Impact analysis

- Before changing an exported or cross-package symbol (function signature, struct field, route, WebSocket message), find every caller and update them in the same change. The compiler catches Go call sites; JSON field names, route strings, and frontend consumers it does not.
- Before committing a multi-file change, read `git diff --stat main...HEAD` and confirm nothing outside the intended scope moved.
- Renames are find-and-replace plus a full build of both layers — there is no rename tooling here.
