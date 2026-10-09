# Contributing to DayMug

Thanks for helping out. This page covers the dev setup, the checks every change must pass, and how to send it. Architecture and per-area deep dives live in [docs/README.md](docs/README.md).

## Prerequisites

- [Go](https://go.dev/) ≥ 1.25
- [Node.js](https://nodejs.org/) ≥ 20 and [pnpm](https://pnpm.io/) ≥ 10 (run pnpm from `frontend/` — that is where its version is pinned)
- Optional: [air](https://github.com/air-verse/air) (backend hot reload), [golangci-lint](https://golangci-lint.run/), [Delve](https://github.com/go-delve/delve)
- To actually chat, the agent runtimes from the [README requirements](README.md#requirements)

Enable the repo's commit hooks once (locale parity, API-contract checks; the optional LLM i18n scan only runs with `DAYMUG_I18N_LLM=1`, see [docs/development/i18n.md](docs/development/i18n.md)):

```bash
.githooks/install.sh
```

## Running locally

```bash
make dev-init        # once: writes data/config.yaml at the repo root
make dev-backend     # terminal 1: air hot reload, serves http://127.0.0.1:8090
make dev-frontend    # terminal 2: Vite on http://localhost:5174, proxies /api (incl. WebSockets) to the backend
```

The Vite proxy targets `http://127.0.0.1:8090`; set `DAYMUG_DEV_BACKEND` to point it elsewhere. `make test-fast` runs the fast backend + frontend suites.

Open http://localhost:5174 and create the first admin account. The dev SQLite database lives in `backend/bin/data/database.db` and survives restarts. Avoid `go run ./cmd/server serve`: its binary sits in a temp dir, so the config and database paths are not stable. Details: [docs/development/local-development.md](docs/development/local-development.md).

`make build` produces the release-style single binary (`backend/bin/daymug`, frontend embedded).

## Debugging

**Backend** — with Delve:

```bash
cd backend
dlv debug ./cmd/server -- serve --config ../data/config.yaml
```

or in VS Code (`.vscode/launch.json`):

```json
{
  "name": "Debug Backend",
  "type": "go",
  "request": "launch",
  "mode": "debug",
  "program": "${workspaceFolder}/backend/cmd/server",
  "args": ["serve", "--config", "${workspaceFolder}/data/config.yaml"],
  "cwd": "${workspaceFolder}/backend"
}
```

**Frontend** — `make dev-frontend`, then the browser DevTools; Vite serves source maps, so breakpoints work in the Sources panel. For VS Code:

```json
{
  "name": "Debug Frontend (Chrome)",
  "type": "chrome",
  "request": "launch",
  "url": "http://localhost:5174",
  "webRoot": "${workspaceFolder}/frontend/src"
}
```

## Checks every change must pass

`.github/workflows/ci.yml` runs these pipelines on every pull request; run them locally first. Fast inner loop from the repo root: `./scripts/test.sh fast`.

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

If you intentionally change a Go response struct, regenerate the contract snapshot **and** update the matching TypeScript interface:

```bash
cd backend && go test ./internal/store -run TestAPIContractSnapshot -update-contract
```

## Conventions

- **Tests ship with the change.** Go: `foo_test.go` next to `foo.go`, stdlib `testing` + `httptest`, table-driven when inputs vary. Frontend: `Foo.test.ts` next to `Foo.vue` (Vitest + `@vue/test-utils` + `happy-dom`). Tests must not touch the host filesystem outside `t.TempDir()` or the network beyond `httptest.Server`.
- **Go**: `gofmt` / `goimports` with local prefix `github.com/DayMug/DayMug`.
- **Frontend**: 2-space indent, ESLint + Prettier, strict TypeScript. User-facing strings go through `t()` with every locale updated — see [docs/development/i18n.md](docs/development/i18n.md).
- **Comments explain why**, not what.
- **Docs are English.** Update the relevant page in `docs/` when behaviour changes.
- **Commit messages** describe what changed and why.

## Sending a change

1. Fork, branch from `main`, keep the change focused.
2. Run the checks above.
3. Open a pull request describing the problem, the fix, and how you verified it.

For security issues, please do not open a public issue — follow [SECURITY.md](SECURITY.md) instead.

## Releases

Maintainers cut releases by pushing a `vX.Y.Z` tag; see [RELEASING.md](RELEASING.md).
